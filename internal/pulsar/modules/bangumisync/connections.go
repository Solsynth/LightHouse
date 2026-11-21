package bangumisync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/structpb"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

const provider = "bangumi"

// errNotLinked says Stargate has no Bangumi connection for this account. The
// local sync row may still exist so preferences survive an unlink/relink.
var errNotLinked = errors.New("bangumi is not linked to this account")

func (i *instance) registerRoutes(host *gin.RouterGroup) {
	r := host.Group("/bangumi")
	r.GET("/connection", authn.RequireAuth(), i.getConnection)
	r.PATCH("/connection", authn.RequireAuth(), i.updateConnection)
	r.POST("/sync", authn.RequireAuth(), i.syncNow)
}

// stargateLink is what Stargate knows about the account's Bangumi grant. The
// access token is used for this reconciliation only; Pulsar never stores it.
type stargateLink struct {
	remote      *model.ExternalSyncConnection
	accessToken string
	username    string
}

// stargateLink reads the account's Bangumi connection from Stargate. The
// gRPC handler refreshes an expiring token before returning it, so the secret
// and refresh flow stay in one service.
func (i *instance) stargateLink(ctx context.Context, accountID uuid.UUID) (*stargateLink, error) {
	if i.linkOverride != nil {
		return i.linkOverride(ctx, accountID)
	}
	if i.core.Clients == nil || i.core.Clients.Account == nil {
		return nil, errors.New("stargate account service is unavailable")
	}
	resp, err := i.core.Clients.Account.ListConnections(ctx, &gen.DyListConnectionsRequest{
		AccountId: accountID.String(),
		Provider:  provider,
	})
	if err != nil {
		return nil, err
	}
	for _, connection := range resp.GetConnections() {
		if !strings.EqualFold(connection.GetProvider(), provider) {
			continue
		}
		token := strings.TrimSpace(connection.GetAccessToken().GetValue())
		if token == "" {
			continue
		}
		meta := connection.GetMeta()
		username := firstMeta(meta, "preferred_username", "username")
		remoteUsername := username
		remote := &model.ExternalSyncConnection{
			AccountID:      accountID,
			Provider:       provider,
			RemoteUserID:   connection.GetProvidedIdentifier(),
			RemoteUsername: &remoteUsername,
			Meta: model.ToJSON(map[string]any{
				"username":   username,
				"nickname":   firstMeta(meta, "display_name", "nickname"),
				"avatar_url": firstMeta(meta, "profile_picture_url", "avatar_url"),
			}),
		}
		return &stargateLink{remote: remote, accessToken: token, username: username}, nil
	}
	return nil, errNotLinked
}

// ensureLocalConnection mirrors Stargate's remote identity into the local row
// that owns sync preferences. A re-link updates identity but preserves the
// account's background-sync and conflict-policy choices.
func (i *instance) ensureLocalConnection(ctx context.Context, link *stargateLink) (*model.ExternalSyncConnection, bool, error) {
	created, err := i.core.Store.EnsureSyncConnection(ctx, link.remote)
	if err != nil {
		return nil, false, err
	}
	stored, err := i.core.Store.GetSyncConnection(ctx, link.remote.AccountID, provider)
	if err != nil {
		return nil, false, err
	}
	return stored, created, nil
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
	link, err := i.stargateLink(c.Request.Context(), accountID)
	if err != nil {
		if errors.Is(err, errNotLinked) {
			httpx.OK(c, connectionResponse{
				Linked:      false,
				Configured:  true,
				Provider:    provider,
				Collections: []collectionState{},
			})
			return
		}
		httpx.Fail(c, errs.New("STARGATE_UNAVAILABLE", "Could not read the Bangumi connection from Stargate.", http.StatusBadGateway))
		return
	}
	local, created, err := i.ensureLocalConnection(c.Request.Context(), link)
	if err != nil {
		httpx.Internal(c)
		return
	}
	if created && !i.disableAutoSync {
		i.wg.Add(1)
		go i.syncAfterLink(local)
	}
	httpx.OK(c, i.connectionView(c.Request.Context(), local))
}

func (i *instance) connectionView(ctx context.Context, conn *model.ExternalSyncConnection) connectionResponse {
	view := connectionResponse{
		Linked:         true,
		Configured:     true,
		Provider:       provider,
		RemoteUserID:   conn.RemoteUserID,
		SyncEnabled:    conn.SyncEnabled,
		ConflictPolicy: i.conflictPolicy(conn),
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
	link, err := i.stargateLink(c.Request.Context(), accountID)
	if err != nil {
		if errors.Is(err, errNotLinked) {
			httpx.NotFound(c, "Link Bangumi from account connections first")
			return
		}
		httpx.Fail(c, errs.New("STARGATE_UNAVAILABLE", "Could not read the Bangumi connection from Stargate.", http.StatusBadGateway))
		return
	}
	if _, _, err := i.ensureLocalConnection(c.Request.Context(), link); err != nil {
		httpx.Internal(c)
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
	updated, err := i.core.Store.GetSyncConnection(c.Request.Context(), accountID, provider)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, i.connectionView(c.Request.Context(), updated))
}

func (i *instance) syncNow(c *gin.Context) {
	accountID, ok := i.accountOf(c)
	if !ok {
		return
	}
	link, err := i.stargateLink(c.Request.Context(), accountID)
	if err != nil {
		if errors.Is(err, errNotLinked) {
			httpx.NotFound(c, "Link Bangumi from account connections first")
			return
		}
		httpx.Fail(c, errs.New("STARGATE_UNAVAILABLE", "Could not read the Bangumi connection from Stargate.", http.StatusBadGateway))
		return
	}
	local, _, err := i.ensureLocalConnection(c.Request.Context(), link)
	if err != nil {
		httpx.Internal(c)
		return
	}
	if !i.tryLock(local.ID) {
		httpx.Fail(c, errs.New("BANGUMI_SYNC_IN_PROGRESS", "A Bangumi sync is already running for this account.", http.StatusConflict))
		return
	}
	defer i.unlock(local.ID)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	summary, syncErr := i.syncConnection(ctx, local)
	now := time.Now().UTC()
	_ = i.core.Store.UpdateSyncConnectionResult(context.Background(), local.ID, &now, syncErr)
	if syncErr != nil {
		httpx.Fail(c, errs.New("BANGUMI_SYNC_FAILED", syncErr.Error(), http.StatusBadGateway))
		return
	}
	httpx.OK(c, summary)
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

func firstMeta(meta map[string]*structpb.Value, keys ...string) string {
	for _, key := range keys {
		if value, ok := meta[key]; ok {
			text := strings.TrimSpace(value.GetStringValue())
			if text != "" {
				return text
			}
		}
	}
	return ""
}
