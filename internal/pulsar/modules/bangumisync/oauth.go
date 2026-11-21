package bangumisync

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/bangumi"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
)

const (
	provider = "bangumi"
	// A consent round-trip is minutes, not hours. Ten minutes is generous for
	// signing in to Bangumi without leaving stale states around.
	oauthStateLifetime = 10 * time.Minute
)

func (i *instance) registerRoutes(host *gin.RouterGroup) {
	r := host.Group("/bangumi")
	r.GET("/connection", authn.RequireAuth(), i.getConnection)
	r.POST("/oauth/start", authn.RequireAuth(), i.startOAuth)
	r.GET("/oauth/callback", i.oauthCallback)
	r.PATCH("/connection", authn.RequireAuth(), i.updateConnection)
	r.DELETE("/connection", authn.RequireAuth(), i.unlink)
	r.POST("/sync", authn.RequireAuth(), i.syncNow)
}

type oauthStartResponse struct {
	AuthorizeURL string    `json:"authorize_url"`
	State        string    `json:"state"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func (i *instance) startOAuth(c *gin.Context) {
	accountID, ok := i.accountOf(c)
	if !ok {
		return
	}
	if !i.client.Configured() {
		httpx.Fail(c, errs.New("BANGUMI_NOT_CONFIGURED",
			"Bangumi account linking is not configured on this deployment.", http.StatusServiceUnavailable))
		return
	}

	state, err := randomState()
	if err != nil {
		httpx.Internal(c)
		return
	}
	row := &model.ExternalOAuthState{
		State:     state,
		Provider:  provider,
		AccountID: accountID,
		ExpiresAt: time.Now().UTC().Add(oauthStateLifetime),
	}
	if err := i.core.Store.CreateOAuthState(c.Request.Context(), row); err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, oauthStartResponse{
		AuthorizeURL: i.client.AuthorizeURL(state),
		State:        state,
		ExpiresAt:    row.ExpiresAt,
	})
}

// oauthCallback is intentionally unauthenticated: the browser arrives from
// Bangumi without a Pulsar session. The single-use state row is what binds the
// callback to the account that started the flow.
func (i *instance) oauthCallback(c *gin.Context) {
	if reason := strings.TrimSpace(c.Query("error")); reason != "" {
		message := strings.TrimSpace(c.Query("error_description"))
		if message == "" {
			message = reason
		}
		i.finishOAuth(c, "error", message)
		return
	}

	state := strings.TrimSpace(c.Query("state"))
	code := strings.TrimSpace(c.Query("code"))
	if state == "" || code == "" {
		i.finishOAuth(c, "error", "Bangumi did not return an authorization code.")
		return
	}
	row, err := i.core.Store.ConsumeOAuthState(c.Request.Context(), state, provider)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			i.finishOAuth(c, "error", "This Bangumi link request expired or was already used. Start again from Solian.")
			return
		}
		i.finishOAuth(c, "error", "Could not verify the Bangumi link request.")
		return
	}

	token, err := i.client.ExchangeCode(c.Request.Context(), code)
	if err != nil {
		i.log.Warn("bangumi token exchange failed", "account_id", row.AccountID, "error", err)
		i.finishOAuth(c, "error", "Bangumi rejected the authorization code. Please try linking again.")
		return
	}
	me, err := i.client.Me(c.Request.Context(), token.AccessToken)
	if err != nil {
		i.log.Warn("bangumi profile lookup failed", "account_id", row.AccountID, "error", err)
		i.finishOAuth(c, "error", "Bangumi authorized the app but the profile could not be read.")
		return
	}
	if strings.TrimSpace(me.Username) == "" {
		i.finishOAuth(c, "error", "Bangumi returned a profile without a username.")
		return
	}

	access := token.AccessToken
	refresh := token.RefreshToken
	var expires *time.Time
	if at := token.ExpiresAt(time.Now()); !at.IsZero() {
		expires = &at
	}
	username := me.Username
	conn := &model.ExternalSyncConnection{
		AccountID:      row.AccountID,
		Provider:       provider,
		RemoteUserID:   strconv.FormatInt(me.ID, 10),
		RemoteUsername: &username,
		AccessToken:    &access,
		RefreshToken:   &refresh,
		TokenExpiresAt: expires,
		SyncEnabled:    true,
		Meta: model.ToJSON(map[string]any{
			"nickname":   me.Nickname,
			"avatar_url": me.Avatar.Best(),
			"sign":       me.Sign,
		}),
	}
	if err := i.core.Store.UpsertSyncConnection(c.Request.Context(), conn); err != nil {
		i.log.Warn("store bangumi connection failed", "account_id", row.AccountID, "error", err)
		i.finishOAuth(c, "error", "Could not save the Bangumi link.")
		return
	}

	// The first reconciliation runs in the background: the browser should see
	// the success page now, not after a library-sized import.
	if stored, err := i.core.Store.GetSyncConnection(context.Background(), row.AccountID, provider); err == nil && !i.disableAutoSync {
		i.wg.Add(1)
		go i.syncAfterLink(stored)
	}

	i.finishOAuth(c, "linked", "")
}

func (i *instance) syncAfterLink(conn *model.ExternalSyncConnection) {
	defer i.wg.Done()
	ctx, cancel := context.WithTimeout(i.ctx, 5*time.Minute)
	defer cancel()
	summary, err := i.syncConnection(ctx, conn)
	now := time.Now().UTC()
	_ = i.core.Store.UpdateSyncConnectionResult(context.Background(), conn.ID, &now, err)
	if err != nil {
		i.log.Warn("initial bangumi sync failed", "account_id", conn.AccountID, "error", err)
		return
	}
	i.log.Info("initial bangumi sync finished",
		"account_id", conn.AccountID, "pulled", summary.Pulled, "pushed", summary.Pushed)
}

func (i *instance) finishOAuth(c *gin.Context, status, message string) {
	target := strings.TrimSpace(i.core.Config.Bangumi.PostAuthRedirect)
	if target == "" {
		if status == "linked" {
			httpx.OK(c, gin.H{"status": status})
			return
		}
		httpx.BadRequest(c, "BANGUMI_OAUTH_FAILED", message)
		return
	}
	parsed, err := url.Parse(target)
	if err != nil {
		httpx.BadRequest(c, "BANGUMI_OAUTH_FAILED", message)
		return
	}
	query := parsed.Query()
	query.Set("bangumi", status)
	if message != "" {
		query.Set("message", message)
	}
	parsed.RawQuery = query.Encode()
	c.Redirect(http.StatusFound, parsed.String())
}

type collectionState struct {
	Domain       string `json:"domain"`
	CollectionID string `json:"collection_id"`
	Name         string `json:"name"`
	Scope        string `json:"scope,omitempty"`
	Items        int64  `json:"items"`
}

type connectionResponse struct {
	Linked         bool              `json:"linked"`
	Configured     bool              `json:"configured"`
	Provider       string            `json:"provider"`
	RemoteUserID   string            `json:"remote_user_id,omitempty"`
	RemoteUsername string            `json:"remote_username,omitempty"`
	Nickname       string            `json:"nickname,omitempty"`
	AvatarURL      string            `json:"avatar_url,omitempty"`
	SyncEnabled    bool              `json:"sync_enabled"`
	ConflictPolicy string            `json:"conflict_policy"`
	TokenExpiresAt *time.Time        `json:"token_expires_at,omitempty"`
	LastSyncedAt   *time.Time        `json:"last_synced_at,omitempty"`
	LastError      string            `json:"last_error,omitempty"`
	LinkedAt       *time.Time        `json:"linked_at,omitempty"`
	Collections    []collectionState `json:"collections"`
}

func (i *instance) getConnection(c *gin.Context) {
	accountID, ok := i.accountOf(c)
	if !ok {
		return
	}
	conn, err := i.core.Store.GetSyncConnection(c.Request.Context(), accountID, provider)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.OK(c, connectionResponse{
				Linked:      false,
				Configured:  i.client.Configured(),
				Provider:    provider,
				Collections: []collectionState{},
			})
			return
		}
		httpx.Internal(c)
		return
	}
	httpx.OK(c, i.connectionView(c.Request.Context(), conn))
}

func (i *instance) connectionView(ctx context.Context, conn *model.ExternalSyncConnection) connectionResponse {
	view := connectionResponse{
		Linked:         true,
		Configured:     i.client.Configured(),
		Provider:       provider,
		RemoteUserID:   conn.RemoteUserID,
		SyncEnabled:    conn.SyncEnabled,
		ConflictPolicy: i.conflictPolicy(conn),
		TokenExpiresAt: conn.TokenExpiresAt,
		LastSyncedAt:   conn.LastSyncedAt,
		Collections:    []collectionState{},
	}
	if conn.RemoteUsername != nil {
		view.RemoteUsername = *conn.RemoteUsername
	}
	if conn.LastError != nil {
		view.LastError = *conn.LastError
	}
	created := conn.CreatedAt
	view.LinkedAt = &created
	if len(conn.Meta) > 0 {
		var meta struct {
			Nickname  string `json:"nickname"`
			AvatarURL string `json:"avatar_url"`
		}
		if json.Unmarshal(conn.Meta, &meta) == nil {
			view.Nickname = meta.Nickname
			view.AvatarURL = meta.AvatarURL
		}
	}
	collections, err := i.core.Store.ListSyncCollections(ctx, conn.AccountID, provider)
	if err == nil {
		for _, collection := range collections {
			state := collectionState{
				Domain:       collection.Domain,
				CollectionID: collection.ID.String(),
				Name:         collection.Name,
			}
			if collection.SyncScope != nil {
				state.Scope = *collection.SyncScope
			}
			if count, err := i.core.Store.CountCollectionItems(ctx, collection.ID); err == nil {
				state.Items = count
			}
			view.Collections = append(view.Collections, state)
		}
	}
	return view
}

type updateConnectionRequest struct {
	SyncEnabled    *bool   `json:"sync_enabled"`
	ConflictPolicy *string `json:"conflict_policy"`
}

func (i *instance) updateConnection(c *gin.Context) {
	accountID, ok := i.accountOf(c)
	if !ok {
		return
	}
	var req updateConnectionRequest
	if !httpx.BindJSON(c, &req) {
		return
	}
	if req.ConflictPolicy != nil {
		value := strings.ToLower(strings.TrimSpace(*req.ConflictPolicy))
		if value != "" && value != "local" && value != "remote" && value != "newest" {
			httpx.BadRequest(c, "INVALID_ARGUMENT", "conflict_policy must be local, remote, newest, or empty")
			return
		}
		req.ConflictPolicy = &value
	}
	if err := i.core.Store.UpdateSyncConnectionSettings(c.Request.Context(), accountID, provider, req.SyncEnabled, req.ConflictPolicy); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.NotFound(c, "Bangumi is not linked to this account")
			return
		}
		httpx.Internal(c)
		return
	}
	conn, err := i.core.Store.GetSyncConnection(c.Request.Context(), accountID, provider)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, i.connectionView(c.Request.Context(), conn))
}

func (i *instance) unlink(c *gin.Context) {
	accountID, ok := i.accountOf(c)
	if !ok {
		return
	}
	if err := i.core.Store.DeleteSyncConnection(c.Request.Context(), accountID, provider); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		httpx.Internal(c)
		return
	}
	httpx.NoContent(c)
}

func (i *instance) syncNow(c *gin.Context) {
	accountID, ok := i.accountOf(c)
	if !ok {
		return
	}
	conn, err := i.core.Store.GetSyncConnection(c.Request.Context(), accountID, provider)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.NotFound(c, "Bangumi is not linked to this account")
			return
		}
		httpx.Internal(c)
		return
	}
	if !i.tryLock(conn.ID) {
		httpx.Fail(c, errs.New("BANGUMI_SYNC_IN_PROGRESS", "A Bangumi sync is already running for this account.", http.StatusConflict))
		return
	}
	defer i.unlock(conn.ID)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	summary, syncErr := i.syncConnection(ctx, conn)
	now := time.Now().UTC()
	_ = i.core.Store.UpdateSyncConnectionResult(context.Background(), conn.ID, &now, syncErr)
	if syncErr != nil {
		httpx.Fail(c, errs.New("BANGUMI_SYNC_FAILED", syncErr.Error(), http.StatusBadGateway))
		return
	}
	httpx.OK(c, summary)
}

// ensureAccessToken refreshes credentials that are close to expiry. Bangumi
// rotates refresh tokens, so the new pair is written back immediately.
func (i *instance) ensureAccessToken(ctx context.Context, conn *model.ExternalSyncConnection) error {
	if conn.AccessToken == nil || strings.TrimSpace(*conn.AccessToken) == "" {
		return bangumi.ErrUnauthorized
	}
	if conn.TokenExpiresAt == nil {
		// No expiry was recorded; use the token until the API rejects it.
		return nil
	}
	if time.Until(*conn.TokenExpiresAt) > 5*time.Minute {
		return nil
	}
	return i.refreshAccessToken(ctx, conn)
}

// refreshAccessToken rotates the pair unconditionally. Bangumi invalidates the
// old refresh token, so both values are written back together.
func (i *instance) refreshAccessToken(ctx context.Context, conn *model.ExternalSyncConnection) error {
	if conn.RefreshToken == nil || strings.TrimSpace(*conn.RefreshToken) == "" {
		return bangumi.ErrUnauthorized
	}
	token, err := i.client.Refresh(ctx, strings.TrimSpace(*conn.RefreshToken))
	if err != nil {
		return err
	}
	access := token.AccessToken
	conn.AccessToken = &access
	if strings.TrimSpace(token.RefreshToken) != "" {
		refresh := token.RefreshToken
		conn.RefreshToken = &refresh
	}
	if at := token.ExpiresAt(time.Now()); !at.IsZero() {
		conn.TokenExpiresAt = &at
	} else {
		conn.TokenExpiresAt = nil
	}
	return i.core.Store.UpdateSyncConnectionTokens(ctx, conn)
}

func (i *instance) accessToken(conn *model.ExternalSyncConnection) (string, error) {
	if conn.AccessToken == nil || strings.TrimSpace(*conn.AccessToken) == "" {
		return "", bangumi.ErrUnauthorized
	}
	return strings.TrimSpace(*conn.AccessToken), nil
}

// ensureUsername fills a missing handle from /v0/me. Older links or a provider
// rename should not strand a working token.
func (i *instance) ensureUsername(ctx context.Context, conn *model.ExternalSyncConnection, token string) (string, error) {
	if conn.RemoteUsername != nil && strings.TrimSpace(*conn.RemoteUsername) != "" {
		return strings.TrimSpace(*conn.RemoteUsername), nil
	}
	me, err := i.client.Me(ctx, token)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(me.Username) == "" {
		return "", errors.New("bangumi profile has no username")
	}
	username := me.Username
	conn.RemoteUsername = &username
	conn.RemoteUserID = strconv.FormatInt(me.ID, 10)
	conn.Meta = model.ToJSON(map[string]any{
		"nickname":   me.Nickname,
		"avatar_url": me.Avatar.Best(),
		"sign":       me.Sign,
	})
	if err := i.core.Store.UpsertSyncConnection(ctx, conn); err != nil {
		return "", err
	}
	return username, nil
}

func (i *instance) conflictPolicy(conn *model.ExternalSyncConnection) string {
	if conn != nil && strings.TrimSpace(conn.ConflictPolicy) != "" {
		return strings.TrimSpace(conn.ConflictPolicy)
	}
	if policy := strings.TrimSpace(i.core.Config.Bangumi.ConflictPolicy); policy != "" {
		return policy
	}
	return "remote"
}

func (i *instance) accountOf(c *gin.Context) (uuid.UUID, bool) {
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return uuid.Nil, false
	}
	return accountID, true
}

func randomState() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
