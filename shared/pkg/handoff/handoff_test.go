package handoff

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	"github.com/redis/go-redis/v9"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/cache"
)

const testClientID = "app.solian.express"

func testRegistry(t *testing.T) func(string) (*Client, error) {
	t.Helper()
	return func(id string) (*Client, error) {
		if id == testClientID {
			return &Client{ID: id, Secret: "s3cr3t", AllowedRedirectHosts: []string{"dx.solsynth.dev"}}, nil
		}
		return nil, nil
	}
}

// issueFixture wires an issuer and redeemer over one store and issues a code
// for the Express host, which is the shape every test starts from.
func issueFixture(t *testing.T, store cache.CacheService) (*Issuer, *Redeemer, string) {
	t.Helper()
	registry := testRegistry(t)
	issuer := &Issuer{Cache: store, Clients: registry}
	redeemer := &Redeemer{Cache: store, Clients: registry}
	code, target, err := issuer.Issue(context.Background(), IssueRequest{
		ClientID:    testClientID,
		RedirectURI: "https://dx.solsynth.dev/releases?page=2",
		SessionID:   "session-1",
		AccountID:   "account-1",
		Scopes:      []string{"openid", "profile"},
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if target.Host != "dx.solsynth.dev" {
		t.Fatalf("target host = %q", target.Host)
	}
	return issuer, redeemer, code
}

// TestRedeemIsSingleUse is the property the protocol rests on: one code, one
// grant. It runs on both store implementations because the Redis path is what
// deployments use.
func TestRedeemIsSingleUse(t *testing.T) {
	assertSingleUse(t, cache.NewMemoryCacheService(64))
	assertSingleUse(t, redisCache(t))
}

func assertSingleUse(t *testing.T, store cache.CacheService) {
	t.Helper()
	ctx := context.Background()
	_, redeemer, code := issueFixture(t, store)

	grant, err := redeemer.Redeem(ctx, RedeemRequest{ClientID: testClientID, Code: code, ClientSecret: "s3cr3t"})
	if err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if grant.SessionID != "session-1" || len(grant.Scopes) != 2 {
		t.Fatalf("grant = %+v", grant)
	}
	if _, err := redeemer.Redeem(ctx, RedeemRequest{ClientID: testClientID, Code: code, ClientSecret: "s3cr3t"}); !IsCode(err, CodeInvalidGrant) {
		t.Fatalf("second redeem error = %v, want invalid_grant", err)
	}
}

// TestRedeemConcurrentSingleWinner covers the race the lock exists for: a
// double-clicked callback must not mint two sessions.
func TestRedeemConcurrentSingleWinner(t *testing.T) {
	ctx := context.Background()
	_, redeemer, code := issueFixture(t, cache.NewMemoryCacheService(64))

	var winners int
	var mu sync.Mutex
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := redeemer.Redeem(ctx, RedeemRequest{ClientID: testClientID, Code: code, ClientSecret: "s3cr3t"}); err == nil {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1", winners)
	}
}

// TestIssueEnforcesTargetPolicy: the issuer, not the caller, decides which
// hosts may receive a code, and the session target must be https.
func TestIssueEnforcesTargetPolicy(t *testing.T) {
	issuer := &Issuer{Cache: cache.NewMemoryCacheService(64), Clients: testRegistry(t)}
	for _, redirect := range []string{
		"https://evil.example/",
		"https://dx.solsynth.dev.evil.example/",
		"http://dx.solsynth.dev/",
		"javascript:alert(1)",
		"//dx.solsynth.dev/",
		"/local/path",
		"https://user@dx.solsynth.dev/",
	} {
		if _, _, err := issuer.Issue(context.Background(), IssueRequest{
			ClientID: testClientID, RedirectURI: redirect, SessionID: "s", AccountID: "a",
		}); err == nil {
			t.Fatalf("issue accepted %q", redirect)
		}
	}

	// A registered parent domain covers its subdomains, and the code lands on
	// the validated target with the query preserved.
	registry := func(id string) (*Client, error) {
		return &Client{ID: id, Secret: "s3cr3t", AllowedRedirectHosts: []string{"solsynth.dev"}}, nil
	}
	relaxed := &Issuer{Cache: cache.NewMemoryCacheService(64), Clients: registry}
	code, target, err := relaxed.Issue(context.Background(), IssueRequest{
		ClientID: "cli", RedirectURI: "https://dx.solsynth.dev/releases?page=2", SessionID: "s", AccountID: "a",
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	appended := AppendCode(target.URI, code)
	if !strings.Contains(appended, "code="+code) || !strings.Contains(appended, "page=2") {
		t.Fatalf("AppendCode = %q", appended)
	}
}

// TestRedeemBindsCodeToClientAndTarget: neither another app, a wrong secret,
// nor a replayed redirect_uri may consume a code.
func TestRedeemBindsCodeToClientAndTarget(t *testing.T) {
	ctx := context.Background()
	store := cache.NewMemoryCacheService(64)
	_, redeemer, code := issueFixture(t, store)

	if _, err := redeemer.Redeem(ctx, RedeemRequest{ClientID: "other.app", Code: code}); !IsCode(err, CodeInvalidClient) {
		t.Fatalf("wrong client = %v, want invalid_client", err)
	}
	if _, err := redeemer.Redeem(ctx, RedeemRequest{ClientID: testClientID, Code: code, ClientSecret: "wrong"}); !IsCode(err, CodeInvalidClient) {
		t.Fatalf("wrong secret = %v, want invalid_client", err)
	}
	if _, err := redeemer.Redeem(ctx, RedeemRequest{
		ClientID: testClientID, Code: code, ClientSecret: "s3cr3t", RedirectURI: "https://dx.solsynth.dev/other",
	}); !IsCode(err, CodeInvalidGrant) {
		t.Fatalf("mismatched redirect_uri = %v, want invalid_grant", err)
	}
	// The failed attempts must not have consumed it.
	if _, err := redeemer.Redeem(ctx, RedeemRequest{
		ClientID: testClientID, Code: code, ClientSecret: "s3cr3t", RedirectURI: "https://dx.solsynth.dev/releases?page=2",
	}); err != nil {
		t.Fatalf("rightful redeem after refusals: %v", err)
	}
}

// TestPublicClientRequiresPKCE: a client that cannot hold a secret must bind
// the code to itself with PKCE, or it is never issued one.
func TestPublicClientRequiresPKCE(t *testing.T) {
	ctx := context.Background()
	store := cache.NewMemoryCacheService(64)
	registry := func(string) (*Client, error) {
		return &Client{ID: "public.app", AllowedRedirectHosts: []string{"dx.solsynth.dev"}}, nil
	}
	issuer := &Issuer{Cache: store, Clients: registry}
	redeemer := &Redeemer{Cache: store, Clients: registry}

	if _, _, err := issuer.Issue(ctx, IssueRequest{
		ClientID: "public.app", RedirectURI: "https://dx.solsynth.dev/", SessionID: "s", AccountID: "a",
	}); !IsCode(err, CodeInvalidRequest) {
		t.Fatalf("public client without PKCE = %v, want invalid_request", err)
	}

	verifier := "abcdefghijklmnopqrstuvwxyz0123456789-._~"
	code, _, err := issuer.Issue(ctx, IssueRequest{
		ClientID: "public.app", RedirectURI: "https://dx.solsynth.dev/", SessionID: "s", AccountID: "a",
		CodeChallenge: ChallengeFor(verifier, ChallengeMethodS256), CodeChallengeMethod: ChallengeMethodS256,
	})
	if err != nil {
		t.Fatalf("issue with PKCE: %v", err)
	}
	if _, err := redeemer.Redeem(ctx, RedeemRequest{ClientID: "public.app", Code: code, CodeVerifier: "nope"}); !IsCode(err, CodeInvalidGrant) {
		t.Fatalf("wrong verifier = %v, want invalid_grant", err)
	}
	if _, err := redeemer.Redeem(ctx, RedeemRequest{ClientID: "public.app", Code: code, CodeVerifier: verifier}); err != nil {
		t.Fatalf("correct verifier: %v", err)
	}
}

// TestExpiredCodeIsRefusedAndDropped uses an injected clock so the test does
// not wait out the window.
func TestExpiredCodeIsRefusedAndDropped(t *testing.T) {
	ctx := context.Background()
	store := cache.NewMemoryCacheService(64)
	registry := testRegistry(t)
	base := time.Now().UTC()
	now := base
	issuer := &Issuer{Cache: store, Clients: registry, Now: func() time.Time { return now }}
	redeemer := &Redeemer{Cache: store, Clients: registry, Now: func() time.Time { return now }}

	code, _, err := issuer.Issue(ctx, IssueRequest{
		ClientID: testClientID, RedirectURI: "https://dx.solsynth.dev/", SessionID: "s", AccountID: "a",
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	now = base.Add(DefaultCodeTTL + time.Second)
	if _, err := redeemer.Redeem(ctx, RedeemRequest{ClientID: testClientID, Code: code, ClientSecret: "s3cr3t"}); !IsCode(err, CodeInvalidGrant) {
		t.Fatalf("expired = %v, want invalid_grant", err)
	}
	if found, _ := store.Get(ctx, CodeKey(code), &Code{}); found {
		t.Fatal("expired code left in the store")
	}
}

// TestMountEndpointsRoundTrip drives the real handlers over HTTP: the issuing
// route requires an authenticated caller, and the redeem route answers the
// relying app's TokenClient.
func TestMountEndpointsRoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := cache.NewMemoryCacheService(128)
	registry := testRegistry(t)
	issuer := &Issuer{Cache: store, Clients: registry}
	redeemer := &Redeemer{Cache: store, Clients: registry}

	router := gin.New()
	api := router.Group("/api/auth")
	MountIssue(api, IssueEndpointDeps{
		Issuer: issuer,
		Session: func(c *gin.Context) (SessionRef, bool) {
			if c.GetHeader("X-Test-Auth") != "yes" {
				return SessionRef{}, false
			}
			return SessionRef{SessionID: "session-7", AccountID: "account-7", Scopes: []string{"openid"}}, true
		},
	})
	MountRedeem(api, RedeemEndpointDeps{
		Redeemer: redeemer,
		Mint: func(_ context.Context, grant *Grant) (*RedeemWireResponse, error) {
			return &RedeemWireResponse{Token: "access-" + grant.SessionID, RefreshToken: "refresh", ExpiresIn: 300}, nil
		},
	})

	body := `{"client_id":"` + testClientID + `","redirect_uri":"https://dx.solsynth.dev/releases"}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, IssueEndpointPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous issue = %d, want 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, IssueEndpointPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Auth", "yes")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("issue = %d body=%s", rec.Code, rec.Body)
	}
	var issued IssueWireResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	if issued.Code == "" || issued.RedirectURI != "https://dx.solsynth.dev/releases" {
		t.Fatalf("issue response = %+v", issued)
	}

	// The relying app redeems over HTTP, exactly as TokenClient does. BaseURL
	// is the issuer's public root; the endpoint path is appended to it.
	server := httptest.NewServer(router)
	defer server.Close()
	client := &TokenClient{BaseURL: server.URL, ClientID: testClientID, ClientSecret: "s3cr3t"}
	tokens, err := client.Redeem(context.Background(), issued.Code)
	if err != nil {
		t.Fatalf("client redeem: %v", err)
	}
	if tokens.Token != "access-session-7" {
		t.Fatalf("token = %q", tokens.Token)
	}
	if _, err := client.Redeem(context.Background(), issued.Code); !IsCode(err, CodeInvalidGrant) {
		t.Fatalf("replay = %v, want invalid_grant", err)
	}
}

// TestIssueRefusesWithoutCache: no shared store means a code cannot be made
// single-use, so the deployment must refuse instead of degrading.
func TestIssueRefusesWithoutCache(t *testing.T) {
	if _, _, err := (&Issuer{Clients: testRegistry(t)}).Issue(context.Background(), IssueRequest{
		ClientID: testClientID, RedirectURI: "https://dx.solsynth.dev/", SessionID: "s", AccountID: "a",
	}); !IsCode(err, CodeServerError) {
		t.Fatalf("issue without cache = %v, want server_error", err)
	}
	if _, err := (&Redeemer{Clients: testRegistry(t)}).Redeem(context.Background(), RedeemRequest{ClientID: testClientID, Code: "x"}); !IsCode(err, CodeServerError) {
		t.Fatalf("redeem without cache = %v, want server_error", err)
	}
}

// redisCache exercises the store deployments actually share, and skips when no
// Redis is reachable so the package's tests run in a bare checkout.
func redisCache(t *testing.T) cache.CacheService {
	t.Helper()
	addr := strings.TrimSpace(os.Getenv("HANDOFF_TEST_REDIS"))
	if addr == "" {
		addr = "127.0.0.1:56379"
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Skipf("redis at %s unreachable: %v", addr, err)
	}
	t.Cleanup(func() { _ = client.Close() })
	t.Cleanup(func() {
		keys, err := client.Keys(context.Background(), "dyson:auth:handoff:*").Result()
		if err == nil && len(keys) > 0 {
			_ = client.Del(context.Background(), keys...).Err()
		}
	})
	return cache.NewRedisCacheService(client)
}

// TestIssueResolvesClientByHost covers the console path: it knows only the
// address it is sending the browser to, not the app's client id.
func TestIssueResolvesClientByHost(t *testing.T) {
	store := cache.NewMemoryCacheService(64)
	resolve := func(host string) (*Client, error) {
		if host == "dx.solsynth.dev" {
			return &Client{ID: "app.solian.express", Secret: "s3cr3t", AllowedRedirectHosts: []string{"solsynth.dev"}}, nil
		}
		return nil, nil
	}
	issuer := &Issuer{
		Cache: store, Clients: func(string) (*Client, error) { return nil, nil }, ClientForHost: resolve,
	}

	code, target, err := issuer.Issue(context.Background(), IssueRequest{
		RedirectURI: "https://dx.solsynth.dev/releases", SessionID: "s", AccountID: "a",
	})
	if err != nil {
		t.Fatalf("issue by host: %v", err)
	}
	if code == "" || target.Host != "dx.solsynth.dev" {
		t.Fatalf("issue by host = %q / %+v", code, target)
	}

	// A host no client is registered for resolves to nothing, so the request is
	// refused rather than attributed to a client at random.
	if _, _, err := issuer.Issue(context.Background(), IssueRequest{
		RedirectURI: "https://evil.example/", SessionID: "s", AccountID: "a",
	}); !IsCode(err, CodeInvalidTarget) {
		t.Fatalf("unregistered host: err = %v, want invalid_target", err)
	}
}
