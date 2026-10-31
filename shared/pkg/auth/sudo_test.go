package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/cache"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

type mockSudoValidator struct {
	valid       bool
	factorTypes []string
	err         error
	gotSession  string
}

func (m *mockSudoValidator) ValidateSudo(_ context.Context, sessionID string) (bool, []string, error) {
	m.gotSession = sessionID
	return m.valid, m.factorTypes, m.err
}

// sudoTestRouter authenticates with mockAuthenticator and applies the given sudo middleware.
func sudoTestRouter(session *gen.DyAuthSession, validator SudoValidator) *gin.Engine {
	gin.SetMode(gin.TestMode)

	c := cache.NewMemoryCacheService(100)
	defer c.Close()

	r := gin.New()
	r.Use(AuthMiddleware(&mockAuthenticator{result: &AuthResult{Session: session}}, c, &mockProfileService{}))
	r.Use(RequireSudo(validator))
	r.POST("/gated", func(ctx *gin.Context) { ctx.Status(http.StatusOK) })
	return r
}

func doGatedRequest(r *gin.Engine) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/gated", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestRequireSudoAllowsElevatedSession(t *testing.T) {
	validator := &mockSudoValidator{valid: true}
	r := sudoTestRouter(&gen.DyAuthSession{Id: "sess-1", AccountId: "acc-1"}, validator)

	w := doGatedRequest(r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if validator.gotSession != "sess-1" {
		t.Fatalf("expected session %q, got %q", "sess-1", validator.gotSession)
	}
}

func TestRequireSudoRejectsUnelevatedSession(t *testing.T) {
	validator := &mockSudoValidator{valid: false, factorTypes: []string{"password", "timed_code"}}
	r := sudoTestRouter(&gen.DyAuthSession{Id: "sess-1"}, validator)

	w := doGatedRequest(r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}

	var body errs.ApiError
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Code != "AUTH_SUDO_REQUIRED" {
		t.Fatalf("expected code AUTH_SUDO_REQUIRED, got %q", body.Code)
	}
	if body.Message != "This action requires re-authentication." {
		t.Fatalf("unexpected message %q", body.Message)
	}
	if body.Detail == nil || *body.Detail != "password,timed_code" {
		t.Fatalf("unexpected detail %v", body.Detail)
	}
}

func TestRequireSudoOmitsEmptyFactorDetail(t *testing.T) {
	validator := &mockSudoValidator{valid: false}
	r := sudoTestRouter(&gen.DyAuthSession{Id: "sess-1"}, validator)

	w := doGatedRequest(r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}

	var body errs.ApiError
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Detail != nil {
		t.Fatalf("expected no detail, got %v", *body.Detail)
	}
}

func TestRequireSudoFailsClosedOnError(t *testing.T) {
	validator := &mockSudoValidator{err: errors.New("auth service is down")}
	r := sudoTestRouter(&gen.DyAuthSession{Id: "sess-1"}, validator)

	w := doGatedRequest(r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}

	var body errs.ApiError
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Code != "SERVICE_UNAVAILABLE" {
		t.Fatalf("expected code SERVICE_UNAVAILABLE, got %q", body.Code)
	}
}

func TestRequireSudoFailsClosedWithoutSession(t *testing.T) {
	for _, tc := range []struct {
		name      string
		validator SudoValidator
		session   *gen.DyAuthSession
	}{
		{name: "nil validator", validator: nil, session: &gen.DyAuthSession{Id: "sess-1"}},
		{name: "empty session id", validator: &mockSudoValidator{valid: true}, session: &gen.DyAuthSession{}},
		{name: "no session", validator: &mockSudoValidator{valid: true}, session: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := doGatedRequest(sudoTestRouter(tc.session, tc.validator))
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("expected 503, got %d", w.Code)
			}
		})
	}
}

func TestRequireSudoUnauthorizedWithoutAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(RequireSudo(&mockSudoValidator{valid: true}))
	r.POST("/gated", func(ctx *gin.Context) { ctx.Status(http.StatusOK) })

	w := doGatedRequest(r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestValidateSudoRejectsEmptySessionID(t *testing.T) {
	a := &GrpcTokenAuthenticator{}

	if _, _, err := a.ValidateSudo(context.Background(), "  "); err == nil {
		t.Fatal("expected an error for an empty session id")
	}
}
