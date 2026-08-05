package securityctl

// API key routes (ApiKeyController port).
//
// C# source:
//	../DysonNetwork/DysonNetwork.Padlock/Auth/ApiKeyController.cs

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/internal/errs"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/permission"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// apiKeyListItem mirrors the ListApiKeys projection
// `new { k.Id, k.Label, k.AppId, k.CreatedAt, k.Session.ExpiredAt }`.
type apiKeyListItem struct {
	Id        string      `json:"id"`
	Label     string      `json:"label"`
	AppId     *string     `json:"app_id,omitempty"`
	CreatedAt *model.Time `json:"created_at"`
	ExpiredAt *model.Time `json:"expired_at,omitempty"`
}

// apiKeyCreated mirrors the CreateApiKey response
// `new { key.Id, key.Label, key.AppId, token, key.CreatedAt, key.Session.ExpiredAt }`.
type apiKeyCreated struct {
	Id        string      `json:"id"`
	Label     string      `json:"label"`
	AppId     *string     `json:"app_id,omitempty"`
	Token     string      `json:"token"`
	CreatedAt *model.Time `json:"created_at"`
	ExpiredAt *model.Time `json:"expired_at,omitempty"`
}

func apiKeyNotFound() *errs.ApiError {
	return errs.New("PADLOCK_API_KEY_NOT_FOUND", "API key not found.", http.StatusNotFound)
}

func (c *controller) requireApiKeysManage(ctx *gin.Context, accountID string) bool {
	allowed, err := c.d.Perm.HasPermission(ctx.Request.Context(), uuid.MustParse(accountID), permission.AuthApiKeysManage)
	if err != nil || !allowed {
		ctx.JSON(http.StatusForbidden, errs.Forbidden("You do not have permission to perform this action."))
		return false
	}
	return true
}

// GET /api/api-keys
func (c *controller) listApiKeys(ctx *gin.Context) {
	user := middleware.CurrentUser(ctx.Request.Context())
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	keys, err := c.d.Store.ListApiKeys(ctx.Request.Context(), user.Id)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load API keys.", http.StatusInternalServerError))
		return
	}
	response := make([]apiKeyListItem, 0, len(keys))
	for _, key := range keys {
		response = append(response, apiKeyListItem{
			Id:        key.Id,
			Label:     key.Label,
			AppId:     key.AppId,
			CreatedAt: key.CreatedAt,
			ExpiredAt: key.ExpiredAt,
		})
	}
	ctx.JSON(http.StatusOK, response)
}

type createApiKeyRequest struct {
	Label     string      `json:"label"`
	ExpiredAt *model.Time `json:"expired_at,omitempty"`
}

// POST /api/api-keys — permission AuthApiKeysManage.
func (c *controller) createApiKey(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	if !c.requireApiKeysManage(ctx, user.Id) {
		return
	}
	var request createApiKeyRequest
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, validationError(map[string][]string{
			"label": {"Label is required."},
		}))
		return
	}
	var expiredAt *time.Time
	if request.ExpiredAt != nil {
		t := request.ExpiredAt.Time()
		expiredAt = &t
	}
	parent := middleware.CurrentSession(reqCtx)
	key, err := c.d.Auth.CreateApiKey(reqCtx, user.Id, request.Label, expiredAt, parent)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, errs.New("PADLOCK_API_KEY_INVALID", err.Error(), http.StatusBadRequest))
		return
	}
	token, err := c.d.Auth.IssueApiKeyToken(reqCtx, key)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to issue API key token.", http.StatusInternalServerError))
		return
	}
	ctx.JSON(http.StatusOK, apiKeyCreated{
		Id:        key.Id,
		Label:     key.Label,
		AppId:     key.AppId,
		Token:     token,
		CreatedAt: key.CreatedAt,
		ExpiredAt: key.ExpiredAt,
	})
}

// DELETE /api/api-keys/{id} — permission AuthApiKeysManage.
func (c *controller) revokeApiKey(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	if !c.requireApiKeysManage(ctx, user.Id) {
		return
	}
	id, ok := parseIDParam(ctx)
	if !ok {
		ctx.JSON(http.StatusNotFound, apiKeyNotFound())
		return
	}
	accountID, _ := uuid.Parse(user.Id)
	key, err := c.d.Auth.GetApiKey(reqCtx, id, &accountID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			ctx.JSON(http.StatusNotFound, apiKeyNotFound())
			return
		}
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load API key.", http.StatusInternalServerError))
		return
	}
	if err := c.d.Auth.RevokeApiKeyToken(reqCtx, key); err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to revoke API key.", http.StatusInternalServerError))
		return
	}
	ctx.Status(http.StatusOK)
}

// POST /api/api-keys/{id}/rotate — permission AuthApiKeysManage.
func (c *controller) rotateApiKey(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	if !c.requireApiKeysManage(ctx, user.Id) {
		return
	}
	id, ok := parseIDParam(ctx)
	if !ok {
		ctx.JSON(http.StatusNotFound, apiKeyNotFound())
		return
	}
	accountID, _ := uuid.Parse(user.Id)
	key, err := c.d.Auth.GetApiKey(reqCtx, id, &accountID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			ctx.JSON(http.StatusNotFound, apiKeyNotFound())
			return
		}
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load API key.", http.StatusInternalServerError))
		return
	}
	rotated, err := c.d.Auth.RotateApiKeyToken(reqCtx, key)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to rotate API key.", http.StatusInternalServerError))
		return
	}
	token, err := c.d.Auth.IssueApiKeyToken(reqCtx, rotated)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to issue API key token.", http.StatusInternalServerError))
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"token": token})
}
