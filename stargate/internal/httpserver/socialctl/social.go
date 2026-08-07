// Package socialctl ports DysonNetwork.Padlock's social login surface
// (Auth/OpenId): OidcController (GET /api/auth/login/{provider},
// POST /api/auth/login/apple/mobile) plus ConnectionController's OAuth
// callback and Apple mobile-connect endpoints.
package socialctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/internal/actionlog"
	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/spell"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

const (
	stateCachePrefix     = "oidc-state:"
	returnURLCachePrefix = "oidc-returning:"
	stateExpiration      = 15 * time.Minute

	flowLogin   = 0
	flowConnect = 1
)

// Deps carries the dependencies used by the social-login routes.
type Deps struct {
	Store *store.Store
	Redis *redis.Client
	Cfg   *config.Config
	Auth  *auth.AuthService
	Logs  *actionlog.Service
	Log   *slog.Logger
	Spells *spell.Service
}

// Register wires the social login routes (OidcController +
// ConnectionController OAuth endpoints). The GET/DELETE /api/connections CRUD
// lives in SecurityCtl.
func Register(api *gin.RouterGroup, d Deps) {
	api.GET("/auth/login/:provider", d.oidcLogin)
	api.POST("/auth/login/apple/mobile", d.appleMobileLogin)
	api.GET("/auth/callback/:provider", d.handleCallback)
	api.POST("/auth/callback/:provider", d.handleCallback)
	api.POST("/auth/connect/apple/mobile", middleware.RequireAuth(), d.connectAppleMobile)
}

// oidcState mirrors OidcState. The C# cache serializer emits snake_case with
// the flow type as an int (0=Login, 1=Connect); the legacy string format
// (OidcState.Serialize, camelCase, or the pipe format) is handled by
// parseOidcState.
type oidcState struct {
	FlowType  int     `json:"flow_type"`
	AccountId *string `json:"account_id"`
	Provider  *string `json:"provider"`
	Nonce     *string `json:"nonce"`
	DeviceId  *string `json:"device_id"`
	ReturnUrl *string `json:"return_url"`
	Version   int     `json:"version"`
}

// tokenExchangeResponse mirrors AuthController.TokenExchangeResponse.
type tokenExchangeResponse struct {
	Token            string `json:"token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
}

// appleMobileConnectRequest mirrors AppleMobileConnectRequest.
type appleMobileConnectRequest struct {
	IdentityToken     string `json:"identity_token" binding:"required"`
	AuthorizationCode string `json:"authorization_code" binding:"required"`
}

// appleMobileSignInRequest mirrors AppleMobileSignInRequest (extends the
// connect request with device fields).
type appleMobileSignInRequest struct {
	IdentityToken     string  `json:"identity_token" binding:"required"`
	AuthorizationCode string  `json:"authorization_code" binding:"required"`
	DeviceId          string  `json:"device_id" binding:"required"`
	DeviceName        *string `json:"device_name"`
}

// ─────────────────────────── GET /api/auth/login/{provider} ───────────────────────────

func (d Deps) oidcLogin(c *gin.Context) {
	provider := c.Param("provider")
	returnURL := c.DefaultQuery("returnUrl", "/")
	deviceID := c.Query("deviceId")
	flow := c.Query("flow")
	if d.Log != nil {
		d.Log.Info("OIDC login request", "provider", provider, "returnUrl", returnURL, "deviceId", deviceID, "flow", flow)
	}

	svc, err := newProvider(provider, d)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.New("OIDC_INIT_FLOW_FAILED", "Error initiating OpenID Connect flow: "+err.Error(), http.StatusBadRequest))
		return
	}
	state := uuid.NewString()
	nonce := uuid.NewString()
	ctx := c.Request.Context()

	// Authenticated users with a non-login flow start an account-connection
	// flow (mirrors OidcController.OidcLogin).
	if flow != "login" {
		if currentUser := middleware.CurrentUser(ctx); currentUser != nil {
			st := &oidcState{
				FlowType:  flowConnect,
				AccountId: &currentUser.Id,
				Provider:  &provider,
				Nonce:     &nonce,
				DeviceId:  strPtr(deviceID),
				Version:   1,
			}
			if err := d.cacheSet(ctx, stateCachePrefix+state, st, stateExpiration); err != nil {
				c.JSON(http.StatusBadRequest, errs.New("OIDC_INIT_FLOW_FAILED", "Error initiating OpenID Connect flow: "+err.Error(), http.StatusBadRequest))
				return
			}
			authURL, err := svc.authorizationURL(ctx, state, nonce)
			if err != nil {
				c.JSON(http.StatusBadRequest, errs.New("OIDC_INIT_FLOW_FAILED", "Error initiating OpenID Connect flow: "+err.Error(), http.StatusBadRequest))
				return
			}
			c.Redirect(http.StatusFound, authURL)
			return
		}
	}

	st := &oidcState{FlowType: flowLogin, ReturnUrl: &returnURL, DeviceId: strPtr(deviceID), Version: 1}
	if err := d.cacheSet(ctx, stateCachePrefix+state, st, stateExpiration); err != nil {
		c.JSON(http.StatusBadRequest, errs.New("OIDC_INIT_FLOW_FAILED", "Error initiating OpenID Connect flow: "+err.Error(), http.StatusBadRequest))
		return
	}
	authURL, err := svc.authorizationURL(ctx, state, nonce)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.New("OIDC_INIT_FLOW_FAILED", "Error initiating OpenID Connect flow: "+err.Error(), http.StatusBadRequest))
		return
	}
	c.Redirect(http.StatusFound, authURL)
}

// ─────────────────────────── POST /api/auth/login/apple/mobile ───────────────────────────

func (d Deps) appleMobileLogin(c *gin.Context) {
	var req appleMobileSignInRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {err.Error()}}))
		return
	}
	if !d.appleAvailable() {
		c.JSON(http.StatusServiceUnavailable, errs.New("OIDC_APPLE_UNAVAILABLE", "Apple OIDC service not available.", http.StatusServiceUnavailable))
		return
	}
	ctx := c.Request.Context()
	svc, _ := newProvider("apple", d)

	userInfo, err := svc.processCallback(ctx, &callbackData{IdToken: req.IdentityToken, Code: req.AuthorizationCode})
	if err != nil {
		var invalid *idTokenValidationError
		if errors.As(err, &invalid) {
			c.JSON(http.StatusUnauthorized, errs.New("OIDC_INVALID_IDENTITY_TOKEN", "Invalid identity token: "+invalid.msg, http.StatusUnauthorized))
			return
		}
		c.JSON(http.StatusInternalServerError, errs.New("OIDC_AUTHENTICATION_FAILED", "Authentication failed: "+err.Error(), http.StatusInternalServerError))
		return
	}

	account, err := d.findOrCreateAccount(ctx, userInfo, "apple")
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("OIDC_AUTHENTICATION_FAILED", "Authentication failed: "+err.Error(), http.StatusInternalServerError))
		return
	}

	session, err := d.createSessionForUser(c, svc, userInfo, account, req.DeviceId, req.DeviceName, model.ClientPlatformIos, middleware.CurrentSession(ctx))
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("OIDC_AUTHENTICATION_FAILED", "Authentication failed: "+err.Error(), http.StatusInternalServerError))
		return
	}
	pair, err := d.Auth.CreateTokenPair(ctx, session)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("OIDC_AUTHENTICATION_FAILED", "Authentication failed: "+err.Error(), http.StatusInternalServerError))
		return
	}
	d.appendAuthCookies(c, pair)

	now := time.Now().UTC()
	c.JSON(http.StatusOK, tokenExchangeResponse{
		Token:            pair.AccessToken,
		RefreshToken:     pair.RefreshToken,
		ExpiresIn:        maxInt64(0, int64(pair.AccessTokenExpiresAt.Sub(now).Seconds())),
		RefreshExpiresIn: maxInt64(0, int64(pair.RefreshTokenExpiresAt.Sub(now).Seconds())),
	})
}

// ─────────────────────────── GET|POST /api/auth/callback/{provider} ───────────────────────────

func (d Deps) handleCallback(c *gin.Context) {
	provider := c.Param("provider")
	svc, err := newProvider(provider, d)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.New("OIDC_PROVIDER_NOT_SUPPORTED", fmt.Sprintf("Provider '%s' is not supported.", provider), http.StatusBadRequest))
		return
	}

	data := extractCallbackData(c)
	if data.State == "" {
		c.JSON(http.StatusBadRequest, errs.New("OIDC_STATE_MISSING", "State parameter is missing.", http.StatusBadRequest))
		return
	}

	ctx := c.Request.Context()
	stateKey := stateCachePrefix + data.State
	st, found := d.loadOidcState(ctx, stateKey)
	if !found {
		if d.Log != nil {
			d.Log.Warn("Invalid or expired OIDC state", "state", data.State)
		}
		c.JSON(http.StatusBadRequest, errs.New("OIDC_STATE_INVALID", "Invalid or expired state parameter.", http.StatusBadRequest))
		return
	}
	// Remove the state to prevent replay attacks.
	d.cacheRemove(ctx, stateKey)

	if st.FlowType == flowConnect && st.AccountId != nil {
		d.handleManualConnection(c, svc, data, *st.AccountId, data.State)
		return
	}
	if st.FlowType == flowLogin {
		if st.ReturnUrl == nil || *st.ReturnUrl == "" || *st.ReturnUrl == "/" {
			d.handleLoginOrRegistration(c, svc, data, st.DeviceId, data.State)
			return
		}
		// Remember the return URL for the login handler.
		_ = d.cacheSet(ctx, returnURLCachePrefix+data.State, *st.ReturnUrl, stateExpiration)
		d.handleLoginOrRegistration(c, svc, data, st.DeviceId, data.State)
		return
	}
	c.JSON(http.StatusBadRequest, errs.New("OIDC_UNSUPPORTED_FLOW_TYPE", "Unsupported flow type.", http.StatusBadRequest))
}

// handleManualConnection ports ConnectionController.HandleManualConnection:
// link the provider identity to the state's account and redirect back.
func (d Deps) handleManualConnection(c *gin.Context, svc provider, data *callbackData, accountID, stateToken string) {
	ctx := c.Request.Context()
	providerName := strings.ToLower(svc.name())

	userInfo, err := svc.processCallback(ctx, data)
	if err != nil {
		if d.Log != nil {
			d.Log.Error("Error processing OIDC callback during connection flow", "provider", providerName, "error", err)
		}
		c.JSON(http.StatusBadRequest, errs.New("OIDC_CALLBACK_PROCESS_FAILED", fmt.Sprintf("Error processing %s authentication: %s", providerName, err.Error()), http.StatusBadRequest))
		return
	}
	if userInfo.UserId == "" {
		c.JSON(http.StatusBadRequest, errs.New("OIDC_MISSING_USER_ID", fmt.Sprintf("%s did not return a valid user identifier.", providerName), http.StatusBadRequest))
		return
	}

	existing, err := d.Store.GetConnectionByProviderIdentifier(ctx, providerName, userInfo.UserId)
	if err == nil && existing != nil && existing.AccountId != accountID {
		c.JSON(http.StatusBadRequest, errs.New("OIDC_ACCOUNT_ALREADY_LINKED", fmt.Sprintf("This %s account is already linked to another user.", providerName), http.StatusBadRequest))
		return
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusInternalServerError, errs.New("OIDC_SAVE_CONNECTION_FAILED", fmt.Sprintf("Failed to save %s connection. Please try again.", providerName), http.StatusInternalServerError))
		return
	}

	created, err := d.Store.TouchConnectionTokens(ctx, accountID, providerName, userInfo.UserId, userInfo.AccessToken, userInfo.RefreshToken, userInfo.toMetadata(), time.Now().UTC())
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("OIDC_SAVE_CONNECTION_FAILED", fmt.Sprintf("Failed to save %s connection. Please try again.", providerName), http.StatusInternalServerError))
		return
	}

	if created {
		ua := c.Request.UserAgent()
		ip := middleware.ClientIP(c.Request)
		d.actionLogConnectionLink(ctx, accountID, providerName, &ua, &ip)
	}

	// Clean up the return URL and redirect.
	returnURL := strings.TrimRight(d.Cfg.SiteUrl, "/") + "/auth/success"
	if found, _ := d.cacheGet(ctx, returnURLCachePrefix+stateToken, &returnURL); found {
		// Keep the cached return URL (written into returnURL by cacheGet).
	}
	d.cacheRemove(ctx, returnURLCachePrefix+stateToken)

	if d.Log != nil {
		d.Log.Info("Redirecting after OIDC connection", "redirectUrl", returnURL)
	}
	c.Redirect(http.StatusFound, returnURL)
}

// handleLoginOrRegistration ports ConnectionController.HandleLoginOrRegistration:
// existing connection → sign in; otherwise find-or-create the account, link
// the connection, sign in, and redirect with the token pair in the query.
func (d Deps) handleLoginOrRegistration(c *gin.Context, svc provider, data *callbackData, deviceID *string, stateToken string) {
	ctx := c.Request.Context()
	providerName := strings.ToLower(svc.name())

	userInfo, err := svc.processCallback(ctx, data)
	if err != nil {
		if d.Log != nil {
			d.Log.Error("Error processing OIDC callback during login/registration flow", "provider", providerName, "error", err)
		}
		c.JSON(http.StatusBadRequest, errs.New("OIDC_CALLBACK_PROCESS_FAILED", "Error processing callback: "+err.Error(), http.StatusBadRequest))
		return
	}
	if userInfo.Email == "" || userInfo.UserId == "" {
		c.JSON(http.StatusBadRequest, errs.New("OIDC_MISSING_EMAIL_OR_USER_ID", fmt.Sprintf("Email or user ID is missing from %s's response.", providerName), http.StatusBadRequest))
		return
	}

	// Retrieve and clean up the return URL.
	returnURL := strings.TrimRight(d.Cfg.SiteUrl, "/") + "/auth/success"
	if found, _ := d.cacheGet(ctx, returnURLCachePrefix+stateToken, &returnURL); found {
		// Keep the cached return URL (written into returnURL by cacheGet).
	}
	d.cacheRemove(ctx, returnURLCachePrefix+stateToken)

	now := time.Now().UTC()

	conn, account, err := d.Store.GetConnectionWithAccount(ctx, providerName, userInfo.UserId)
	if err == nil && conn != nil {
		// Login an existing user.
		session, err := d.createSessionForUser(c, svc, userInfo, account, deviceIDOrEmpty(deviceID), nil, model.ClientPlatformWeb, middleware.CurrentSession(ctx))
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("OIDC_AUTHENTICATION_FAILED", "Authentication failed: "+err.Error(), http.StatusInternalServerError))
			return
		}
		pair, err := d.Auth.CreateTokenPair(ctx, session)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("OIDC_AUTHENTICATION_FAILED", "Authentication failed: "+err.Error(), http.StatusInternalServerError))
			return
		}
		d.appendAuthCookies(c, pair)
		redirectURL := buildLoginRedirectURL(returnURL, pair)
		if d.Log != nil {
			d.Log.Info("OIDC login successful", "userId", conn.AccountId, "redirectUrl", redirectURL)
		}
		c.Redirect(http.StatusFound, redirectURL)
		return
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusInternalServerError, errs.New("OIDC_AUTHENTICATION_FAILED", "Authentication failed: "+err.Error(), http.StatusInternalServerError))
		return
	}

	// Register a new user (or link the provider to an account found by email).
	account, err = d.Store.LookupAccount(ctx, userInfo.Email)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusInternalServerError, errs.New("OIDC_AUTHENTICATION_FAILED", "Authentication failed: "+err.Error(), http.StatusInternalServerError))
		return
	}
	if account == nil {
		account, err = d.createAccountFromSocial(ctx, userInfo)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errs.New("OIDC_AUTHENTICATION_FAILED", "Authentication failed: "+err.Error(), http.StatusInternalServerError))
			return
		}
	}
	if _, err := d.Store.UpsertConnection(ctx, account.Id, providerName, userInfo.UserId, userInfo.AccessToken, userInfo.RefreshToken, userInfo.toMetadata(), now); err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("OIDC_AUTHENTICATION_FAILED", "Authentication failed: "+err.Error(), http.StatusInternalServerError))
		return
	}

	session, err := d.createSessionForUser(c, svc, userInfo, account, deviceIDOrEmpty(deviceID), nil, model.ClientPlatformWeb, middleware.CurrentSession(ctx))
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("OIDC_AUTHENTICATION_FAILED", "Authentication failed: "+err.Error(), http.StatusInternalServerError))
		return
	}
	pair, err := d.Auth.CreateTokenPair(ctx, session)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("OIDC_AUTHENTICATION_FAILED", "Authentication failed: "+err.Error(), http.StatusInternalServerError))
		return
	}
	d.appendAuthCookies(c, pair)
	redirectURL := buildLoginRedirectURL(returnURL, pair)
	if d.Log != nil {
		d.Log.Info("OIDC registration successful", "userId", account.Id, "redirectUrl", redirectURL)
	}
	c.Redirect(http.StatusFound, redirectURL)
}

// ─────────────────────────── POST /api/auth/connect/apple/mobile ───────────────────────────

func (d Deps) connectAppleMobile(c *gin.Context) {
	currentUser := middleware.CurrentUser(c.Request.Context())
	if currentUser == nil {
		c.JSON(http.StatusUnauthorized, errs.New("AUTH_UNAUTHORIZED", "Authentication is required.", http.StatusUnauthorized))
		return
	}
	var req appleMobileConnectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {err.Error()}}))
		return
	}
	if !d.appleAvailable() {
		c.JSON(http.StatusServiceUnavailable, errs.New("OIDC_APPLE_UNAVAILABLE", "Apple OIDC service not available.", http.StatusServiceUnavailable))
		return
	}
	ctx := c.Request.Context()
	svc, _ := newProvider("apple", d)

	userInfo, err := svc.processCallback(ctx, &callbackData{IdToken: req.IdentityToken, Code: req.AuthorizationCode})
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.New("OIDC_APPLE_TOKEN_PROCESS_FAILED", "Error processing Apple token: "+err.Error(), http.StatusBadRequest))
		return
	}

	existing, err := d.Store.GetConnectionByProviderIdentifier(ctx, "apple", userInfo.UserId)
	if err == nil && existing != nil {
		message := "This Apple account is already linked to another user."
		if existing.AccountId == currentUser.Id {
			message = "This Apple account is already linked to your account."
		}
		c.JSON(http.StatusBadRequest, errs.New("OIDC_APPLE_ALREADY_LINKED", message, http.StatusBadRequest))
		return
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusInternalServerError, errs.New("OIDC_APPLE_TOKEN_PROCESS_FAILED", "Error processing Apple token: "+err.Error(), http.StatusInternalServerError))
		return
	}

	if err := d.Store.InsertConnection(ctx, currentUser.Id, "apple", userInfo.UserId, userInfo.AccessToken, userInfo.RefreshToken, userInfo.toMetadata(), time.Now().UTC()); err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("OIDC_APPLE_TOKEN_PROCESS_FAILED", "Error processing Apple token: "+err.Error(), http.StatusInternalServerError))
		return
	}

	ua := c.Request.UserAgent()
	ip := middleware.ClientIP(c.Request)
	d.actionLogConnectionLink(ctx, currentUser.Id, "apple", &ua, &ip)

	c.JSON(http.StatusOK, gin.H{"message": "Successfully connected Apple account."})
}

// ─────────────────────────── shared helpers ───────────────────────────

// findOrCreateAccount ports OidcController.FindOrCreateAccount.
func (d Deps) findOrCreateAccount(ctx context.Context, userInfo *userInfo, provider string) (*model.Account, error) {
	if userInfo.Email == "" {
		return nil, errors.New("Email is required for account creation")
	}
	now := time.Now().UTC()

	existing, err := d.Store.LookupAccount(ctx, userInfo.Email)
	if err == nil && existing != nil {
		created, err := d.Store.UpsertConnection(ctx, existing.Id, provider, userInfo.UserId, userInfo.AccessToken, userInfo.RefreshToken, userInfo.toMetadata(), now)
		if err != nil {
			return nil, err
		}
		if created {
			d.actionLogConnectionLink(ctx, existing.Id, provider, nil, nil)
		}
		return existing, nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	account, err := d.createAccountFromSocial(ctx, userInfo)
	if err != nil {
		return nil, err
	}
	if _, err := d.Store.UpsertConnection(ctx, account.Id, provider, userInfo.UserId, userInfo.AccessToken, userInfo.RefreshToken, userInfo.toMetadata(), now); err != nil {
		return nil, err
	}
	d.actionLogConnectionLink(ctx, account.Id, provider, nil, nil)
	return account, nil
}

// createSessionForUser ports OidcService.CreateSessionForUserAsync: ensures
// the provider connection row exists, creates the device + an Oidc session
// (30-day expiry, parent session linked when present) and records the NewLogin
// action log.
func (d Deps) createSessionForUser(c *gin.Context, svc provider, userInfo *userInfo, account *model.Account, deviceID string, deviceName *string, platform model.ClientPlatform, parentSession *model.AuthSession) (*model.AuthSession, error) {
	ctx := c.Request.Context()
	providerName := svc.name()
	now := time.Now().UTC()

	// The connection was created by the caller (FindOrCreateAccount /
	// HandleLoginOrRegistration); only insert when it is missing.
	_, err := d.Store.GetConnectionByAccountAndProvider(ctx, account.Id, providerName)
	if errors.Is(err, store.ErrNotFound) {
		if err := d.Store.InsertConnection(ctx, account.Id, providerName, userInfo.UserId, userInfo.AccessToken, userInfo.RefreshToken, userInfo.toMetadata(), now); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}

	device, err := d.Auth.GetOrCreateDevice(ctx, account.Id, deviceID, deviceName, platform)
	if err != nil {
		return nil, err
	}
	var clientID *uuid.UUID
	if device.Id != "" {
		if id, err := uuid.Parse(device.Id); err == nil {
			clientID = &id
		}
	}
	var parentID *uuid.UUID
	if parentSession != nil {
		if id, err := uuid.Parse(parentSession.Id); err == nil {
			parentID = &id
		}
	}

	session, err := d.Store.CreateOidcSession(ctx, account.Id, clientID, parentID, now.Add(30*24*time.Hour), now)
	if err != nil {
		return nil, err
	}

	if d.Logs != nil {
		ua := c.Request.UserAgent()
		ip := middleware.ClientIP(c.Request)
		sid := session.Id
		_ = d.Logs.Create(ctx, account.Id, model.ActionLogNewLogin, map[string]any{
			"session_type": model.SessionTypeOidc.String(),
			"provider":     providerName,
		}, ua, ip, nil, &sid)
	}
	return session, nil
}

// createAccountFromSocial ports AccountService.CreateAccount(OidcUserInfo):
// an available username is derived from the email prefix and the account is
// created with the provider's display name.
func (d Deps) createAccountFromSocial(ctx context.Context, userInfo *userInfo) (*model.Account, error) {
	if userInfo.Email == "" {
		return nil, errors.New("Email is required for account creation")
	}
	displayName := userInfo.DisplayName
	if displayName == "" {
		displayName = strings.TrimSpace(userInfo.FirstName + " " + userInfo.LastName)
	}
	baseName := strings.ToLower(strings.SplitN(userInfo.Email, "@", 2)[0])
	name, err := d.generateAvailableUsername(ctx, baseName)
	if err != nil {
		return nil, err
	}
	if displayName == "" {
		displayName = name
	}
	account, err := d.Store.CreateAccountFromSocial(ctx, name, displayName, userInfo.Email, userInfo.EmailVerified, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	// Mirror the magic-spell path: a provider-verified email (e.g. Google's
	// email_verified claim) activates the account immediately when no entry
	// tests are required; with tests required, activation is deferred to
	// Passport (accounts.activated, consumed in main.go). Failures are
	// logged, never failing the login — the account is already created and
	// the connection linked by the caller.
	if userInfo.EmailVerified && d.Spells != nil {
		if err := d.Spells.ActivateAccountAfterVerifiedContact(ctx, account.Id); err != nil {
			if d.Log != nil {
				d.Log.Error("activate account after social registration", "account_id", account.Id, "error", err)
			}
		}
	}
	return account, nil
}

// generateAvailableUsername ports AccountService.GenerateAvailableUsername:
// keep only letters/digits/_/- from the base name, then append a numeric
// suffix until the name is free.
func (d Deps) generateAvailableUsername(ctx context.Context, baseName string) (string, error) {
	var normalized strings.Builder
	for _, r := range baseName {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			normalized.WriteRune(r)
		}
	}
	base := normalized.String()
	if base == "" {
		base = "user"
	}
	candidate := base
	suffix := 1
	for {
		taken, err := d.Store.CheckAccountNameTaken(ctx, candidate)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s%d", base, suffix)
		suffix++
	}
}

// appendAuthCookies mirrors AppendAuthCookies (HttpOnly, SameSite=Lax,
// domain from config).
func (d Deps) appendAuthCookies(c *gin.Context, pair *auth.TokenPair) {
	c.SetCookie("AuthToken", pair.AccessToken, cookieMaxAge(pair.AccessTokenExpiresAt), "/", d.Cfg.Auth.CookieDomain, d.Cfg.Auth.CookieSecure, true)
	c.SetCookie("RefreshToken", pair.RefreshToken, cookieMaxAge(pair.RefreshTokenExpiresAt), "/", d.Cfg.Auth.CookieDomain, d.Cfg.Auth.CookieSecure, true)
}

// buildLoginRedirectURL ports ConnectionController.BuildLoginRedirectUrl: the
// token pair is appended to the return URL as query parameters.
func buildLoginRedirectURL(redirectBaseURL string, pair *auth.TokenPair) string {
	now := time.Now().UTC()
	q := url.Values{}
	q.Set("token", pair.AccessToken)
	q.Set("refreshToken", pair.RefreshToken)
	q.Set("expiresIn", strconv.FormatInt(maxInt64(0, int64(pair.AccessTokenExpiresAt.Sub(now).Seconds())), 10))
	q.Set("refreshExpiresIn", strconv.FormatInt(maxInt64(0, int64(pair.RefreshTokenExpiresAt.Sub(now).Seconds())), 10))
	sep := "?"
	if strings.Contains(redirectBaseURL, "?") {
		sep = "&"
	}
	return redirectBaseURL + sep + q.Encode()
}

// extractCallbackData ports ConnectionController.ExtractCallbackData. Gin
// already URL-decodes query/form values (the C# UnescapeDataString calls are
// therefore no-ops here).
func extractCallbackData(c *gin.Context) *callbackData {
	data := &callbackData{QueryParameters: map[string]string{}}
	if c.Request.Method == http.MethodGet {
		data.Code = c.Query("code")
		data.IdToken = c.Query("id_token")
		data.State = c.Query("state")
		for k, values := range c.Request.URL.Query() {
			if len(values) > 0 {
				data.QueryParameters[k] = values[0]
			}
		}
		return data
	}
	if c.Request.Method == http.MethodPost && isFormContentType(c.Request.Header.Get("Content-Type")) {
		data.Code = c.PostForm("code")
		data.IdToken = c.PostForm("id_token")
		data.State = c.PostForm("state")
		data.RawData = c.PostForm("user")
		for k, values := range c.Request.PostForm {
			if len(values) > 0 {
				data.QueryParameters[k] = values[0]
			}
		}
	}
	return data
}

func isFormContentType(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	return ct == "application/x-www-form-urlencoded" || ct == "multipart/form-data"
}

// loadOidcState reads the state object from the cache; on a miss it falls
// back to the legacy string format (OidcState.TryParse), mirroring the C#.
func (d Deps) loadOidcState(ctx context.Context, key string) (*oidcState, bool) {
	var st oidcState
	if found, err := d.cacheGet(ctx, key, &st); err == nil && found {
		return &st, true
	}
	var stateString string
	if found, err := d.cacheGet(ctx, key, &stateString); err == nil && found {
		return parseOidcState(stateString)
	}
	return nil, false
}

// parseOidcState ports OidcState.TryParse: JSON first, then the legacy pipe
// format.
func parseOidcState(stateString string) (*oidcState, bool) {
	if stateString == "" {
		return nil, false
	}
	if st, ok := parseOidcStateJSON(stateString); ok {
		return st, true
	}
	return parseOidcStateLegacy(stateString)
}

func parseOidcStateJSON(s string) (*oidcState, bool) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, false
	}
	normalized := make(map[string]any, len(raw))
	for k, v := range raw {
		normalized[camelToSnake(k)] = v
	}
	if v, ok := normalized["flow_type"]; ok {
		if name, isString := v.(string); isString {
			if strings.EqualFold(name, "connect") {
				normalized["flow_type"] = float64(flowConnect)
			} else {
				normalized["flow_type"] = float64(flowLogin)
			}
		}
	}
	b, err := json.Marshal(normalized)
	if err != nil {
		return nil, false
	}
	var st oidcState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, false
	}
	return &st, true
}

func parseOidcStateLegacy(s string) (*oidcState, bool) {
	parts := strings.Split(s, "|")
	// Connection flow: {accountId}|{provider}|{nonce}|{deviceId}|connect
	if len(parts) >= 5 {
		if _, err := uuid.Parse(parts[0]); err == nil && strings.EqualFold(parts[len(parts)-1], "connect") {
			st := &oidcState{FlowType: flowConnect, AccountId: &parts[0], Provider: &parts[1], Nonce: &parts[2], Version: 1}
			if parts[3] != "" {
				st.DeviceId = &parts[3]
			}
			return st, true
		}
	}
	// Login flow: {returnUrl}|{deviceId}|login (2-3 parts)
	if len(parts) >= 2 && len(parts) <= 3 && (len(parts) < 3 || strings.EqualFold(parts[len(parts)-1], "login")) {
		st := &oidcState{FlowType: flowLogin, ReturnUrl: &parts[0], Version: 1}
		if parts[1] != "" {
			st.DeviceId = &parts[1]
		}
		return st, true
	}
	// Legacy single-part: {returnUrl}
	if len(parts) == 1 {
		return &oidcState{FlowType: flowLogin, ReturnUrl: &parts[0], Version: 1}, true
	}
	return nil, false
}

func camelToSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r + ('a' - 'A'))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (d Deps) actionLogConnectionLink(ctx context.Context, accountID, provider string, userAgent, ipAddress *string) {
	if d.Logs == nil {
		return
	}
	_ = d.Logs.Create(ctx, accountID, model.ActionLogAccountConnectionLink, map[string]any{"provider": provider}, derefStr(userAgent), derefStr(ipAddress), nil, nil)
}

func (d Deps) appleAvailable() bool {
	return d.Cfg.Oidc.Apple.ClientId != "" && d.Cfg.Oidc.Apple.TeamId != ""
}

// ─────────────────────────── cache helpers (nil-safe when Redis is down) ───────────────────────────

func (d Deps) cacheGet(ctx context.Context, key string, dest any) (bool, error) {
	if d.Redis == nil || d.Redis.Cache == nil {
		return false, nil
	}
	return d.Redis.Cache.Get(ctx, key, dest)
}

func (d Deps) cacheSet(ctx context.Context, key string, value any, ttl time.Duration) error {
	if d.Redis == nil || d.Redis.Cache == nil {
		return errors.New("cache unavailable")
	}
	return d.Redis.Cache.Set(ctx, key, value, ttl)
}

func (d Deps) cacheRemove(ctx context.Context, key string) {
	if d.Redis == nil || d.Redis.Cache == nil {
		return
	}
	_ = d.Redis.Cache.Remove(ctx, key)
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deviceIDOrEmpty(deviceID *string) string {
	if deviceID == nil {
		return ""
	}
	return *deviceID
}

func cookieMaxAge(expiresAt time.Time) int {
	seconds := int(time.Until(expiresAt).Seconds())
	if seconds < 0 {
		return 0
	}
	return seconds
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
