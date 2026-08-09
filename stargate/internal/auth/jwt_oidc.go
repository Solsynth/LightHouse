package auth

import (
	"crypto/rsa"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// CreateOidcUserToken signs an OIDC access token with the auth key pair.
//
// OIDC issuer and audience are explicit token parameters; they must not be
// replaced with the defaults used by ordinary Stargate tokens.
func (s *JWTService) CreateOidcUserToken(session *model.AuthSession, account *model.Account, accountVersion int, expiresAt time.Time, issuer, audience string, scopes []string, additionalClaims map[string]any) (string, error) {
	return s.createOidcUserToken(s.private, session, account, accountVersion, expiresAt, issuer, audience, scopes, additionalClaims)
}

// CreateOidcUserTokenWithSigner signs an OIDC access token with the provider
// key. This is required when the OIDC provider is configured with a key pair
// distinct from the ordinary auth key pair.
func (s *JWTService) CreateOidcUserTokenWithSigner(signer *rsa.PrivateKey, session *model.AuthSession, account *model.Account, accountVersion int, expiresAt time.Time, issuer, audience string, scopes []string, additionalClaims map[string]any) (string, error) {
	return s.createOidcUserToken(signer, session, account, accountVersion, expiresAt, issuer, audience, scopes, additionalClaims)
}

func (s *JWTService) createOidcUserToken(signer *rsa.PrivateKey, session *model.AuthSession, account *model.Account, accountVersion int, expiresAt time.Time, issuer, audience string, scopes []string, additionalClaims map[string]any) (string, error) {
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"iss":          issuer,
		"aud":          audience,
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
	if len(scopes) > 0 {
		claims["scope"] = strings.Join(scopes, " ")
	}
	for k, v := range additionalClaims {
		claims[k] = v
	}
	if signer == nil {
		return "", fmt.Errorf("OIDC signing key is not configured")
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "solar-network"
	return token.SignedString(signer)
}
