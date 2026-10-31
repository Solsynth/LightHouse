// Package handoff implements the cross-domain session hand-off shared by the
// Solar Network first-party apps.
//
// # The problem it solves
//
// A session is carried in an HttpOnly cookie, and a cookie has exactly one
// Domain attribute that must domain-match the request host (RFC 6265 §4.1.2.3;
// a browser drops a cookie whose Domain does not match). Two first-party apps
// on unrelated registrable domains — say id.solian.app and dx.solsynth.dev —
// therefore cannot share a browser session however much they trust each other,
// because no single response can plant a cookie on both. Setting the Domain to
// ".solian.app,.solsynth.dev" is not a list: the user agent rejects the value
// as invalid and stores a host-only cookie.
//
// # The protocol
//
// The issuer (the deployment that owns the sign-in page and mints sessions)
// hands the relying app a single-use code in a redirect; the app redeems it
// server-to-server and sets its own session cookie on its own domain.
//
//	app console      app server        issuer console      issuer server
//	     |                 |                  |                  |
//	     |-- 302 {issuer}/sign-in?redirectUri={app page} ------->|
//	     |                 |                  |-- sign-in ------>| cookie on the issuer domain
//	     |                 |                  |-- POST /issue -->| code (single use, bound to
//	     |                 |                  |                  |   the app, 60s)
//	     |<-- 302 {app page}?code=… ------------------------------|
//	     |-- POST {own server} /auth/session {code} ------------->| (same-origin; the browser
//	     |                 |                  |                  |  reaches its own server)
//	     |                 |-- POST {issuer}/redeem {code, secret} ------>|
//	     |                 |<-- {token, refresh_token, …} ----------------|
//	     |<-- Set-Cookie AuthToken, Domain={app domain} ---------|
//
// The code is a bearer credential for a short window, so: it is single-use,
// it expires (default 60s), and it is redeemable only by the client it was
// issued to (confidential clients prove that with a shared secret; public
// clients must use PKCE). The issuer validates the target host before issuing,
// so an attacker cannot park a code on a domain the fleet does not own.
//
// The app's server performs the redemption so the secret never reaches the
// browser and so the tokens are never placed in a URL, the history, or a
// referrer. The only thing in the address bar is the one-time code, which the
// app's console strips as soon as it has been exchanged.
//
// # Sharing a cache instead of calling over HTTP
//
// Issuer and Redeemer speak through a cache.CacheService. When both sides share
// one Redis (the usual deployment) they can drive the same code store directly;
// when they do not, the relying app uses HTTPClient to reach the issuer's
// redeem endpoint, which MountRedeem serves.
package handoff

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"time"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/cache"
)

// Default policy values. Every one of them is overridable per deployment; they
// are the conservative end of the useful range.
const (
	// DefaultCodeTTL is how long a code stays redeemable. The window only has
	// to cover the navigation plus one round trip; keeping it short bounds what
	// a leaked code (a third-party script reading the address bar) is worth.
	DefaultCodeTTL = 60 * time.Second
	// MinCodeTTL and MaxCodeTTL bound a configured value.
	MinCodeTTL = 15 * time.Second
	MaxCodeTTL = 10 * time.Minute

	// CodeLength is the number of random bytes behind a code (43 base64url
	// characters). Matching the issuer's other single-use tokens.
	CodeLength = 32

	codeKeyPrefix = "auth:handoff:code:"
	codeLockTTL   = 10 * time.Second
	codeLockWait  = 2 * time.Second
	codeLockRetry = 25 * time.Millisecond
)

// OAuth-style error codes used by both the wire responses and the SDK errors.
const (
	CodeInvalidRequest             = "invalid_request"
	CodeInvalidClient              = "invalid_client"
	CodeInvalidGrant               = "invalid_grant"
	CodeInvalidTarget              = "invalid_target"
	CodeUnsupportedChallengeMethod = "unsupported_code_challenge_method"
	CodeServerError                = "server_error"
)

// PKCE code challenge methods.
const (
	ChallengeMethodS256  = "S256"
	ChallengeMethodPlain = "plain"
)

// Error is a protocol failure carrying an OAuth-style code so an HTTP endpoint
// can hand it back verbatim without leaking internals.
type Error struct {
	Code    string
	Message string
	Status  int
}

func (e *Error) Error() string {
	return e.Code + ": " + e.Message
}

// NewError builds a protocol error, deriving the HTTP status from the code.
func NewError(code, message string) *Error {
	return &Error{Code: code, Message: message, Status: StatusForCode(code)}
}

// StatusForCode maps a protocol error code to the HTTP status an endpoint
// should answer with.
func StatusForCode(code string) int {
	switch code {
	case CodeInvalidClient:
		return http.StatusUnauthorized
	case CodeServerError:
		return http.StatusInternalServerError
	default:
		return http.StatusBadRequest
	}
}

// IsCode reports whether err is a protocol error with the given code.
func IsCode(err error, code string) bool {
	var target *Error
	if !asError(err, &target) {
		return false
	}
	return target.Code == code
}

func asError(err error, target **Error) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(*Error); ok {
		*target = e
		return true
	}
	return false
}

// Code is the server-side record of an issued hand-off. It is stored in the
// cache under one key, consumed by the first successful redemption, and never
// exposed to the browser beyond the opaque code string itself.
type Code struct {
	// ClientID is the relying app this code was issued to. Redeeming it with
	// any other client is refused.
	ClientID string `json:"client_id"`
	// RedirectURI is the validated absolute target the app asked for, with its
	// scheme, host, path and query frozen at issue time.
	RedirectURI string `json:"redirect_uri"`
	// SessionID and AccountID identify the issuer-side session the code may be
	// exchanged into a token pair for. The issuer mints; the code only names
	// what to mint from.
	SessionID string `json:"session_id"`
	AccountID string `json:"account_id"`
	// Scopes are the scopes the issued session may carry into the app's token.
	Scopes []string `json:"scopes,omitempty"`
	// CodeChallenge is the PKCE challenge a public client must satisfy. Empty
	// for confidential clients.
	CodeChallenge string `json:"code_challenge,omitempty"`
	// ChallengeMethod is S256 or plain, whatever the client asked for.
	ChallengeMethod string    `json:"challenge_method,omitempty"`
	IssuedAt        time.Time `json:"issued_at"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// Expired reports whether the code's own lifetime has elapsed. The cache TTL
// normally removes it first; this covers a cache that outlives its entry.
func (c *Code) Expired(now time.Time) bool {
	return c == nil || !now.Before(c.ExpiresAt)
}

// Grant is what a redeemed code yields: the identity the relying app may mint
// its own tokens for. It deliberately carries no token — only the issuer can
// mint issuer tokens, and the app mints its own from this.
type Grant struct {
	ClientID    string    `json:"client_id"`
	SessionID   string    `json:"session_id"`
	AccountID   string    `json:"account_id"`
	Scopes      []string  `json:"scopes,omitempty"`
	RedirectURI string    `json:"redirect_uri"`
	IssuedAt    time.Time `json:"issued_at"`
}

// CodeKey is the cache key holding a code record. The cache service adds the
// shared "dyson:" prefix, so the stored Redis key is
// "dyson:auth:handoff:code:{code}".
func CodeKey(code string) string {
	return codeKeyPrefix + code
}

// Target is a validated hand-off destination.
type Target struct {
	// URI is the normalized absolute URL: scheme://host[:port] + path + query.
	URI string
	// Scheme is "https" or "http" (lowercased).
	Scheme string
	// Host is the hostname without port (lowercased, no trailing dot).
	Host string
}

// RequiresHTTPS reports whether the target's scheme is https.
func (t *Target) RequiresHTTPS() bool {
	return t != nil && t.Scheme == "https"
}

// ValidateTarget applies the hand-off target policy: an absolute http(s) URL
// with no credentials, whose host is one of the allowed hosts (exactly, or a
// subdomain of one), and which is https when requireHTTPS is set.
//
// allowedHosts entries may carry a leading dot (".solsynth.dev" and
// "solsynth.dev" name the same scope) and any port, which is stripped: a host
// list is about domains, not origins. A port in the target is preserved and
// does not affect the match.
func ValidateTarget(raw string, allowedHosts []string, requireHTTPS bool) (*Target, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, NewError(CodeInvalidTarget, "A redirect target is required.")
	}
	// Control characters would let a target smuggle a header or a new URL past
	// a caller that logs or echoes it; a protocol-relative "//host" is a
	// navigation to another site wearing a path's clothing.
	if strings.ContainsAny(trimmed, "\r\n\t") || strings.HasPrefix(trimmed, "//") {
		return nil, NewError(CodeInvalidTarget, "The redirect target is not a valid absolute URL.")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return nil, NewError(CodeInvalidTarget, "The redirect target is not a valid absolute URL.")
	}
	if parsed.User != nil {
		return nil, NewError(CodeInvalidTarget, "The redirect target must not carry credentials.")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		// Refuses javascript:, data: and other scheme handlers.
		return nil, NewError(CodeInvalidTarget, "The redirect target must use http or https.")
	}
	if scheme == "http" && requireHTTPS {
		return nil, NewError(CodeInvalidTarget, "The redirect target must use https.")
	}
	host := hostnameOf(parsed.Host)
	if host == "" {
		return nil, NewError(CodeInvalidTarget, "The redirect target has no host.")
	}
	if !HostAllowed(host, allowedHosts) {
		return nil, NewError(CodeInvalidTarget, "The redirect target is not a registered hand-off host.")
	}
	return &Target{URI: parsed.String(), Scheme: scheme, Host: host}, nil
}

// HostAllowed reports whether host is one of allowedHosts or a subdomain of one.
func HostAllowed(host string, allowedHosts []string) bool {
	host = normalizeHost(host)
	if host == "" {
		return false
	}
	for _, allowed := range allowedHosts {
		base := normalizeHost(allowed)
		if base == "" {
			continue
		}
		if host == base || strings.HasSuffix(host, "."+base) {
			return true
		}
	}
	return false
}

// normalizeHost strips a leading dot (a cookie scope spelling), a port and a
// trailing dot, and lowercases the result.
func normalizeHost(host string) string {
	trimmed := strings.TrimSpace(host)
	if parsed, err := url.Parse("//" + trimmed); err == nil && parsed.Host != "" {
		host = parsed.Hostname()
	}
	return strings.ToLower(strings.Trim(strings.TrimPrefix(strings.TrimSpace(host), "."), "."))
}

// hostnameOf drops the port from an authority, keeping an IPv6 literal whole.
func hostnameOf(authority string) string {
	if parsed, err := url.Parse("//" + strings.TrimSpace(authority)); err == nil && parsed.Host != "" {
		return strings.ToLower(strings.Trim(parsed.Hostname(), "."))
	}
	return ""
}

// NewCode returns a fresh opaque code string. Codes are never derived from the
// session: they must not be guessable or forgeable from anything the caller has.
func NewCode() (string, error) {
	buf := make([]byte, CodeLength)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// ChallengeFor computes the PKCE challenge for a verifier under method:
// BASE64URL(SHA256(verifier)) for S256, the verifier unchanged for plain.
func ChallengeFor(verifier, method string) string {
	if method == ChallengeMethodPlain {
		return verifier
	}
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// VerifyChallenge reports whether verifier satisfies the stored challenge.
// The comparison is constant-time-ish by construction: the challenge is a hash
// under S256, and plain is the only case that compares secrets directly (which
// is why public clients should ask for S256).
func VerifyChallenge(verifier, challenge, method string) bool {
	if challenge == "" {
		return true
	}
	if strings.TrimSpace(verifier) == "" {
		return false
	}
	expected := ChallengeFor(verifier, normalizeChallengeMethod(method))
	return subtleEqual(expected, challenge)
}

// normalizeChallengeMethod defaults an empty method to S256, the only method a
// client should ask for.
func normalizeChallengeMethod(method string) string {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case "", ChallengeMethodS256:
		return ChallengeMethodS256
	case strings.ToUpper(ChallengeMethodPlain):
		return ChallengeMethodPlain
	default:
		return ChallengeMethodS256
	}
}

// SupportedChallengeMethod reports whether method is one this package implements.
func SupportedChallengeMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case "", ChallengeMethodS256, strings.ToUpper(ChallengeMethodPlain):
		return true
	default:
		return false
	}
}

// subtleEqual compares two strings without an early exit.
func subtleEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// AppendCode returns target with the code appended as a query parameter,
// preserving any query string the target already carries.
func AppendCode(target, code string) string {
	parsed, err := url.Parse(target)
	if err != nil {
		sep := "?"
		if strings.Contains(target, "?") {
			sep = "&"
		}
		return target + sep + url.Values{"code": {code}}.Encode()
	}
	query := parsed.Query()
	query.Set("code", code)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// clampTTL keeps a configured lifetime inside the useful range.
func clampTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return DefaultCodeTTL
	}
	if ttl < MinCodeTTL {
		return MinCodeTTL
	}
	if ttl > MaxCodeTTL {
		return MaxCodeTTL
	}
	return ttl
}

// requireCache returns the cache service or a protocol error when none is
// wired. A hand-off without a shared store cannot be single-use, so it is
// refused rather than degraded.
func requireCache(c cache.CacheService) error {
	if c == nil {
		return NewError(CodeServerError, "Session hand-off requires a cache service.")
	}
	return nil
}

// authorityOf returns the host[:port] of a raw URL, empty when it has none.
// It is the cheapest way to ask "which host is this target on" before the URL
// has been validated.
func authorityOf(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return parsed.Host
}
