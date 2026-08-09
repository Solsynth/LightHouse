package auth

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/model"
)

// ClaimInt reads an integer claim that may be serialized either as a JSON
// number (float64 after parsing) or as a string: the C# minting writes
// ver/epoch as strings (JWT claims are string-valued) and validates with
// int.TryParse, so a float64-only assertion silently skips the check on
// fleet-issued tokens.
func ClaimInt(claims jwt.MapClaims, name string) (int, bool) {
	switch v := claims[name].(type) {
	case float64:
		return int(v), true
	case string:
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// ClaimType is the JWT claim holding the token use ("user"|"refresh"|"api_key").
// Legacy tokens use "token_use" instead; the validation path falls back to it.
const ClaimType = "type"

// LegacyClaimTokenUse is the historical token-use claim name.
const LegacyClaimTokenUse = "token_use"

// TokenUse values.
const (
	TokenUseUser    = "user"
	TokenUseRefresh = "refresh"
	TokenUseApiKey  = "api_key"
)

// JWTService signs and validates the RS256 JWTs exactly like Padlock's
// AuthJwtService. The signing keys MUST be the same PEM files the C# fleet
// used, so in-flight tokens keep validating.
type JWTService struct {
	issuer       string
	validIssuers []string
	audience     string
	private      *rsa.PrivateKey
	public       *rsa.PublicKey
}

// NewJWTService loads the RSA key pair from the configured paths.
func NewJWTService(cfg *config.Config) (*JWTService, error) {
	private, err := loadRSAPrivateKey(cfg.Auth.PrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load auth private key: %w", err)
	}
	public, err := loadRSAPublicKey(cfg.Auth.PublicKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load auth public key: %w", err)
	}
	issuer := cfg.Auth.Issuer
	if issuer == "" {
		issuer = "solar-network"
	}
	validIssuers := make([]string, 0, len(cfg.Auth.ValidIssuers)+2)
	for _, candidate := range append(append(append([]string(nil), cfg.Auth.ValidIssuers...), issuer), cfg.OidcProvider.IssuerUri) {
		if candidate == "" {
			continue
		}
		alreadyListed := false
		for _, listed := range validIssuers {
			if listed == candidate {
				alreadyListed = true
				break
			}
		}
		if !alreadyListed {
			validIssuers = append(validIssuers, candidate)
		}
	}
	audience := ""
	if len(cfg.Auth.Audiences) > 0 {
		audience = cfg.Auth.Audiences[0]
	}
	if audience == "" {
		audience = "solar-network"
	}
	return &JWTService{
		issuer: issuer, validIssuers: validIssuers, audience: audience,
		private: private, public: public,
	}, nil
}

func (s *JWTService) issuerValid(issuer string) bool {
	if issuer == "" {
		return false
	}
	for _, validIssuer := range s.validIssuers {
		if issuer == validIssuer {
			return true
		}
	}
	return issuer == s.issuer
}

func loadRSAPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rsaKey, ok := key.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, errors.New("unsupported private key format")
}

func loadRSAPublicKey(path string) (*rsa.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
		if pub, ok := cert.PublicKey.(*rsa.PublicKey); ok {
			return pub, nil
		}
	}
	if key, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		if rsaKey, ok := key.(*rsa.PublicKey); ok {
			return rsaKey, nil
		}
	}
	if key, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, errors.New("unsupported public key format")
}

// PublicKey exposes the RSA public key (used by JWKS).
func (s *JWTService) PublicKey() *rsa.PublicKey { return s.public }

// CreateUserToken signs a user token with the exact claim set of the C#.
func (s *JWTService) CreateUserToken(session *model.AuthSession, account *model.Account, accountVersion int, expiresAt time.Time) (string, error) {
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"sub":          account.Id,
		"jti":          session.Id,
		"sid":          session.Id,
		ClaimType:      TokenUseUser,
		"ver":          fmt.Sprintf("%d", accountVersion),
		"epoch":        fmt.Sprintf("%d", session.Epoch),
		"is_superuser": boolStr(account.IsSuperuser),
		"name":         account.Name,
		"nick":         account.Nick,
		"region":       account.Region,
		"perk_level":   fmt.Sprintf("%d", account.PerkLevel),
		"iat":          now.Unix(),
		"nbf":          now.Unix(),
		"exp":          expiresAt.Unix(),
	}
	if len(session.Scopes) > 0 {
		claims["scope"] = strings.Join(session.Scopes, " ")
	}
	return s.sign(claims, now, expiresAt)
}

// CreateRefreshToken signs a refresh token.
func (s *JWTService) CreateRefreshToken(session *model.AuthSession, accountVersion int, expiresAt time.Time) (string, error) {
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"sub":     session.AccountId,
		"jti":     session.Id,
		"sid":     session.Id,
		ClaimType: TokenUseRefresh,
		"ver":     fmt.Sprintf("%d", accountVersion),
		"epoch":   fmt.Sprintf("%d", session.Epoch),
		"iat":     now.Unix(),
		"nbf":     now.Unix(),
		"exp":     expiresAt.Unix(),
	}
	return s.sign(claims, now, expiresAt)
}

// CreateBotToken signs an API-key (bot) token.
func (s *JWTService) CreateBotToken(key *model.ApiKey, session *model.AuthSession, accountVersion int) (string, error) {
	now := time.Now().UTC()
	expiresAt := time.Now().UTC().Add(30 * 24 * time.Hour)
	if session.ExpiredAt != nil {
		expiresAt = session.ExpiredAt.Time()
	}
	claims := jwt.MapClaims{
		"sub":        key.AccountId,
		"jti":        session.Id,
		"sid":        session.Id,
		ClaimType:    TokenUseApiKey,
		"api_key_id": key.Id,
		"account_id": key.AccountId,
		"ver":        fmt.Sprintf("%d", accountVersion),
		"epoch":      fmt.Sprintf("%d", session.Epoch),
		"iat":        now.Unix(),
		"nbf":        now.Unix(),
		"exp":        expiresAt.Unix(),
	}
	return s.sign(claims, now, expiresAt)
}

func (s *JWTService) sign(claims jwt.MapClaims, now, expiresAt time.Time) (string, error) {
	// iss/aud are added centrally, mirroring the C# CreateJwt.
	claims["iss"] = s.issuer
	claims["aud"] = s.audience
	// OAuth scope claims are serialized as one space-delimited string.
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "solar-network"
	return token.SignedString(s.private)
}

// parseAndVerify validates the JWT signature and algorithm without applying
// issuer, audience, or expiry policy.
func (s *JWTService) parseAndVerify(tokenText string) (jwt.MapClaims, bool) {
	parser := jwt.NewParser(jwt.WithValidMethods([]string{"RS256"}), jwt.WithoutClaimsValidation())
	token, err := parser.Parse(tokenText, func(t *jwt.Token) (any, error) {
		return s.public, nil
	})
	if err != nil || !token.Valid {
		return nil, false
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	return claims, ok
}

// ValidateJwt validates signature, issuer, audience and nbf. Like the C#,
// exp is NOT enforced here (the caller governs it: AuthenticateToken maps an
// expired access token to TOKEN_EXPIRED, session expiry governs refresh
// tokens); nbf is checked with a 1-minute clock skew. WithoutClaimsValidation
// is required because the golang-jwt v5 default validator enforces exp.
func (s *JWTService) ValidateJwt(tokenText string) (bool, jwt.MapClaims) {
	claims, ok := s.parseAndVerify(tokenText)
	if !ok {
		return false, nil
	}
	if iss, _ := claims["iss"].(string); !s.issuerValid(iss) {
		return false, nil
	}
	// Audience may be a string or an array of strings.
	audOK := false
	if aud, ok := claims["aud"].(string); ok && aud == s.audience {
		audOK = true
	} else if auds, ok := claims["aud"].([]any); ok {
		for _, a := range auds {
			if av, ok := a.(string); ok && av == s.audience {
				audOK = true
				break
			}
		}
	}
	if !audOK {
		return false, nil
	}
	if nbf, ok := claims["nbf"].(float64); ok {
		skew := time.Now().Add(time.Minute)
		if time.Unix(int64(nbf), 0).After(skew) {
			return false, nil
		}
	}
	return true, claims
}

// TokenUseOf extracts the token-use claim with the legacy fallback.
func TokenUseOf(claims jwt.MapClaims) string {
	if v, ok := claims[ClaimType].(string); ok && v != "" {
		return v
	}
	if v, ok := claims[LegacyClaimTokenUse].(string); ok && v != "" {
		return v
	}
	return TokenUseUser
}

// ParseUUIDClaim parses a claim as a UUID, returning false when absent/invalid.
func ParseUUIDClaim(claims jwt.MapClaims, name string) (uuid.UUID, bool) {
	v, ok := claims[name].(string)
	if !ok {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(v)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// IsApiKeyTokenString reports whether the JWT's "type" claim is api_key,
// matching Golaunch pkg/auth.IsApiKeyToken.
func IsApiKeyTokenString(token string) bool {
	if strings.Count(token, ".") != 2 {
		return false
	}
	payload, _, err := jwt.NewParser().ParseUnverified(token, jwt.MapClaims{})
	if err != nil {
		return false
	}
	claims, ok := payload.Claims.(jwt.MapClaims)
	if !ok {
		return false
	}
	return claims[ClaimType] == TokenUseApiKey
}
