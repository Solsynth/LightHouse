// Package securityctl ports Padlock's account-security HTTP surface:
// AccountSecurityController (factors incl. passkeys, sessions, devices,
// contacts, authorized-apps), ApiKeyController and ConnectionController.
//
// Routes mirror the C# controllers verbatim: same paths, DTOs (snake_case),
// error codes/messages and status codes. See the C# sources:
//
//	../DysonNetwork/DysonNetwork.Padlock/Account/AccountSecurityController.cs
//	../DysonNetwork/DysonNetwork.Padlock/Auth/ApiKeyController.cs
//	../DysonNetwork/DysonNetwork.Padlock/Auth/OpenId/ConnectionController.cs
package securityctl

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/actionlog"
	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/errs"
	"src.solsynth.dev/sosys/stargate/internal/grpcclient"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/permission"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/spell"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// Deps carries the dependencies the security controllers need.
type Deps struct {
	Store   *store.Store
	Redis   *redis.Client
	Cfg     *config.Config
	Auth    *auth.AuthService
	Token   *auth.TokenAuthService
	Perm    *permission.Service
	Logs    *actionlog.Service
	Clients *grpcclient.Clients
	Spells  *spell.Service
	Log     *slog.Logger
}

type controller struct {
	d Deps
}

// Register mounts the account-security routes on the /api router group.
func Register(api *gin.RouterGroup, d Deps) {
	c := &controller{d: d}

	// AccountSecurityController is [RequireInteractiveSession] at class
	// level: every route below rejects API-key tokens with 403.
	security := api.Group("", c.requireInteractive)

	// ── Identity ──
	security.GET("identity", c.getCurrentIdentity)

	// ── Auth factors ──
	security.GET("factors", c.getAuthFactors)
	security.POST("factors", c.createAuthFactor)
	security.POST("factors/passkey/start", c.startPasskeyRegistration)
	security.POST("factors/passkey/complete", c.completePasskeyRegistration)
	security.GET("factors/passkey", c.getPasskeys)
	security.PATCH("factors/passkey/:id", c.updatePasskey)
	security.DELETE("factors/passkey/:id", c.deletePasskey)
	security.POST("factors/:id/enable", c.enableAuthFactor)
	security.POST("factors/:id/disable", c.disableAuthFactor)
	security.DELETE("factors/:id", c.deleteAuthFactor)

	// ── Sessions ──
	security.GET("sessions", c.getSessions)
	security.GET("sessions/:id/children", c.getSessionChildren)
	security.DELETE("sessions/:id", c.deleteSession)
	security.DELETE("sessions/current", c.deleteCurrentSession)

	// ── Devices ──
	security.GET("devices", c.getDevices)
	security.DELETE("devices/:deviceId", c.deleteDevice)
	security.PATCH("devices/:deviceId/label", c.updateDeviceLabel)
	security.PATCH("devices/current/label", c.updateCurrentDeviceLabel)

	// ── Contacts ──
	security.GET("contacts", c.getContacts)
	security.POST("contacts", c.createContact)
	security.POST("contacts/:id/verify", c.verifyContact)
	security.POST("contacts/:id/primary", c.setPrimaryContact)
	security.POST("contacts/:id/public", c.setPublicContact)
	security.DELETE("contacts/:id/public", c.unsetPublicContact)
	security.DELETE("contacts/:id", c.deleteContact)

	// ── Authorized apps ──
	security.GET("authorized-apps", c.getAuthorizedApps)
	security.POST("authorized-apps/:id/scopes", c.authorizeAppScopes)
	security.DELETE("authorized-apps/:id", c.deauthorizeApp)

	// ── API keys (ApiKeyController; [Authorize] only, no interactive gate) ──
	api.GET("api-keys", c.listApiKeys)
	api.POST("api-keys", c.createApiKey)
	api.DELETE("api-keys/:id", c.revokeApiKey)
	api.POST("api-keys/:id/rotate", c.rotateApiKey)

	// ── Connections (ConnectionController; [Authorize] only) ──
	api.GET("connections", c.getConnections)
	api.DELETE("connections/:id", c.removeConnection)
	api.POST("connections/:id/visibility", c.setConnectionVisibility)
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// requireInteractive mirrors RequireInteractiveSession: API-key tokens are
// rejected with FORBIDDEN / 403 (ApiError.Unauthorized with forbidden: true).
func (c *controller) requireInteractive(ctx *gin.Context) {
	if middleware.CurrentTokenType(ctx.Request.Context()) == auth.TokenTypeApiKey {
		ctx.AbortWithStatusJSON(http.StatusForbidden, errs.New("FORBIDDEN", "Interactive session token required.", http.StatusForbidden))
		return
	}
	ctx.Next()
}

// unauthorized401 mirrors the C# controllers' inline 401 payload.
func unauthorized401() *errs.ApiError {
	return errs.New("UNAUTHORIZED", "Authentication is required.", http.StatusUnauthorized)
}

// authUnauthorized401 mirrors ConnectionController's 401 payload.
func authUnauthorized401() *errs.ApiError {
	return errs.New("AUTH_UNAUTHORIZED", "Authentication is required.", http.StatusUnauthorized)
}

// validationError mirrors ApiError.Validation with the C# default message.
func validationError(fieldErrors map[string][]string) *errs.ApiError {
	return &errs.ApiError{
		Code:    "VALIDATION_ERROR",
		Message: "One or more validation errors occurred.",
		Status:  http.StatusBadRequest,
		Errors:  fieldErrors,
	}
}

// notFoundResource mirrors ApiError.NotFound(resource): NOT_FOUND with the
// resource echoed in the message and detail.
func notFoundResource(resource string) *errs.ApiError {
	return &errs.ApiError{
		Code:    "NOT_FOUND",
		Message: fmt.Sprintf("The requested resource '%s' was not found.", resource),
		Status:  http.StatusNotFound,
		Detail:  &resource,
	}
}

func parseIDParam(ctx *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

func queryBool(ctx *gin.Context, key string, fallback bool) bool {
	raw, ok := ctx.GetQuery(key)
	if !ok || raw == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return parsed
}

// actionLog records an account action log with the current request context.
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
		c.d.Log.Warn("action log", "action", action, "error", err)
	}
}

func factorTypeString(t model.AuthFactorType) string {
	switch t {
	case model.AuthFactorTypePassword:
		return "Password"
	case model.AuthFactorTypeEmailCode:
		return "EmailCode"
	case model.AuthFactorTypeInAppCode:
		return "InAppCode"
	case model.AuthFactorTypeTimedCode:
		return "TimedCode"
	case model.AuthFactorTypePinCode:
		return "PinCode"
	case model.AuthFactorTypeRecoveryCode:
		return "RecoveryCode"
	case model.AuthFactorTypeNfcToken:
		return "NfcToken"
	case model.AuthFactorTypePasskey:
		return "Passkey"
	case model.AuthFactorTypeQrLogin:
		return "QrLogin"
	default:
		return strconv.Itoa(int(t))
	}
}

// newRecoveryCode mirrors Guid.NewGuid().ToString("N"): 32 lowercase hex chars.
func newRecoveryCode() string {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	id, _ := uuid.FromBytes(raw)
	return strings.ReplaceAll(id.String(), "-", "")
}

// newTotpSecret mirrors Base32Encoding.ToString(RandomNumberGenerator(20)):
// unpadded base32 of 20 random bytes.
func newTotpSecret() string {
	raw := make([]byte, 20)
	_, _ = rand.Read(raw)
	return base32Encode(raw)
}

const base32Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

// base32Encode is the RFC 4648 base32 encoder without padding.
func base32Encode(src []byte) string {
	var sb strings.Builder
	var buffer uint64
	var bits uint
	for _, b := range src {
		buffer = buffer<<8 | uint64(b)
		bits += 8
		for bits >= 5 {
			sb.WriteByte(base32Alphabet[(buffer>>(bits-5))&31])
			bits -= 5
		}
	}
	if bits > 0 {
		sb.WriteByte(base32Alphabet[(buffer<<(5-bits))&31])
	}
	return sb.String()
}

func queryIntDefault(ctx *gin.Context, key string, fallback int) int {
	raw := ctx.Query(key)
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 0 {
		return fallback
	}
	if parsed > 200 {
		return 200
	}
	return parsed
}

// rawJSONString binds a body that is a bare JSON string (the C# [FromBody]
// string parameters).
func rawJSONString(ctx *gin.Context) (string, bool) {
	raw, err := ctx.GetRawData()
	if err != nil {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func decodeBase64OrURL(value string) ([]byte, error) {
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.RawURLEncoding.DecodeString(value)
}

// ---------------------------------------------------------------------------
// Identity
// ---------------------------------------------------------------------------

// GET /api/identity — the current account (AccountSecurityController).
func (c *controller) getCurrentIdentity(ctx *gin.Context) {
	user := middleware.CurrentUser(ctx.Request.Context())
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	account, err := c.d.Store.GetAccountByID(ctx.Request.Context(), uuid.MustParse(user.Id))
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load account.", http.StatusInternalServerError))
		return
	}
	ctx.JSON(http.StatusOK, account)
}

// ---------------------------------------------------------------------------
// Auth factors
// ---------------------------------------------------------------------------

// GET /api/factors
func (c *controller) getAuthFactors(ctx *gin.Context) {
	user := middleware.CurrentUser(ctx.Request.Context())
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	factors, err := c.d.Store.ListAllFactors(ctx.Request.Context(), uuid.MustParse(user.Id))
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load auth factors.", http.StatusInternalServerError))
		return
	}
	ctx.JSON(http.StatusOK, factors)
}

type authFactorRequest struct {
	Type   model.AuthFactorType `json:"type"`
	Secret *string              `json:"secret,omitempty"`
}

// POST /api/factors
func (c *controller) createAuthFactor(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	var request authFactorRequest
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, validationError(map[string][]string{
			"factor": {"Invalid factor request body."},
		}))
		return
	}
	// Recovery code must be enabled before creating other auth factors.
	if request.Type != model.AuthFactorTypeRecoveryCode {
		enabled, err := c.d.Store.HasEnabledFactor(reqCtx, user.Id, model.AuthFactorTypeRecoveryCode)
		if err != nil {
			ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to check auth factors.", http.StatusInternalServerError))
			return
		}
		if !enabled {
			ctx.JSON(http.StatusBadRequest, validationError(map[string][]string{
				"factor": {"Recovery code must be enabled before creating other auth factors."},
			}))
			return
		}
	}
	exists, err := c.d.Store.CheckAuthFactorExists(reqCtx, user.Id, request.Type)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to check auth factors.", http.StatusInternalServerError))
		return
	}
	if exists {
		ctx.JSON(http.StatusBadRequest, validationError(map[string][]string{
			"factor": {fmt.Sprintf("Auth factor with type %d already exists.", int(request.Type))},
		}))
		return
	}

	factor, err := c.buildAuthFactor(reqCtx, user, request.Type, request.Secret)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, errs.New("PADLOCK_AUTH_FACTOR_INVALID", "Invalid factor request.", http.StatusBadRequest))
		return
	}
	factor, err = c.d.Store.InsertAuthFactor(reqCtx, factor)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to create auth factor.", http.StatusInternalServerError))
		return
	}
	session := middleware.CurrentSession(reqCtx)
	var sessionID *string
	if session != nil {
		sessionID = &session.Id
	}
	c.actionLog(reqCtx, user.Id, model.ActionLogAuthFactorCreate,
		map[string]any{"factor_type": factorTypeString(factor.Type)}, ctx.Request, sessionID)
	ctx.JSON(http.StatusOK, factor)
}

// buildAuthFactor mirrors AccountService.CreateAuthFactor.
func (c *controller) buildAuthFactor(ctx context.Context, account *model.Account, ftype model.AuthFactorType, secret *string) (*model.AuthFactor, error) {
	now := model.NewTime(time.Now().UTC())
	switch ftype {
	case model.AuthFactorTypeRecoveryCode:
		code := newRecoveryCode()
		return &model.AuthFactor{
			Type:            ftype,
			Trustworthy:     0,
			AccountId:       account.Id,
			Secret:          code,
			EnabledAt:       now,
			CreatedResponse: map[string]any{"recovery_code": code},
		}, nil
	case model.AuthFactorTypePassword:
		if secret == nil || strings.TrimSpace(*secret) == "" {
			return nil, errors.New("secret required")
		}
		hash, err := auth.HashPassword(*secret)
		if err != nil {
			return nil, err
		}
		return &model.AuthFactor{
			Type: ftype, Trustworthy: 1, AccountId: account.Id, Secret: hash, EnabledAt: now,
		}, nil
	case model.AuthFactorTypeEmailCode:
		return &model.AuthFactor{Type: ftype, Trustworthy: 2, AccountId: account.Id, EnabledAt: now}, nil
	case model.AuthFactorTypeInAppCode:
		return &model.AuthFactor{Type: ftype, Trustworthy: 2, AccountId: account.Id, EnabledAt: now}, nil
	case model.AuthFactorTypeTimedCode:
		totpSecret := ""
		if secret != nil && strings.TrimSpace(*secret) != "" {
			totpSecret = strings.TrimSpace(*secret)
		} else {
			totpSecret = newTotpSecret()
		}
		label := account.Name
		if label == "" {
			label = account.Id
		}
		uri := fmt.Sprintf("otpauth://totp/SolarNetwork:%s?secret=%s&issuer=SolarNetwork&digits=6&period=30",
			url.PathEscape(label), totpSecret)
		return &model.AuthFactor{
			Type:            ftype,
			Trustworthy:     3,
			AccountId:       account.Id,
			Secret:          totpSecret,
			EnabledAt:       now,
			CreatedResponse: map[string]any{"secret": totpSecret, "uri": uri},
		}, nil
	case model.AuthFactorTypePinCode:
		if secret == nil || strings.TrimSpace(*secret) == "" {
			return nil, errors.New("secret required")
		}
		hash, err := auth.HashPassword(*secret)
		if err != nil {
			return nil, err
		}
		return &model.AuthFactor{
			Type: ftype, Trustworthy: 0, AccountId: account.Id, Secret: hash, EnabledAt: now,
		}, nil
	case model.AuthFactorTypeNfcToken:
		factor := &model.AuthFactor{Type: ftype, Trustworthy: 1, AccountId: account.Id, EnabledAt: now}
		if secret != nil && *secret != "" {
			factor.Config = map[string]any{"tag_id": *secret}
		}
		return factor, nil
	case model.AuthFactorTypePasskey:
		return &model.AuthFactor{Type: ftype, Trustworthy: 4, AccountId: account.Id, EnabledAt: now}, nil
	case model.AuthFactorTypeQrLogin:
		return &model.AuthFactor{Type: ftype, Trustworthy: 3, AccountId: account.Id, EnabledAt: now}, nil
	default:
		return nil, errors.New("unsupported factor type")
	}
}

// POST /api/factors/{id}/enable — body is a raw JSON string (the factor code).
func (c *controller) enableAuthFactor(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	id, ok := parseIDParam(ctx)
	if !ok {
		ctx.JSON(http.StatusNotFound, notFoundResource(ctx.Param("id")))
		return
	}
	factor, err := c.d.Store.GetAuthFactorByID(reqCtx, user.Id, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			ctx.JSON(http.StatusNotFound, notFoundResource(id.String()))
			return
		}
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load auth factor.", http.StatusInternalServerError))
		return
	}
	var code *string
	if raw, err := ctx.GetRawData(); err == nil && len(strings.TrimSpace(string(raw))) > 0 {
		var parsed string
		if json.Unmarshal(raw, &parsed) == nil {
			code = &parsed
		}
	}
	if err := c.enableFactorLogic(reqCtx, user, factor, code); err != nil {
		ctx.JSON(http.StatusBadRequest, errs.New("PADLOCK_AUTH_FACTOR_OPERATION_FAILED", err.Error(), http.StatusBadRequest))
		return
	}
	ctx.JSON(http.StatusOK, factor)
}

// enableFactorLogic mirrors AccountService.EnableAuthFactor.
func (c *controller) enableFactorLogic(ctx context.Context, account *model.Account, factor *model.AuthFactor, code *string) error {
	meta := map[string]any{"factor_type": factorTypeString(factor.Type)}
	if factor.Type == model.AuthFactorTypeRecoveryCode {
		newCode := newRecoveryCode()
		factor.Secret = newCode
		factor.EnabledAt = model.NewTime(time.Now().UTC())
		factor.CreatedResponse = map[string]any{"recovery_code": newCode}
		meta["regenerated"] = true
		if err := c.d.Store.UpdateAuthFactor(ctx, factor); err != nil {
			return err
		}
		c.actionLog(ctx, account.Id, model.ActionLogAuthFactorEnable, meta, nil, nil)
		return nil
	}
	switch factor.Type {
	case model.AuthFactorTypePassword, model.AuthFactorTypeTimedCode,
		model.AuthFactorTypePasskey, model.AuthFactorTypeQrLogin:
		factor.EnabledAt = model.NewTime(time.Now().UTC())
		if err := c.d.Store.UpdateAuthFactor(ctx, factor); err != nil {
			return err
		}
		c.actionLog(ctx, account.Id, model.ActionLogAuthFactorEnable, meta, nil, nil)
		return nil
	}
	// Code-verified factors: EmailCode / InAppCode / PinCode / NfcToken.
	if code == nil || *code == "" || !c.verifyFactorCode(ctx, factor, *code) {
		return errors.New("Invalid factor code.")
	}
	factor.EnabledAt = model.NewTime(time.Now().UTC())
	if err := c.d.Store.UpdateAuthFactor(ctx, factor); err != nil {
		return err
	}
	c.actionLog(ctx, account.Id, model.ActionLogAuthFactorEnable, meta, nil, nil)
	return nil
}

// verifyFactorCode mirrors AccountService.VerifyFactorCode.
func (c *controller) verifyFactorCode(ctx context.Context, factor *model.AuthFactor, code string) bool {
	switch factor.Type {
	case model.AuthFactorTypeEmailCode, model.AuthFactorTypeInAppCode:
		if c.d.Redis == nil {
			return false
		}
		key := "authfactor:" + factor.Id + ":code"
		var cached string
		found, err := c.d.Redis.Cache.Get(ctx, key, &cached)
		if err != nil || !found || cached != code {
			return false
		}
		_ = c.d.Redis.Cache.Remove(ctx, key)
		return true
	case model.AuthFactorTypePassword, model.AuthFactorTypePinCode:
		valid, err := auth.VerifyFactorPassword(factor, code)
		return err == nil && valid
	case model.AuthFactorTypeTimedCode:
		valid, err := auth.VerifyFactorPassword(factor, code)
		return err == nil && valid
	case model.AuthFactorTypeNfcToken:
		// NFC verification is delegated to Passport via gRPC; no client is
		// wired yet, so the code cannot be verified (degrades like the C#
		// with the RPC target down).
		return false
	default:
		return false
	}
}

// POST /api/factors/{id}/disable
func (c *controller) disableAuthFactor(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	id, ok := parseIDParam(ctx)
	if !ok {
		ctx.JSON(http.StatusNotFound, errs.New("PADLOCK_AUTH_FACTOR_NOT_FOUND", "Auth factor not found.", http.StatusNotFound))
		return
	}
	factor, err := c.d.Store.GetAuthFactorByID(reqCtx, user.Id, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			ctx.JSON(http.StatusNotFound, errs.New("PADLOCK_AUTH_FACTOR_NOT_FOUND", "Auth factor not found.", http.StatusNotFound))
			return
		}
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load auth factor.", http.StatusInternalServerError))
		return
	}
	factor.EnabledAt = nil
	if err := c.d.Store.UpdateAuthFactor(reqCtx, factor); err != nil {
		ctx.JSON(http.StatusBadRequest, errs.New("PADLOCK_AUTH_FACTOR_OPERATION_FAILED", err.Error(), http.StatusBadRequest))
		return
	}
	session := middleware.CurrentSession(reqCtx)
	var sessionID *string
	if session != nil {
		sessionID = &session.Id
	}
	c.actionLog(reqCtx, user.Id, model.ActionLogAuthFactorDisable,
		map[string]any{"factor_type": factorTypeString(factor.Type)}, ctx.Request, sessionID)
	ctx.JSON(http.StatusOK, factor)
}

// DELETE /api/factors/{id}
func (c *controller) deleteAuthFactor(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	id, ok := parseIDParam(ctx)
	if !ok {
		ctx.JSON(http.StatusNotFound, errs.New("PADLOCK_AUTH_FACTOR_NOT_FOUND", "Auth factor not found.", http.StatusNotFound))
		return
	}
	factor, err := c.d.Store.GetAuthFactorByID(reqCtx, user.Id, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			ctx.JSON(http.StatusNotFound, errs.New("PADLOCK_AUTH_FACTOR_NOT_FOUND", "Auth factor not found.", http.StatusNotFound))
			return
		}
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load auth factor.", http.StatusInternalServerError))
		return
	}
	if factor.Type == model.AuthFactorTypePasskey {
		if err := c.d.Store.DeletePasskeysByAccount(reqCtx, user.Id); err != nil {
			ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to delete passkeys.", http.StatusInternalServerError))
			return
		}
	}
	if err := c.d.Store.DeleteAuthFactorRow(reqCtx, id); err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to delete auth factor.", http.StatusInternalServerError))
		return
	}
	session := middleware.CurrentSession(reqCtx)
	var sessionID *string
	if session != nil {
		sessionID = &session.Id
	}
	c.actionLog(reqCtx, user.Id, model.ActionLogAuthFactorDelete,
		map[string]any{"factor_type": factorTypeString(factor.Type)}, ctx.Request, sessionID)
	ctx.Status(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Passkeys (WebAuthn via go-webauthn)
// ---------------------------------------------------------------------------

const passkeyChallengePrefix = "passkey:challenge:"
const passkeyChallengeTTL = 5 * time.Minute

// webauthnUser adapts model.Account to the go-webauthn User interface.
type webauthnUser struct {
	account *model.Account
}

func (u webauthnUser) WebAuthnID() []byte {
	id, _ := uuid.Parse(u.account.Id)
	return id[:]
}

func (u webauthnUser) WebAuthnName() string {
	return u.account.Name
}

func (u webauthnUser) WebAuthnDisplayName() string {
	if u.account.Nick != "" {
		return u.account.Nick
	}
	return u.account.Name
}

func (u webauthnUser) WebAuthnCredentials() []webauthn.Credential {
	return nil
}

// newWebAuthn builds a WebAuthn instance from Cfg.WebAuthn, mirroring the C#
// configuration lookups (WebAuthn:RpId / WebAuthn:RpName with host fallback).
func (c *controller) newWebAuthn(r *http.Request) (*webauthn.WebAuthn, error) {
	rpID := c.d.Cfg.WebAuthn.RpId
	if rpID == "" {
		rpID = r.Host
	}
	rpName := c.d.Cfg.WebAuthn.RpName
	if rpName == "" {
		rpName = "Solar Network"
	}
	origins := c.d.Cfg.WebAuthn.RelatedOrigins
	if len(origins) == 0 {
		origins = []string{c.d.Cfg.BaseUrl}
	}
	return webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPDisplayName: rpName,
		RPOrigins:     origins,
	})
}

type passkeyRegistrationStartRequest struct {
	DeviceId   string  `json:"device_id"`
	DeviceName *string `json:"device_name,omitempty"`
}

type publicKeyCredentialParameters struct {
	Type string `json:"type"`
	Alg  int    `json:"alg"`
}

type authenticatorSelectionCriteria struct {
	AuthenticatorAttachment string `json:"authenticator_attachment"`
	ResidentKey             string `json:"resident_key"`
	UserVerification        string `json:"user_verification"`
}

type passkeyRegistrationStartResponse struct {
	Challenge              string                          `json:"challenge"`
	RpId                   string                          `json:"rp_id"`
	RpName                 string                          `json:"rp_name"`
	UserId                 string                          `json:"user_id"`
	UserName               string                          `json:"user_name"`
	DisplayName            string                          `json:"display_name"`
	PubKeyCredParams       []publicKeyCredentialParameters `json:"pub_key_cred_params"`
	Timeout                int                             `json:"timeout"`
	AuthenticatorSelection *authenticatorSelectionCriteria `json:"authenticator_selection"`
}

// POST /api/factors/passkey/start
func (c *controller) startPasskeyRegistration(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	var request passkeyRegistrationStartRequest
	if err := ctx.ShouldBindJSON(&request); err != nil || request.DeviceId == "" {
		ctx.JSON(http.StatusBadRequest, validationError(map[string][]string{
			"device_id": {"Device id is required."},
		}))
		return
	}
	recoveryEnabled, err := c.d.Store.HasEnabledFactor(reqCtx, user.Id, model.AuthFactorTypeRecoveryCode)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to check auth factors.", http.StatusInternalServerError))
		return
	}
	if !recoveryEnabled {
		ctx.JSON(http.StatusBadRequest, validationError(map[string][]string{
			"factor": {"Recovery code must be enabled before creating passkey."},
		}))
		return
	}
	passkeyEnabled, err := c.d.Store.HasEnabledFactor(reqCtx, user.Id, model.AuthFactorTypePasskey)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to check auth factors.", http.StatusInternalServerError))
		return
	}
	if !passkeyEnabled {
		ctx.JSON(http.StatusBadRequest, validationError(map[string][]string{
			"factor": {"Passkey factor must be enabled before registering passkeys."},
		}))
		return
	}

	wa, err := c.newWebAuthn(ctx.Request)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "WebAuthn is not configured.", http.StatusInternalServerError))
		return
	}
	creation, sessionData, err := wa.BeginRegistration(webauthnUser{account: user},
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			AuthenticatorAttachment: protocol.Platform,
			ResidentKey:             protocol.ResidentKeyRequirementPreferred,
			UserVerification:        protocol.VerificationPreferred,
		}))
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to begin passkey registration.", http.StatusInternalServerError))
		return
	}
	_ = creation
	if c.d.Redis == nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Redis is not configured.", http.StatusInternalServerError))
		return
	}
	key := passkeyChallengePrefix + user.Id + ":" + request.DeviceId
	if err := c.d.Redis.Cache.Set(reqCtx, key, sessionData, passkeyChallengeTTL); err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to store passkey challenge.", http.StatusInternalServerError))
		return
	}
	displayName := user.Nick
	if displayName == "" {
		displayName = user.Name
	}
	ctx.JSON(http.StatusOK, passkeyRegistrationStartResponse{
		Challenge:        sessionData.Challenge,
		RpId:             wa.Config.RPID,
		RpName:           wa.Config.RPDisplayName,
		UserId:           user.Id,
		UserName:         user.Name,
		DisplayName:      displayName,
		PubKeyCredParams: []publicKeyCredentialParameters{{Type: "public-key", Alg: -7}},
		Timeout:          60000,
		AuthenticatorSelection: &authenticatorSelectionCriteria{
			AuthenticatorAttachment: "platform",
			ResidentKey:             "preferred",
			UserVerification:        "preferred",
		},
	})
}

type passkeyRegistrationCompleteRequest struct {
	DeviceId          string `json:"device_id"`
	ClientDataJson    string `json:"client_data_json"`
	AttestationObject string `json:"attestation_object"`
	Label             string `json:"label"`
}

// POST /api/factors/passkey/complete
func (c *controller) completePasskeyRegistration(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	var request passkeyRegistrationCompleteRequest
	if err := ctx.ShouldBindJSON(&request); err != nil || request.DeviceId == "" {
		ctx.JSON(http.StatusBadRequest, errs.New("PADLOCK_PASSKEY_REGISTRATION_FAILED", "Passkey registration failed.", http.StatusBadRequest))
		return
	}
	passkeyEnabled, err := c.d.Store.HasEnabledFactor(reqCtx, user.Id, model.AuthFactorTypePasskey)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to check auth factors.", http.StatusInternalServerError))
		return
	}
	if !passkeyEnabled {
		ctx.JSON(http.StatusBadRequest, errs.New("PADLOCK_PASSKEY_FACTOR_NOT_ENABLED", "Passkey factor is not enabled.", http.StatusBadRequest))
		return
	}
	if c.d.Redis == nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Redis is not configured.", http.StatusInternalServerError))
		return
	}
	key := passkeyChallengePrefix + user.Id + ":" + request.DeviceId
	var sessionData webauthn.SessionData
	found, err := c.d.Redis.Cache.Get(reqCtx, key, &sessionData)
	if err != nil || !found {
		ctx.JSON(http.StatusBadRequest, errs.New("PADLOCK_PASSKEY_REGISTRATION_FAILED", "Passkey registration failed.", http.StatusBadRequest))
		return
	}

	attestationBytes, err := decodeBase64OrURL(request.AttestationObject)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, errs.New("PADLOCK_PASSKEY_REGISTRATION_FAILED", "Passkey registration failed.", http.StatusBadRequest))
		return
	}
	clientData, err := decodeBase64OrURL(request.ClientDataJson)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, errs.New("PADLOCK_PASSKEY_REGISTRATION_FAILED", "Passkey registration failed.", http.StatusBadRequest))
		return
	}

	// Extract the credential id from the attestation object's auth data
	// (the C# client does not send a credential id in the request).
	var attObj protocol.AttestationObject
	if err := webauthncbor.Unmarshal(attestationBytes, &attObj); err != nil {
		ctx.JSON(http.StatusBadRequest, errs.New("PADLOCK_PASSKEY_REGISTRATION_FAILED", "Passkey registration failed.", http.StatusBadRequest))
		return
	}
	if err := attObj.AuthData.Unmarshal(attObj.RawAuthData); err != nil {
		ctx.JSON(http.StatusBadRequest, errs.New("PADLOCK_PASSKEY_REGISTRATION_FAILED", "Passkey registration failed.", http.StatusBadRequest))
		return
	}
	credentialID := attObj.AuthData.AttData.CredentialID
	if len(credentialID) == 0 {
		ctx.JSON(http.StatusBadRequest, errs.New("PADLOCK_PASSKEY_REGISTRATION_FAILED", "Passkey registration failed.", http.StatusBadRequest))
		return
	}

	// Synthesize the standard PublicKeyCredential JSON the go-webauthn parser
	// expects and verify the attestation.
	body, err := json.Marshal(map[string]any{
		"id":    base64.RawURLEncoding.EncodeToString(credentialID),
		"rawId": base64.RawURLEncoding.EncodeToString(credentialID),
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientData),
			"attestationObject": base64.RawURLEncoding.EncodeToString(attestationBytes),
		},
	})
	if err != nil {
		ctx.JSON(http.StatusBadRequest, errs.New("PADLOCK_PASSKEY_REGISTRATION_FAILED", "Passkey registration failed.", http.StatusBadRequest))
		return
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(body)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, errs.New("PADLOCK_PASSKEY_REGISTRATION_FAILED", "Passkey registration failed.", http.StatusBadRequest))
		return
	}
	wa, err := c.newWebAuthn(ctx.Request)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "WebAuthn is not configured.", http.StatusInternalServerError))
		return
	}
	credential, err := wa.CreateCredential(webauthnUser{account: user}, sessionData, parsed)
	if err != nil || credential == nil {
		ctx.JSON(http.StatusBadRequest, errs.New("PADLOCK_PASSKEY_REGISTRATION_FAILED", "Passkey registration failed.", http.StatusBadRequest))
		return
	}

	credentialIDBase64 := base64.StdEncoding.EncodeToString(credential.ID)
	exists, err := c.d.Store.PasskeyCredentialIDExists(reqCtx, credentialIDBase64)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to check passkeys.", http.StatusInternalServerError))
		return
	}
	if exists {
		ctx.JSON(http.StatusBadRequest, errs.New("PADLOCK_PASSKEY_ALREADY_REGISTERED", "Passkey is already registered.", http.StatusBadRequest))
		return
	}
	credentialJSON, err := json.Marshal(credential)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to store passkey.", http.StatusInternalServerError))
		return
	}
	passkey, err := c.d.Store.InsertPasskey(reqCtx, &model.Passkey{
		AccountId:    user.Id,
		Label:        request.Label,
		CredentialId: credentialIDBase64,
		Credential:   string(credentialJSON),
	})
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to store passkey.", http.StatusInternalServerError))
		return
	}
	_ = c.d.Redis.Cache.Remove(reqCtx, key)
	ctx.JSON(http.StatusOK, passkey)
}

// GET /api/factors/passkey
func (c *controller) getPasskeys(ctx *gin.Context) {
	user := middleware.CurrentUser(ctx.Request.Context())
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	passkeys, err := c.d.Store.ListPasskeys(ctx.Request.Context(), user.Id)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load passkeys.", http.StatusInternalServerError))
		return
	}
	ctx.JSON(http.StatusOK, passkeys)
}

type updatePasskeyRequest struct {
	Label string `json:"label"`
}

// PATCH /api/factors/passkey/{id}
func (c *controller) updatePasskey(ctx *gin.Context) {
	user := middleware.CurrentUser(ctx.Request.Context())
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	id, ok := parseIDParam(ctx)
	if !ok {
		ctx.JSON(http.StatusNotFound, notFoundResource(ctx.Param("id")))
		return
	}
	if _, err := c.d.Store.GetPasskeyByID(ctx.Request.Context(), user.Id, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			ctx.JSON(http.StatusNotFound, notFoundResource(id.String()))
			return
		}
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load passkey.", http.StatusInternalServerError))
		return
	}
	var request updatePasskeyRequest
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, validationError(map[string][]string{
			"label": {"Label is required."},
		}))
		return
	}
	passkey, err := c.d.Store.UpdatePasskeyLabel(ctx.Request.Context(), id, request.Label)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to update passkey.", http.StatusInternalServerError))
		return
	}
	ctx.JSON(http.StatusOK, passkey)
}

// DELETE /api/factors/passkey/{id}
func (c *controller) deletePasskey(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	id, ok := parseIDParam(ctx)
	if !ok {
		ctx.JSON(http.StatusNotFound, notFoundResource(ctx.Param("id")))
		return
	}
	if _, err := c.d.Store.GetPasskeyByID(reqCtx, user.Id, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			ctx.JSON(http.StatusNotFound, notFoundResource(id.String()))
			return
		}
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load passkey.", http.StatusInternalServerError))
		return
	}
	if err := c.d.Store.DeletePasskeyRow(reqCtx, id); err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to delete passkey.", http.StatusInternalServerError))
		return
	}
	ctx.Status(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

// GET /api/sessions
func (c *controller) getSessions(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	currentSession := middleware.CurrentSession(reqCtx)
	if user == nil || currentSession == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	take := queryIntDefault(ctx, "take", 20)
	offset := queryIntDefault(ctx, "offset", 0)
	includeChildren := queryBool(ctx, "includeChildren", false)

	var typ *model.SessionType
	if raw := ctx.Query("type"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			t := model.SessionType(parsed)
			typ = &t
		}
	}
	var clientID *uuid.UUID
	if raw := ctx.Query("clientId"); raw != "" {
		if parsed, err := uuid.Parse(raw); err == nil {
			clientID = &parsed
		}
	}
	sessions, total, err := c.d.Store.ListSessions(reqCtx, user.Id, typ, clientID, includeChildren, take, offset)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load sessions.", http.StatusInternalServerError))
		return
	}
	for i := range sessions {
		sessions[i].IsCurrent = sessions[i].Id == currentSession.Id
	}
	ctx.Header("X-Total", strconv.Itoa(total))
	ctx.Header("X-Auth-Session", currentSession.Id)
	ctx.JSON(http.StatusOK, sessions)
}

// GET /api/sessions/{id}/children
func (c *controller) getSessionChildren(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	currentSession := middleware.CurrentSession(reqCtx)
	if user == nil || currentSession == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	id, ok := parseIDParam(ctx)
	if !ok {
		ctx.JSON(http.StatusNotFound, notFoundResource(ctx.Param("id")))
		return
	}
	if _, err := c.d.Store.GetOwnedSession(reqCtx, user.Id, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			ctx.JSON(http.StatusNotFound, notFoundResource(id.String()))
			return
		}
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load session.", http.StatusInternalServerError))
		return
	}
	take := queryIntDefault(ctx, "take", 20)
	offset := queryIntDefault(ctx, "offset", 0)
	children, total, err := c.d.Store.ListSessionChildren(reqCtx, user.Id, id, take, offset)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load sessions.", http.StatusInternalServerError))
		return
	}
	for i := range children {
		children[i].IsCurrent = children[i].Id == currentSession.Id
	}
	ctx.Header("X-Total", strconv.Itoa(total))
	ctx.Header("X-Auth-Session", currentSession.Id)
	ctx.JSON(http.StatusOK, children)
}

// DELETE /api/sessions/{id}
func (c *controller) deleteSession(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	id, ok := parseIDParam(ctx)
	if !ok {
		ctx.JSON(http.StatusNotFound, notFoundResource(ctx.Param("id")))
		return
	}
	if _, err := c.d.Store.GetOwnedSession(reqCtx, user.Id, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			ctx.JSON(http.StatusNotFound, notFoundResource(id.String()))
			return
		}
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load session.", http.StatusInternalServerError))
		return
	}
	if _, err := c.d.Auth.RevokeSession(reqCtx, id); err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to revoke session.", http.StatusInternalServerError))
		return
	}
	_ = c.d.Token.RevokeJti(reqCtx, id.String())
	session := middleware.CurrentSession(reqCtx)
	var sessionID *string
	if session != nil {
		sessionID = &session.Id
	}
	c.actionLog(reqCtx, user.Id, model.ActionLogSessionRevoke,
		map[string]any{"session_id": id.String()}, ctx.Request, sessionID)
	ctx.Status(http.StatusNoContent)
}

// DELETE /api/sessions/current
func (c *controller) deleteCurrentSession(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	currentSession := middleware.CurrentSession(reqCtx)
	if user == nil || currentSession == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	sessionID, err := uuid.Parse(currentSession.Id)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to revoke session.", http.StatusInternalServerError))
		return
	}
	if _, err := c.d.Auth.RevokeSession(reqCtx, sessionID); err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to revoke session.", http.StatusInternalServerError))
		return
	}
	_ = c.d.Token.RevokeJti(reqCtx, currentSession.Id)
	c.actionLog(reqCtx, user.Id, model.ActionLogSessionRevoke,
		map[string]any{"session_id": currentSession.Id}, ctx.Request, &currentSession.Id)
	ctx.Status(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Devices
// ---------------------------------------------------------------------------

// deviceWithSessions mirrors SnAuthClientWithSessions.FromClient: only the
// client's core fields plus the bound sessions are serialized.
type deviceWithSessions struct {
	Id          string               `json:"id"`
	Platform    model.ClientPlatform `json:"platform"`
	DeviceName  string               `json:"device_name"`
	DeviceLabel *string              `json:"device_label,omitempty"`
	DeviceId    string               `json:"device_id"`
	AccountId   string               `json:"account_id"`
	Sessions    []model.AuthSession  `json:"sessions"`
}

// GET /api/devices
func (c *controller) getDevices(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	currentSession := middleware.CurrentSession(reqCtx)
	if user == nil || currentSession == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	take := queryIntDefault(ctx, "take", 20)
	offset := queryIntDefault(ctx, "offset", 0)
	devices, total, err := c.d.Store.ListDevices(reqCtx, user.Id, take, offset)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load devices.", http.StatusInternalServerError))
		return
	}
	ctx.Header("X-Total", strconv.Itoa(total))
	ctx.Header("X-Auth-Session", currentSession.Id)

	clientIDs := make([]uuid.UUID, 0, len(devices))
	for _, device := range devices {
		if id, err := uuid.Parse(device.Id); err == nil {
			clientIDs = append(clientIDs, id)
		}
	}
	sessionsByClient, err := c.d.Store.ListSessionsByClientIDs(reqCtx, clientIDs)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load sessions.", http.StatusInternalServerError))
		return
	}
	result := make([]deviceWithSessions, 0, len(devices))
	for _, device := range devices {
		item := deviceWithSessions{
			Id:          device.Id,
			Platform:    device.Platform,
			DeviceName:  device.DeviceName,
			DeviceLabel: device.DeviceLabel,
			DeviceId:    device.DeviceId,
			AccountId:   device.AccountId,
			Sessions:    []model.AuthSession{},
		}
		if sessions, ok := sessionsByClient[device.Id]; ok {
			item.Sessions = sessions
		}
		result = append(result, item)
	}
	ctx.JSON(http.StatusOK, result)
}

// DELETE /api/devices/{deviceId}
func (c *controller) deleteDevice(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	deviceID := ctx.Param("deviceId")
	if deviceID == "" {
		ctx.JSON(http.StatusNotFound, errs.New("PADLOCK_DEVICE_NOT_FOUND", "Device not found.", http.StatusNotFound))
		return
	}
	if err := c.d.Store.DeleteDevice(reqCtx, user.Id, deviceID, time.Now().UTC()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// The C# AccountService throws (500); we surface a 404 with the
			// same code the label routes use for a missing device.
			ctx.JSON(http.StatusNotFound, errs.New("PADLOCK_DEVICE_NOT_FOUND", "Device not found.", http.StatusNotFound))
			return
		}
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to delete device.", http.StatusInternalServerError))
		return
	}
	session := middleware.CurrentSession(reqCtx)
	var sessionID *string
	if session != nil {
		sessionID = &session.Id
	}
	c.actionLog(reqCtx, user.Id, model.ActionLogDeviceRevoke,
		map[string]any{"device_id": deviceID}, ctx.Request, sessionID)
	ctx.Status(http.StatusNoContent)
}

// PATCH /api/devices/{deviceId}/label — body is a raw JSON string.
func (c *controller) updateDeviceLabel(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	deviceID := ctx.Param("deviceId")
	label, ok := rawJSONString(ctx)
	if !ok {
		ctx.JSON(http.StatusBadRequest, validationError(map[string][]string{
			"label": {"Label is required."},
		}))
		return
	}
	if err := c.d.Store.UpdateDeviceName(reqCtx, user.Id, deviceID, label); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			ctx.JSON(http.StatusNotFound, errs.New("PADLOCK_DEVICE_NOT_FOUND", "Device not found.", http.StatusNotFound))
			return
		}
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to update device.", http.StatusInternalServerError))
		return
	}
	session := middleware.CurrentSession(reqCtx)
	var sessionID *string
	if session != nil {
		sessionID = &session.Id
	}
	c.actionLog(reqCtx, user.Id, model.ActionLogDeviceRename,
		map[string]any{"device_id": deviceID, "label": label}, ctx.Request, sessionID)
	ctx.Status(http.StatusNoContent)
}

// PATCH /api/devices/current/label — body is a raw JSON string.
func (c *controller) updateCurrentDeviceLabel(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	currentSession := middleware.CurrentSession(reqCtx)
	if user == nil || currentSession == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	if currentSession.ClientId == nil {
		ctx.JSON(http.StatusNotFound, errs.New("PADLOCK_DEVICE_NOT_FOUND", "Device not found.", http.StatusNotFound))
		return
	}
	device, err := c.d.Store.GetClientByID(reqCtx, uuid.MustParse(*currentSession.ClientId))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			ctx.JSON(http.StatusNotFound, errs.New("PADLOCK_DEVICE_NOT_FOUND", "Device not found.", http.StatusNotFound))
			return
		}
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load device.", http.StatusInternalServerError))
		return
	}
	label, ok := rawJSONString(ctx)
	if !ok {
		ctx.JSON(http.StatusBadRequest, validationError(map[string][]string{
			"label": {"Label is required."},
		}))
		return
	}
	if err := c.d.Store.UpdateDeviceName(reqCtx, user.Id, device.DeviceId, label); err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to update device.", http.StatusInternalServerError))
		return
	}
	c.actionLog(reqCtx, user.Id, model.ActionLogDeviceRename,
		map[string]any{"device_id": device.DeviceId, "label": label}, ctx.Request, &currentSession.Id)
	ctx.Status(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Contacts
// ---------------------------------------------------------------------------

// GET /api/contacts
func (c *controller) getContacts(ctx *gin.Context) {
	user := middleware.CurrentUser(ctx.Request.Context())
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	contacts, err := c.d.Store.ListContacts(ctx.Request.Context(), user.Id)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load contacts.", http.StatusInternalServerError))
		return
	}
	ctx.JSON(http.StatusOK, contacts)
}

type accountContactRequest struct {
	Type    model.ContactType `json:"type"`
	Content string            `json:"content"`
}

// POST /api/contacts
func (c *controller) createContact(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	var request accountContactRequest
	if err := ctx.ShouldBindJSON(&request); err != nil || request.Content == "" {
		ctx.JSON(http.StatusBadRequest, validationError(map[string][]string{
			"content": {"Content is required."},
		}))
		return
	}
	contact, err := c.d.Store.InsertContact(reqCtx, &model.Contact{
		AccountId: user.Id,
		Type:      int(request.Type),
		Content:   request.Content,
		IsPrimary: false,
	})
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to create contact.", http.StatusInternalServerError))
		return
	}
	ctx.JSON(http.StatusOK, contact)
}

func (c *controller) loadContact(ctx *gin.Context) (*model.Contact, bool) {
	user := middleware.CurrentUser(ctx.Request.Context())
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return nil, false
	}
	id, ok := parseIDParam(ctx)
	if !ok {
		ctx.JSON(http.StatusNotFound, errs.New("PADLOCK_CONTACT_NOT_FOUND", "Contact not found.", http.StatusNotFound))
		return nil, false
	}
	contact, err := c.d.Store.GetContactByID(ctx.Request.Context(), user.Id, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			ctx.JSON(http.StatusNotFound, errs.New("PADLOCK_CONTACT_NOT_FOUND", "Contact not found.", http.StatusNotFound))
			return nil, false
		}
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load contact.", http.StatusInternalServerError))
		return nil, false
	}
	return contact, true
}

// POST /api/contacts/{id}/verify
func (c *controller) verifyContact(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	contact, ok := c.loadContact(ctx)
	if !ok {
		return
	}
	if err := c.requestContactVerification(reqCtx, user, contact); err != nil {
		ctx.JSON(http.StatusBadRequest, errs.New("PADLOCK_CONTACT_VERIFICATION_FAILED", err.Error(), http.StatusBadRequest))
		return
	}
	ctx.JSON(http.StatusOK, contact)
}

// requestContactVerification mirrors AccountService.RequestContactVerification:
// creates a 24h contact-verification magic spell (preventRepeat) and emails
// it, mirroring the Passport MagicSpellService dispatch.
func (c *controller) requestContactVerification(ctx context.Context, account *model.Account, contact *model.Contact) error {
	if contact.AccountId != account.Id {
		return errors.New("Contact does not belong to the account.")
	}
	if contact.VerifiedAt != nil {
		return errors.New("Contact has already been verified.")
	}
	if contact.Type != int(model.ContactTypeEmail) {
		return errors.New("Only email contact methods can be verified.")
	}
	expiresAt := time.Now().UTC().Add(24 * time.Hour)
	spell, err := c.d.Spells.CreateMagicSpell(ctx, account.Id, model.MagicSpellTypeContactVerification, map[string]any{
		"contact_id":     contact.Id,
		"contact_type":   "Email",
		"contact_method": contact.Content,
	}, spell.CreateOptions{ExpiresAt: &expiresAt, PreventRepeat: true})
	if err != nil {
		return err
	}
	return c.d.Spells.NotifyMagicSpell(ctx, spell, true)
}

// POST /api/contacts/{id}/primary
func (c *controller) setPrimaryContact(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	contact, ok := c.loadContact(ctx)
	if !ok {
		return
	}
	if err := c.d.Store.SetContactPrimary(reqCtx, user.Id, contact.Type, uuid.MustParse(contact.Id)); err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to update contact.", http.StatusInternalServerError))
		return
	}
	contact.IsPrimary = true
	ctx.JSON(http.StatusOK, contact)
}

// POST /api/contacts/{id}/public
func (c *controller) setPublicContact(ctx *gin.Context) {
	c.setContactPublic(ctx, true)
}

// DELETE /api/contacts/{id}/public
func (c *controller) unsetPublicContact(ctx *gin.Context) {
	c.setContactPublic(ctx, false)
}

func (c *controller) setContactPublic(ctx *gin.Context, isPublic bool) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	contact, ok := c.loadContact(ctx)
	if !ok {
		return
	}
	contact.IsPublic = isPublic
	if err := c.d.Store.UpdateContact(ctx, contact); err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to update contact.", http.StatusInternalServerError))
		return
	}
	ctx.JSON(http.StatusOK, contact)
}

// DELETE /api/contacts/{id}
func (c *controller) deleteContact(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	contact, ok := c.loadContact(ctx)
	if !ok {
		return
	}
	if err := c.d.Store.DeleteContactRow(reqCtx, uuid.MustParse(contact.Id)); err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to delete contact.", http.StatusInternalServerError))
		return
	}
	ctx.Status(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Authorized apps
// ---------------------------------------------------------------------------

// authorizedAppResponse mirrors the C# AuthorizedAppResponse record.
type authorizedAppResponse struct {
	Id               string                            `json:"id"`
	AppId            string                            `json:"app_id"`
	Type             model.AuthorizedAppType           `json:"type"`
	AppSlug          *string                           `json:"app_slug,omitempty"`
	AppName          *string                           `json:"app_name,omitempty"`
	AppDescription   *string                           `json:"app_description,omitempty"`
	Picture          *model.SnCloudFileReferenceObject `json:"picture,omitempty"`
	Background       *model.SnCloudFileReferenceObject `json:"background,omitempty"`
	Scopes           []string                          `json:"scopes"`
	LastAuthorizedAt *model.Time                       `json:"last_authorized_at"`
	LastUsedAt       *model.Time                       `json:"last_used_at,omitempty"`
}

// GET /api/authorized-apps?type=
func (c *controller) getAuthorizedApps(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	var typ *model.AuthorizedAppType
	if raw := ctx.Query("type"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			t := model.AuthorizedAppType(parsed)
			typ = &t
		}
	}
	apps, err := c.d.Store.ListAuthorizedApps(reqCtx, user.Id, typ)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load authorized apps.", http.StatusInternalServerError))
		return
	}
	appDetails := c.fetchAppDetails(reqCtx, apps)
	response := make([]authorizedAppResponse, 0, len(apps))
	for _, app := range apps {
		detail, hasDetail := appDetails[app.AppId]
		response = append(response, authorizedAppResponse{
			Id:               app.Id,
			AppId:            app.AppId,
			Type:             app.Type,
			AppSlug:          nullCoalesce(app.AppSlug, appSlugOf(detail, hasDetail)),
			AppName:          nullCoalesce(app.AppName, appNameOf(detail, hasDetail)),
			AppDescription:   appDescriptionOf(detail, hasDetail),
			Picture:          appPictureOf(detail, hasDetail),
			Background:       appBackgroundOf(detail, hasDetail),
			Scopes:           app.Scopes,
			LastAuthorizedAt: app.LastAuthorizedAt,
			LastUsedAt:       app.LastUsedAt,
		})
	}
	ctx.JSON(http.StatusOK, response)
}

// fetchAppDetails resolves custom-app details from Develop, degrading
// gracefully when the client is unavailable or the app was deleted.
func (c *controller) fetchAppDetails(ctx context.Context, apps []model.AuthorizedApp) map[string]*gen.DyCustomApp {
	details := map[string]*gen.DyCustomApp{}
	if c.d.Clients == nil || c.d.Clients.Develop == nil {
		return details
	}
	seen := map[string]struct{}{}
	for _, app := range apps {
		if _, ok := seen[app.AppId]; ok {
			continue
		}
		seen[app.AppId] = struct{}{}
		resp, err := c.d.Clients.Develop.GetCustomApp(ctx, &gen.DyGetCustomAppRequest{
			Query: &gen.DyGetCustomAppRequest_Id{Id: app.AppId},
		})
		if err != nil || resp == nil || resp.App == nil {
			continue
		}
		details[app.AppId] = resp.App
	}
	return details
}

type authorizeAppScopesRequest struct {
	Scopes []string `json:"scopes"`
}

// POST /api/authorized-apps/{id}/scopes — permission AccountAuthorizedAppsManage.
func (c *controller) authorizeAppScopes(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	allowed, err := c.d.Perm.HasPermission(reqCtx, uuid.MustParse(user.Id), permission.AccountAuthorizedAppsManage)
	if err != nil || !allowed {
		ctx.JSON(http.StatusForbidden, errs.Forbidden("You do not have permission to perform this action."))
		return
	}
	id, ok := parseIDParam(ctx)
	if !ok {
		ctx.JSON(http.StatusNotFound, errs.New("PADLOCK_AUTHORIZED_APP_NOT_FOUND", "Authorized app was not found.", http.StatusNotFound))
		return
	}
	var request authorizeAppScopesRequest
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, errs.New("PADLOCK_AUTH_SCOPE_REQUIRED", "At least one scope is required.", http.StatusBadRequest))
		return
	}
	if len(request.Scopes) == 0 {
		ctx.JSON(http.StatusBadRequest, errs.New("PADLOCK_AUTH_SCOPE_REQUIRED", "At least one scope is required.", http.StatusBadRequest))
		return
	}

	var record *model.AuthorizedApp
	records, err := c.d.Store.ListAuthorizedApps(reqCtx, user.Id, nil)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load authorized apps.", http.StatusInternalServerError))
		return
	}
	for i := range records {
		if records[i].Id == id.String() {
			record = &records[i]
			break
		}
	}
	if record == nil {
		ctx.JSON(http.StatusNotFound, errs.New("PADLOCK_AUTHORIZED_APP_NOT_FOUND", "Authorized app was not found.", http.StatusNotFound))
		return
	}

	app, err := c.getCustomApp(reqCtx, record.AppId)
	if err != nil {
		ctx.JSON(http.StatusNotFound, errs.New("PADLOCK_APP_NOT_FOUND", "App was not found.", http.StatusNotFound))
		return
	}

	allowedScopes := map[string]struct{}{}
	if app.OauthConfig != nil {
		for _, scope := range app.OauthConfig.AllowedScopes {
			allowedScopes[scope] = struct{}{}
		}
	}
	// Port of: request.Scopes (trimmed, distinct) concat authorized.Scopes
	// (distinct); every scope must be allowed by the app.
	newScopes := make([]string, 0, len(request.Scopes))
	seen := map[string]struct{}{}
	for _, scope := range request.Scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if _, dup := seen[scope]; dup {
			continue
		}
		seen[scope] = struct{}{}
		newScopes = append(newScopes, scope)
	}
	merged := append([]string{}, record.Scopes...)
	merged = append(merged, newScopes...)
	distinct := make([]string, 0, len(merged))
	seenMerged := map[string]struct{}{}
	for _, scope := range merged {
		if _, dup := seenMerged[scope]; dup {
			continue
		}
		seenMerged[scope] = struct{}{}
		distinct = append(distinct, scope)
	}
	for _, scope := range distinct {
		if _, ok := allowedScopes[scope]; !ok {
			ctx.JSON(http.StatusBadRequest, errs.New("PADLOCK_AUTH_SCOPE_NOT_ALLOWED", "One or more scopes are not allowed by this app.", http.StatusBadRequest))
			return
		}
	}

	slug, name := appSlugName(app)
	authorized, err := c.d.Auth.UpsertAuthorizedAppAsync(reqCtx, user.Id, record.AppId, record.Type, slug, name, distinct)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to authorize app.", http.StatusInternalServerError))
		return
	}
	ctx.JSON(http.StatusOK, authorizedAppResponse{
		Id:               authorized.Id,
		AppId:            authorized.AppId,
		Type:             authorized.Type,
		AppSlug:          nullCoalesce(authorized.AppSlug, slug),
		AppName:          nullCoalesce(authorized.AppName, name),
		AppDescription:   appDescription(app),
		Picture:          appPicture(app),
		Background:       appBackground(app),
		Scopes:           authorized.Scopes,
		LastAuthorizedAt: authorized.LastAuthorizedAt,
		LastUsedAt:       authorized.LastUsedAt,
	})
}

// DELETE /api/authorized-apps/{id}?type= — permission AccountAuthorizedAppsManage.
func (c *controller) deauthorizeApp(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.JSON(http.StatusUnauthorized, unauthorized401())
		return
	}
	allowed, err := c.d.Perm.HasPermission(reqCtx, uuid.MustParse(user.Id), permission.AccountAuthorizedAppsManage)
	if err != nil || !allowed {
		ctx.JSON(http.StatusForbidden, errs.Forbidden("You do not have permission to perform this action."))
		return
	}
	id, ok := parseIDParam(ctx)
	if !ok {
		ctx.JSON(http.StatusNotFound, errs.New("PADLOCK_AUTHORIZED_APP_NOT_FOUND", "Authorized app was not found.", http.StatusNotFound))
		return
	}
	var typ *model.AuthorizedAppType
	if raw := ctx.Query("type"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			t := model.AuthorizedAppType(parsed)
			typ = &t
		}
	}
	count, err := c.d.Auth.RevokeAuthorizedAppAccessByIdAsync(reqCtx, user.Id, id.String(), typ)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to revoke authorized app.", http.StatusInternalServerError))
		return
	}
	if count == 0 {
		ctx.JSON(http.StatusNotFound, errs.New("PADLOCK_AUTHORIZED_APP_NOT_FOUND", "Authorized app was not found.", http.StatusNotFound))
		return
	}
	ctx.Status(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Custom-app gRPC helpers (Develop service; degrade when unavailable)
// ---------------------------------------------------------------------------

func (c *controller) getCustomApp(ctx context.Context, appID string) (*gen.DyCustomApp, error) {
	if c.d.Clients == nil || c.d.Clients.Develop == nil {
		return nil, errors.New("develop client not configured")
	}
	resp, err := c.d.Clients.Develop.GetCustomApp(ctx, &gen.DyGetCustomAppRequest{
		Query: &gen.DyGetCustomAppRequest_Id{Id: appID},
	})
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.App == nil {
		return nil, errors.New("app not found")
	}
	return resp.App, nil
}

func appSlugName(app *gen.DyCustomApp) (*string, *string) {
	var slug, name *string
	if app.Slug != "" {
		slug = &app.Slug
	}
	if app.Name != "" {
		name = &app.Name
	}
	return slug, name
}

func appDescription(app *gen.DyCustomApp) *string {
	if app.Description == "" {
		return nil
	}
	return &app.Description
}

func appPicture(app *gen.DyCustomApp) *model.SnCloudFileReferenceObject {
	if app.Picture == nil {
		return nil
	}
	return fileRefFromProto(app.Picture)
}

func appBackground(app *gen.DyCustomApp) *model.SnCloudFileReferenceObject {
	if app.Background == nil {
		return nil
	}
	return fileRefFromProto(app.Background)
}

func fileRefFromProto(file *gen.DyCloudFile) *model.SnCloudFileReferenceObject {
	ref := &model.SnCloudFileReferenceObject{
		Id:       file.Id,
		Url:      file.Url,
		MimeType: file.MimeType,
		Size:     &file.Size,
	}
	if file.Width != nil {
		w := int64(*file.Width)
		ref.Width = &w
	}
	if file.Height != nil {
		h := int64(*file.Height)
		ref.Height = &h
	}
	if file.Blurhash != nil {
		ref.Blurhash = *file.Blurhash
	}
	return ref
}

// nullCoalesce mirrors C# `??`: returns a when non-nil, else b.
func nullCoalesce(a, b *string) *string {
	if a != nil {
		return a
	}
	return b
}

func appSlugOf(detail *gen.DyCustomApp, has bool) *string {
	if !has {
		return nil
	}
	slug, _ := appSlugName(detail)
	return slug
}

func appNameOf(detail *gen.DyCustomApp, has bool) *string {
	if !has {
		return nil
	}
	_, name := appSlugName(detail)
	return name
}

func appDescriptionOf(detail *gen.DyCustomApp, has bool) *string {
	if !has {
		return nil
	}
	return appDescription(detail)
}

func appPictureOf(detail *gen.DyCustomApp, has bool) *model.SnCloudFileReferenceObject {
	if !has {
		return nil
	}
	return appPicture(detail)
}

func appBackgroundOf(detail *gen.DyCustomApp, has bool) *model.SnCloudFileReferenceObject {
	if !has {
		return nil
	}
	return appBackground(detail)
}
