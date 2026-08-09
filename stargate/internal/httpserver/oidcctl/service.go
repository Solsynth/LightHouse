// Package oidcctl is the Go port of Padlock's OIDC provider
// (Auth/OidcProvider): authorize, token, userinfo, device-code and
// well-known endpoints under /api/auth/open plus /.well-known/*.
package oidcctl

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"src.solsynth.dev/sosys/go/pkg/models"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// Redis key prefixes mirror OidcProviderService exactly.
const (
	cacheKeyPrefixClientId   = "auth:oidc-client:id:"
	cacheKeyPrefixClientSlug = "auth:oidc-client:slug:"
	cacheKeyPrefixAuthCode   = "auth:oidc-code:"
	cacheKeyPrefixDeviceCode = "auth:device-code:"
	cacheKeyPrefixUserCode   = "auth:user-code:"

	codeChallengeMethodS256  = "S256"
	codeChallengeMethodPlain = "PLAIN"

	deviceCodePollingIntervalSeconds = 5
	deviceCodeSlowDownStepSeconds    = 5
	deviceCodeLifetime               = 10 * time.Minute
	clientCacheTTL                   = 5 * time.Minute

	// CustomAppStatus values (C# enum: Developing=0, Staging=1,
	// Production=2, Suspended=3).
	customAppStatusProduction = 2

	deviceCodeStatusPending  = 0
	deviceCodeStatusApproved = 1
	deviceCodeStatusDeclined = 2
	deviceCodeStatusExpired  = 3

	defaultIssuer = "https://your-issuer-uri.com"
)

var supportedCodeChallengeMethods = []string{"S256", "plain"}

// oidcClient is the subset of SnCustomApp the provider needs. It is cached
// in Redis (auth:oidc-client:id:/slug:) with snake_case JSON, mirroring the
// C# client cache.
type oidcClient struct {
	Id                string                            `json:"id"`
	Slug              string                            `json:"slug"`
	Name              string                            `json:"name"`
	Status            int                               `json:"status"`
	Picture           *model.SnCloudFileReferenceObject `json:"picture,omitempty"`
	Background        *model.SnCloudFileReferenceObject `json:"background,omitempty"`
	HomeUri           *string                           `json:"home_uri,omitempty"`
	PolicyUri         *string                           `json:"policy_uri,omitempty"`
	TermsOfServiceUri *string                           `json:"terms_of_service_uri,omitempty"`
	RedirectUris      []string                          `json:"redirect_uris,omitempty"`
	AllowedScopes     []string                          `json:"allowed_scopes,omitempty"`
	IsPublicClient    bool                              `json:"is_public_client"`
}

// authorizationCodeInfo mirrors AuthorizationCodeInfo (stored under
// auth:oidc-code:{code}; C# serializes snake_case with nulls included).
type authorizationCodeInfo struct {
	ClientId            string            `json:"client_id"`
	AccountId           *string           `json:"account_id"`
	ExternalUserInfo    *externalUserInfo `json:"external_user_info"`
	RedirectUri         string            `json:"redirect_uri"`
	Scopes              []string          `json:"scopes"`
	CodeChallenge       *string           `json:"code_challenge"`
	CodeChallengeMethod *string           `json:"code_challenge_method"`
	Nonce               *string           `json:"nonce"`
	CreatedAt           time.Time         `json:"created_at"`
}

// externalUserInfo mirrors ExternalUserInfo.
type externalUserInfo struct {
	Provider string  `json:"provider"`
	UserId   string  `json:"user_id"`
	Email    *string `json:"email"`
	Name     *string `json:"name"`
}

// deviceCodeInfo mirrors DeviceCodeInfo (stored under auth:device-code:).
// Status is the DeviceCodeStatus enum serialized as an int (System.Text.Json
// default), matching what the C# writes to Redis.
type deviceCodeInfo struct {
	DeviceCode             string     `json:"device_code"`
	UserCode               string     `json:"user_code"`
	ClientId               string     `json:"client_id"`
	AccountId              *string    `json:"account_id"`
	Scopes                 []string   `json:"scopes"`
	Nonce                  *string    `json:"nonce"`
	Status                 int        `json:"status"`
	CreatedAt              time.Time  `json:"created_at"`
	ExpiresAt              time.Time  `json:"expires_at"`
	PollingIntervalSeconds int        `json:"polling_interval_seconds"`
	LastPolledAt           *time.Time `json:"last_polled_at"`
	ApprovedAt             *time.Time `json:"approved_at"`
	ApprovedBySessionId    *string    `json:"approved_by_session_id"`
}

// service is the Go port of OidcProviderService.
type service struct {
	store   *store.Store
	redis   *redis.Client
	cfg     *config.Config
	jwt     *auth.JWTService
	token   *auth.TokenAuthService
	authSvc *auth.AuthService
	develop gen.DyCustomAppServiceClient
	log     *slog.Logger

	issuer          string
	publicKey       *rsa.PublicKey
	privateKey      *rsa.PrivateKey
	accessLifetime  time.Duration
	refreshLifetime time.Duration
	codeLifetime    time.Duration
}

func newService(d Deps) (*service, error) {
	issuer := d.Cfg.OidcProvider.IssuerUri
	if issuer == "" {
		issuer = defaultIssuer
	}
	pubKey, privKey := loadOIDCKeys(d.Cfg)
	var develop gen.DyCustomAppServiceClient
	if d.Clients != nil {
		develop = d.Clients.Develop
	}
	return &service{
		store:           d.Store,
		redis:           d.Redis,
		cfg:             d.Cfg,
		jwt:             d.JWT,
		token:           d.Token,
		authSvc:         d.Auth,
		develop:         develop,
		log:             d.Log,
		issuer:          issuer,
		publicKey:       pubKey,
		privateKey:      privKey,
		accessLifetime:  oidcDuration(d.Cfg.OidcProvider.AccessTokenLifetime, time.Hour),
		refreshLifetime: oidcDuration(d.Cfg.OidcProvider.RefreshTokenLifetime, 30*24*time.Hour),
		codeLifetime:    oidcDuration(d.Cfg.OidcProvider.AuthorizationCodeLifetime, 5*time.Minute),
	}, nil
}

func oidcDuration(s string, fallback time.Duration) time.Duration {
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return fallback
}

// loadOIDCKeys loads the OIDC provider RSA key pair. The C# reads
// OidcProvider:PublicKeyPath/PrivateKeyPath; when unset the auth keys are
// used (the deployment configures both to the same files).
func loadOIDCKeys(cfg *config.Config) (*rsa.PublicKey, *rsa.PrivateKey) {
	pubPath := cfg.OidcProvider.PublicKeyPath
	privPath := cfg.OidcProvider.PrivateKeyPath
	if pubPath == "" {
		pubPath = cfg.Auth.PublicKeyPath
	}
	if privPath == "" {
		privPath = cfg.Auth.PrivateKeyPath
	}
	var pub *rsa.PublicKey
	if data, err := os.ReadFile(pubPath); err == nil {
		if block, _ := pem.Decode(data); block != nil {
			if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
				if p, ok := cert.PublicKey.(*rsa.PublicKey); ok {
					pub = p
				}
			} else if key, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
				if p, ok := key.(*rsa.PublicKey); ok {
					pub = p
				}
			} else if key, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
				pub = key
			}
		}
	}
	var priv *rsa.PrivateKey
	if data, err := os.ReadFile(privPath); err == nil {
		if block, _ := pem.Decode(data); block != nil {
			if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
				if k, ok := key.(*rsa.PrivateKey); ok {
					priv = k
				}
			} else if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
				priv = key
			}
		}
	}
	return pub, priv
}

// --- Client lookup (local config, Develop gRPC + Redis cache) ---

func (s *service) findLocalClient(identifier string) *oidcClient {
	if s.cfg == nil {
		return nil
	}
	configured := s.cfg.FindLocalOAuthClient(identifier)
	if configured == nil {
		return nil
	}
	id := configured.Id
	if id == "" {
		id = configured.Slug
	}
	slug := configured.Slug
	if slug == "" {
		slug = id
	}
	if id == "" || slug == "" {
		return nil
	}
	status := configured.Status
	if status == 0 {
		status = customAppStatusProduction
	}
	client := &oidcClient{
		Id:             id,
		Slug:           slug,
		Name:           configured.Name,
		Status:         status,
		RedirectUris:   configured.RedirectUris,
		AllowedScopes:  configured.AllowedScopes,
		IsPublicClient: configured.IsPublicClient,
	}
	if configured.HomeUri != "" {
		client.HomeUri = &configured.HomeUri
	}
	if configured.PolicyUri != "" {
		client.PolicyUri = &configured.PolicyUri
	}
	if configured.TermsOfServiceUri != "" {
		client.TermsOfServiceUri = &configured.TermsOfServiceUri
	}
	return client
}

func (s *service) findClientByIdentifier(ctx context.Context, identifier string) (*oidcClient, error) {
	if _, err := uuid.Parse(identifier); err == nil {
		return s.findClientByID(ctx, identifier)
	}
	return s.findClientBySlug(ctx, identifier)
}

func (s *service) findClientByID(ctx context.Context, id string) (*oidcClient, error) {
	if client := s.findLocalClient(id); client != nil {
		return client, nil
	}
	var client oidcClient
	found, err := s.redis.Cache.Get(ctx, cacheKeyPrefixClientId+id, &client)
	if err == nil && found {
		return &client, nil
	}
	client, err = s.fetchClient(ctx, id, "")
	if err != nil || client.Id == "" {
		return nil, err
	}
	_ = s.redis.Cache.Set(ctx, cacheKeyPrefixClientId+id, &client, clientCacheTTL)
	return &client, nil
}

func (s *service) findClientBySlug(ctx context.Context, slug string) (*oidcClient, error) {
	if client := s.findLocalClient(slug); client != nil {
		return client, nil
	}
	var client oidcClient
	found, err := s.redis.Cache.Get(ctx, cacheKeyPrefixClientSlug+slug, &client)
	if err == nil && found {
		return &client, nil
	}
	client, err = s.fetchClient(ctx, "", slug)
	if err != nil || client.Id == "" {
		return nil, err
	}
	_ = s.redis.Cache.Set(ctx, cacheKeyPrefixClientSlug+slug, &client, clientCacheTTL)
	return &client, nil
}

func (s *service) fetchClient(ctx context.Context, id, slug string) (oidcClient, error) {
	if s.develop == nil {
		if s.log != nil {
			s.log.Warn("OIDC client lookup skipped: Develop gRPC client is not configured ([services] develop)", "id", id, "slug", slug)
		}
		return oidcClient{}, nil
	}
	req := &gen.DyGetCustomAppRequest{}
	if id != "" {
		req.Query = &gen.DyGetCustomAppRequest_Id{Id: id}
	} else {
		req.Query = &gen.DyGetCustomAppRequest_Slug{Slug: slug}
	}
	resp, err := s.develop.GetCustomApp(ctx, req)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return oidcClient{}, nil
		}
		return oidcClient{}, err
	}
	if resp == nil || resp.GetApp() == nil {
		return oidcClient{}, nil
	}
	return clientFromProto(resp.GetApp()), nil
}

func clientFromProto(p *gen.DyCustomApp) oidcClient {
	client := oidcClient{
		Id:     p.GetId(),
		Slug:   p.GetSlug(),
		Name:   p.GetName(),
		Status: customAppStatusFromProto(p.GetStatus()),
	}
	if f := p.GetPicture(); f != nil {
		client.Picture = cloudFileFromProto(f)
	}
	if f := p.GetBackground(); f != nil {
		client.Background = cloudFileFromProto(f)
	}
	if l := p.GetLinks(); l != nil {
		if v := l.GetHomePage(); v != "" {
			client.HomeUri = &v
		}
		if v := l.GetPrivacyPolicy(); v != "" {
			client.PolicyUri = &v
		}
		if v := l.GetTermsOfService(); v != "" {
			client.TermsOfServiceUri = &v
		}
	}
	if o := p.GetOauthConfig(); o != nil {
		client.RedirectUris = o.GetRedirectUris()
		client.AllowedScopes = o.GetAllowedScopes()
		client.IsPublicClient = o.GetIsPublicClient()
	}
	return client
}

func customAppStatusFromProto(s gen.DyCustomAppStatus) int {
	switch s {
	case gen.DyCustomAppStatus_DY_PRODUCTION:
		return 2
	case gen.DyCustomAppStatus_DY_STAGING:
		return 1
	case gen.DyCustomAppStatus_DY_SUSPENDED:
		return 3
	default:
		return 0
	}
}

func cloudFileFromProto(f *gen.DyCloudFile) *model.SnCloudFileReferenceObject {
	ref := models.FromProtoValue(f)
	return &ref
}

// ValidateClientCredentialsAsync checks local configuration first, then
// falls back to Develop's CheckCustomAppSecret with IsOidc=true.
func (s *service) validateClientCredentials(ctx context.Context, clientID, secret string) (bool, error) {
	if s.cfg != nil {
		if configured := s.cfg.FindLocalOAuthClient(clientID); configured != nil {
			if configured.ClientSecret == "" {
				return false, nil
			}
			return subtle.ConstantTimeCompare([]byte(configured.ClientSecret), []byte(secret)) == 1, nil
		}
	}
	if s.develop == nil {
		return false, nil
	}
	isOidc := true
	resp, err := s.develop.CheckCustomAppSecret(ctx, &gen.DyCheckCustomAppSecretRequest{
		SecretIdentifier: &gen.DyCheckCustomAppSecretRequest_AppId{AppId: clientID},
		Secret:           secret,
		IsOidc:           &isOidc,
	})
	if err != nil {
		return false, err
	}
	return resp.GetValid(), nil
}

// --- Scope / redirect validation ---

func (s *service) isPublicClient(client *oidcClient) bool {
	return client != nil && client.IsPublicClient
}

func (s *service) validateRequestedScopes(client *oidcClient, requested []string) (bool, []string, string) {
	normalized := []string{}
	seen := map[string]struct{}{}
	for _, scope := range requested {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if _, dup := seen[scope]; dup {
			continue
		}
		seen[scope] = struct{}{}
		normalized = append(normalized, scope)
	}

	allowed := map[string]struct{}{}
	for _, scope := range client.AllowedScopes {
		if scope == "" {
			continue
		}
		allowed[strings.ToLower(scope)] = struct{}{}
	}

	var disallowed []string
	for _, scope := range normalized {
		if _, ok := allowed[strings.ToLower(scope)]; !ok {
			disallowed = append(disallowed, scope)
		}
	}
	if len(disallowed) > 0 {
		return false, nil, fmt.Sprintf("The following scopes are not allowed for this client: %s", strings.Join(disallowed, " "))
	}
	return true, normalized, ""
}

// validateRedirectURI mirrors ValidateRedirectUriAsync: non-Production
// clients accept any redirect URI.
func (s *service) validateRedirectURI(ctx context.Context, clientID, redirectURI string) (bool, error) {
	if redirectURI == "" {
		return false, nil
	}
	client, err := s.findClientByID(ctx, clientID)
	if err != nil {
		return false, err
	}
	if client == nil || client.Status != customAppStatusProduction {
		return true, nil
	}
	if len(client.RedirectUris) == 0 {
		return false, nil
	}
	for _, allowedURI := range client.RedirectUris {
		if isWildcardRedirectUriMatch(allowedURI, redirectURI) {
			return true, nil
		}
	}
	return false, nil
}

func isWildcardRedirectUriMatch(allowedURI, redirectURI string) bool {
	if allowedURI == "" || redirectURI == "" {
		return false
	}
	if allowedURI == redirectURI {
		return true
	}
	if !strings.Contains(allowedURI, "*") {
		return false
	}
	allowedObj, err1 := url.Parse(allowedURI)
	redirectObj, err2 := url.Parse(redirectURI)
	if err1 != nil || err2 != nil {
		return false
	}
	if allowedObj.Scheme != redirectObj.Scheme || uriPort(allowedObj) != uriPort(redirectObj) {
		return false
	}
	allowedHost := allowedObj.Hostname()
	redirectHost := redirectObj.Hostname()
	if strings.HasPrefix(allowedHost, "*.") {
		baseDomain := allowedHost[2:]
		if redirectHost == baseDomain || strings.HasSuffix(redirectHost, "."+baseDomain) {
			allowedPath := strings.TrimSuffix(allowedObj.Path, "/")
			redirectPath := strings.TrimSuffix(redirectObj.Path, "/")
			return allowedPath == "" || strings.HasPrefix(strings.ToLower(redirectPath), strings.ToLower(allowedPath))
		}
	}
	return false
}

// uriPort mirrors .NET Uri.Port, which substitutes scheme default ports.
func uriPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch u.Scheme {
	case "http":
		return "80"
	case "https":
		return "443"
	}
	return ""
}

func isSupportedCodeChallengeMethod(method string) bool {
	if method == "" {
		return true
	}
	for _, m := range supportedCodeChallengeMethods {
		if strings.EqualFold(m, method) {
			return true
		}
	}
	return false
}

// --- Authorization codes ---

func (s *service) generateAuthorizationCode(ctx context.Context, clientID, accountID, redirectURI string, scopes []string, codeChallenge, codeChallengeMethod, nonce *string) (string, error) {
	info := &authorizationCodeInfo{
		ClientId:            clientID,
		AccountId:           &accountID,
		RedirectUri:         redirectURI,
		Scopes:              scopes,
		CodeChallenge:       codeChallenge,
		CodeChallengeMethod: codeChallengeMethod,
		Nonce:               nonce,
		CreatedAt:           time.Now().UTC(),
	}
	return s.storeAuthorizationCode(ctx, info)
}

func (s *service) storeAuthorizationCode(ctx context.Context, info *authorizationCodeInfo) (string, error) {
	code := generateRandomString(32)
	if err := s.redis.Cache.Set(ctx, cacheKeyPrefixAuthCode+code, info, s.codeLifetime); err != nil {
		return "", err
	}
	return code, nil
}

func (s *service) validateAuthorizationCode(ctx context.Context, code, clientID string, redirectURI, codeVerifier *string, isPublicClient bool) (*authorizationCodeInfo, error) {
	var info authorizationCodeInfo
	found, err := s.redis.Cache.Get(ctx, cacheKeyPrefixAuthCode+code, &info)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	if info.ClientId != clientID {
		return nil, nil
	}
	if redirectURI != nil && *redirectURI != "" && info.RedirectUri != *redirectURI {
		return nil, nil
	}
	if isPublicClient && (info.CodeChallenge == nil || *info.CodeChallenge == "") {
		return nil, nil
	}
	if info.CodeChallenge != nil && *info.CodeChallenge != "" {
		if codeVerifier == nil || *codeVerifier == "" {
			return nil, nil
		}
		if !verifyCodeChallengeWithFallback(*codeVerifier, *info.CodeChallenge, info.CodeChallengeMethod) {
			return nil, nil
		}
	}
	// Single-use: remove the code before returning it.
	_ = s.redis.Cache.Remove(ctx, cacheKeyPrefixAuthCode+code)
	return &info, nil
}

func verifyCodeChallenge(codeVerifier, codeChallenge, method string) bool {
	if codeVerifier == "" {
		return false
	}
	if method != codeChallengeMethodS256 {
		return method == codeChallengeMethodPlain && codeVerifier == codeChallenge
	}
	sum := sha256.Sum256([]byte(codeVerifier))
	encoded := base64.RawURLEncoding.EncodeToString(sum[:])
	return encoded == codeChallenge
}

func verifyCodeChallengeWithFallback(codeVerifier, codeChallenge string, method *string) bool {
	normalized := ""
	if method != nil {
		normalized = strings.ToUpper(*method)
	}
	switch normalized {
	case codeChallengeMethodS256:
		return verifyCodeChallenge(codeVerifier, codeChallenge, codeChallengeMethodS256)
	case codeChallengeMethodPlain:
		return verifyCodeChallenge(codeVerifier, codeChallenge, codeChallengeMethodPlain)
	default:
		return verifyCodeChallenge(codeVerifier, codeChallenge, codeChallengeMethodS256) ||
			verifyCodeChallenge(codeVerifier, codeChallenge, codeChallengeMethodPlain)
	}
}

// --- Device codes ---

func (s *service) generateDeviceCode(ctx context.Context, clientID string, scopes []string, nonce *string) (*deviceCodeInfo, error) {
	now := time.Now().UTC()
	info := &deviceCodeInfo{
		DeviceCode:             generateRandomString(32),
		UserCode:               generateUserCode(),
		ClientId:               clientID,
		Scopes:                 scopes,
		Nonce:                  nonce,
		Status:                 deviceCodeStatusPending,
		CreatedAt:              now,
		ExpiresAt:              now.Add(deviceCodeLifetime),
		PollingIntervalSeconds: deviceCodePollingIntervalSeconds,
	}
	if err := s.redis.Cache.Set(ctx, cacheKeyPrefixDeviceCode+info.DeviceCode, info, deviceCodeLifetime); err != nil {
		return nil, err
	}
	if err := s.redis.Cache.Set(ctx, cacheKeyPrefixUserCode+info.UserCode, info.DeviceCode, deviceCodeLifetime); err != nil {
		return nil, err
	}
	return info, nil
}

func (s *service) getDeviceCodeByUserCode(ctx context.Context, userCode string) (*deviceCodeInfo, error) {
	var deviceCode string
	found, err := s.redis.Cache.Get(ctx, cacheKeyPrefixUserCode+userCode, &deviceCode)
	if err != nil {
		return nil, err
	}
	if !found || deviceCode == "" {
		return nil, nil
	}
	return s.getDeviceCode(ctx, deviceCode)
}

func (s *service) getDeviceCode(ctx context.Context, deviceCode string) (*deviceCodeInfo, error) {
	var info deviceCodeInfo
	found, err := s.redis.Cache.Get(ctx, cacheKeyPrefixDeviceCode+deviceCode, &info)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &info, nil
}

func (s *service) updateDeviceCode(ctx context.Context, info *deviceCodeInfo) error {
	remaining := time.Until(info.ExpiresAt)
	if remaining <= 0 {
		return nil
	}
	if err := s.redis.Cache.Set(ctx, cacheKeyPrefixDeviceCode+info.DeviceCode, info, remaining); err != nil {
		return err
	}
	return s.redis.Cache.Set(ctx, cacheKeyPrefixUserCode+info.UserCode, info.DeviceCode, remaining)
}

// --- Token flows ---

// handleAuthorizationCodeFlow mirrors HandleAuthorizationCodeFlowAsync.
func (s *service) handleAuthorizationCodeFlow(ctx context.Context, authCode *authorizationCodeInfo, clientID, ipAddress, userAgent string) (*model.AuthSession, *string, []string, error) {
	if authCode.AccountId == nil {
		return nil, nil, nil, errors.New("Invalid authorization code, account id is missing.")
	}
	session, err := s.findValidSession(ctx, *authCode.AccountId, clientID)
	if err != nil {
		return nil, nil, nil, err
	}
	if session == nil {
		session, err = s.authSvc.CreateSessionForOidc(ctx, s.store.DB, *authCode.AccountId, &clientID, nil, ipAddress, userAgent)
		if err != nil {
			return nil, nil, nil, err
		}
		account, err := s.store.GetAccountByID(ctx, uuid.MustParse(session.AccountId))
		if err != nil {
			return nil, nil, nil, err
		}
		session.Account = account
	}
	if err := s.setSessionScopes(ctx, session, authCode.Scopes); err != nil {
		return nil, nil, nil, err
	}
	return session, authCode.Nonce, session.Scopes, nil
}

// handleRefreshTokenFlow mirrors HandleRefreshTokenFlowAsync.
func (s *service) handleRefreshTokenFlow(ctx context.Context, clientID, refreshToken string) (*model.AuthSession, *string, []string, error) {
	isValid, claims := s.jwt.ValidateJwt(refreshToken)
	if !isValid || claims == nil {
		return nil, nil, nil, errors.New("Invalid refresh token")
	}
	if auth.TokenUseOf(claims) != auth.TokenUseRefresh {
		return nil, nil, nil, errors.New("Invalid refresh token")
	}
	jti, ok := auth.ParseUUIDClaim(claims, "jti")
	if !ok || jti == uuid.Nil {
		return nil, nil, nil, errors.New("Invalid refresh token")
	}
	sessionIDText, _ := claims["sid"].(string)
	if sessionIDText == "" {
		sessionIDText = jti.String()
	}
	sessionID, err := uuid.Parse(sessionIDText)
	if err != nil {
		return nil, nil, nil, errors.New("Invalid refresh token")
	}
	accountIDText, _ := claims["sub"].(string)
	accountID, err := uuid.Parse(accountIDText)
	if err != nil {
		return nil, nil, nil, errors.New("Invalid refresh token")
	}
	if claimVersion, ok := auth.ClaimInt(claims, "ver"); ok {
		currentVersion, err := s.token.GetAccountVersion(ctx, accountID.String())
		if err != nil {
			return nil, nil, nil, err
		}
		if int(claimVersion) < currentVersion {
			return nil, nil, nil, errors.New("Refresh token has been invalidated")
		}
	}
	session, err := s.store.GetSessionWithAccount(ctx, sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, nil, errors.New("Session not found")
		}
		return nil, nil, nil, err
	}
	now := time.Now().UTC()
	if session.ExpiredAt != nil && session.ExpiredAt.Time().Before(now) {
		return nil, nil, nil, errors.New("Session has expired")
	}
	if session.AppId == nil || *session.AppId != clientID || session.AccountId != accountID.String() || session.Type != model.SessionTypeOAuth {
		return nil, nil, nil, errors.New("Refresh token does not match client")
	}
	if len(session.Scopes) == 0 {
		authorizedScopes, err := s.store.GetAuthorizedAppScopes(ctx, accountID.String(), clientID)
		if err == nil && authorizedScopes != nil {
			if err := s.setSessionScopes(ctx, session, authorizedScopes); err != nil {
				return nil, nil, nil, err
			}
		}
	}
	if tokenEpoch, ok := auth.ClaimInt(claims, "epoch"); ok && tokenEpoch != session.Epoch {
		return nil, nil, nil, errors.New("Refresh token has been revoked")
	}
	newExpiry := now.Add(s.refreshLifetime)
	if err := s.store.UpdateSessionRefresh(ctx, sessionID.String(), now, newExpiry); err != nil {
		return nil, nil, nil, err
	}
	session.LastGrantedAt = model.NewTime(now)
	session.ExpiredAt = model.NewTime(newExpiry)
	session.Epoch++
	_ = s.redis.Cache.Remove(ctx, "auth:session:"+session.Id)
	_ = s.redis.Raw.Del(ctx, fmt.Sprintf("auth:session_tokens:%s", session.Id)).Err()
	return session, nil, session.Scopes, nil
}

// generateTokenResponseForCode runs the authorization_code grant
// (GenerateTokenResponseAsync with an authorization code).
func (s *service) generateTokenResponseForCode(ctx context.Context, client *oidcClient, code, redirectURI, codeVerifier string, isPublicClient bool, ipAddress, userAgent string) (*tokenResponse, error) {
	var redirectPtr, verifierPtr *string
	if redirectURI != "" {
		redirectPtr = &redirectURI
	}
	if codeVerifier != "" {
		verifierPtr = &codeVerifier
	}
	authCode, err := s.validateAuthorizationCode(ctx, code, client.Id, redirectPtr, verifierPtr, isPublicClient)
	if err != nil {
		return nil, err
	}
	if authCode == nil {
		return nil, errors.New("Invalid authorization code")
	}
	if authCode.AccountId != nil {
		session, nonce, scopes, err := s.handleAuthorizationCodeFlow(ctx, authCode, client.Id, ipAddress, userAgent)
		if err != nil {
			return nil, err
		}
		slug, name := client.Slug, client.Name
		if _, err := s.authSvc.UpsertAuthorizedAppAsync(ctx, session.AccountId, client.Id, model.AuthorizedAppTypeOidc, &slug, &name, scopes); err != nil {
			return nil, err
		}
		return s.issueTokenPair(ctx, client, session, nonce, scopes)
	}
	if authCode.ExternalUserInfo != nil {
		onboardingToken, err := s.generateOnboardingToken(client, authCode.ExternalUserInfo, authCode.Nonce)
		if err != nil {
			return nil, err
		}
		return &tokenResponse{OnboardingToken: &onboardingToken, TokenType: "Onboarding"}, nil
	}
	return nil, errors.New("Invalid authorization code state.")
}

// generateTokenResponseForRefresh runs the refresh_token grant.
func (s *service) generateTokenResponseForRefresh(ctx context.Context, client *oidcClient, refreshToken, ipAddress, userAgent string) (*tokenResponse, error) {
	session, nonce, scopes, err := s.handleRefreshTokenFlow(ctx, client.Id, refreshToken)
	if err != nil {
		return nil, err
	}
	slug, name := client.Slug, client.Name
	if _, err := s.authSvc.UpsertAuthorizedAppAsync(ctx, session.AccountId, client.Id, model.AuthorizedAppTypeOidc, &slug, &name, scopes); err != nil {
		return nil, err
	}
	return s.issueTokenPair(ctx, client, session, nonce, scopes)
}

// handleDeviceCodeGrant mirrors HandleDeviceCodeGrantAsync.
func (s *service) handleDeviceCodeGrant(ctx context.Context, deviceCode, clientID, ipAddress, userAgent string) (*tokenResponse, error) {
	info, err := s.getDeviceCode(ctx, deviceCode)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, errors.New("Invalid device code.")
	}
	if info.ClientId != clientID {
		return nil, errors.New("Device code was not issued to this client.")
	}
	now := time.Now().UTC()
	if now.After(info.ExpiresAt) {
		info.Status = deviceCodeStatusExpired
		_ = s.updateDeviceCode(ctx, info)
		return nil, errors.New("Device code has expired.")
	}
	if info.Status == deviceCodeStatusDeclined {
		return nil, errors.New("Device code authorization was declined.")
	}
	if info.Status != deviceCodeStatusApproved || info.AccountId == nil {
		if info.LastPolledAt != nil && now.Before(info.LastPolledAt.Add(time.Duration(info.PollingIntervalSeconds)*time.Second)) {
			info.PollingIntervalSeconds += deviceCodeSlowDownStepSeconds
			info.LastPolledAt = &now
			_ = s.updateDeviceCode(ctx, info)
			return nil, errors.New("Slow down.")
		}
		info.LastPolledAt = &now
		_ = s.updateDeviceCode(ctx, info)
		return nil, errors.New("Authorization pending.")
	}

	account, err := s.store.GetAccountByID(ctx, uuid.MustParse(*info.AccountId))
	if err != nil {
		return nil, errors.New("Account not found.")
	}
	session, err := s.findValidSession(ctx, account.Id, clientID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		session, err = s.authSvc.CreateSessionForOidc(ctx, s.store.DB, account.Id, &clientID, nil, ipAddress, userAgent)
		if err != nil {
			return nil, err
		}
	}
	if session.Account == nil {
		session.Account = account
	}
	if err := s.setSessionScopes(ctx, session, info.Scopes); err != nil {
		return nil, err
	}
	if _, err := s.authSvc.UpsertAuthorizedAppAsync(ctx, session.AccountId, clientID, model.AuthorizedAppTypeOidc, nil, nil, info.Scopes); err != nil {
		return nil, err
	}
	client, err := s.findClientByID(ctx, clientID)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("Client not found")
	}
	resp, err := s.issueTokenPair(ctx, client, session, info.Nonce, info.Scopes)
	if err != nil {
		return nil, err
	}
	_ = s.redis.Cache.Remove(ctx, cacheKeyPrefixDeviceCode+deviceCode)
	_ = s.redis.Cache.Remove(ctx, cacheKeyPrefixUserCode+info.UserCode)
	return resp, nil
}

// issueTokenPair builds the Bearer token response (access, id, refresh).
func (s *service) issueTokenPair(ctx context.Context, client *oidcClient, session *model.AuthSession, nonce *string, scopes []string) (*tokenResponse, error) {
	now := time.Now().UTC()
	expiresIn := int(s.accessLifetime.Seconds())
	expiresAt := now.Add(s.accessLifetime)

	accessToken, err := s.generateJwtToken(ctx, client, session, expiresAt, scopes)
	if err != nil {
		return nil, err
	}
	idToken, err := s.generateIdToken(ctx, client, session, nonce, scopes)
	if err != nil {
		return nil, err
	}
	sessionVersion, err := s.token.GetAccountVersion(ctx, session.AccountId)
	if err != nil {
		return nil, err
	}
	refreshExpiry := now.Add(s.refreshLifetime)
	if session.ExpiredAt != nil {
		refreshExpiry = session.ExpiredAt.Time()
	}
	refreshToken, err := s.jwt.CreateRefreshToken(session, sessionVersion, refreshExpiry)
	if err != nil {
		return nil, err
	}
	resp := &tokenResponse{
		AccessToken:  &accessToken,
		IdToken:      &idToken,
		ExpiresIn:    expiresIn,
		TokenType:    "Bearer",
		RefreshToken: &refreshToken,
	}
	if len(scopes) > 0 {
		joined := strings.Join(scopes, " ")
		resp.Scope = &joined
	}
	return resp, nil
}

// generateJwtToken mirrors GenerateJwtToken: user token with OIDC
// issuer/audience overrides and an azp claim.
func (s *service) generateJwtToken(ctx context.Context, client *oidcClient, session *model.AuthSession, expiresAt time.Time, scopes []string) (string, error) {
	if session.Account == nil {
		return "", errors.New("Session account is required for OIDC access token.")
	}
	sessionVersion, err := s.token.GetAccountVersion(ctx, session.AccountId)
	if err != nil {
		return "", err
	}
	effectiveScopes := scopes
	if effectiveScopes == nil {
		effectiveScopes = client.AllowedScopes
	}
	return s.jwt.CreateOidcUserTokenWithSigner(s.privateKey, session, session.Account, sessionVersion, expiresAt, s.issuer, client.Slug, effectiveScopes, map[string]any{"azp": client.Slug})
}

// generateIdToken mirrors GenerateIdToken (signed with the OIDC provider key).
func (s *service) generateIdToken(ctx context.Context, client *oidcClient, session *model.AuthSession, nonce *string, scopes []string) (string, error) {
	if s.privateKey == nil {
		return "", errors.New("OIDC private key is not configured")
	}
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"iss": s.issuer,
		"sub": session.AccountId,
		"aud": client.Slug,
		"iat": now.Unix(),
		"exp": now.Add(s.accessLifetime).Unix(),
		"azp": client.Slug,
	}
	if session.CreatedAt != nil {
		claims["auth_time"] = session.CreatedAt.Time().Unix()
	}
	if nonce != nil && *nonce != "" {
		claims["nonce"] = *nonce
	}
	if containsFold(scopes, "email") {
		if contact, err := s.store.GetEmailContact(ctx, session.AccountId); err == nil && contact != nil {
			claims["email"] = contact.Content
			claims["email_verified"] = contact.VerifiedAt != nil
		}
	}
	if scopes != nil && containsFold(scopes, "profile") {
		if session.Account != nil && session.Account.Name != "" {
			claims["preferred_username"] = session.Account.Name
		}
		if session.Account != nil && session.Account.Nick != "" {
			claims["name"] = session.Account.Nick
		}
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return token.SignedString(s.privateKey)
}

// generateOnboardingToken mirrors GenerateOnboardingToken (15-minute
// provider-bound token; only reachable via externally issued codes).
func (s *service) generateOnboardingToken(client *oidcClient, ext *externalUserInfo, nonce *string) (string, error) {
	if s.privateKey == nil {
		return "", errors.New("OIDC private key is not configured")
	}
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"iss":              s.issuer,
		"aud":              client.Slug,
		"iat":              now.Unix(),
		"exp":              now.Add(15 * time.Minute).Unix(),
		"provider":         ext.Provider,
		"provider_user_id": ext.UserId,
	}
	if ext.Email != nil && *ext.Email != "" {
		claims["email"] = *ext.Email
	}
	if ext.Name != nil && *ext.Name != "" {
		claims["name"] = *ext.Name
	}
	if nonce != nil && *nonce != "" {
		claims["nonce"] = *nonce
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return token.SignedString(s.privateKey)
}

// validateToken mirrors OidcProviderService.ValidateToken: OIDC issuer,
// no audience check, lifetime enforced, RS256 only.
func (s *service) validateToken(token string) (jwt.MapClaims, bool) {
	if s.publicKey == nil {
		return nil, false
	}
	parser := jwt.NewParser(jwt.WithValidMethods([]string{"RS256"}))
	parsed, err := parser.Parse(token, func(t *jwt.Token) (any, error) {
		return s.publicKey, nil
	})
	if err != nil {
		return nil, false
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok || !parsed.Valid {
		return nil, false
	}
	if iss, _ := claims["iss"].(string); iss != s.issuer {
		return nil, false
	}
	return claims, true
}

// setSessionScopes mirrors SetSessionScopesAsync (persist + cache bust).
func (s *service) setSessionScopes(ctx context.Context, session *model.AuthSession, scopes []string) error {
	var normalized []string
	seen := map[string]struct{}{}
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		key := strings.ToLower(scope)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, scope)
	}
	if equalScopesFold(session.Scopes, normalized) {
		return nil
	}
	if err := s.store.UpdateSessionScopes(ctx, session.Id, normalized); err != nil {
		return err
	}
	session.Scopes = normalized
	_ = s.redis.Cache.Remove(ctx, "auth:session:"+session.Id)
	_ = s.redis.Raw.Del(ctx, fmt.Sprintf("auth:session_tokens:%s", session.Id)).Err()
	return nil
}

func (s *service) findValidSession(ctx context.Context, accountID, clientID string) (*model.AuthSession, error) {
	session, err := s.store.FindValidOauthSession(ctx, accountID, clientID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return session, nil
}

// --- Helpers ---

func containsFold(list []string, needle string) bool {
	for _, item := range list {
		if strings.EqualFold(item, needle) {
			return true
		}
	}
	return false
}

func equalScopesFold(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !strings.EqualFold(a[i], b[i]) {
			return false
		}
	}
	return true
}

const randomStringChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~"

// generateRandomString mirrors the C# GenerateRandomString (same charset and
// modulo-biased mapping, byte-for-byte distribution parity).
func generateRandomString(length int) string {
	buf := make([]byte, length)
	for i := range buf {
		var rb [4]byte
		_, _ = rand.Read(rb[:])
		buf[i] = randomStringChars[uint32(binary.LittleEndian.Uint32(rb[:]))%uint32(len(randomStringChars))]
	}
	return string(buf)
}

const userCodeChars = "BCDFGHJKLMNPQRSTVWXYZ"

// generateUserCode mirrors the C# GenerateUserCode (XXXX-XXXX).
func generateUserCode() string {
	code := make([]byte, 9)
	bytes := make([]byte, 8)
	_, _ = rand.Read(bytes)
	for i := range code {
		if i == 4 {
			code[i] = '-'
			continue
		}
		sourceIndex := i
		if i > 4 {
			sourceIndex = i - 1
		}
		code[i] = userCodeChars[bytes[sourceIndex]%byte(len(userCodeChars))]
	}
	return string(code)
}

func strPtr(s string) *string { return &s }
