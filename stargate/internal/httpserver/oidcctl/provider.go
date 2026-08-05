package oidcctl

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/grpcclient"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// Deps is the dependency set the OIDC provider controller needs.
type Deps struct {
	Store   *store.Store
	Redis   *redis.Client
	Cfg     *config.Config
	JWT     *auth.JWTService
	Token   *auth.TokenAuthService
	Auth    *auth.AuthService
	Clients *grpcclient.Clients
	Log     *slog.Logger
}

// Register mounts the /api/auth/open/* routes.
func Register(api *gin.RouterGroup, d Deps) {
	svc, err := newService(d)
	if err != nil {
		panic("oidcctl: " + err.Error())
	}
	open := api.Group("/auth/open")
	open.GET("/authorize", svc.handleAuthorize)
	open.POST("/authorize", middleware.RequireAuth(), svc.handleAuthorizePost)
	open.POST("/token", svc.handleToken)
	open.GET("/userinfo", svc.handleUserInfo)
	open.POST("/device/code", svc.handleDeviceCode)
	open.GET("/device/code/:userCode", svc.handleDeviceCodeStatus)
	open.POST("/device/code/:userCode/approve", middleware.RequireAuth(), middleware.RequireInteractive(), svc.handleDeviceCodeApprove)
	open.POST("/device/code/:userCode/decline", middleware.RequireAuth(), middleware.RequireInteractive(), svc.handleDeviceCodeDecline)
}

// RegisterTop mounts the root-level /.well-known routes (the C# uses absolute
// attribute routes, which cannot be expressed under the /api group). main.go
// calls this with srv.Engine.
func RegisterTop(engine *gin.Engine, d Deps) {
	svc, err := newService(d)
	if err != nil {
		panic("oidcctl: " + err.Error())
	}
	engine.GET("/.well-known/openid-configuration", svc.handleConfiguration)
	engine.GET("/.well-known/jwks", svc.handleJwks)
}

// Wire responses (snake_case; nulls omitted per the house JSON policy).

type errorResponse struct {
	Error            string  `json:"error"`
	ErrorDescription *string `json:"error_description,omitempty"`
	ErrorUri         *string `json:"error_uri,omitempty"`
	State            *string `json:"state,omitempty"`
}

type tokenResponse struct {
	AccessToken     *string `json:"access_token,omitempty"`
	ExpiresIn       int     `json:"expires_in"`
	TokenType       string  `json:"token_type"`
	RefreshToken    *string `json:"refresh_token,omitempty"`
	Scope           *string `json:"scope,omitempty"`
	IdToken         *string `json:"id_token,omitempty"`
	OnboardingToken *string `json:"onboarding_token,omitempty"`
}

type clientInfoResponse struct {
	ClientId            string                            `json:"client_id"`
	Picture             *model.SnCloudFileReferenceObject `json:"picture,omitempty"`
	Background          *model.SnCloudFileReferenceObject `json:"background,omitempty"`
	ClientName          *string                           `json:"client_name,omitempty"`
	HomeUri             *string                           `json:"home_uri,omitempty"`
	PolicyUri           *string                           `json:"policy_uri,omitempty"`
	TermsOfServiceUri   *string                           `json:"terms_of_service_uri,omitempty"`
	ResponseTypes       *string                           `json:"response_types,omitempty"`
	Scopes              []string                          `json:"scopes"`
	State               *string                           `json:"state,omitempty"`
	Nonce               *string                           `json:"nonce,omitempty"`
	CodeChallenge       *string                           `json:"code_challenge,omitempty"`
	CodeChallengeMethod *string                           `json:"code_challenge_method,omitempty"`
}

type deviceCodeResponse struct {
	DeviceCode              string  `json:"device_code"`
	UserCode                string  `json:"user_code"`
	VerificationUri         string  `json:"verification_uri"`
	VerificationUriComplete *string `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int     `json:"expires_in"`
	Interval                int     `json:"interval"`
}

type deviceCodeStatusResponse struct {
	UserCode        string      `json:"user_code"`
	ClientId        string      `json:"client_id"`
	ClientName      string      `json:"client_name"`
	ClientSlug      string      `json:"client_slug"`
	Scopes          []string    `json:"scopes"`
	Status          string      `json:"status"`
	ExpiresAt       *model.Time `json:"expires_at"`
	ExpiresIn       int         `json:"expires_in"`
	Interval        int         `json:"interval"`
	VerificationUri string      `json:"verification_uri"`
}

func (s *service) badRequest(c *gin.Context, code, description string) {
	c.JSON(http.StatusBadRequest, errorResponse{Error: code, ErrorDescription: strPtr(description)})
}

func (s *service) notFound(c *gin.Context, code, description string) {
	c.JSON(http.StatusNotFound, errorResponse{Error: code, ErrorDescription: strPtr(description)})
}

func (s *service) serverError(c *gin.Context, err error) {
	if s.log != nil {
		s.log.Error("oidc provider error", "error", err)
	}
	c.Status(http.StatusInternalServerError)
}

// GET /api/auth/open/authorize
func (s *service) handleAuthorize(c *gin.Context) {
	ctx := c.Request.Context()
	clientId := c.Query("client_id")
	responseType := c.Query("response_type")
	redirectUri := c.Query("redirect_uri")
	scope := c.Query("scope")
	state := c.Query("state")
	nonce := c.Query("nonce")
	codeChallenge := c.Query("code_challenge")
	codeChallengeMethod := c.Query("code_challenge_method")

	if clientId == "" {
		s.badRequest(c, "invalid_request", "client_id is required")
		return
	}
	client, err := s.findClientByIdentifier(ctx, clientId)
	if err != nil {
		s.serverError(c, err)
		return
	}
	if client == nil {
		s.badRequest(c, "unauthorized_client", "Client not found")
		return
	}
	if responseType == "" {
		s.badRequest(c, "invalid_request", "response_type is required")
		return
	}
	allowedResponseTypes := map[string]bool{"code": true, "token": true, "id_token": true}
	for _, rt := range strings.Fields(responseType) {
		if !allowedResponseTypes[rt] {
			s.badRequest(c, "unsupported_response_type", "The requested response type is not supported")
			return
		}
	}
	if redirectUri != "" {
		valid, err := s.validateRedirectURI(ctx, client.Id, redirectUri)
		if err != nil {
			s.serverError(c, err)
			return
		}
		if !valid {
			s.badRequest(c, "invalid_request", "Invalid redirect_uri")
			return
		}
	}
	if codeChallenge != "" && !isSupportedCodeChallengeMethod(codeChallengeMethod) {
		s.badRequest(c, "invalid_request", "Unsupported code_challenge_method.")
		return
	}

	requestedScopes := strings.Fields(scope)
	scopesValid, normalizedScopes, scopeError := s.validateRequestedScopes(client, requestedScopes)
	if !scopesValid {
		s.badRequest(c, "invalid_scope", scopeError)
		return
	}

	c.JSON(http.StatusOK, clientInfoResponse{
		ClientId:            client.Slug,
		Picture:             client.Picture,
		Background:          client.Background,
		ClientName:          strPtr(client.Name),
		HomeUri:             client.HomeUri,
		PolicyUri:           client.PolicyUri,
		TermsOfServiceUri:   client.TermsOfServiceUri,
		ResponseTypes:       strPtr(responseType),
		Scopes:              normalizedScopes,
		State:               strPtrOrNil(state),
		Nonce:               strPtrOrNil(nonce),
		CodeChallenge:       strPtrOrNil(codeChallenge),
		CodeChallengeMethod: strPtrOrNil(codeChallengeMethod),
	})
}

// POST /api/auth/open/authorize (form-urlencoded, authenticated)
func (s *service) handleAuthorizePost(c *gin.Context) {
	ctx := c.Request.Context()
	account := middleware.CurrentUser(ctx)
	if account == nil {
		c.Status(http.StatusUnauthorized)
		return
	}
	authorize := c.PostForm("authorize")
	clientId := c.PostForm("client_id")
	redirectUriForm := postFormOrNil(c, "redirect_uri")
	scope := c.PostForm("scope")
	state := c.PostForm("state")
	nonce := c.PostForm("nonce")
	codeChallenge := c.PostForm("code_challenge")
	codeChallengeMethod := c.PostForm("code_challenge_method")
	redirectUri := ""
	if redirectUriForm != nil {
		redirectUri = *redirectUriForm
	}

	client, err := s.findClientByIdentifier(ctx, clientId)
	if err != nil {
		s.serverError(c, err)
		return
	}
	if client == nil {
		s.badRequest(c, "unauthorized_client", "Client not found")
		return
	}

	isPublicClient := s.isPublicClient(client)
	if isPublicClient && codeChallenge == "" {
		s.badRequest(c, "invalid_request", "PKCE is required for public clients. Please provide code_challenge.")
		return
	}
	if codeChallenge != "" && !isSupportedCodeChallengeMethod(codeChallengeMethod) {
		s.badRequest(c, "invalid_request", "Unsupported code_challenge_method.")
		return
	}

	// Denied: authorize is empty, unparseable, or false.
	denied := authorize == ""
	if !denied {
		approved, parseErr := parseBool(authorize)
		denied = parseErr != nil || !approved
	}
	if denied {
		fallback := "https://example.com"
		if client.HomeUri != nil {
			fallback = *client.HomeUri
		}
		base := redirectUri
		if base == "" {
			base = fallback
		}
		errorUri := buildURIWithQuery(base, map[string]string{
			"error":             "access_denied",
			"error_description": "The user denied the authorization request",
		}, state)
		c.JSON(http.StatusOK, gin.H{"redirect_uri": errorUri})
		return
	}

	if redirectUri != "" {
		valid, err := s.validateRedirectURI(ctx, client.Id, redirectUri)
		if err != nil {
			s.serverError(c, err)
			return
		}
		if !valid {
			s.badRequest(c, "invalid_request", "Invalid redirect_uri")
			return
		}
	} else if redirectUriForm == nil && len(client.RedirectUris) > 0 {
		// The C# uses ??= (null-coalescing): the fallback only applies when
		// the form field is absent, not when it is an empty string.
		redirectUri = client.RedirectUris[0]
	}
	if redirectUri == "" {
		s.badRequest(c, "invalid_request", "No valid redirect_uri available")
		return
	}

	requestedScopes := strings.Fields(scope)
	scopesValid, normalizedScopes, scopeError := s.validateRequestedScopes(client, requestedScopes)
	if !scopesValid {
		s.badRequest(c, "invalid_scope", scopeError)
		return
	}

	authorizationCode, err := s.generateAuthorizationCode(ctx, client.Id, account.Id, redirectUri, normalizedScopes,
		strPtrOrNil(codeChallenge), strPtrOrNil(codeChallengeMethod), strPtrOrNil(nonce))
	if err != nil {
		if s.log != nil {
			s.log.Error("error processing authorization request", "error", err)
		}
		c.JSON(http.StatusInternalServerError, errorResponse{
			Error:            "server_error",
			ErrorDescription: strPtr("An error occurred while processing your request"),
		})
		return
	}
	redirectURIWithCode := buildURIWithQuery(redirectUri, map[string]string{"code": authorizationCode}, state)
	c.JSON(http.StatusOK, gin.H{"redirect_uri": redirectURIWithCode})
}

// POST /api/auth/open/token (form-urlencoded)
func (s *service) handleToken(c *gin.Context) {
	ctx := c.Request.Context()
	ipAddress := middleware.ClientIP(c.Request)
	userAgent := c.Request.UserAgent()

	grantType := c.PostForm("grant_type")
	clientId := c.PostForm("client_id")
	code := c.PostForm("code")
	redirectUri := c.PostForm("redirect_uri")
	clientSecret := c.PostForm("client_secret")
	refreshToken := c.PostForm("refresh_token")
	codeVerifier := c.PostForm("code_verifier")
	deviceCode := c.PostForm("device_code")

	if clientId == "" {
		s.badRequest(c, "invalid_request", "client_id is required")
		return
	}
	client, err := s.findClientByIdentifier(ctx, clientId)
	if err != nil {
		s.serverError(c, err)
		return
	}
	if client == nil {
		s.badRequest(c, "unauthorized_client", "Client not found")
		return
	}
	isPublicClient := s.isPublicClient(client)

	switch grantType {
	case "authorization_code":
		if code == "" {
			s.badRequest(c, "invalid_request", "Authorization code is required")
			return
		}
		if !isPublicClient && !s.validClientSecret(c, client.Id, clientSecret) {
			return
		}
		resp, err := s.generateTokenResponseForCode(ctx, client, code, redirectUri, codeVerifier, isPublicClient, ipAddress, userAgent)
		if err != nil {
			// The C# does not catch authorization_code grant failures; an
			// invalid code surfaces as an empty 500. Mirrored faithfully.
			s.log.Warn("OIDC authorization_code grant failed", "client_id", clientId, "error", err)
			c.Status(http.StatusInternalServerError)
			return
		}
		c.JSON(http.StatusOK, resp)

	case "refresh_token":
		if refreshToken == "" {
			s.badRequest(c, "invalid_request", "Refresh token is required")
			return
		}
		if !isPublicClient && !s.validClientSecret(c, client.Id, clientSecret) {
			return
		}
		resp, err := s.generateTokenResponseForRefresh(ctx, client, refreshToken, ipAddress, userAgent)
		if err != nil {
			s.log.Warn("OIDC refresh token grant failed", "client_id", clientId, "error", err)
			s.badRequest(c, "invalid_grant", "Invalid or expired refresh token")
			return
		}
		c.JSON(http.StatusOK, resp)

	case "urn:ietf:params:oauth:grant-type:device_code":
		if deviceCode == "" {
			s.badRequest(c, "invalid_request", "device_code is required")
			return
		}
		if !isPublicClient && !s.validClientSecret(c, client.Id, clientSecret) {
			return
		}
		resp, err := s.handleDeviceCodeGrant(ctx, deviceCode, client.Id, ipAddress, userAgent)
		if err != nil {
			s.badRequest(c, deviceGrantErrorCode(err), err.Error())
			return
		}
		c.JSON(http.StatusOK, resp)

	default:
		c.JSON(http.StatusBadRequest, errorResponse{Error: "unsupported_grant_type"})
	}
}

func (s *service) validClientSecret(c *gin.Context, clientId, clientSecret string) bool {
	if clientSecret == "" {
		s.badRequest(c, "invalid_client", "Invalid client credentials")
		return false
	}
	valid, err := s.validateClientCredentials(c.Request.Context(), clientId, clientSecret)
	if err != nil {
		s.serverError(c, err)
		return false
	}
	if !valid {
		s.badRequest(c, "invalid_client", "Invalid client credentials")
		return false
	}
	return true
}

// deviceGrantErrorCode mirrors the controller's ex.Message switch.
func deviceGrantErrorCode(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "pending"):
		return "authorization_pending"
	case strings.Contains(strings.ToLower(msg), "slow down"):
		return "slow_down"
	case strings.Contains(msg, "expired"):
		return "expired_token"
	case strings.Contains(msg, "declined"):
		return "access_denied"
	default:
		return "invalid_grant"
	}
}

// GET /api/auth/open/userinfo
func (s *service) handleUserInfo(c *gin.Context) {
	ctx := c.Request.Context()
	bearer := c.GetHeader("Authorization")
	if !strings.HasPrefix(bearer, "Bearer ") {
		c.Header("WWW-Authenticate", "Bearer")
		c.Status(http.StatusUnauthorized)
		return
	}
	token := strings.TrimSpace(bearer[len("Bearer "):])
	claims, valid := s.validateToken(token)
	if !valid {
		c.Header("WWW-Authenticate", `Bearer error="invalid_token"`)
		c.Status(http.StatusUnauthorized)
		return
	}
	accountIdText, _ := claims["sub"].(string)
	sessionIdText, _ := claims["jti"].(string)
	accountId, err1 := parseUUIDStr(accountIdText)
	sessionId, err2 := parseUUIDStr(sessionIdText)
	if err1 != nil || err2 != nil {
		c.Header("WWW-Authenticate", `Bearer error="invalid_token"`)
		c.Status(http.StatusUnauthorized)
		return
	}
	session, err := s.store.GetSessionWithAccount(ctx, sessionId)
	if err != nil || session == nil || session.AccountId != accountId.String() || session.Account == nil {
		c.Header("WWW-Authenticate", `Bearer error="invalid_token"`)
		c.Status(http.StatusUnauthorized)
		return
	}
	currentUser := session.Account

	scopes := scopesFromClaims(claims)
	userInfo := map[string]any{"sub": currentUser.Id}
	if _, ok := scopes["profile"]; ok {
		userInfo["name"] = currentUser.Name
		userInfo["preferred_username"] = currentUser.Nick
	} else if _, ok := scopes["name"]; ok {
		userInfo["name"] = currentUser.Name
		userInfo["preferred_username"] = currentUser.Nick
	}
	if _, ok := scopes["email"]; ok {
		if contact, err := s.store.GetEmailContact(ctx, currentUser.Id); err == nil && contact != nil {
			userInfo["email"] = contact.Content
			userInfo["email_verified"] = contact.VerifiedAt != nil
		}
	}
	c.JSON(http.StatusOK, userInfo)
}

// POST /api/auth/open/device/code
func (s *service) handleDeviceCode(c *gin.Context) {
	ctx := c.Request.Context()
	clientId := c.PostForm("client_id")
	scope := c.PostForm("scope")
	nonce := c.PostForm("nonce")

	if clientId == "" {
		s.badRequest(c, "invalid_request", "client_id is required")
		return
	}
	client, err := s.findClientByIdentifier(ctx, clientId)
	if err != nil {
		s.serverError(c, err)
		return
	}
	if client == nil {
		s.badRequest(c, "unauthorized_client", "Client not found")
		return
	}
	requestedScopes := strings.Fields(scope)
	scopesValid, normalizedScopes, scopeError := s.validateRequestedScopes(client, requestedScopes)
	if !scopesValid {
		s.badRequest(c, "invalid_scope", scopeError)
		return
	}

	info, err := s.generateDeviceCode(ctx, client.Id, normalizedScopes, strPtrOrNil(nonce))
	if err != nil {
		s.serverError(c, err)
		return
	}
	verificationUri := s.deviceVerificationUri()
	complete := verificationUri + "?code=" + url.QueryEscape(info.UserCode)
	c.JSON(http.StatusOK, deviceCodeResponse{
		DeviceCode:              info.DeviceCode,
		UserCode:                info.UserCode,
		VerificationUri:         verificationUri,
		VerificationUriComplete: &complete,
		ExpiresIn:               int(info.ExpiresAt.Sub(info.CreatedAt).Seconds()),
		Interval:                info.PollingIntervalSeconds,
	})
}

// GET /api/auth/open/device/code/{userCode}
func (s *service) handleDeviceCodeStatus(c *gin.Context) {
	ctx := c.Request.Context()
	userCode := normalizeUserCode(c.Param("userCode"))
	info, err := s.getDeviceCodeByUserCode(ctx, userCode)
	if err != nil {
		s.serverError(c, err)
		return
	}
	if info == nil {
		s.notFound(c, "not_found", "Device code not found or expired.")
		return
	}
	client, err := s.findClientByID(ctx, info.ClientId)
	if err != nil {
		s.serverError(c, err)
		return
	}
	now := timeNow()
	clientId := info.ClientId
	clientSlug := ""
	clientName := info.ClientId
	if client != nil {
		clientId = client.Slug
		clientName = client.Name
		clientSlug = client.Slug
	}
	statusText := []string{"pending", "approved", "declined", "expired"}[info.Status]
	c.JSON(http.StatusOK, deviceCodeStatusResponse{
		UserCode:        info.UserCode,
		ClientId:        clientId,
		ClientName:      clientName,
		ClientSlug:      clientSlug,
		Scopes:          info.Scopes,
		Status:          statusText,
		ExpiresAt:       model.NewTime(info.ExpiresAt),
		ExpiresIn:       max(0, int(info.ExpiresAt.Sub(now).Seconds())),
		Interval:        info.PollingIntervalSeconds,
		VerificationUri: s.deviceVerificationUri(),
	})
}

// POST /api/auth/open/device/code/{userCode}/approve
func (s *service) handleDeviceCodeApprove(c *gin.Context) {
	ctx := c.Request.Context()
	currentUser := middleware.CurrentUser(ctx)
	currentSession := middleware.CurrentSession(ctx)
	if currentUser == nil || currentSession == nil {
		c.Status(http.StatusUnauthorized)
		return
	}
	userCode := normalizeUserCode(c.Param("userCode"))
	info, err := s.getDeviceCodeByUserCode(ctx, userCode)
	if err != nil {
		s.serverError(c, err)
		return
	}
	if info == nil {
		s.notFound(c, "not_found", "Device code not found or expired.")
		return
	}
	if info.Status != deviceCodeStatusPending {
		s.badRequest(c, "invalid_request", "Device code is no longer pending.")
		return
	}
	now := timeNow()
	if now.After(info.ExpiresAt) {
		info.Status = deviceCodeStatusExpired
		_ = s.updateDeviceCode(ctx, info)
		s.badRequest(c, "expired_token", "Device code has expired.")
		return
	}
	info.AccountId = strPtr(currentUser.Id)
	info.Status = deviceCodeStatusApproved
	info.ApprovedAt = &now
	info.ApprovedBySessionId = strPtr(currentSession.Id)
	if err := s.updateDeviceCode(ctx, info); err != nil {
		s.serverError(c, err)
		return
	}
	c.Status(http.StatusOK)
}

// POST /api/auth/open/device/code/{userCode}/decline
func (s *service) handleDeviceCodeDecline(c *gin.Context) {
	ctx := c.Request.Context()
	currentUser := middleware.CurrentUser(ctx)
	currentSession := middleware.CurrentSession(ctx)
	if currentUser == nil || currentSession == nil {
		c.Status(http.StatusUnauthorized)
		return
	}
	userCode := normalizeUserCode(c.Param("userCode"))
	info, err := s.getDeviceCodeByUserCode(ctx, userCode)
	if err != nil {
		s.serverError(c, err)
		return
	}
	if info == nil {
		s.notFound(c, "not_found", "Device code not found or expired.")
		return
	}
	if info.Status != deviceCodeStatusPending {
		s.badRequest(c, "invalid_request", "Device code is no longer pending.")
		return
	}
	now := timeNow()
	info.Status = deviceCodeStatusDeclined
	info.ApprovedAt = &now
	info.ApprovedBySessionId = strPtr(currentSession.Id)
	if err := s.updateDeviceCode(ctx, info); err != nil {
		s.serverError(c, err)
		return
	}
	c.Status(http.StatusOK)
}

// --- helpers ---

func (s *service) deviceVerificationUri() string {
	siteUrl := s.cfg.SiteUrl
	if siteUrl == "" {
		siteUrl = "https://solsynth.dev"
	}
	return strings.TrimSuffix(siteUrl, "/") + "/auth/device"
}

func normalizeUserCode(userCode string) string {
	return strings.ToUpper(strings.TrimSpace(userCode))
}

func parseUUIDStr(s string) (uuid.UUID, error) {
	return uuid.Parse(s)
}

func scopesFromClaims(claims jwt.MapClaims) map[string]struct{} {
	set := map[string]struct{}{}
	v, ok := claims["scope"]
	if !ok {
		return set
	}
	switch t := v.(type) {
	case string:
		set[t] = struct{}{}
	case []any:
		for _, item := range t {
			if sv, ok := item.(string); ok {
				set[sv] = struct{}{}
			}
		}
	case []string:
		for _, sv := range t {
			set[sv] = struct{}{}
		}
	}
	return set
}

// buildURIWithQuery appends query parameters to a URI, preserving any
// existing query (mirrors UriBuilder + HttpUtility.ParseQueryString).
func buildURIWithQuery(base string, params map[string]string, state string) string {
	u, err := url.Parse(base)
	if err != nil {
		u = &url.URL{}
	}
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func postFormOrNil(c *gin.Context, key string) *string {
	if c.Request.Form == nil {
		_ = c.Request.ParseMultipartForm(0)
	}
	if _, ok := c.Request.PostForm[key]; ok {
		v := c.PostForm(key)
		return &v
	}
	return nil
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1":
		return true, nil
	case "false", "0":
		return false, nil
	}
	return false, errNotBool
}

var errNotBool = errors.New("not a bool")

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func timeNow() time.Time { return time.Now().UTC() }
