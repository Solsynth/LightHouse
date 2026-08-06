package oidcctl

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// handleConfiguration mirrors OidcProviderController.GetConfiguration. The
// endpoint/issuer URLs use the trimmed issuer and the configured SiteUrl /
// BaseUrl; the provider endpoints use the /stargate service prefix the
// deployed gateway exposes (Padlock fully replaced — the legacy /padlock
// prefix is no longer routed).
func (s *service) handleConfiguration(c *gin.Context) {
	issuer := strings.TrimSuffix(s.issuer, "/")
	baseUrl := strings.TrimSuffix(s.cfg.BaseUrl, "/")
	siteUrl := strings.TrimSuffix(s.cfg.SiteUrl, "/")

	c.JSON(http.StatusOK, gin.H{
		"issuer":                                issuer,
		"authorization_endpoint":                siteUrl + "/auth/authorize",
		"device_authorization_endpoint":         baseUrl + "/stargate/auth/open/device/code",
		"token_endpoint":                        baseUrl + "/stargate/auth/open/token",
		"userinfo_endpoint":                     baseUrl + "/stargate/auth/open/userinfo",
		"jwks_uri":                              baseUrl + "/.well-known/jwks",
		"scopes_supported":                      []string{"openid", "profile", "email"},
		"response_types_supported":              []string{"code", "token", "id_token", "code token", "code id_token", "token id_token", "code token id_token"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token", "urn:ietf:params:oauth:grant-type:device_code"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post", "none"},
		"id_token_signing_alg_values_supported": []string{"HS256", "RS256"},
		"subject_types_supported":               []string{"public"},
		"claims_supported":                      []string{"sub", "name", "email", "email_verified"},
		"code_challenge_methods_supported":      []string{"S256", "plain"},
		"response_modes_supported":              []string{"query", "fragment", "form_post"},
		"request_parameter_supported":           true,
		"request_uri_parameter_supported":       true,
		"require_request_uri_registration":      false,
	})
}

// handleJwks mirrors OidcProviderController.GetJwks: kid is
// base64url(SHA256(modulus)[:8]) with the url-safe unpadded alphabet.
func (s *service) handleJwks(c *gin.Context) {
	if s.publicKey == nil {
		c.String(http.StatusBadRequest, "Public key is not configured")
		return
	}
	modulus := s.publicKey.N.FillBytes(make([]byte, s.publicKey.Size()))
	exponent := bigEndianBytes(s.publicKey.E)
	sum := sha256.Sum256(modulus)
	kid := base64.RawURLEncoding.EncodeToString(sum[:8])

	c.JSON(http.StatusOK, gin.H{
		"keys": []gin.H{{
			"kty": "RSA",
			"use": "sig",
			"kid": kid,
			"n":   base64.RawURLEncoding.EncodeToString(modulus),
			"e":   base64.RawURLEncoding.EncodeToString(exponent),
			"alg": "RS256",
		}},
	})
}

// bigEndianBytes renders an int as the minimal big-endian bytes (matches
// RSA.ExportParameters for exponents < 2^32).
func bigEndianBytes(v int) []byte {
	if v <= 0 {
		return []byte{0}
	}
	u := uint32(v)
	var buf [4]byte
	buf[0] = byte(u >> 24)
	buf[1] = byte(u >> 16)
	buf[2] = byte(u >> 8)
	buf[3] = byte(u)
	start := 0
	for start < 3 && buf[start] == 0 {
		start++
	}
	return buf[start:]
}
