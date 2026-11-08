// Package keyctl mounts the account public-key surface: SSH and PGP key
// management for the signed-in account, plus an anonymous, read-only listing
// of an account's keys for other services and clients that verify identities.
//
// Mutations require an interactive session and elevation (adding or removing a
// public key changes how the account can be reached and proven); reads only
// require a session. API-key tokens never reach the management routes.
package keyctl

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	"src.solsynth.dev/sosys/stargate/accountkeys"
	"src.solsynth.dev/sosys/stargate/actionlog"
	"src.solsynth.dev/sosys/stargate/auth"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/store"
)

// Deps carries the dependencies the key controller needs.
type Deps struct {
	Keys     *accountkeys.Store
	Accounts *store.Store
	Logs     *actionlog.Service
	Log      *slog.Logger
}

type controller struct {
	d Deps
}

// Register mounts the key routes on the /api router group.
func Register(api *gin.RouterGroup, d Deps) {
	c := &controller{d: d}

	// Management routes share the AccountSecurityController's interactive
	// gate: an API-key token can manage neither its own keys nor the account's.
	keys := api.Group("keys", c.requireInteractive)
	keys.GET("ssh", c.listSSH)
	keys.POST("ssh", middleware.RequireAuth(), middleware.RequireSudo(), c.createSSH)
	keys.PATCH("ssh/:id", middleware.RequireAuth(), c.renameSSH)
	keys.DELETE("ssh/:id", middleware.RequireAuth(), middleware.RequireSudo(), c.deleteSSH)

	keys.GET("pgp", c.listPGP)
	keys.POST("pgp", middleware.RequireAuth(), middleware.RequireSudo(), c.createPGP)
	keys.PATCH("pgp/:id", middleware.RequireAuth(), c.renamePGP)
	keys.DELETE("pgp/:id", middleware.RequireAuth(), middleware.RequireSudo(), c.deletePGP)

	// Public read surface. Keys are public material; a service that verifies
	// a signature or resolves a git SSH fingerprint only needs the account
	// name.
	api.GET("accounts/:name/keys", c.publicKeys)
}

// ── helpers ─────────────────────────────────────────────────────────────────

func (c *controller) requireInteractive(ctx *gin.Context) {
	if middleware.CurrentTokenType(ctx.Request.Context()) == auth.TokenTypeApiKey {
		ctx.AbortWithStatusJSON(http.StatusForbidden, errs.New("FORBIDDEN", "Interactive session token required.", http.StatusForbidden))
		return
	}
	ctx.Next()
}

func unauthorized401() *errs.ApiError {
	return errs.New("UNAUTHORIZED", "Authentication is required.", http.StatusUnauthorized)
}

func validationError(field string, messages []string) *errs.ApiError {
	return errs.Validation(map[string][]string{field: messages})
}

func internalError() *errs.ApiError {
	return errs.New("INTERNAL_ERROR", "Internal server error.", http.StatusInternalServerError)
}

func keyNotFound() *errs.ApiError {
	return errs.New("ACCOUNT_KEY_NOT_FOUND", "The key was not found.", http.StatusNotFound)
}

func keyInvalid(message string) *errs.ApiError {
	return errs.New("ACCOUNT_KEY_INVALID", message, http.StatusBadRequest)
}

func keyExists() *errs.ApiError {
	return errs.New("ACCOUNT_KEY_EXISTS", "This key is already registered on the account.", http.StatusConflict)
}

func keyLimit() *errs.ApiError {
	return errs.New("ACCOUNT_KEY_LIMIT_REACHED", "This account has reached the key limit.", http.StatusBadRequest)
}

func (c *controller) actionLog(ctx context.Context, accountID string, action model.ActionLogType, meta map[string]any, r *http.Request, sessionID *string) {
	if c.d.Logs == nil {
		return
	}
	ua := ""
	ip := ""
	if r != nil {
		ua = r.UserAgent()
		ip = middleware.ClientIP(r)
	}
	var sid *string
	if sessionID != nil && *sessionID != "" {
		sid = sessionID
	}
	if err := c.d.Logs.Create(ctx, accountID, action, meta, ua, ip, nil, sid); err != nil {
		if c.d.Log != nil {
			c.d.Log.Warn("action log", "action", action, "error", err)
		}
	}
}

func currentSessionID(ctx context.Context) *string {
	session := middleware.CurrentSession(ctx)
	if session == nil || session.Id == "" {
		return nil
	}
	return &session.Id
}

func accountID(user *model.Account) (uuid.UUID, bool) {
	id, err := uuid.Parse(user.Id)
	return id, err == nil
}

func parseKeyID(ctx *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(ctx.Param("id"))
	return id, err == nil
}

func keyMeta(kind, id, label, fingerprint string) map[string]any {
	return map[string]any{
		"key_id":      id,
		"kind":        kind,
		"label":       label,
		"fingerprint": fingerprint,
	}
}

// writeFailure maps the domain errors shared by both key kinds to the HTTP
// envelope.
func (c *controller) writeFailure(ctx *gin.Context, err error, invalidMessage string) {
	switch {
	case errors.Is(err, accountkeys.ErrDuplicate):
		ctx.JSON(http.StatusConflict, keyExists())
	case errors.Is(err, accountkeys.ErrLimit):
		ctx.JSON(http.StatusBadRequest, keyLimit())
	case errors.Is(err, accountkeys.ErrNotFound):
		ctx.JSON(http.StatusNotFound, keyNotFound())
	case errors.Is(err, accountkeys.ErrPrivateKey):
		ctx.JSON(http.StatusBadRequest, keyInvalid("The block contains private key material. Only public keys are accepted."))
	case errors.Is(err, accountkeys.ErrInvalidSSHKey), errors.Is(err, accountkeys.ErrInvalidPGPKey):
		ctx.JSON(http.StatusBadRequest, keyInvalid(invalidMessage))
	case errors.Is(err, accountkeys.ErrLabelTooLong):
		ctx.JSON(http.StatusBadRequest, validationError("label", []string{"Label is longer than 128 characters."}))
	default:
		ctx.JSON(http.StatusInternalServerError, internalError())
	}
}

// ── SSH ─────────────────────────────────────────────────────────────────────

// GET /api/keys/ssh
func (c *controller) listSSH(ctx *gin.Context) {
	user := middleware.CurrentUser(ctx.Request.Context())
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	id, ok := accountID(user)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	keys, err := c.d.Keys.ListSSHKeys(ctx.Request.Context(), id)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, internalError())
		return
	}
	ctx.JSON(http.StatusOK, keys)
}

type createKeyRequest struct {
	Label     string `json:"label"`
	PublicKey string `json:"public_key"`
}

// POST /api/keys/ssh
func (c *controller) createSSH(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	var request createKeyRequest
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, validationError("public_key", []string{"A public key is required."}))
		return
	}
	info, err := accountkeys.ParseSSHKey(request.PublicKey)
	if err != nil {
		c.writeFailure(ctx, err, "The SSH public key could not be parsed. Paste one authorized_keys line.")
		return
	}
	fallback := info.Comment
	if fallback == "" {
		fallback = info.Algorithm
	}
	label, err := accountkeys.LabelOrFallback(request.Label, fallback)
	if err != nil {
		c.writeFailure(ctx, err, "")
		return
	}
	id, ok := accountID(user)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	key, err := c.d.Keys.CreateSSHKey(reqCtx, id, label, info)
	if err != nil {
		c.writeFailure(ctx, err, "The SSH public key could not be parsed. Paste one authorized_keys line.")
		return
	}
	c.actionLog(reqCtx, user.Id, model.ActionLogSSHKeyCreate,
		keyMeta("ssh", key.Id, key.Label, key.Fingerprint), ctx.Request, currentSessionID(reqCtx))
	ctx.JSON(http.StatusOK, key)
}

type renameKeyRequest struct {
	Label string `json:"label"`
}

// PATCH /api/keys/ssh/:id
func (c *controller) renameSSH(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	id, ok := parseKeyID(ctx)
	if !ok {
		ctx.JSON(http.StatusNotFound, keyNotFound())
		return
	}
	var request renameKeyRequest
	if err := ctx.ShouldBindJSON(&request); err != nil || request.Label == "" {
		ctx.JSON(http.StatusBadRequest, validationError("label", []string{"Label is required."}))
		return
	}
	label, err := accountkeys.LabelOrFallback(request.Label, "")
	if err != nil {
		c.writeFailure(ctx, err, "")
		return
	}
	account, ok := accountID(user)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	key, err := c.d.Keys.RenameSSHKey(reqCtx, account, id, label)
	if err != nil {
		c.writeFailure(ctx, err, "")
		return
	}
	c.actionLog(reqCtx, user.Id, model.ActionLogSSHKeyRename,
		keyMeta("ssh", key.Id, key.Label, key.Fingerprint), ctx.Request, currentSessionID(reqCtx))
	ctx.JSON(http.StatusOK, key)
}

// DELETE /api/keys/ssh/:id
func (c *controller) deleteSSH(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	id, ok := parseKeyID(ctx)
	if !ok {
		ctx.JSON(http.StatusNotFound, keyNotFound())
		return
	}
	account, ok := accountID(user)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	if err := c.d.Keys.DeleteSSHKey(reqCtx, account, id); err != nil {
		c.writeFailure(ctx, err, "")
		return
	}
	c.actionLog(reqCtx, user.Id, model.ActionLogSSHKeyDelete,
		keyMeta("ssh", id.String(), "", ""), ctx.Request, currentSessionID(reqCtx))
	ctx.Status(http.StatusNoContent)
}

// ── PGP ─────────────────────────────────────────────────────────────────────

// GET /api/keys/pgp
func (c *controller) listPGP(ctx *gin.Context) {
	user := middleware.CurrentUser(ctx.Request.Context())
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	id, ok := accountID(user)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	keys, err := c.d.Keys.ListPGPKeys(ctx.Request.Context(), id)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, internalError())
		return
	}
	ctx.JSON(http.StatusOK, keys)
}

// POST /api/keys/pgp
func (c *controller) createPGP(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	var request createKeyRequest
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, validationError("public_key", []string{"A public key is required."}))
		return
	}
	info, err := accountkeys.ParsePGPKey(request.PublicKey)
	if err != nil {
		c.writeFailure(ctx, err, "The PGP public key could not be parsed. Paste one armored public key block.")
		return
	}
	fallback := "PGP key"
	if len(info.UserIDs) > 0 {
		fallback = info.UserIDs[0]
	}
	label, err := accountkeys.LabelOrFallback(request.Label, fallback)
	if err != nil {
		c.writeFailure(ctx, err, "")
		return
	}
	id, ok := accountID(user)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	key, err := c.d.Keys.CreatePGPKey(reqCtx, id, label, info)
	if err != nil {
		c.writeFailure(ctx, err, "The PGP public key could not be parsed. Paste one armored public key block.")
		return
	}
	c.actionLog(reqCtx, user.Id, model.ActionLogPGPKeyCreate,
		keyMeta("pgp", key.Id, key.Label, key.Fingerprint), ctx.Request, currentSessionID(reqCtx))
	ctx.JSON(http.StatusOK, key)
}

// PATCH /api/keys/pgp/:id
func (c *controller) renamePGP(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	id, ok := parseKeyID(ctx)
	if !ok {
		ctx.JSON(http.StatusNotFound, keyNotFound())
		return
	}
	var request renameKeyRequest
	if err := ctx.ShouldBindJSON(&request); err != nil || request.Label == "" {
		ctx.JSON(http.StatusBadRequest, validationError("label", []string{"Label is required."}))
		return
	}
	label, err := accountkeys.LabelOrFallback(request.Label, "")
	if err != nil {
		c.writeFailure(ctx, err, "")
		return
	}
	account, ok := accountID(user)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	key, err := c.d.Keys.RenamePGPKey(reqCtx, account, id, label)
	if err != nil {
		c.writeFailure(ctx, err, "")
		return
	}
	c.actionLog(reqCtx, user.Id, model.ActionLogPGPKeyRename,
		keyMeta("pgp", key.Id, key.Label, key.Fingerprint), ctx.Request, currentSessionID(reqCtx))
	ctx.JSON(http.StatusOK, key)
}

// DELETE /api/keys/pgp/:id
func (c *controller) deletePGP(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	id, ok := parseKeyID(ctx)
	if !ok {
		ctx.JSON(http.StatusNotFound, keyNotFound())
		return
	}
	account, ok := accountID(user)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	if err := c.d.Keys.DeletePGPKey(reqCtx, account, id); err != nil {
		c.writeFailure(ctx, err, "")
		return
	}
	c.actionLog(reqCtx, user.Id, model.ActionLogPGPKeyDelete,
		keyMeta("pgp", id.String(), "", ""), ctx.Request, currentSessionID(reqCtx))
	ctx.Status(http.StatusNoContent)
}

// ── public listing ──────────────────────────────────────────────────────────

// GET /api/accounts/:name/keys
func (c *controller) publicKeys(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	name := ctx.Param("name")
	var account *model.Account
	var err error
	if id, parseErr := uuid.Parse(name); parseErr == nil {
		account, err = c.d.Accounts.GetAccountByID(reqCtx, id)
	} else {
		account, err = c.d.Accounts.GetAccountByName(reqCtx, name)
	}
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			ctx.JSON(http.StatusNotFound, errs.New("NOT_FOUND", "The requested account was not found.", http.StatusNotFound))
			return
		}
		ctx.JSON(http.StatusInternalServerError, internalError())
		return
	}
	accountID, parseErr := uuid.Parse(account.Id)
	if parseErr != nil {
		ctx.JSON(http.StatusInternalServerError, internalError())
		return
	}
	sshKeys, err := c.d.Keys.ListSSHKeys(reqCtx, accountID)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, internalError())
		return
	}
	pgpKeys, err := c.d.Keys.ListPGPKeys(reqCtx, accountID)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, internalError())
		return
	}
	ctx.JSON(http.StatusOK, model.AccountKeys{SSH: sshKeys, PGP: pgpKeys})
}
