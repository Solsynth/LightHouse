package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// CreateOidcUserToken signs an OIDC access token, mirroring
// AuthJwtService.CreateUserToken with issuerOverride, audienceOverride,
// scopesOverride and additionalClaims as used by the OIDC provider
// (OidcProviderService.GenerateJwtToken). Unlike the plain CreateUserToken
// it sets explicit iss/aud claims (the C# CreateJwt always sets them) and
// serializes the repeatable "scope" claim as a JSON array, matching how
// JwtSecurityTokenHandler writes repeated claims.
func (s *JWTService) CreateOidcUserToken(session *model.AuthSession, account *model.Account, accountVersion int, expiresAt time.Time, issuer, audience string, scopes []string, additionalClaims map[string]any) (string, error) {
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
		claims["scope"] = scopes
	}
	for k, v := range additionalClaims {
		claims[k] = v
	}
	return s.sign(claims, now, expiresAt)
}
