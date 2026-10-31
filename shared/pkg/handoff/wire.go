package handoff

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
)

// The wire protocol between an issuer and a relying app that does not share
// its cache. Both sides use the types below so the two cannot drift: the gin
// endpoints and the HTTP client are written against these structs alone.
//
// Two endpoints make up the protocol.
//
//	POST {issuer}/api/auth/handoff/session   (authenticated on the issuer)
//	    {client_id, redirect_uri, code_challenge?, code_challenge_method?}
//	  → {code, redirect_uri, expires_in}
//
//	POST {issuer}/api/auth/handoff/redeem    (anonymous, server-to-server)
//	    {client_id, code, client_secret?, code_verifier?}
//	  → {token, refresh_token, expires_in, refresh_expires_in}
//
// Failures return {error, error_description} with an OAuth-style error code and
// an HTTP status from StatusForCode.

// IssueWireRequest is the body of an issue call. The issuer takes the session
// to hand off from the caller's own authentication, never from this body.
type IssueWireRequest struct {
	ClientID            string `json:"client_id"`
	RedirectURI         string `json:"redirect_uri"`
	CodeChallenge       string `json:"code_challenge,omitempty"`
	CodeChallengeMethod string `json:"code_challenge_method,omitempty"`
	// AllowedHosts optionally narrows the target policy for this call to the
	// hosts the *caller* accepts. The client's registered hosts always apply;
	// this is an additional restriction, so it can only refuse, never grant.
	AllowedHosts []string `json:"allowed_hosts,omitempty"`
}

// IssueWireResponse is the answer to a successful issue call. RedirectURI is
// the validated destination the browser should be sent to; the caller appends
// Code to it with AppendCode.
type IssueWireResponse struct {
	Code        string `json:"code"`
	RedirectURI string `json:"redirect_uri"`
	ExpiresIn   int64  `json:"expires_in"`
}

// RedeemWireRequest is the body of a redeem call.
type RedeemWireRequest struct {
	ClientID     string `json:"client_id"`
	Code         string `json:"code"`
	ClientSecret string `json:"client_secret,omitempty"`
	CodeVerifier string `json:"code_verifier,omitempty"`
	RedirectURI  string `json:"redirect_uri,omitempty"`
}

// RenewWireRequest is the body of a renew call: the relying app's server
// exchanging a refresh token it holds for a fresh pair.
type RenewWireRequest struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
	RefreshToken string `json:"refresh_token"`
}

// RedeemWireResponse is what the issuer's redeem endpoint returns: the token
// pair the relying app's server stores in its own session cookies, plus the
// profile it should show. It mirrors the fleet's TokenExchangeResponse so both
// C# and Go consumers can reuse their existing parsing.
type RedeemWireResponse struct {
	Token            string `json:"token"`
	RefreshToken     string `json:"refresh_token,omitempty"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshExpiresIn int64  `json:"refresh_expires_in,omitempty"`
}

// ErrorWireResponse is the failure body of both endpoints.
type ErrorWireResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

// TokenClient is the relying app's side of the protocol: it redeems a code
// against the issuer's HTTP endpoint and hands back the token pair.
//
// Use this when the app does not share the issuer's cache (a separate
// deployment, a different Redis). When they do share one, call Redeemer
// directly and skip the extra hop.
type TokenClient struct {
	// BaseURL is the issuer's public base, e.g. "https://id.solian.app". The
	// endpoints above are appended to it.
	BaseURL string
	// HTTPClient performs the call. Nil uses http.DefaultClient with a 10s
	// timeout: this call sits in front of a redirect handler, so it may not
	// hang.
	HTTPClient *http.Client
	// ClientID and ClientSecret identify the app. A public client leaves the
	// secret empty and must pass a PKCE verifier per call.
	ClientID     string
	ClientSecret string
}

// Redeem exchanges a code for a token pair.
func (t *TokenClient) Redeem(ctx context.Context, code string, opts ...RedeemOption) (*RedeemWireResponse, error) {
	req := RedeemWireRequest{
		ClientID:     strings.TrimSpace(t.ClientID),
		Code:         strings.TrimSpace(code),
		ClientSecret: t.ClientSecret,
	}
	for _, opt := range opts {
		opt(&req)
	}
	return t.post(ctx, RedeemEndpointPath, req)
}

// Renew exchanges a refresh token for a new pair, so a hand-off session outlives
// the access token's short lifetime. The relying app calls this in place of
// reading an issuer cookie it does not have.
func (t *TokenClient) Renew(ctx context.Context, refreshToken string) (*RedeemWireResponse, error) {
	return t.post(ctx, RenewEndpointPath, RenewWireRequest{
		ClientID:     strings.TrimSpace(t.ClientID),
		ClientSecret: t.ClientSecret,
		RefreshToken: strings.TrimSpace(refreshToken),
	})
}

// post performs one authenticated-by-body call against an issuer endpoint and
// decodes the token response. Both legs share it so their error handling cannot
// drift.
func (t *TokenClient) post(ctx context.Context, path string, body any) (*RedeemWireResponse, error) {
	if strings.TrimSpace(t.BaseURL) == "" {
		return nil, NewError(CodeServerError, "The hand-off issuer base URL is not configured.")
	}
	if strings.TrimSpace(t.ClientID) == "" {
		return nil, NewError(CodeInvalidRequest, "The hand-off client ID is not configured.")
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(t.BaseURL, "/") + path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := t.client().Do(httpReq)
	if err != nil {
		return nil, NewError(CodeServerError, "The hand-off issuer could not be reached.")
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure ErrorWireResponse
		if json.Unmarshal(payload, &failure) == nil && failure.Error != "" {
			return nil, &Error{
				Code:    failure.Error,
				Message: firstNonEmpty(failure.ErrorDescription, failure.Error),
				Status:  resp.StatusCode,
			}
		}
		return nil, &Error{
			Code:    CodeServerError,
			Message: fmt.Sprintf("The hand-off issuer answered %d.", resp.StatusCode),
			Status:  resp.StatusCode,
		}
	}
	var out RedeemWireResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, NewError(CodeServerError, "The hand-off issuer returned an unreadable response.")
	}
	if strings.TrimSpace(out.Token) == "" {
		return nil, NewError(CodeServerError, "The hand-off issuer returned no token.")
	}
	return &out, nil
}

// RedeemOption adjusts one redeem call.
type RedeemOption func(*RedeemWireRequest)

// WithCodeVerifier supplies the PKCE verifier for a public client.
func WithCodeVerifier(verifier string) RedeemOption {
	return func(req *RedeemWireRequest) { req.CodeVerifier = verifier }
}

// WithRedirectURI echoes the redirect URI the code was issued for. The issuer
// refuses a mismatch, so a caller that knows the target should send it.
func WithRedirectURI(uri string) RedeemOption {
	return func(req *RedeemWireRequest) { req.RedirectURI = uri }
}

func (t *TokenClient) client() *http.Client {
	if t.HTTPClient != nil {
		return t.HTTPClient
	}
	return defaultHTTPClient
}

var defaultHTTPClient = &http.Client{Timeout: 10 * time.Second}

// Canonical endpoint paths. An issuer that mounts its own routes elsewhere can
// serve the protocol at any path; these are the ones MountIssue and MountRedeem
// use and the ones TokenClient calls.
const (
	IssueEndpointPath = "/api/auth/handoff/session"
	// RedeemEndpointPath trades the single-use code for a token pair.
	RedeemEndpointPath = "/api/auth/handoff/redeem"
	// RenewEndpointPath exchanges a refresh token for a new pair. It carries a
	// refresh token, never a code, so an issuer may serve it from a different
	// surface than redeem.
	RenewEndpointPath = "/api/auth/handoff/renew"
)

// SessionLookup resolves the issuer-side session a hand-off is asked for. The
// issuer calls it with the request context of an *authenticated* caller; a
// deployment returns ok=false to refuse (no session, wrong session type).
type SessionLookup func(c *gin.Context) (session SessionRef, ok bool)

// SessionRef names the session being handed off.
type SessionRef struct {
	SessionID string
	AccountID string
	Scopes    []string
}

// IssueEndpointDeps wires MountIssue to a deployment.
type IssueEndpointDeps struct {
	Issuer *Issuer
	// Session resolves the session being handed off. Required: the endpoint
	// never trusts a session ID from the request body.
	Session SessionLookup
	// Middleware runs before the handler, so the issuer can require its own
	// authentication (or sudo). A route that is not authenticated must reject
	// in Middleware or by returning ok=false from Session.
	Middleware []gin.HandlerFunc
	// RequireHTTPS forces the target to be https. Nil defaults to true: an
	// https deployment hands a session to an https app or not at all.
	RequireHTTPS *bool
}

// RedeemEndpointDeps wires MountRedeem to a deployment.
type RedeemEndpointDeps struct {
	Redeemer *Redeemer
	// Mint turns a redeemed grant into the app's token pair. Required: the
	// issuer owns minting, the code only names a session.
	Mint func(ctx context.Context, grant *Grant) (*RedeemWireResponse, error)
}

// MountIssue registers the issue endpoint on group. The caller must have
// authenticated the request before this handler runs (a route-level middleware
// or a check inside Session), because the session it hands off is the caller's.
func MountIssue(group *gin.RouterGroup, deps IssueEndpointDeps) {
	handlers := append(append([]gin.HandlerFunc{}, deps.Middleware...), func(c *gin.Context) {
		var req IssueWireRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			writeHandoffError(c, NewError(CodeInvalidRequest, "Invalid request body."))
			return
		}
		session, ok := SessionRef{}, false
		if deps.Session != nil {
			session, ok = deps.Session(c)
		}
		if !ok {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		code, target, err := deps.Issuer.Issue(c.Request.Context(), IssueRequest{
			ClientID:            req.ClientID,
			RedirectURI:         req.RedirectURI,
			SessionID:           session.SessionID,
			AccountID:           session.AccountID,
			Scopes:              session.Scopes,
			CodeChallenge:       req.CodeChallenge,
			CodeChallengeMethod: req.CodeChallengeMethod,
			RequireHTTPS:        deps.RequireHTTPS,
			RestrictHosts:       req.AllowedHosts,
		})
		if err != nil {
			writeHandoffError(c, err)
			return
		}
		c.JSON(http.StatusOK, IssueWireResponse{
			Code:        code,
			RedirectURI: target.URI,
			ExpiresIn:   int64(clampTTL(deps.Issuer.CodeTTL).Seconds()),
		})
	})
	group.POST(IssuePathSuffix, handlers...)
}

// MountRedeem registers the redeem endpoint on group. It is anonymous by
// design: the code is the credential, and the caller is another server.
func MountRedeem(group *gin.RouterGroup, deps RedeemEndpointDeps) {
	group.POST(RedeemPathSuffix, func(c *gin.Context) {
		var req RedeemWireRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			writeHandoffError(c, NewError(CodeInvalidRequest, "Invalid request body."))
			return
		}
		grant, err := deps.Redeemer.Redeem(c.Request.Context(), RedeemRequest{
			ClientID:     req.ClientID,
			Code:         req.Code,
			ClientSecret: req.ClientSecret,
			CodeVerifier: req.CodeVerifier,
			RedirectURI:  req.RedirectURI,
		})
		if err != nil {
			writeHandoffError(c, err)
			return
		}
		if deps.Mint == nil {
			writeHandoffError(c, NewError(CodeServerError, "The issuer cannot mint a session for this app."))
			return
		}
		response, err := deps.Mint(c.Request.Context(), grant)
		if err != nil {
			writeHandoffError(c, err)
			return
		}
		c.JSON(http.StatusOK, response)
	})
}

// RenewEndpointDeps wires MountRenew to a deployment.
type RenewEndpointDeps struct {
	// Clients resolves a client by ID, as on the Issuer and Redeemer.
	Clients func(clientID string) (*Client, error)
	// Renew trades a valid refresh token for a fresh pair. Required: the
	// issuer re-issues on its own session store, so this is its own call.
	Renew func(ctx context.Context, grant RefreshGrant) (*RedeemWireResponse, error)
}

// RefreshGrant names the session a refresh token was accepted for. The client
// fields let a deployment check the token belongs to the asking app.
type RefreshGrant struct {
	ClientID     string
	RefreshToken string
}

// MountRenew registers the renew endpoint on group. It is anonymous by design,
// like redeem: the refresh token is the credential and the caller is another
// server. The client secret, when the client has one, is what stops a leaked
// refresh token from being traded in by anything that finds it.
func MountRenew(group *gin.RouterGroup, deps RenewEndpointDeps) {
	group.POST(RenewPathSuffix, func(c *gin.Context) {
		var req RenewWireRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			writeHandoffError(c, NewError(CodeInvalidRequest, "Invalid request body."))
			return
		}
		if strings.TrimSpace(req.RefreshToken) == "" {
			writeHandoffError(c, NewError(CodeInvalidRequest, "refresh_token is required."))
			return
		}
		if deps.Clients == nil || deps.Renew == nil {
			writeHandoffError(c, NewError(CodeServerError, "The hand-off renew endpoint is not configured."))
			return
		}
		client, err := deps.Clients(strings.TrimSpace(req.ClientID))
		if err != nil {
			writeHandoffError(c, err)
			return
		}
		if client == nil {
			writeHandoffError(c, NewError(CodeInvalidClient, "Unknown client."))
			return
		}
		if !client.IsPublic() && !clientSecretMatches(client.Secret, req.ClientSecret) {
			writeHandoffError(c, NewError(CodeInvalidClient, "Invalid client credentials."))
			return
		}
		response, err := deps.Renew(c.Request.Context(), RefreshGrant{
			ClientID:     client.ID,
			RefreshToken: strings.TrimSpace(req.RefreshToken),
		})
		if err != nil {
			writeHandoffError(c, err)
			return
		}
		c.JSON(http.StatusOK, response)
	})
}

// RenewPathSuffix is the route suffix MountRenew registers.
const RenewPathSuffix = "/handoff/renew"

// IssuePathSuffix and RedeemPathSuffix are the route suffixes a group mounts,
// so a deployment can nest them under its own prefix.
const (
	IssuePathSuffix  = "/handoff/session"
	RedeemPathSuffix = "/handoff/redeem"
)

// writeHandoffError renders a protocol error in the wire shape and status.
func writeHandoffError(c *gin.Context, err error) {
	status := HTTPStatusForError(err)
	code := ErrorCodeFor(err)
	description := err.Error()
	var protocol *Error
	if asError(err, &protocol) {
		description = protocol.Message
	}
	c.JSON(status, ErrorWireResponse{Error: code, ErrorDescription: description})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
