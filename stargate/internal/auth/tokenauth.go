package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// Session cache key + TTL contract shared with the C# fleet and downstream
// Go services (see Golaunch pkg/auth/authcache.go).
const (
	SessionCacheTTL       = time.Hour
	SessionTokensGroupFmt = "auth:session_tokens:%s"
	AccountSessionsGroup  = "auth:account_sessions:%s"
	AccountVersionPrefix  = "auth:account_ver:"
	RevokedJtiPrefix      = "auth:revoked:jti:"
	RevokedJtiTTL         = 30 * 24 * time.Hour
)

// MsgTokenExpired is the rejection reason for a token whose exp claim has
// passed. The HTTP middleware maps it to the TOKEN_EXPIRED ApiError code so
// clients can refresh and retry.
const MsgTokenExpired = "Token has expired."

// tokenExpired reports whether the JWT exp claim is in the past. The claim
// is read string-or-number (ClaimInt) because the C# minting serializes
// claims as strings (see ClaimInt).
func tokenExpired(claims jwt.MapClaims) bool {
	exp, ok := ClaimInt(claims, "exp")
	return ok && time.Now().Unix() > int64(exp)
}

// TokenType mirrors Padlock's TokenType enum (wire context value).
type TokenType int

const (
	TokenTypeAuthKey TokenType = 0
	TokenTypeApiKey  TokenType = 1
	TokenTypeOidcKey TokenType = 2
	TokenTypeUnknown TokenType = 3
)

// TokenAuthService validates bearer tokens exactly like Padlock's
// TokenAuthService: JWT validation, refresh-token rejection, Redis session
// cache with epoch checks, DB fallback, OIDC-issuer fallback.
type TokenAuthService struct {
	store *store.Store
	redis *redis.Client
	jwt   *JWTService
	perk  PerkProvider
	apps  CustomAppProvider
	oidc  *OIDCValidator
	log   *slog.Logger
}

// PerkProvider hydrates the perk subscription for an account (wallet service).
type PerkProvider interface {
	GetPerkSubscription(ctx context.Context, accountID string) (*model.SnSubscriptionReferenceObject, error)
}

// CustomAppProvider looks up OIDC clients (custom apps) from the Develop
// service, mirroring OidcProviderService.FindClientByIdAsync.
type CustomAppProvider interface {
	GetCustomAppSlug(ctx context.Context, appID string) (string, error)
}

// OIDCValidator validates OIDC-issuer tokens (same key pair, relaxed issuer).
type OIDCValidator struct {
	jwt *JWTService
}

// NewTokenAuthService wires the service.
func NewTokenAuthService(st *store.Store, rc *redis.Client, j *JWTService, perk PerkProvider, apps CustomAppProvider, log *slog.Logger) *TokenAuthService {
	return &TokenAuthService{store: st, redis: rc, jwt: j, perk: perk, apps: apps, oidc: &OIDCValidator{jwt: j}, log: log}
}

// TokenInfo carries an extracted token.
type TokenInfo struct {
	Token string
	Type  TokenType
}

// ExtractToken mirrors Padlock's _ExtractToken order: Authorization header
// (Bearer/Bot), then tk query param, then AuthToken cookie. Legacy AtField /
// AkField schemes are dropped (grace window ended 2026-03-20).
func ExtractToken(r *http.Request) *TokenInfo {
	authHeader := normalizeAuthHeader(r.Header.Get("Authorization"))
	if authHeader != "" {
		if rest, ok := strings.CutPrefix(authHeader, "Bearer "); ok {
			rest = strings.TrimSpace(rest)
			return &TokenInfo{Token: rest, Type: tokenTypeFor(rest)}
		}
		if rest, ok := strings.CutPrefix(authHeader, "Bot "); ok {
			return &TokenInfo{Token: strings.TrimSpace(rest), Type: TokenTypeApiKey}
		}
	}
	if tk := r.URL.Query().Get("tk"); tk != "" {
		return &TokenInfo{Token: tk, Type: TokenTypeAuthKey}
	}
	if cookie, err := r.Cookie("AuthToken"); err == nil && cookie.Value != "" {
		return &TokenInfo{Token: cookie.Value, Type: tokenTypeFor(cookie.Value)}
	}
	return nil
}

func tokenTypeFor(token string) TokenType {
	if strings.Count(token, ".") == 2 {
		return TokenTypeOidcKey
	}
	return TokenTypeAuthKey
}

func normalizeAuthHeader(raw string) string {
	value := strings.TrimSpace(raw)
	if len(value) >= 2 && value[0] == '[' && value[len(value)-1] == ']' {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	return value
}

// AuthenticateToken validates a token and returns the session (with account)
// plus the token use. Errors are returned as (false, nil, message, use).
func (t *TokenAuthService) AuthenticateToken(ctx context.Context, token, ipAddress string) (bool, *model.AuthSession, string, string) {
	if strings.TrimSpace(token) == "" {
		return false, nil, "No token provided.", ""
	}
	tokenHash := sha256.Sum256([]byte(token))
	log := t.log.With("token_fp", hex.EncodeToString(tokenHash[:])[:8])

	// Primary path: Padlock-issued JWT.
	sessionID, tokenUse, tokenEpoch, claims, validated := t.validateToken(token)
	oidcPath := false
	if !validated {
		// OIDC fallback (relaxed issuer validation).
		oidcSessionID, oidcUse, oidcEpoch, oidcClaims, ok := t.validateOidcToken(token)
		if !ok || oidcSessionID == uuid.Nil {
			log.Debug("token validation failed")
			return false, nil, "Invalid token.", ""
		}
		oidcPath = true
		sessionID, tokenUse, tokenEpoch, claims = oidcSessionID, oidcUse, oidcEpoch, oidcClaims
	}
	if tokenUse == TokenUseRefresh {
		return false, nil, "Refresh token cannot be used for authentication.", tokenUse
	}

	// JWT exp is enforced here, unlike ValidateJwt (which mirrors the C# and
	// skips it): an expired access token is a refreshable condition, so it
	// gets a distinct rejection the HTTP layer maps to the TOKEN_EXPIRED
	// ApiError code. Session expiry below still governs the session itself.
	if tokenExpired(claims) {
		return false, nil, MsgTokenExpired, tokenUse
	}

	// Cache hit path.
	cacheKey := "auth:session:" + sessionID.String()
	var cached gen.DyAuthSession
	if found, err := t.redis.Cache.Get(ctx, cacheKey, &cached); err == nil && found && cached.Id != "" {
		effectiveEpoch := tokenEpoch
		if int(cached.Epoch) != effectiveEpoch {
			_ = t.redis.Cache.Remove(ctx, cacheKey)
			return false, nil, "Token has been invalidated.", tokenUse
		}
		if cached.ExpiredAt != nil && cached.ExpiredAt.AsTime().Before(time.Now().UTC()) {
			_ = t.redis.Cache.Remove(ctx, cacheKey)
			return false, nil, "Session has been expired.", tokenUse
		}
		if oidcPath {
			if ok, msg := t.validateOidcBinding(ctx, &cached, claims); !ok {
				return false, nil, msg, tokenUse
			}
		}
		session := sessionFromProto(&cached)
		applyScopesFromToken(session, claims)
		return true, session, "", tokenUse
	}

	// DB path.
	session, err := t.store.GetSessionWithAccount(ctx, sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			log.Info("session not found")
			return false, nil, "Session was not found.", tokenUse
		}
		log.Error("load session", "error", err)
		return false, nil, "Authentication error.", ""
	}
	if session.Epoch != tokenEpoch {
		return false, nil, "Token has been invalidated.", tokenUse
	}
	if session.ExpiredAt != nil && session.ExpiredAt.Time().Before(time.Now().UTC()) {
		return false, nil, "Session has been expired.", tokenUse
	}

	// Perk hydration (wallet).
	t.hydratePerk(ctx, session.Account)

	proto := SessionToProto(session)
	group := "auth:account_sessions:" + session.AccountId
	if err := t.redis.Cache.SetWithGroups(ctx, cacheKey, proto, []string{group}, SessionCacheTTL); err != nil {
		log.Warn("cache session", "error", err)
	}

	if oidcPath {
		if ok, msg := t.validateOidcBinding(ctx, proto, claims); !ok {
			return false, nil, msg, tokenUse
		}
	}
	applyScopesFromToken(session, claims)
	return true, session, "", tokenUse
}

// validateToken implements TokenAuthService.ValidateToken for JWT tokens.
// Legacy compact tokens are rejected (accept-until 2026-03-20 has passed).
func (t *TokenAuthService) validateToken(token string) (sessionID uuid.UUID, tokenUse string, epoch int, claims jwt.MapClaims, ok bool) {
	parts := strings.Count(token, ".") + 1
	if parts != 3 {
		return uuid.Nil, "", 0, nil, false
	}
	isValid, claims := t.jwt.ValidateJwt(token)
	if !isValid {
		return uuid.Nil, "", 0, nil, false
	}
	jti, hasJti := ParseUUIDClaim(claims, "jti")
	if !hasJti {
		return uuid.Nil, "", 0, nil, false
	}
	tokenUse = TokenUseOf(claims)
	if e, ok := ClaimInt(claims, "epoch"); ok {
		epoch = e
	}
	return jti, tokenUse, epoch, claims, true
}

// validateOidcToken accepts tokens signed by the OIDC issuer key with relaxed
// issuer/audience validation (mirrors ValidateOidcTokenRelaxed).
func (t *TokenAuthService) validateOidcToken(token string) (sessionID uuid.UUID, tokenUse string, epoch int, claims jwt.MapClaims, ok bool) {
	// The OIDC provider uses the same RSA key pair (config OidcProvider keys
	// default to the auth keys), so the JWTService validator applies. The
	// relaxed variant skips issuer/audience checks.
	parts := strings.Count(token, ".") + 1
	if parts != 3 {
		return uuid.Nil, "", 0, nil, false
	}
	parser := jwt.NewParser(jwt.WithValidMethods([]string{"RS256"}), jwt.WithoutClaimsValidation())
	parsed, err := parser.Parse(token, func(tk *jwt.Token) (any, error) {
		return t.jwt.PublicKey(), nil
	})
	if err != nil || !parsed.Valid {
		return uuid.Nil, "", 0, nil, false
	}
	claims, ok = parsed.Claims.(jwt.MapClaims)
	if !ok {
		return uuid.Nil, "", 0, nil, false
	}
	sessionID, ok = ParseUUIDClaim(claims, "sid")
	if !ok {
		sessionID, ok = ParseUUIDClaim(claims, "jti")
	}
	if !ok {
		return uuid.Nil, "", 0, nil, false
	}
	tokenUse = TokenUseOf(claims)
	if e, ok := ClaimInt(claims, "epoch"); ok {
		epoch = e
	}
	return sessionID, tokenUse, epoch, claims, true
}

// validateOidcBinding checks an OIDC token against the OAuth session: the
// session must be OAuth-typed with an app id whose slug is in the audience.
func (t *TokenAuthService) validateOidcBinding(ctx context.Context, session *gen.DyAuthSession, jwtClaims jwt.MapClaims) (bool, string) {
	if session.AppId == nil || session.Type != gen.DySessionType_DY_OAUTH {
		return false, "OIDC token is not bound to an OAuth session."
	}
	appID := session.AppId.Value
	slug, err := t.apps.GetCustomAppSlug(ctx, appID)
	if err != nil || slug == "" {
		return false, "OIDC client is not found for this session."
	}
	if jwtClaims == nil {
		return true, ""
	}
	matched := false
	if aud, ok := jwtClaims["aud"].(string); ok && aud == slug {
		matched = true
	} else if auds, ok := jwtClaims["aud"].([]any); ok {
		for _, a := range auds {
			if av, ok := a.(string); ok && av == slug {
				matched = true
				break
			}
		}
	}
	if !matched {
		return false, "OIDC token audience mismatch."
	}
	if azp, ok := jwtClaims["azp"].(string); ok && azp != "" && azp != slug {
		return false, "OIDC token authorized party mismatch."
	}
	return true, ""
}

// unverifiedClaims parses a JWT's payload without signature validation.
func unverifiedClaims(token string) (jwt.MapClaims, bool) {
	parsed, _, err := jwt.NewParser().ParseUnverified(token, jwt.MapClaims{})
	if err != nil {
		return nil, false
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	return claims, ok
}

// firstSessionID extracts the session id claim (sid, falling back to jti).
func firstSessionID(claims jwt.MapClaims) string {
	for _, name := range []string{"sid", "jti"} {
		if v, ok := claims[name].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// IsOidcToken reports whether the token is an OIDC-issued access token
// (relaxed-issuer path) rather than a plain Padlock user token: OIDC tokens
// carry the azp claim and an OIDC provider issuer.
func (t *TokenAuthService) IsOidcToken(token string) bool {
	claims, ok := unverifiedClaims(token)
	if !ok {
		return false
	}
	if _, hasAzp := claims["azp"]; hasAzp {
		return true
	}
	if iss, ok := claims["iss"].(string); ok && iss != "" && iss != t.jwt.issuer {
		return true
	}
	return false
}

// AutoRenewable reports whether an expired token may be silently rotated
// with the given refresh token: the token must be a plain user token (not
// an API key or OIDC-issued) and both must reference the same session. OIDC
// clients rotate through the OAuth refresh grant instead — rotating their
// session here would bump the epoch and revoke the client's refresh tokens.
func (t *TokenAuthService) AutoRenewable(token, refreshToken string) bool {
	if IsApiKeyTokenString(token) {
		return false
	}
	if t.IsOidcToken(token) {
		return false
	}
	claims, ok := unverifiedClaims(token)
	if !ok {
		return false
	}
	refreshClaims, ok := unverifiedClaims(refreshToken)
	if !ok {
		return false
	}
	sid := firstSessionID(claims)
	refreshSid := firstSessionID(refreshClaims)
	return sid != "" && sid == refreshSid
}

func (t *TokenAuthService) hydratePerk(ctx context.Context, account *model.Account) {
	if account == nil || t.perk == nil {
		return
	}
	sub, err := t.perk.GetPerkSubscription(ctx, account.Id)
	if err != nil {
		t.log.Warn("hydrate perk", "account", account.Id, "error", err)
		account.PerkSubscription = nil
		account.PerkLevel = 0
		return
	}
	if sub == nil {
		account.PerkSubscription = nil
		account.PerkLevel = 0
		return
	}
	account.PerkSubscription = sub
	account.PerkLevel = sub.PerkLevel
}

// applyScopesFromToken overrides the session scopes with the token's scopes.
func applyScopesFromToken(session *model.AuthSession, claims jwt.MapClaims) {
	if claims == nil {
		return
	}
	var scopes []string
	seen := map[string]struct{}{}
	if v, ok := claims["scope"].(string); ok {
		for _, part := range strings.Fields(v) {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if _, dup := seen[part]; !dup {
				seen[part] = struct{}{}
				scopes = append(scopes, part)
			}
		}
	} else if arr, ok := claims["scope"].([]any); ok {
		for _, item := range arr {
			if part, ok := item.(string); ok && part != "" {
				if _, dup := seen[part]; !dup {
					seen[part] = struct{}{}
					scopes = append(scopes, part)
				}
			}
		}
	}
	if len(scopes) > 0 {
		session.Scopes = scopes
	}
}

func sessionFromProto(p *gen.DyAuthSession) *model.AuthSession {
	session := &model.AuthSession{
		Id:            p.Id,
		LastGrantedAt: protoTimeToModel(p.LastGrantedAt),
		ExpiredAt:     protoTimeToModel(p.ExpiredAt),
		AccountId:     p.AccountId,
		Audiences:     p.Audiences,
		Scopes:        p.Scopes,
		Type:          model.SessionType(p.Type),
		Epoch:         int(p.Epoch),
	}
	if p.IpAddress != nil {
		v := p.IpAddress.Value
		session.IpAddress = &v
	}
	if p.UserAgent != nil {
		v := p.UserAgent.Value
		session.UserAgent = &v
	}
	if p.ClientId != nil {
		v := *p.ClientId
		session.ClientId = &v
	}
	if p.AppId != nil {
		v := p.AppId.Value
		session.AppId = &v
	}
	if p.Account != nil {
		session.Account = accountFromProto(p.Account)
	}
	return session
}

func protoTimeToModel(t *timestamppb.Timestamp) *model.Time {
	if t == nil {
		return nil
	}
	return model.NewTime(t.AsTime())
}

func accountFromProto(p *gen.DyAccount) *model.Account {
	a := &model.Account{
		Id:          p.Id,
		Name:        p.Name,
		Nick:        p.Nick,
		Language:    p.Language,
		Region:      p.Region,
		ActivatedAt: protoTimeToModel(p.ActivatedAt),
		IsSuperuser: p.IsSuperuser,
		CreatedAt:   protoTimeToModel(p.CreatedAt),
		UpdatedAt:   protoTimeToModel(p.UpdatedAt),
		PerkLevel:   int(p.GetPerkLevel()),
	}
	if p.AutomatedId != nil {
		v := p.AutomatedId.Value
		a.AutomatedId = &v
	}
	if p.PerkSubscription != nil && p.PerkSubscription.Id != "" {
		sub := p.PerkSubscription
		a.PerkSubscription = &model.SnSubscriptionReferenceObject{
			Id:          sub.Id,
			Identifier:  sub.Identifier,
			DisplayName: sub.DisplayName,
			PerkLevel:   int(sub.PerkLevel),
			IsActive:    sub.IsActive,
			IsAvailable: sub.IsAvailable,
			IsFreeTrial: sub.IsFreeTrial,
			Status:      int(sub.Status),
			BegunAt:     protoTimeToModel(sub.BegunAt),
			EndedAt:     protoTimeToModel(sub.EndedAt),
			RenewalAt:   protoTimeToModel(sub.RenewalAt),
			BasePrice:   sub.BasePrice,
			FinalPrice:  sub.FinalPrice,
			AccountId:   sub.AccountId,
			CreatedAt:   protoTimeToModel(sub.CreatedAt),
		}
	}
	return a
}

// GetAccountVersion reads auth:account_ver:{id} (0 when absent).
func (t *TokenAuthService) GetAccountVersion(ctx context.Context, accountID string) (int, error) {
	var v int
	found, err := t.redis.Cache.Get(ctx, AccountVersionPrefix+accountID, &v)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, nil
	}
	return v, nil
}

// BumpAccountVersion increments auth:account_ver:{id} with a 90d TTL.
func (t *TokenAuthService) BumpAccountVersion(ctx context.Context, accountID string) (int, error) {
	current, err := t.GetAccountVersion(ctx, accountID)
	if err != nil {
		return 0, err
	}
	next := current + 1
	if err := t.redis.Cache.Set(ctx, AccountVersionPrefix+accountID, next, 90*24*time.Hour); err != nil {
		return 0, err
	}
	return next, nil
}

// RevokeJti records a revoked JTI with a 30-day TTL via the shared cache
// (dyson: prefix). Note: the current C# code never writes this key — token
// invalidation is enforced by session epoch + account version — but the
// constant exists for downstream compatibility.
func (t *TokenAuthService) RevokeJti(ctx context.Context, jti string) error {
	return t.redis.Cache.Set(ctx, RevokedJtiPrefix+jti, "1", RevokedJtiTTL)
}
