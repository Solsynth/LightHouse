package handoff

import (
	"context"
	"net/http"
	"strings"
	"time"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/cache"
)

// Client is one relying app the issuer will hand a session to.
type Client struct {
	// ID identifies the app on the wire and is what a code is bound to. It
	// must be stable: changing it invalidates nothing stored, but a code
	// issued under one ID is not redeemable under another.
	ID string
	// Secret is the shared secret a confidential client proves itself with.
	// Empty marks a public client (a browser app or one that cannot hold a
	// secret), which must use PKCE instead.
	Secret string
	// AllowedRedirectHosts are the hosts (or parent domains) this client may
	// be handed a session on. The issuer validates the requested target
	// against this list before it issues anything.
	AllowedRedirectHosts []string
}

// IsPublic reports whether the client cannot hold a secret and must therefore
// be required to use PKCE.
func (c Client) IsPublic() bool {
	return strings.TrimSpace(c.Secret) == ""
}

// IssueRequest is one hand-off request from an app that has just finished
// signing a person in on the issuer's console.
type IssueRequest struct {
	// ClientID names the app.
	ClientID string
	// RedirectURI is where the browser will carry the code. It must be an
	// absolute URL on a host the client is registered for.
	RedirectURI string
	// SessionID and AccountID name the issuer-side session being handed off.
	SessionID string
	AccountID string
	// Scopes optionally narrows what the app may take from the session.
	Scopes []string
	// CodeChallenge/CodeChallengeMethod carry PKCE for public clients.
	CodeChallenge       string
	CodeChallengeMethod string
	// RequireHTTPS forces the target to be https. Defaults to true: an https
	// deployment hands a session to an https app or not at all.
	RequireHTTPS *bool
	// RestrictHosts narrows this call's target to the hosts the *caller*
	// accepts, on top of the client's registered hosts. An intersection, so it
	// can only refuse a target, never add one.
	RestrictHosts []string
}

// RedeemRequest is the app's claim on a code.
type RedeemRequest struct {
	// ClientID must equal the ID the code was issued to.
	ClientID string
	// Code is the opaque single-use string.
	Code string
	// ClientSecret proves a confidential client's identity.
	ClientSecret string
	// CodeVerifier is the PKCE verifier for a public client.
	CodeVerifier string
	// RedirectURI, when non-empty, must equal the one the code was issued for.
	RedirectURI string
}

// Issuer hands issuer-side sessions to relying apps. It owns the code store:
// a deployment that also serves the redeem endpoint creates one Issuer and
// wires the same cache into it and into Redeemer.
type Issuer struct {
	// Cache is the shared store. Required: without it a code cannot be
	// single-use, so Issue fails rather than degrading to a replayable token.
	Cache cache.CacheService
	// Clients resolves a client by ID. Return (nil, nil) for an unknown ID.
	Clients func(clientID string) (*Client, error)
	// CodeTTL overrides the redeem window (default DefaultCodeTTL, clamped by
	// MinCodeTTL/MaxCodeTTL).
	CodeTTL time.Duration
	// Now overrides the clock. Nil uses time.Now (UTC).
	Now func() time.Time
	// ClientForHost resolves the client that is registered for a redirect
	// host, so a console that only knows where it is sending the browser does
	// not have to know the app's client id. Optional: when nil, every request
	// must name its ClientID.
	ClientForHost func(host string) (*Client, error)
}

// Issue validates the request, stores a code bound to the client, session and
// target, and returns the code string plus the validated target the caller
// should redirect the browser to.
//
// The returned target is the authority on where the browser may go: the caller
// appends the code with AppendCode rather than trusting its own copy of the
// requested URL.
func (i *Issuer) Issue(ctx context.Context, req IssueRequest) (string, *Target, error) {
	if err := requireCache(i.Cache); err != nil {
		return "", nil, err
	}
	if i.Clients == nil {
		return "", nil, NewError(CodeServerError, "Session hand-off has no client registry.")
	}
	clientID := strings.TrimSpace(req.ClientID)
	client, err := i.resolveClient(clientID, req.RedirectURI)
	if err != nil {
		return "", nil, err
	}
	if client == nil {
		return "", nil, NewError(CodeInvalidClient, "Unknown client.")
	}
	if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.AccountID) == "" {
		return "", nil, NewError(CodeInvalidRequest, "The session being handed off is missing.")
	}
	if !SupportedChallengeMethod(req.CodeChallengeMethod) {
		return "", nil, NewError(CodeUnsupportedChallengeMethod, "Unsupported code_challenge_method.")
	}
	requireHTTPS := true
	if req.RequireHTTPS != nil {
		requireHTTPS = *req.RequireHTTPS
	}
	target, err := ValidateTarget(req.RedirectURI, client.AllowedRedirectHosts, requireHTTPS)
	if err != nil {
		return "", nil, err
	}
	// A caller may refuse a host the client registration would allow; it can
	// never widen it, so this is an intersection rather than a replacement.
	if len(req.RestrictHosts) > 0 && !HostAllowed(target.Host, req.RestrictHosts) {
		return "", nil, NewError(CodeInvalidTarget, "The redirect target is not one this caller accepts.")
	}
	// A public client cannot hold a secret, so PKCE is the only thing binding
	// the code to the app that asked for it.
	if client.IsPublic() && strings.TrimSpace(req.CodeChallenge) == "" {
		return "", nil, NewError(CodeInvalidRequest, "A public client must provide code_challenge.")
	}

	code, err := NewCode()
	if err != nil {
		return "", nil, err
	}
	now := i.now()
	record := &Code{
		ClientID:        client.ID,
		RedirectURI:     target.URI,
		SessionID:       req.SessionID,
		AccountID:       req.AccountID,
		Scopes:          normalizeScopes(req.Scopes),
		CodeChallenge:   strings.TrimSpace(req.CodeChallenge),
		ChallengeMethod: normalizeChallengeMethod(req.CodeChallengeMethod),
		IssuedAt:        now,
		ExpiresAt:       now.Add(clampTTL(i.CodeTTL)),
	}
	if err := i.Cache.Set(ctx, CodeKey(code), record, clampTTL(i.CodeTTL)); err != nil {
		return "", nil, NewError(CodeServerError, "The hand-off code could not be stored.")
	}
	// Read back so a cache that silently drops the value (or a serializer the
	// record is incompatible with) fails here rather than stranding the app on
	// a code that can never be redeemed.
	var stored Code
	found, err := i.Cache.Get(ctx, CodeKey(code), &stored)
	if err != nil {
		_ = i.Cache.Remove(ctx, CodeKey(code))
		return "", nil, err
	}
	if !found || stored.ClientID != client.ID || stored.SessionID != req.SessionID {
		_ = i.Cache.Remove(ctx, CodeKey(code))
		return "", nil, NewError(CodeServerError, "The cache did not retain the session hand-off code.")
	}
	return code, target, nil
}

// resolveClient finds the client a request is for: by id when given, else by
// the host it wants to land on. A console that only knows its own address can
// therefore ask for a hand-off without being told the app's client id, and a
// host no client is registered for resolves to no client at all.
func (i *Issuer) resolveClient(clientID, redirectURI string) (*Client, error) {
	if clientID != "" {
		return i.Clients(clientID)
	}
	if i.ClientForHost == nil {
		return nil, NewError(CodeInvalidRequest, "client_id is required.")
	}
	host := hostnameOf(authorityOf(redirectURI))
	if host == "" {
		return nil, NewError(CodeInvalidTarget, "The redirect target has no host.")
	}
	client, err := i.ClientForHost(host)
	if err != nil {
		return nil, err
	}
	if client == nil {
		// No client claims this host. That is a refusal of the *target*, not a
		// credential problem: the caller named a destination the fleet does not
		// own, and answering invalid_request would misdirect it to fix a client
		// id it never sent.
		return nil, NewError(CodeInvalidTarget, "The redirect target is not a registered hand-off host.")
	}
	return client, nil
}

// now returns the issuer clock in UTC.
func (i *Issuer) now() time.Time {
	if i.Now != nil {
		return i.Now().UTC()
	}
	return time.Now().UTC()
}

// Redeemer turns a code into a Grant. It is what a deployment serving the
// redeem endpoint mounts, and what an issuer-side HTTP handler calls.
type Redeemer struct {
	// Cache must be the same store the Issuer wrote to.
	Cache cache.CacheService
	// Clients resolves a client by ID, exactly as on the Issuer. Required so a
	// public client's PKCE requirement is known at redeem time.
	Clients func(clientID string) (*Client, error)
	// Now overrides the clock. Nil uses time.Now (UTC).
	Now func() time.Time
}

// Redeem consumes the code and returns the grant it stands for. The code is
// removed before the grant is returned: a second call with the same code fails
// with invalid_grant whether or not the first caller did anything with it.
//
// Redemption is serialized by a short lock, so two concurrent calls with one
// code cannot both succeed; the loser sees invalid_grant.
func (r *Redeemer) Redeem(ctx context.Context, req RedeemRequest) (*Grant, error) {
	if err := requireCache(r.Cache); err != nil {
		return nil, err
	}
	if r.Clients == nil {
		return nil, NewError(CodeServerError, "Session hand-off has no client registry.")
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		return nil, NewError(CodeInvalidRequest, "code is required.")
	}

	var stored Code
	found, err := r.Cache.Get(ctx, CodeKey(code), &stored)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, NewError(CodeInvalidGrant, "The hand-off code is invalid or has already been used.")
	}
	if stored.Expired(r.now()) {
		_ = r.Cache.Remove(ctx, CodeKey(code))
		return nil, NewError(CodeInvalidGrant, "The hand-off code has expired.")
	}
	if stored.ClientID != strings.TrimSpace(req.ClientID) {
		// Another app may not claim this code. Left in place: the rightful app
		// may still be racing to redeem it.
		return nil, NewError(CodeInvalidClient, "The hand-off code was issued to a different client.")
	}
	if req.RedirectURI != "" && req.RedirectURI != stored.RedirectURI {
		return nil, NewError(CodeInvalidGrant, "The redirect_uri does not match the one the code was issued for.")
	}
	client, err := r.Clients(stored.ClientID)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, NewError(CodeInvalidClient, "Unknown client.")
	}
	if client.IsPublic() {
		if stored.CodeChallenge == "" {
			return nil, NewError(CodeInvalidGrant, "The hand-off code has no PKCE challenge.")
		}
		if !VerifyChallenge(req.CodeVerifier, stored.CodeChallenge, stored.ChallengeMethod) {
			return nil, NewError(CodeInvalidGrant, "The code_verifier does not match the code_challenge.")
		}
	} else if !clientSecretMatches(client.Secret, req.ClientSecret) {
		return nil, NewError(CodeInvalidClient, "Invalid client credentials.")
	}

	// Consume: whoever wins this lock is the only redeemer. The code is gone
	// before the grant is built, so a failure after this point cannot leave a
	// replayable code behind.
	acquired, grant, err := cache.ExecuteWithLockValue(ctx, r.Cache, codeLockKey(code), codeLockTTL, codeLockWait, codeLockRetry,
		func(lockCtx context.Context) (*Grant, error) {
			var current Code
			still, err := r.Cache.Get(lockCtx, CodeKey(code), &current)
			if err != nil {
				return nil, err
			}
			if !still || current.ClientID != stored.ClientID || current.Expired(r.now()) {
				return nil, NewError(CodeInvalidGrant, "The hand-off code is invalid or has already been used.")
			}
			if err := r.Cache.Remove(lockCtx, CodeKey(code)); err != nil {
				return nil, err
			}
			return &Grant{
				ClientID:    current.ClientID,
				SessionID:   current.SessionID,
				AccountID:   current.AccountID,
				Scopes:      current.Scopes,
				RedirectURI: current.RedirectURI,
				IssuedAt:    r.now(),
			}, nil
		})
	if err != nil {
		return nil, err
	}
	if !acquired || grant == nil {
		return nil, NewError(CodeInvalidGrant, "The hand-off code is invalid or has already been used.")
	}
	return grant, nil
}

// now returns the redeemer clock in UTC.
func (r *Redeemer) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// codeLockKey is the lock resource guarding one code's redemption. It is a
// lock key, not a data key, so it deliberately lives outside CodeKey's prefix
// hierarchy to keep "auth:handoff:code:*" meaning "codes" alone.
func codeLockKey(code string) string {
	return "auth:handoff:redeem:" + code
}

// clientSecretMatches compares a presented secret to the registered one.
func clientSecretMatches(expected, presented string) bool {
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return false
	}
	return subtleEqual(expected, strings.TrimSpace(presented))
}

// normalizeScopes trims, dedupes and drops empty scope names.
func normalizeScopes(scopes []string) []string {
	if len(scopes) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(scopes))
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if _, dup := seen[scope]; dup {
			continue
		}
		seen[scope] = struct{}{}
		out = append(out, scope)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// HTTPStatusForError returns the status an endpoint should answer with for err,
// defaulting to 500 for anything that is not a protocol error.
func HTTPStatusForError(err error) int {
	var protocol *Error
	if asError(err, &protocol) {
		return protocol.Status
	}
	return http.StatusInternalServerError
}

// ErrorCodeFor returns the OAuth-style code carried by err, or server_error.
func ErrorCodeFor(err error) string {
	var protocol *Error
	if asError(err, &protocol) {
		return protocol.Code
	}
	return CodeServerError
}
