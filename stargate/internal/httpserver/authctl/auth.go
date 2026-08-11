// Package authctl ports Padlock's auth + account HTTP controllers
// (AuthController, QrLoginController, CaptchaController,
// AccountController, AccountCurrentController pin-status,
// WebAuthnDiscoveryController) into Stargate. All routes live under
// /api/... and mirror the C# path/method/error contract exactly.
package authctl

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/big"
	mathrand "math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/geo"
	"src.solsynth.dev/sosys/stargate/internal/grpcclient"
	"src.solsynth.dev/sosys/stargate/internal/localization"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/spell"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// Deps bundles the services authctl uses. Only fields consumed by these
// controllers are included.
type Deps struct {
	Store   *store.Store
	Redis   *redis.Client
	Cfg     *config.Config
	Token   *auth.TokenAuthService
	Auth    *auth.AuthService
	Geo     *geo.Service
	Clients *grpcclient.Clients
	Events  auth.EventBus
	Spells  *spell.Service
	Log     *slog.Logger
}

type handler struct {
	d Deps
}

func (h *handler) requireCache(c *gin.Context) bool {
	if h.d.Redis != nil && h.d.Redis.Available() {
		return true
	}
	c.JSON(http.StatusServiceUnavailable, errs.New(
		"SERVICE_UNAVAILABLE", "This feature requires the cache service.", http.StatusServiceUnavailable))
	return false
}

// Register wires every Phase 4 auth/account route onto the /api group.
func Register(api *gin.RouterGroup, d Deps) {
	h := &handler{d: d}

	authGroup := api.Group("/auth")
	{
		authGroup.POST("/challenge", h.createChallenge)
		authGroup.GET("/challenge/:id", h.getChallenge)
		authGroup.GET("/challenge/:id/factors", h.getChallengeFactors)
		authGroup.POST("/challenge/:id/factors/:factorId", h.requestFactorCode)
		authGroup.PATCH("/challenge/:id", h.doChallenge)
		authGroup.POST("/challenge/:id/passkey/start", h.startPasskeyChallenge)
		authGroup.POST("/challenge/:id/passkey/complete", h.completePasskeyChallenge)
		authGroup.GET("/challenge/pending", middleware.RequireAuth(), middleware.RequireInteractive(), h.getPendingChallenges)
		authGroup.POST("/challenge/:id/approve", middleware.RequireAuth(), middleware.RequireInteractive(), h.approveChallenge)
		authGroup.POST("/challenge/:id/decline", middleware.RequireAuth(), middleware.RequireInteractive(), h.declineChallenge)
		authGroup.POST("/passkey/start", h.startPasskeyLogin)
		authGroup.POST("/passkey/:id/complete", h.completePasskeyLogin)
		authGroup.POST("/token", h.exchangeToken)
		authGroup.POST("/refresh", h.refreshToken)
		authGroup.POST("/captcha", h.validateCaptcha)
		authGroup.POST("/recover", h.recoverAccount)
		authGroup.POST("/logout", middleware.RequireAuth(), middleware.RequireInteractive(), h.logout)
		authGroup.POST("/login/session", middleware.RequireAuth(), middleware.RequireInteractive(), h.loginFromSession)
		authGroup.GET("/me", middleware.RequireAuth(), h.getCurrentAuthIdentity)
		authGroup.POST("/sudo", middleware.RequireAuth(), middleware.RequireInteractive(), h.enableSudoMode)
	}

	qr := api.Group("/auth/qr")
	{
		qr.POST("/generate", h.generateQrChallenge)
		qr.GET("/:id", h.getQrStatus)
		qr.POST("/:id/scan", middleware.RequireAuth(), middleware.RequireInteractive(), h.scanQrChallenge)
		qr.POST("/:id/approve", middleware.RequireAuth(), middleware.RequireInteractive(), h.approveQrChallenge)
		qr.POST("/:id/decline", middleware.RequireAuth(), middleware.RequireInteractive(), h.declineQrChallenge)
	}

	captcha := api.Group("/auth/captcha")
	{
		captcha.POST("/verify", h.captchaVerify)
		captcha.GET("", h.captchaGetConfig)
	}

	accounts := api.Group("/accounts")
	{
		accounts.POST("/validate", h.validateCreateAccount)
		accounts.POST("", h.createAccount)
	}

	// AccountCurrentController: GET/PATCH /api/accounts/me are owned by
	// profilectl (Phase 8 merged hydrated variant); only pin-status remains.
	api.GET("/accounts/me/pin-status", middleware.RequireAuth(), h.getPinStatus)

	// WebAuthnDiscoveryController (the /.well-known/webauthn route is
	// registered engine-level via RegisterWellKnown).
	api.GET("/auth/webauthn/config", h.webauthnConfig)
}

// detectChallengeRisk ports AuthService.DetectChallengeRisk: it computes the
// number of required authentication steps for a new challenge.
//
// Unlike the C# source, NfcToken and Passkey factors are excluded from the
// step count alongside PinCode/RecoveryCode/QrLogin: neither can satisfy a
// step of the username-challenge flow. Passkeys are only offered to the
// client through the separate discoverable-passkey flow
// (startPasskeyLogin), which mints its own single-step challenge, and NFC
// verification degrades to failure here (the Passport gRPC RPC is not
// ported). Counting them produced challenges whose StepTotal exceeded the
// factors the picker can complete, stranding the login after the last usable
// factor with an empty picker.
func (h *handler) detectChallengeRisk(ctx context.Context, accountID, ipAddress, userAgent string) (int, error) {
	factors, err := h.d.Store.GetAuthFactors(ctx, uuid.MustParse(accountID))
	if err != nil {
		return 0, err
	}
	enabledFactors := make([]model.AuthFactor, 0, len(factors))
	for _, f := range factors {
		ft := model.AuthFactorType(f.Type)
		if f.EnabledAt != nil && ft != model.AuthFactorTypePinCode &&
			ft != model.AuthFactorTypeRecoveryCode && ft != model.AuthFactorTypeQrLogin &&
			ft != model.AuthFactorTypeNfcToken && ft != model.AuthFactorTypePasskey {
			enabledFactors = append(enabledFactors, f)
		}
	}
	maxSteps := len(enabledFactors)
	if maxSteps == 0 {
		return 0, errors.New("Account has no authentication factors configured.")
	}
	// If password is the only factor, skip the risk calculation.
	allPassword := true
	for _, f := range enabledFactors {
		if model.AuthFactorType(f.Type) != model.AuthFactorTypePassword {
			allPassword = false
			break
		}
	}
	if allPassword {
		return 1, nil
	}

	riskScore := 0.0
	recentSessions, err := h.d.Store.ListRecentSessions(ctx, accountID, 10)
	if err != nil {
		return 0, err
	}
	recentChallengeIDs := make([]uuid.UUID, 0, len(recentSessions))
	for _, s := range recentSessions {
		if s.ChallengeID != nil {
			recentChallengeIDs = append(recentChallengeIDs, *s.ChallengeID)
		}
	}
	recentChallenges, err := h.d.Store.ListChallengesByIDs(ctx, recentChallengeIDs)
	if err != nil {
		return 0, err
	}

	if strings.TrimSpace(ipAddress) == "" {
		riskScore += 10
	} else {
		ipPreviouslyUsed := false
		for _, ch := range recentChallenges {
			if ch.IpAddress != nil && *ch.IpAddress == ipAddress {
				ipPreviouslyUsed = true
				break
			}
		}
		if !ipPreviouslyUsed {
			riskScore += 8
		}
		var lastKnownIP *string
		for _, ch := range recentChallenges {
			if ch.IpAddress != nil && *ch.IpAddress != "" {
				lastKnownIP = ch.IpAddress
				break
			}
		}
		if lastKnownIP != nil && *lastKnownIP != ipAddress {
			riskScore += 6
		}
	}

	if strings.TrimSpace(userAgent) == "" {
		riskScore += 5
	} else {
		uaPreviouslyUsed := false
		for _, ch := range recentChallenges {
			if ch.UserAgent != nil && *ch.UserAgent != "" && strings.EqualFold(*ch.UserAgent, userAgent) {
				uaPreviouslyUsed = true
				break
			}
		}
		if !uaPreviouslyUsed {
			riskScore += 4
		}
	}

	now := time.Now().UTC()
	if len(recentSessions) > 0 && recentSessions[0].LastGrantedAt != nil {
		hoursSinceLastLogin := now.Sub(recentSessions[0].LastGrantedAt.Time()).Hours()
		if hoursSinceLastLogin > 720 {
			riskScore += 9
		} else if hoursSinceLastLogin > 168 {
			riskScore += 6
		} else if hoursSinceLastLogin > 24 {
			riskScore += 3
		}
	} else {
		riskScore += 7
	}

	recentFailed, err := h.d.Store.SumRecentFailedChallengeAttempts(ctx, accountID, now.Add(-time.Hour))
	if err != nil {
		return 0, err
	}
	if recentFailed > 0 {
		riskScore += math.Min(float64(recentFailed)*2, 10)
	}

	totalAuthFactors := len(enabledFactors)
	timedCodeEnabled := false
	pinCodeEnabled := false
	for _, f := range enabledFactors {
		switch model.AuthFactorType(f.Type) {
		case model.AuthFactorTypeTimedCode:
			timedCodeEnabled = true
		case model.AuthFactorTypePinCode:
			pinCodeEnabled = true
		}
	}
	if totalAuthFactors >= 2 {
		riskScore -= 3
	} else if totalAuthFactors == 1 {
		riskScore -= 1
	}
	if timedCodeEnabled {
		riskScore -= 2
	}
	if pinCodeEnabled {
		riskScore -= 1
	}

	trustedDevice := false
	for _, s := range recentSessions {
		if s.ClientID != nil && s.CreatedAt.After(now.Add(-30*24*time.Hour)) {
			trustedDevice = true
			break
		}
	}
	if trustedDevice {
		riskScore -= 1
	}

	riskScore = math.Max(0, math.Min(riskScore, 20))
	riskWeight := 0.5
	if maxSteps > 0 {
		riskWeight = riskScore / 20.0
	}
	totalRequiredSteps := roundHalfToEven(float64(maxSteps) * riskWeight)
	if totalRequiredSteps > maxSteps {
		totalRequiredSteps = maxSteps
	}
	if totalRequiredSteps < 1 {
		totalRequiredSteps = 1
	}
	return totalRequiredSteps, nil
}

// roundHalfToEven mirrors Math.Round's default banker's rounding.
func roundHalfToEven(x float64) int {
	floor := math.Floor(x)
	diff := x - floor
	if diff > 0.5 {
		return int(floor + 1)
	}
	if diff < 0.5 {
		return int(floor)
	}
	if int(floor)%2 == 0 {
		return int(floor)
	}
	return int(floor + 1)
}

// ---------------------------------------------------------------------------
// Token exchange + cookies
// ---------------------------------------------------------------------------

// tokenExchangeResponse mirrors TokenExchangeResponse (AuthController.cs).
type tokenExchangeResponse struct {
	Token            string `json:"token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
}

func tokenExchangeBody(pair *auth.TokenPair) tokenExchangeResponse {
	now := time.Now()
	return tokenExchangeResponse{
		Token:            pair.AccessToken,
		RefreshToken:     pair.RefreshToken,
		ExpiresIn:        int64(math.Max(0, pair.AccessTokenExpiresAt.Sub(now).Seconds())),
		RefreshExpiresIn: int64(math.Max(0, pair.RefreshTokenExpiresAt.Sub(now).Seconds())),
	}
}

// setAuthCookies mirrors SetAuthCookies (HttpOnly, Secure, SameSite=Lax,
// domain from AuthToken:CookieDomain).
func (h *handler) setAuthCookies(c *gin.Context, pair *auth.TokenPair) {
	domain := h.d.Cfg.Auth.CookieDomain
	secure := h.d.Cfg.Auth.CookieSecure
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("AuthToken", pair.AccessToken, int(pair.AccessTokenExpiresAt.Sub(time.Now()).Seconds()), "/", domain, secure, true)
	c.SetCookie("RefreshToken", pair.RefreshToken, int(pair.RefreshTokenExpiresAt.Sub(time.Now()).Seconds()), "/", domain, secure, true)
}

func (h *handler) clearAuthCookies(c *gin.Context) {
	domain := h.d.Cfg.Auth.CookieDomain
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("AuthToken", "", -1, "/", domain, false, true)
	c.SetCookie("RefreshToken", "", -1, "/", domain, false, true)
}

// ---------------------------------------------------------------------------
// AuthController: challenge lifecycle
// ---------------------------------------------------------------------------

// challengeRequest mirrors ChallengeRequest.
type challengeRequest struct {
	Platform   model.ClientPlatform `json:"platform"`
	Account    string               `json:"account"`
	DeviceId   string               `json:"device_id"`
	DeviceName *string              `json:"device_name"`
	Audiences  []string             `json:"audiences"`
	Scopes     []string             `json:"scopes"`
}

func (h *handler) createChallenge(c *gin.Context) {
	ctx := c.Request.Context()
	var req challengeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("BAD_REQUEST", "Invalid request body."))
		return
	}

	account, err := h.d.Store.LookupAccount(ctx, req.Account)
	if err != nil || account == nil {
		c.JSON(http.StatusNotFound, errs.New("AUTH_ACCOUNT_NOT_FOUND", "Account was not found.", http.StatusNotFound))
		return
	}

	punishment, err := h.d.Store.GetActivePunishmentOverview(ctx, account.Id)
	if err != nil {
		h.logError("load punishment overview", err)
	}
	if punishment != nil && (punishment.Type == model.PunishmentDisableAccount || punishment.Type == model.PunishmentBlockLogin) {
		c.JSON(http.StatusLocked, &errs.ApiError{
			Code:    "ACCOUNT_LOCKED",
			Message: "Account is locked due to a punishment.",
			Detail:  &punishment.Reason,
			Status:  http.StatusLocked,
		})
		return
	}

	factors, err := h.d.Store.GetAuthFactors(ctx, uuid.MustParse(account.Id))
	if err != nil {
		h.logError("load auth factors", err)
	}
	hasAuthFactors := false
	for _, f := range factors {
		if f.EnabledAt != nil && model.AuthFactorType(f.Type) != model.AuthFactorTypeRecoveryCode {
			hasAuthFactors = true
			break
		}
	}
	if !hasAuthFactors {
		c.JSON(http.StatusForbidden, errs.New("NO_AUTH_FACTORS", "Account has no authentication factors configured.", http.StatusForbidden))
		return
	}

	now := time.Now().UTC()
	ipAddress := middleware.ClientIP(c.Request)
	userAgent := c.Request.UserAgent()
	deviceName := userAgent
	if req.DeviceName != nil {
		deviceName = *req.DeviceName
	}

	existing, err := h.d.Store.FindLiveChallenge(ctx, account.Id, ipAddress, userAgent, req.DeviceId)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		h.logError("find live challenge", err)
	}
	if existing != nil {
		c.JSON(http.StatusOK, existing)
		return
	}

	steps, err := h.detectChallengeRisk(ctx, account.Id, ipAddress, userAgent)
	if err != nil {
		c.JSON(http.StatusForbidden, errs.New("NO_AUTH_FACTORS", "Account has no authentication factors configured.", http.StatusForbidden))
		return
	}

	challenge := &model.AuthChallenge{
		Id:         uuid.NewString(),
		ExpiredAt:  model.NewTime(now.Add(time.Hour)),
		StepTotal:  steps,
		StepRemain: steps,
		Audiences:  req.Audiences,
		Scopes:     req.Scopes,
		IpAddress:  &ipAddress,
		UserAgent:  &userAgent,
		Location:   h.d.Geo.GetPointFromIp(ipAddress),
		DeviceId:   req.DeviceId,
		DeviceName: &deviceName,
		Platform:   req.Platform,
		AccountId:  account.Id,
		CreatedAt:  model.NewTime(now),
		UpdatedAt:  model.NewTime(now),
	}
	if err := h.d.Store.CreateAuthChallenge(ctx, challenge); err != nil {
		h.logError("create challenge", err)
		c.JSON(http.StatusInternalServerError, errs.New("SERVER_ERROR", "An internal server error occurred.", http.StatusInternalServerError))
		return
	}
	c.JSON(http.StatusOK, challenge)
}

func (h *handler) getChallenge(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		c.JSON(http.StatusNotFound, errs.New("AUTH_CHALLENGE_NOT_FOUND", "Auth challenge was not found.", http.StatusNotFound))
		return
	}
	challenge, err := h.d.Store.GetAuthChallenge(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, errs.New("AUTH_CHALLENGE_NOT_FOUND", "Auth challenge was not found.", http.StatusNotFound))
		return
	}
	c.JSON(http.StatusOK, challenge)
}

func (h *handler) getChallengeFactors(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		c.JSON(http.StatusNotFound, errs.New("AUTH_CHALLENGE_NOT_FOUND", "Auth challenge was not found.", http.StatusNotFound))
		return
	}
	challenge, err := h.d.Store.GetAuthChallenge(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, errs.New("AUTH_CHALLENGE_NOT_FOUND", "Auth challenge was not found.", http.StatusNotFound))
		return
	}
	factors, err := h.d.Store.GetAuthFactors(c.Request.Context(), uuid.MustParse(challenge.AccountId))
	if err != nil {
		h.logError("load challenge factors", err)
		c.JSON(http.StatusInternalServerError, errs.New("SERVER_ERROR", "An internal server error occurred.", http.StatusInternalServerError))
		return
	}
	result := make([]model.AuthFactor, 0, len(factors))
	for _, f := range factors {
		if f.EnabledAt != nil && f.Trustworthy >= 1 &&
			model.AuthFactorType(f.Type) != model.AuthFactorTypeRecoveryCode &&
			model.AuthFactorType(f.Type) != model.AuthFactorTypeQrLogin {
			result = append(result, f)
		}
	}
	c.JSON(http.StatusOK, result)
}

// performChallengeRequest mirrors PerformChallengeRequest.
type performChallengeRequest struct {
	FactorId string `json:"factor_id"`
	Password string `json:"password"`
}

func (h *handler) requestFactorCode(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		c.JSON(http.StatusNotFound, errs.New("AUTH_CHALLENGE_NOT_FOUND", "Auth challenge was not found.", http.StatusNotFound))
		return
	}
	factorID, ok := parseUUIDParam(c, "factorId")
	if !ok {
		c.JSON(http.StatusNotFound, errs.New("AUTH_FACTOR_NOT_FOUND", "Auth factor was not found.", http.StatusNotFound))
		return
	}
	challenge, err := h.d.Store.GetAuthChallenge(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, errs.New("AUTH_CHALLENGE_NOT_FOUND", "Auth challenge was not found.", http.StatusNotFound))
		return
	}
	factor, err := h.d.Store.GetAuthFactorByID(ctx, challenge.AccountId, factorID)
	if err != nil {
		c.JSON(http.StatusNotFound, errs.New("AUTH_FACTOR_NOT_FOUND", "Auth factor was not found.", http.StatusNotFound))
		return
	}
	account, err := h.d.Store.GetAccountByID(ctx, uuid.MustParse(challenge.AccountId))
	if err != nil {
		c.JSON(http.StatusNotFound, errs.New("AUTH_ACCOUNT_NOT_FOUND", "Account was not found.", http.StatusNotFound))
		return
	}
	if err := h.sendFactorCode(ctx, account, factor); err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_FACTOR_SEND_FAILED", err.Error()))
		return
	}
	c.Status(http.StatusOK)
}

// doChallenge ports PATCH /api/auth/challenge/{id}.
func (h *handler) doChallenge(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		c.JSON(http.StatusNotFound, errs.New("AUTH_CHALLENGE_NOT_FOUND", "Auth challenge was not found.", http.StatusNotFound))
		return
	}
	var req performChallengeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("BAD_REQUEST", "Invalid request body."))
		return
	}
	challenge, err := h.d.Store.GetAuthChallenge(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, errs.New("AUTH_CHALLENGE_NOT_FOUND", "Auth challenge was not found.", http.StatusNotFound))
		return
	}
	factorID, err := uuid.Parse(req.FactorId)
	if err != nil {
		c.JSON(http.StatusNotFound, errs.New("AUTH_FACTOR_NOT_FOUND", "Auth factor was not found.", http.StatusNotFound))
		return
	}
	factor, err := h.d.Store.GetAuthFactorByID(ctx, challenge.AccountId, factorID)
	if err != nil {
		c.JSON(http.StatusNotFound, errs.New("AUTH_FACTOR_NOT_FOUND", "Auth factor was not found.", http.StatusNotFound))
		return
	}
	if factor.EnabledAt == nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_FACTOR_NOT_ENABLED", "Auth factor is not enabled."))
		return
	}
	if factor.Trustworthy <= 0 {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_FACTOR_NOT_TRUSTWORTHY", "Auth factor is not trustworthy."))
		return
	}

	if challenge.StepRemain == 0 {
		c.JSON(http.StatusOK, challenge)
		return
	}

	now := time.Now().UTC()
	if challenge.ExpiredAt != nil && now.After(challenge.ExpiredAt.Time()) {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_CHALLENGE_EXPIRED", "Auth challenge has expired."))
		return
	}
	if containsString(challenge.BlacklistFactors, factor.Id) {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_FACTOR_ALREADY_USED", "Auth factor already used."))
		return
	}

	isFirstFactor := len(challenge.BlacklistFactors) == 0

	okVerify, _ := h.verifyFactorCode(ctx, factor, req.Password)
	if !okVerify {
		challenge.FailedAttempts++
		challenge.UpdatedAt = model.NewTime(now)
		_ = h.d.Store.UpdateAuthChallenge(ctx, challenge)
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_INVALID_PASSWORD", "Invalid password."))
		return
	}

	challenge.StepRemain -= factor.Trustworthy
	if challenge.StepRemain < 0 {
		challenge.StepRemain = 0
	}
	challenge.BlacklistFactors = append(challenge.BlacklistFactors, factor.Id)
	challenge.UpdatedAt = model.NewTime(now)
	if err := h.d.Store.UpdateAuthChallenge(ctx, challenge); err != nil {
		h.logError("update challenge", err)
	}

	if isFirstFactor && challenge.StepRemain > 0 {
		h.publishChallengePending(ctx, challenge)
	}
	if challenge.StepRemain == 0 {
		h.pushLoginNotification(ctx, challenge, true)
	}
	c.JSON(http.StatusOK, challenge)
}

// ---------------------------------------------------------------------------
// Factor verification (mirrors AccountService.VerifyFactorCode + SendFactorCode)
// ---------------------------------------------------------------------------

const authFactorCodePrefix = "authfactor:"

func (h *handler) verifyFactorCode(ctx context.Context, factor *model.AuthFactor, code string) (bool, error) {
	switch model.AuthFactorType(factor.Type) {
	case model.AuthFactorTypeEmailCode, model.AuthFactorTypeInAppCode:
		if h.d.Redis == nil || !h.d.Redis.Available() {
			return false, nil
		}
		var cached string
		found, err := h.d.Redis.Cache.Get(ctx, authFactorCodePrefix+factor.Id+":code", &cached)
		if err != nil || !found || cached != code {
			return false, nil
		}
		_ = h.d.Redis.Cache.Remove(ctx, authFactorCodePrefix+factor.Id+":code")
		return true, nil
	case model.AuthFactorTypePassword, model.AuthFactorTypePinCode, model.AuthFactorTypeTimedCode:
		return auth.VerifyFactorPassword(factor, code)
	default:
		// Passkey flows use their own endpoints; NFC tokens verify via the
		// Passport gRPC service which is not ported (degrades to failure,
		// matching the C# behavior when the RPC fails).
		return false, nil
	}
}

func (h *handler) sendFactorCode(ctx context.Context, account *model.Account, factor *model.AuthFactor) error {
	code := fmt.Sprintf("%06d", mathrand.IntN(900000)+100000)
	switch model.AuthFactorType(factor.Type) {
	case model.AuthFactorTypeInAppCode:
		if h.d.Redis == nil || !h.d.Redis.Available() {
			return errors.New("in-app factor code service is unavailable")
		}
		var cached string
		if found, _ := h.d.Redis.Cache.Get(ctx, authFactorCodePrefix+factor.Id+":code", &cached); found && cached != "" {
			return errors.New("A factor code has been sent and in active duration.")
		}
		if err := h.pushNotificationErr(ctx, account.Id, "auth.verification",
			localization.Localize(account.Language, "authCodeTitle", nil),
			localization.Localize(account.Language, "authCodeBody", map[string]string{"code": code}), false); err != nil {
			return err
		}
		return h.d.Redis.Cache.Set(ctx, authFactorCodePrefix+factor.Id+":code", code, 5*time.Minute)
	case model.AuthFactorTypeEmailCode:
		if h.d.Redis == nil || !h.d.Redis.Available() {
			return errors.New("email factor code service is unavailable")
		}
		var cached string
		if found, _ := h.d.Redis.Cache.Get(ctx, authFactorCodePrefix+factor.Id+":code", &cached); found && cached != "" {
			return errors.New("A factor code has been sent and in active duration.")
		}
		contact, err := h.d.Store.GetEmailContactForNotify(ctx, account.Id, true)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				if h.d.Log != nil {
					h.d.Log.Warn("email factor code not sent: no verified email contact",
						"factor_id", factor.Id, "account_id", account.Id)
				}
				return nil
			}
			return err
		}
		if h.d.Spells == nil {
			return errors.New("email factor code delivery is not configured")
		}
		if err := h.d.Spells.SendFactorCodeEmail(ctx, account, contact.Content, code); err != nil {
			return err
		}
		return h.d.Redis.Cache.Set(ctx, authFactorCodePrefix+factor.Id+":code", code, 30*time.Minute)
	default:
		return nil
	}
}

// ---------------------------------------------------------------------------
// Passkey assertion (ports AccountService.GeneratePasskeyAssertionChallengeAsync
// + VerifyPasskeyAssertionAsync with the P-256/ES256 verification)
// ---------------------------------------------------------------------------

// passkeyCredentialDescriptor mirrors AccountService.PasskeyCredentialDescriptor.
type passkeyCredentialDescriptor struct {
	Type       string   `json:"type"`
	Id         string   `json:"id"`
	Transports []string `json:"transports"`
}

// passkeyAuthenticationStartResponse mirrors PasskeyAuthenticationStartResponse.
type passkeyAuthenticationStartResponse struct {
	Challenge        string                        `json:"challenge"`
	RpId             string                        `json:"rp_id"`
	AllowCredentials []passkeyCredentialDescriptor `json:"allow_credentials"`
	Timeout          int                           `json:"timeout"`
	UserVerification string                        `json:"user_verification"`
}

// passkeyLoginStartResponse mirrors PasskeyLoginStartResponse.
type passkeyLoginStartResponse struct {
	passkeyAuthenticationStartResponse
	AuthChallengeId string `json:"auth_challenge_id"`
}

// passkeyAuthenticationCompleteRequest mirrors PasskeyAuthenticationCompleteRequest.
type passkeyAuthenticationCompleteRequest struct {
	CredentialId      string  `json:"credential_id"`
	ClientDataJson    string  `json:"client_data_json"`
	AuthenticatorData string  `json:"authenticator_data"`
	Signature         string  `json:"signature"`
	UserHandle        *string `json:"user_handle"`
}

func decodeBase64OrBase64Url(value string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(value); err == nil {
		return b, nil
	}
	normalized := strings.ReplaceAll(strings.ReplaceAll(value, "-", "+"), "_", "/")
	if pad := len(normalized) % 4; pad != 0 {
		normalized += strings.Repeat("=", 4-pad)
	}
	return base64.StdEncoding.DecodeString(normalized)
}

func normalizePasskeyCredentialId(value string) (string, bool) {
	raw, err := decodeBase64OrBase64Url(value)
	if err != nil {
		return "", false
	}
	return base64.StdEncoding.EncodeToString(raw), true
}

func parsePasskeyCredential(credentialJSON string) *model.PasskeyCredential {
	var cred model.PasskeyCredential
	if err := json.Unmarshal([]byte(credentialJSON), &cred); err != nil {
		return nil
	}
	return &cred
}

func (h *handler) generatePasskeyAssertionChallenge(ctx context.Context, challengeID string) (string, error) {
	if h.d.Redis == nil || !h.d.Redis.Available() {
		return "", errors.New("passkey service is unavailable")
	}
	challengeBytes := make([]byte, 32)
	if _, err := rand.Read(challengeBytes); err != nil {
		return "", err
	}
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes)
	if err := h.d.Redis.Cache.Set(ctx, "passkey:assertion:"+challengeID, challenge, 5*time.Minute); err != nil {
		return "", err
	}
	return challenge, nil
}

func (h *handler) verifyPasskeyAssertion(ctx context.Context, cred *model.PasskeyCredential, challengeID, credentialID, clientDataJson, authenticatorData, signature string) bool {
	if h.d.Redis == nil || !h.d.Redis.Available() {
		return false
	}
	var storedChallenge string
	found, err := h.d.Redis.Cache.Get(ctx, "passkey:assertion:"+challengeID, &storedChallenge)
	if err != nil || !found || storedChallenge == "" {
		return false
	}
	challengeKey := "passkey:assertion:" + challengeID

	if cred.CredentialId != credentialID {
		return false
	}
	authData, err := decodeBase64OrBase64Url(authenticatorData)
	if err != nil || len(authData) < 37 {
		return false
	}
	if authData[32]&0x01 == 0 {
		return false
	}

	clientDataBytes, err := decodeClientDataJsonBytes(clientDataJson)
	if err != nil {
		return false
	}
	clientDataHash := sha256.Sum256(clientDataBytes)
	signedData := make([]byte, 0, 37+len(clientDataHash))
	signedData = append(signedData, authData[:37]...)
	signedData = append(signedData, clientDataHash[:]...)

	sig, err := decodeBase64OrBase64Url(signature)
	if err != nil {
		return false
	}
	pub := &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(cred.PublicKeyX),
		Y:     new(big.Int).SetBytes(cred.PublicKeyY),
	}
	if !ecdsa.VerifyASN1(pub, signedData, sig) {
		return false
	}

	var clientData struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(clientDataBytes, &clientData); err != nil {
		return false
	}
	if clientData.Type != "webauthn.get" {
		return false
	}
	if clientData.Challenge != storedChallenge {
		return false
	}
	_ = h.d.Redis.Cache.Remove(ctx, challengeKey)
	return true
}

func decodeClientDataJsonBytes(value string) ([]byte, error) {
	b, err := decodeBase64OrBase64Url(value)
	if err != nil {
		return []byte(value), nil
	}
	return b, nil
}

// ---------------------------------------------------------------------------
// Challenge passkey endpoints
// ---------------------------------------------------------------------------

func (h *handler) startPasskeyChallenge(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		c.JSON(http.StatusNotFound, errs.New("AUTH_CHALLENGE_NOT_FOUND", "Auth challenge was not found.", http.StatusNotFound))
		return
	}
	challenge, err := h.d.Store.GetAuthChallenge(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, errs.New("AUTH_CHALLENGE_NOT_FOUND", "Auth challenge was not found.", http.StatusNotFound))
		return
	}
	factors, err := h.d.Store.GetAuthFactors(ctx, uuid.MustParse(challenge.AccountId))
	if err != nil {
		h.logError("load factors", err)
	}
	var passkeyFactor *model.AuthFactor
	for i := range factors {
		f := &factors[i]
		if model.AuthFactorType(f.Type) == model.AuthFactorTypePasskey && f.EnabledAt != nil &&
			f.Trustworthy > 0 && !containsString(challenge.BlacklistFactors, f.Id) {
			passkeyFactor = f
			break
		}
	}
	if passkeyFactor == nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_PASSKEY_FACTOR_NOT_AVAILABLE", "No available passkey factor was found."))
		return
	}
	if challenge.StepRemain == 0 {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_CHALLENGE_ALREADY_COMPLETED", "Auth challenge already completed."))
		return
	}
	now := time.Now().UTC()
	if challenge.ExpiredAt != nil && now.After(challenge.ExpiredAt.Time()) {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_CHALLENGE_EXPIRED", "Auth challenge has expired."))
		return
	}
	passkeys, err := h.d.Store.ListPasskeys(ctx, challenge.AccountId)
	if err != nil {
		h.logError("list passkeys", err)
	}
	if len(passkeys) == 0 {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_NO_PASSKEYS_REGISTERED", "No passkeys are registered for this account."))
		return
	}
	assertionChallenge, err := h.generatePasskeyAssertionChallenge(ctx, challenge.Id)
	if err != nil {
		h.logError("generate passkey assertion challenge", err)
		c.JSON(http.StatusInternalServerError, errs.New("SERVER_ERROR", "An internal server error occurred.", http.StatusInternalServerError))
		return
	}
	credentials := make([]passkeyCredentialDescriptor, 0, len(passkeys))
	for _, pk := range passkeys {
		credentials = append(credentials, passkeyCredentialDescriptor{
			Type:       "public-key",
			Id:         pk.CredentialId,
			Transports: []string{"internal", "hybrid", "usb", "nfc", "ble"},
		})
	}
	c.JSON(http.StatusOK, passkeyAuthenticationStartResponse{
		Challenge:        assertionChallenge,
		RpId:             h.passkeyRpId(c),
		AllowCredentials: credentials,
		Timeout:          60000,
		UserVerification: "preferred",
	})
}

func (h *handler) completePasskeyChallenge(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		c.JSON(http.StatusNotFound, errs.New("AUTH_CHALLENGE_NOT_FOUND", "Auth challenge was not found.", http.StatusNotFound))
		return
	}
	var req passkeyAuthenticationCompleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("BAD_REQUEST", "Invalid request body."))
		return
	}
	challenge, err := h.d.Store.GetAuthChallenge(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, errs.New("AUTH_CHALLENGE_NOT_FOUND", "Auth challenge was not found.", http.StatusNotFound))
		return
	}
	factor, err := h.d.Store.GetAuthFactorByType(ctx, challenge.AccountId, model.AuthFactorTypePasskey)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_PASSKEY_FACTOR_NOT_ENABLED", "Passkey factor is not enabled."))
		return
	}
	if factor.EnabledAt == nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_FACTOR_NOT_ENABLED", "Auth factor is not enabled."))
		return
	}
	if factor.Trustworthy <= 0 {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_FACTOR_NOT_TRUSTWORTHY", "Auth factor is not trustworthy."))
		return
	}
	if challenge.StepRemain == 0 {
		c.JSON(http.StatusOK, challenge)
		return
	}
	now := time.Now().UTC()
	if challenge.ExpiredAt != nil && now.After(challenge.ExpiredAt.Time()) {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_CHALLENGE_EXPIRED", "Auth challenge has expired."))
		return
	}
	if containsString(challenge.BlacklistFactors, factor.Id) {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_FACTOR_ALREADY_USED", "Auth factor already used."))
		return
	}
	credentialID, ok := normalizePasskeyCredentialId(req.CredentialId)
	if !ok {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_PASSKEY_CREDENTIAL_INVALID", "Passkey credential ID is invalid."))
		return
	}
	passkey, err := h.d.Store.GetPasskeyByAccountAndCredentialID(ctx, challenge.AccountId, credentialID)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_PASSKEY_NOT_REGISTERED", "Passkey is not registered for this account."))
		return
	}
	credential := parsePasskeyCredential(passkey.Credential)
	if credential == nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_PASSKEY_INVALID", "Passkey is invalid."))
		return
	}

	isFirstFactor := len(challenge.BlacklistFactors) == 0

	if !h.verifyPasskeyAssertion(ctx, credential, challenge.Id, credentialID,
		req.ClientDataJson, req.AuthenticatorData, req.Signature) {
		challenge.FailedAttempts++
		challenge.UpdatedAt = model.NewTime(now)
		_ = h.d.Store.UpdateAuthChallenge(ctx, challenge)
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_INVALID_PASSKEY_ASSERTION", "Invalid passkey assertion."))
		return
	}

	challenge.StepRemain -= factor.Trustworthy
	if challenge.StepRemain < 0 {
		challenge.StepRemain = 0
	}
	challenge.BlacklistFactors = append(challenge.BlacklistFactors, factor.Id)
	challenge.UpdatedAt = model.NewTime(now)
	if err := h.d.Store.UpdateAuthChallenge(ctx, challenge); err != nil {
		h.logError("update challenge", err)
	}

	if isFirstFactor && challenge.StepRemain > 0 {
		h.publishChallengePending(ctx, challenge)
	}
	if challenge.StepRemain == 0 {
		h.pushLoginNotification(ctx, challenge, true)
	}
	c.JSON(http.StatusOK, challenge)
}

// passkeyLoginStartRequest mirrors PasskeyLoginStartRequest.
type passkeyLoginStartRequest struct {
	Platform   model.ClientPlatform `json:"platform"`
	DeviceId   string               `json:"device_id"`
	DeviceName *string              `json:"device_name"`
	Audiences  []string             `json:"audiences"`
	Scopes     []string             `json:"scopes"`
}

func (h *handler) startPasskeyLogin(c *gin.Context) {
	ctx := c.Request.Context()
	var req passkeyLoginStartRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("BAD_REQUEST", "Invalid request body."))
		return
	}
	now := time.Now().UTC()
	ipAddress := middleware.ClientIP(c.Request)
	userAgent := c.Request.UserAgent()
	deviceName := userAgent
	if req.DeviceName != nil {
		deviceName = *req.DeviceName
	}
	challenge := &model.AuthChallenge{
		Id:         uuid.NewString(),
		StepTotal:  1,
		StepRemain: 1,
		DeviceId:   req.DeviceId,
		DeviceName: &deviceName,
		Platform:   req.Platform,
		IpAddress:  &ipAddress,
		UserAgent:  &userAgent,
		Location:   h.d.Geo.GetPointFromIp(ipAddress),
		Audiences:  req.Audiences,
		Scopes:     req.Scopes,
		AccountId:  uuid.Nil.String(),
		ExpiredAt:  model.NewTime(now.Add(5 * time.Minute)),
		CreatedAt:  model.NewTime(now),
		UpdatedAt:  model.NewTime(now),
	}
	if err := h.d.Store.CreateAuthChallenge(ctx, challenge); err != nil {
		h.logError("create passkey challenge", err)
		c.JSON(http.StatusInternalServerError, errs.New("SERVER_ERROR", "An internal server error occurred.", http.StatusInternalServerError))
		return
	}
	assertionChallenge, err := h.generatePasskeyAssertionChallenge(ctx, challenge.Id)
	if err != nil {
		h.logError("generate passkey assertion challenge", err)
		c.JSON(http.StatusInternalServerError, errs.New("SERVER_ERROR", "An internal server error occurred.", http.StatusInternalServerError))
		return
	}
	c.JSON(http.StatusOK, passkeyLoginStartResponse{
		passkeyAuthenticationStartResponse: passkeyAuthenticationStartResponse{
			Challenge:        assertionChallenge,
			RpId:             h.passkeyRpId(c),
			AllowCredentials: []passkeyCredentialDescriptor{},
			Timeout:          60000,
			UserVerification: "preferred",
		},
		AuthChallengeId: challenge.Id,
	})
}

func (h *handler) completePasskeyLogin(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		c.JSON(http.StatusNotFound, errs.New("AUTH_CHALLENGE_NOT_FOUND", "Auth challenge was not found.", http.StatusNotFound))
		return
	}
	var req passkeyAuthenticationCompleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("BAD_REQUEST", "Invalid request body."))
		return
	}
	challenge, err := h.d.Store.GetAuthChallenge(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, errs.New("AUTH_CHALLENGE_NOT_FOUND", "Auth challenge was not found.", http.StatusNotFound))
		return
	}
	if challenge.AccountId != uuid.Nil.String() || challenge.StepRemain == 0 {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_CHALLENGE_NOT_PENDING", "Auth challenge is no longer pending."))
		return
	}
	now := time.Now().UTC()
	if challenge.ExpiredAt != nil && now.After(challenge.ExpiredAt.Time()) {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_CHALLENGE_EXPIRED", "Auth challenge has expired."))
		return
	}
	credentialID, ok := normalizePasskeyCredentialId(req.CredentialId)
	if !ok {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_PASSKEY_CREDENTIAL_INVALID", "Passkey credential ID is invalid."))
		return
	}
	passkey, err := h.d.Store.GetPasskeyByCredentialID(ctx, credentialID)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_PASSKEY_NOT_FOUND", "Passkey was not found."))
		return
	}
	factor, err := h.d.Store.GetAuthFactorByType(ctx, passkey.AccountId, model.AuthFactorTypePasskey)
	if err != nil || factor.EnabledAt == nil || factor.Trustworthy <= 0 {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_PASSKEY_FACTOR_NOT_ENABLED", "Passkey factor is not enabled for this account."))
		return
	}
	credential := parsePasskeyCredential(passkey.Credential)
	if credential == nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_PASSKEY_INVALID", "Passkey is invalid."))
		return
	}
	if !h.verifyPasskeyAssertion(ctx, credential, challenge.Id, credentialID,
		req.ClientDataJson, req.AuthenticatorData, req.Signature) {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_INVALID_PASSKEY_ASSERTION", "Invalid passkey assertion."))
		return
	}

	challenge.AccountId = passkey.AccountId
	challenge.StepRemain = 0
	challenge.UpdatedAt = model.NewTime(now)
	if err := h.d.Store.UpdateAuthChallenge(ctx, challenge); err != nil {
		h.logError("update challenge", err)
	}
	c.JSON(http.StatusOK, challenge)
}

// ---------------------------------------------------------------------------
// Pending / approve / decline
// ---------------------------------------------------------------------------

func (h *handler) getPendingChallenges(c *gin.Context) {
	user := middleware.CurrentUser(c.Request.Context())
	if user == nil {
		c.JSON(http.StatusUnauthorized, errs.New("AUTH_UNAUTHORIZED", "Authentication is required.", http.StatusUnauthorized))
		return
	}
	challenges, err := h.d.Store.ListPendingChallenges(c.Request.Context(), user.Id)
	if err != nil {
		h.logError("list pending challenges", err)
		c.JSON(http.StatusInternalServerError, errs.New("SERVER_ERROR", "An internal server error occurred.", http.StatusInternalServerError))
		return
	}
	if challenges == nil {
		challenges = []model.AuthChallenge{}
	}
	c.JSON(http.StatusOK, challenges)
}

// sudoRequest mirrors SudoRequest.
type sudoRequest struct {
	PinCode *string `json:"pin_code"`
}

func (h *handler) approveChallenge(c *gin.Context) {
	ctx := c.Request.Context()
	user := middleware.CurrentUser(ctx)
	session := middleware.CurrentSession(ctx)
	if user == nil {
		c.JSON(http.StatusUnauthorized, errs.New("AUTH_UNAUTHORIZED", "Authentication is required.", http.StatusUnauthorized))
		return
	}
	if session == nil {
		c.JSON(http.StatusUnauthorized, errs.New("AUTH_SESSION_REQUIRED", "A valid session is required.", http.StatusUnauthorized))
		return
	}
	var req sudoRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("BAD_REQUEST", "Invalid request body."))
		return
	}
	valid, err := h.d.Auth.ValidateSudoMode(ctx, session, req.PinCode)
	if err != nil {
		h.logError("validate sudo mode", err)
	}
	if !valid {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_INVALID_PIN", "Invalid PIN code."))
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		c.JSON(http.StatusNotFound, errs.New("AUTH_CHALLENGE_NOT_FOUND", "Auth challenge was not found.", http.StatusNotFound))
		return
	}
	challenge, err := h.d.Store.GetAuthChallenge(ctx, id)
	if err != nil || challenge.AccountId != user.Id {
		c.JSON(http.StatusNotFound, errs.New("AUTH_CHALLENGE_NOT_FOUND", "Auth challenge was not found.", http.StatusNotFound))
		return
	}
	now := time.Now().UTC()
	if challenge.ExpiredAt != nil && now.After(challenge.ExpiredAt.Time()) {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_CHALLENGE_EXPIRED", "Auth challenge has expired."))
		return
	}
	if challenge.ApprovedAt != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_CHALLENGE_ALREADY_APPROVED", "Challenge already approved."))
		return
	}
	if challenge.DeclinedAt != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_CHALLENGE_ALREADY_DECLINED", "Challenge already declined."))
		return
	}

	challenge.StepRemain = 0
	challenge.ApprovedAt = model.NewTime(now)
	challenge.ApprovedBySessionId = &session.Id
	challenge.UpdatedAt = model.NewTime(now)
	if err := h.d.Store.UpdateAuthChallenge(ctx, challenge); err != nil {
		h.logError("update challenge", err)
	}

	h.publishWS(ctx, user.Id, "auth.challenge.approved", map[string]any{
		"challenge_id":       challenge.Id,
		"approved_by_device": session.Id,
	})
	deviceName := "unknown"
	if challenge.DeviceName != nil && *challenge.DeviceName != "" {
		deviceName = *challenge.DeviceName
	}
	h.pushNotification(ctx, user.Id, "auth.challenge_approved",
		localization.Localize(user.Language, "loginApprovedTitle", nil),
		localization.Localize(user.Language, "loginApprovedBody", map[string]string{"deviceName": deviceName}), false)
	c.Status(http.StatusOK)
}

func (h *handler) declineChallenge(c *gin.Context) {
	ctx := c.Request.Context()
	user := middleware.CurrentUser(ctx)
	session := middleware.CurrentSession(ctx)
	if user == nil {
		c.JSON(http.StatusUnauthorized, errs.New("AUTH_UNAUTHORIZED", "Authentication is required.", http.StatusUnauthorized))
		return
	}
	if session == nil {
		c.JSON(http.StatusUnauthorized, errs.New("AUTH_SESSION_REQUIRED", "A valid session is required.", http.StatusUnauthorized))
		return
	}
	var req sudoRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("BAD_REQUEST", "Invalid request body."))
		return
	}
	valid, err := h.d.Auth.ValidateSudoMode(ctx, session, req.PinCode)
	if err != nil {
		h.logError("validate sudo mode", err)
	}
	if !valid {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_INVALID_PIN", "Invalid PIN code."))
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		c.JSON(http.StatusNotFound, errs.New("AUTH_CHALLENGE_NOT_FOUND", "Auth challenge was not found.", http.StatusNotFound))
		return
	}
	challenge, err := h.d.Store.GetAuthChallenge(ctx, id)
	if err != nil || challenge.AccountId != user.Id {
		c.JSON(http.StatusNotFound, errs.New("AUTH_CHALLENGE_NOT_FOUND", "Auth challenge was not found.", http.StatusNotFound))
		return
	}
	now := time.Now().UTC()
	if challenge.ExpiredAt != nil && now.After(challenge.ExpiredAt.Time()) {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_CHALLENGE_EXPIRED", "Auth challenge has expired."))
		return
	}
	if challenge.ApprovedAt != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_CHALLENGE_ALREADY_APPROVED", "Challenge already approved."))
		return
	}
	if challenge.DeclinedAt != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_CHALLENGE_ALREADY_DECLINED", "Challenge already declined."))
		return
	}

	challenge.DeclinedAt = model.NewTime(now)
	challenge.UpdatedAt = model.NewTime(now)
	if err := h.d.Store.UpdateAuthChallenge(ctx, challenge); err != nil {
		h.logError("update challenge", err)
	}

	h.publishWS(ctx, user.Id, "auth.challenge.declined", map[string]any{
		"challenge_id":       challenge.Id,
		"declined_by_device": session.Id,
	})
	deviceName := "unknown"
	if challenge.DeviceName != nil && *challenge.DeviceName != "" {
		deviceName = *challenge.DeviceName
	}
	h.pushNotification(ctx, user.Id, "auth.challenge_declined",
		localization.Localize(user.Language, "loginDeclinedTitle", nil),
		localization.Localize(user.Language, "loginDeclinedBody", map[string]string{"deviceName": deviceName}), false)
	c.Status(http.StatusOK)
}

// ---------------------------------------------------------------------------
// Token exchange, refresh, recover, logout, login/session, me, sudo
// ---------------------------------------------------------------------------

// tokenExchangeRequest mirrors TokenExchangeRequest.
type tokenExchangeRequest struct {
	GrantType    string  `json:"grant_type"`
	RefreshToken *string `json:"refresh_token"`
	Code         *string `json:"code"`
}

func (h *handler) exchangeToken(c *gin.Context) {
	ctx := c.Request.Context()
	var req tokenExchangeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("BAD_REQUEST", "Invalid request body."))
		return
	}
	switch req.GrantType {
	case "authorization_code":
		challengeID, err := uuid.Parse(derefStr(req.Code))
		if err != nil {
			c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_INVALID_CODE", "Invalid or missing authorization code."))
			return
		}
		challenge, err := h.d.Store.GetAuthChallenge(ctx, challengeID)
		if err != nil {
			c.JSON(http.StatusBadRequest, errs.New("AUTH_CHALLENGE_NOT_FOUND", "Authorization code not found or expired.", http.StatusBadRequest))
			return
		}
		punishment, err := h.d.Store.GetActivePunishmentOverview(ctx, challenge.AccountId)
		if err != nil {
			h.logError("load punishment overview", err)
		}
		if punishment != nil && (punishment.Type == model.PunishmentDisableAccount || punishment.Type == model.PunishmentBlockLogin) {
			c.JSON(http.StatusLocked, &errs.ApiError{
				Code:    "ACCOUNT_LOCKED",
				Message: "Account is locked due to a punishment.",
				Detail:  &punishment.Reason,
				Status:  http.StatusLocked,
			})
			return
		}
		pair, err := h.d.Auth.CreateSessionAndIssueTokens(ctx, challenge)
		if err != nil {
			c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_CREATE_SESSION_FAILED", err.Error()))
			return
		}
		h.setAuthCookies(c, pair)
		c.JSON(http.StatusOK, tokenExchangeBody(pair))
	case "refresh_token":
		submitted := derefStr(req.RefreshToken)
		if strings.TrimSpace(submitted) == "" {
			if cookie, err := c.Cookie("RefreshToken"); err == nil {
				submitted = cookie
			}
		}
		if strings.TrimSpace(submitted) == "" {
			c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_REFRESH_TOKEN_REQUIRED", "Missing refresh token."))
			return
		}
		pair, _, err := h.d.Auth.RefreshSessionAndIssueTokens(ctx, submitted)
		if err != nil {
			c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_REFRESH_FAILED", err.Error()))
			return
		}
		h.setAuthCookies(c, pair)
		c.JSON(http.StatusOK, tokenExchangeBody(pair))
	default:
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_UNSUPPORTED_GRANT_TYPE", "Unsupported grant type."))
	}
}

func (h *handler) refreshToken(c *gin.Context) {
	ctx := c.Request.Context()
	refreshToken, err := c.Cookie("RefreshToken")
	if err != nil || strings.TrimSpace(refreshToken) == "" {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_REFRESH_TOKEN_REQUIRED", "Missing refresh token."))
		return
	}
	pair, _, err := h.d.Auth.RefreshSessionAndIssueTokens(ctx, refreshToken)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_REFRESH_FAILED", err.Error()))
		return
	}
	h.setAuthCookies(c, pair)
	c.JSON(http.StatusOK, tokenExchangeBody(pair))
}

// validateCaptcha ports POST /api/auth/captcha (raw JSON string token body).
func (h *handler) validateCaptcha(c *gin.Context) {
	ctx := c.Request.Context()
	raw, err := c.GetRawData()
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}
	var token string
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &token); err != nil {
			c.Status(http.StatusBadRequest)
			return
		}
	}
	valid, err := h.d.Auth.ValidateCaptcha(ctx, token)
	if err != nil || !valid {
		c.Status(http.StatusBadRequest)
		return
	}
	c.Status(http.StatusOK)
}

// recoveryRequest mirrors RecoveryRequest.
type recoveryRequest struct {
	Account      string               `json:"account"`
	RecoveryCode string               `json:"recovery_code"`
	CaptchaToken string               `json:"captcha_token"`
	DeviceId     string               `json:"device_id"`
	DeviceName   *string              `json:"device_name"`
	Platform     model.ClientPlatform `json:"platform"`
}

func (h *handler) recoverAccount(c *gin.Context) {
	ctx := c.Request.Context()
	var req recoveryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("BAD_REQUEST", "Invalid request body."))
		return
	}
	valid, err := h.d.Auth.ValidateCaptcha(ctx, req.CaptchaToken)
	if err != nil {
		h.logError("validate captcha", err)
	}
	if !valid {
		c.JSON(http.StatusBadRequest, validationError(map[string][]string{
			"captcha_token": {"Invalid captcha token."},
		}))
		return
	}
	account, err := h.d.Store.LookupAccount(ctx, req.Account)
	if err != nil || account == nil {
		c.JSON(http.StatusBadRequest, &errs.ApiError{
			Code:    "NOT_FOUND",
			Message: "Unable to find the account.",
			Detail:  &req.Account,
			Status:  http.StatusBadRequest,
		})
		return
	}
	pair, err := h.d.Auth.RecoverAccountWithRecoveryCode(ctx, account.Id, req.RecoveryCode,
		req.DeviceId, req.Platform, req.DeviceName, middleware.ClientIP(c.Request), c.Request.UserAgent())
	if err != nil {
		c.JSON(http.StatusBadRequest, &errs.ApiError{
			Code:    "RECOVERY_FAILED",
			Message: err.Error(),
			Status:  http.StatusBadRequest,
		})
		return
	}
	h.setAuthCookies(c, pair)
	c.JSON(http.StatusOK, tokenExchangeBody(pair))
}

func (h *handler) logout(c *gin.Context) {
	ctx := c.Request.Context()
	session := middleware.CurrentSession(ctx)
	if session != nil {
		_, _ = h.d.Auth.RevokeSession(ctx, uuid.MustParse(session.Id))
		_ = h.d.Token.RevokeJti(ctx, session.Id)
	}
	h.clearAuthCookies(c)
	c.Status(http.StatusOK)
}

// newSessionRequest mirrors NewSessionRequest.
type newSessionRequest struct {
	DeviceId   string               `json:"device_id"`
	DeviceName *string              `json:"device_name"`
	Platform   model.ClientPlatform `json:"platform"`
	ExpiredAt  *model.Time          `json:"expired_at"`
}

func (h *handler) loginFromSession(c *gin.Context) {
	ctx := c.Request.Context()
	user := middleware.CurrentUser(ctx)
	session := middleware.CurrentSession(ctx)
	if user == nil || session == nil {
		c.JSON(http.StatusUnauthorized, errs.New("AUTH_UNAUTHORIZED", "Authentication is required.", http.StatusUnauthorized))
		return
	}
	var req newSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("BAD_REQUEST", "Invalid request body."))
		return
	}
	var expiredAt *time.Time
	if req.ExpiredAt != nil {
		t := req.ExpiredAt.Time()
		expiredAt = &t
	}
	newSession, err := h.d.Auth.CreateSessionFromParent(ctx, session, req.DeviceId, req.DeviceName, req.Platform, expiredAt)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_CREATE_SESSION_FAILED", err.Error()))
		return
	}
	pair, err := h.d.Auth.CreateTokenPair(ctx, newSession)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_CREATE_SESSION_FAILED", err.Error()))
		return
	}
	h.setAuthCookies(c, pair)
	c.JSON(http.StatusOK, tokenExchangeBody(pair))
}

func tokenTypeName(t auth.TokenType) string {
	switch t {
	case auth.TokenTypeAuthKey:
		return "AuthKey"
	case auth.TokenTypeApiKey:
		return "ApiKey"
	case auth.TokenTypeOidcKey:
		return "OidcKey"
	default:
		return "Unknown"
	}
}

// getCurrentAuthIdentity ports GET /api/auth/me (auth-only identity).
func (h *handler) getCurrentAuthIdentity(c *gin.Context) {
	user := middleware.CurrentUser(c.Request.Context())
	session := middleware.CurrentSession(c.Request.Context())
	if user == nil || session == nil {
		c.JSON(http.StatusUnauthorized, errs.New("AUTH_UNAUTHORIZED", "Authentication is required.", http.StatusUnauthorized))
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id":           user.Id,
		"name":         user.Name,
		"nick":         user.Nick,
		"language":     user.Language,
		"region":       user.Region,
		"is_superuser": user.IsSuperuser,
		"activated_at": user.ActivatedAt,
		"session_id":   session.Id,
		"token_type":   tokenTypeName(middleware.CurrentTokenType(c.Request.Context())),
	})
}

func (h *handler) enableSudoMode(c *gin.Context) {
	ctx := c.Request.Context()
	session := middleware.CurrentSession(ctx)
	if session == nil {
		c.JSON(http.StatusUnauthorized, errs.New("AUTH_SESSION_REQUIRED", "A valid session is required.", http.StatusUnauthorized))
		return
	}
	var req sudoRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("BAD_REQUEST", "Invalid request body."))
		return
	}
	valid, err := h.d.Auth.ValidateSudoMode(ctx, session, req.PinCode)
	if err != nil {
		h.logError("validate sudo mode", err)
	}
	if !valid {
		c.JSON(http.StatusBadRequest, errs.BadRequest("AUTH_INVALID_PIN", "Invalid PIN code."))
		return
	}
	c.Status(http.StatusOK)
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// validationError mirrors ApiError.Validation with the C# default message.
func validationError(errors map[string][]string) *errs.ApiError {
	return &errs.ApiError{
		Code:    "VALIDATION_ERROR",
		Message: "One or more validation errors occurred.",
		Status:  http.StatusBadRequest,
		Errors:  errors,
	}
}

func parseUUIDParam(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

func containsString(list []string, target string) bool {
	for _, v := range list {
		if v == target {
			return true
		}
	}
	return false
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func platformName(p model.ClientPlatform) string {
	switch p {
	case model.ClientPlatformWeb:
		return "Web"
	case model.ClientPlatformIos:
		return "Ios"
	case model.ClientPlatformAndroid:
		return "Android"
	case model.ClientPlatformMacOs:
		return "MacOs"
	case model.ClientPlatformWindows:
		return "Windows"
	case model.ClientPlatformLinux:
		return "Linux"
	default:
		return "Unidentified"
	}
}

func (h *handler) passkeyRpId(c *gin.Context) string {
	if h.d.Cfg.WebAuthn.RpId != "" {
		return h.d.Cfg.WebAuthn.RpId
	}
	return c.Request.Host
}

// publishChallengePending mirrors the ws + ring side effects of the first
// (pending) factor step in DoChallenge / CompletePasskeyChallenge.
func (h *handler) publishChallengePending(ctx context.Context, challenge *model.AuthChallenge) {
	h.publishWS(ctx, challenge.AccountId, "auth.challenge.pending", map[string]any{
		"challenge_id": challenge.Id,
		"device_name":  challenge.DeviceName,
		"ip_address":   challenge.IpAddress,
		"platform":     platformName(challenge.Platform),
		"created_at":   challenge.CreatedAt,
	})
	account, err := h.d.Store.GetAccountByID(ctx, uuid.MustParse(challenge.AccountId))
	if err != nil {
		return
	}
	deviceName := "unknown"
	if challenge.DeviceName != nil && *challenge.DeviceName != "" {
		deviceName = *challenge.DeviceName
	}
	ipAddress := "unknown"
	if challenge.IpAddress != nil && *challenge.IpAddress != "" {
		ipAddress = *challenge.IpAddress
	}
	h.pushNotification(ctx, account.Id, "auth.login_attempt",
		localization.Localize(account.Language, "loginAttemptTitle", nil),
		localization.Localize(account.Language, "loginAttemptBody", map[string]string{
			"deviceName": deviceName,
			"ipAddress":  ipAddress,
		}), true)
}

// pushLoginNotification mirrors the auth.login ring notification when a
// challenge finishes.
func (h *handler) pushLoginNotification(ctx context.Context, challenge *model.AuthChallenge, _ bool) {
	account, err := h.d.Store.GetAccountByID(ctx, uuid.MustParse(challenge.AccountId))
	if err != nil {
		return
	}
	deviceName := "unknown"
	if challenge.DeviceName != nil && *challenge.DeviceName != "" {
		deviceName = *challenge.DeviceName
	}
	ipAddress := "unknown"
	if challenge.IpAddress != nil && *challenge.IpAddress != "" {
		ipAddress = *challenge.IpAddress
	}
	h.pushNotification(ctx, account.Id, "auth.login",
		localization.Localize(account.Language, "newLoginTitle", nil),
		localization.Localize(account.Language, "newLoginBody", map[string]string{
			"deviceName": deviceName,
			"ipAddress":  ipAddress,
		}), true)
}

func (h *handler) publishWS(ctx context.Context, target, event string, payload any) {
	if h.d.Events == nil {
		return
	}
	if err := h.d.Events.PublishWS(ctx, target, event, payload); err != nil && h.d.Log != nil {
		h.d.Log.Warn("publish ws event", "event", event, "error", err)
	}
}

func (h *handler) pushNotification(ctx context.Context, userID, topic, title, body string, savable bool) {
	_ = h.pushNotificationErr(ctx, userID, topic, title, body, savable)
}

// pushNotificationErr mirrors pushNotification but surfaces delivery failures
// (Ring unavailable, RPC error) instead of swallowing them.
func (h *handler) pushNotificationErr(ctx context.Context, userID, topic, title, body string, savable bool) error {
	if h.d.Clients == nil || h.d.Clients.Ring == nil {
		return errors.New("ring service is not configured")
	}
	pushCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	_, err := h.d.Clients.Ring.SendPushNotificationToUser(pushCtx, &gen.DySendPushNotificationToUserRequest{
		UserId: userID,
		Notification: &gen.DyPushNotification{
			Topic:     topic,
			Title:     title,
			Body:      body,
			IsSavable: savable,
		},
	})
	return err
}

func (h *handler) logError(msg string, err error) {
	if h.d.Log != nil {
		h.d.Log.Warn(msg, "error", err)
	}
}
