// Package synckeys manages per-account custom API keys for external sync
// compatibility layers (SyncYomi, future Komga/Kavita/webhook adapters, etc.)
// that cannot perform interactive Stargate OAuth flows.
package synckeys

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/store"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

const contextAccountIDKey = "sync_api_key_account_id"

// Factory returns the reusable sync API key management module.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             "app.solian.pulsar.synckeys",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Models:         []any{&model.SyncAPIKey{}},
		Capabilities:   []string{"sync.keys"},
		New:            func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	core *core.Services
}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	i.core = host.Core

	group := host.API.Group("/sync/keys", authn.RequireAuth())
	group.POST("", i.createKey)
	group.GET("", i.listKeys)
	group.DELETE("/:id", i.revokeKey)

	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

type createKeyRequest struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes,omitempty"`
}

type createKeyResponse struct {
	*model.SyncAPIKey
	Key string `json:"key"`
}

func (i *instance) createKey(c *gin.Context) {
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}

	var req createKeyRequest
	if !httpx.BindJSON(c, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		httpx.Fail(c, errs.Validation(map[string][]string{"name": {"name is required."}}))
		return
	}

	row, rawKey, err := i.core.Store.CreateSyncAPIKey(c.Request.Context(), accountID, req.Name, req.Scopes)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.Created(c, createKeyResponse{
		SyncAPIKey: row,
		Key:        rawKey,
	})
}

func (i *instance) listKeys(c *gin.Context) {
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}

	keys, err := i.core.Store.ListSyncAPIKeys(c.Request.Context(), accountID)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, keys)
}

func (i *instance) revokeKey(c *gin.Context) {
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}

	keyID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.BadRequest(c, "INVALID_ID", "invalid key id")
		return
	}

	if err := i.core.Store.RevokeSyncAPIKey(c.Request.Context(), accountID, keyID); err != nil {
		if errors.Is(err, store.ErrSyncKeyNotFound) {
			httpx.NotFound(c, "sync api key not found")
			return
		}
		httpx.Internal(c)
		return
	}
	c.Status(http.StatusNoContent)
}

// RequireAPIKey returns a reusable Gin middleware that authenticates external
// sync clients using a custom API key (via X-API-Token, Authorization: Bearer,
// or ?apikey=), enforces the optional protocol scope, and stores the resolved
// account ID in the Gin context.
func RequireAPIKey(st *store.Store, scope string) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractAPIKey(c)
		if token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, errs.Unauthorized("missing api key"))
			return
		}
		row, err := st.AuthenticateSyncAPIKey(c.Request.Context(), token, scope)
		if err != nil {
			if errors.Is(err, store.ErrSyncKeyScopeDenied) {
				c.AbortWithStatusJSON(http.StatusForbidden, errs.Forbidden("api key lacks required scope"))
				return
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized, errs.Unauthorized("invalid api key"))
			return
		}
		c.Set(contextAccountIDKey, row.AccountID)
		c.Next()
	}
}

// AccountID returns the account ID resolved by RequireAPIKey.
func AccountID(c *gin.Context) (uuid.UUID, bool) {
	value, ok := c.Get(contextAccountIDKey)
	if !ok {
		return uuid.Nil, false
	}
	id, ok := value.(uuid.UUID)
	return id, ok && id != uuid.Nil
}

func extractAPIKey(c *gin.Context) string {
	if token := strings.TrimSpace(c.GetHeader("X-API-Token")); token != "" {
		return token
	}
	if authHeader := strings.TrimSpace(c.GetHeader("Authorization")); authHeader != "" {
		if len(authHeader) > 7 && strings.EqualFold(authHeader[:7], "Bearer ") {
			if token := strings.TrimSpace(authHeader[7:]); token != "" {
				return token
			}
		}
	}
	if token := strings.TrimSpace(c.Query("apikey")); token != "" {
		return token
	}
	return ""
}
