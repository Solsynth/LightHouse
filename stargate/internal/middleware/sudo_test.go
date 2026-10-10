package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

type fakeSudoChecker struct {
	elevated bool
	elevErr  error
	hint     string
	hintErr  error
	failures int
}

func (f *fakeSudoChecker) IsSudoElevated(context.Context, string) (bool, error) {
	return f.elevated, f.elevErr
}

func (f *fakeSudoChecker) SudoFactorHint(context.Context, string) (string, error) {
	return f.hint, f.hintErr
}

func (f *fakeSudoChecker) RecordSudoFailure(context.Context, string, string, string, string) {
	f.failures++
}

// newSudoRouter builds a gated route whose upstream middleware injects the
// authenticated user + session into the context (what the Auth middleware
// does), so RequireSudo can be exercised without a token service.
func newSudoRouter(checker SudoChecker) *gin.Engine {
	gin.SetMode(gin.TestMode)
	SetSudoChecker(checker)
	e := gin.New()
	e.GET("/gated",
		func(c *gin.Context) {
			ctx := context.WithValue(c.Request.Context(), ctxKeyCurrentUser, &model.Account{Id: "acct-1"})
			ctx = context.WithValue(ctx, ctxKeyCurrentSession, &model.AuthSession{Id: "sess-1"})
			c.Request = c.Request.WithContext(ctx)
			c.Next()
		},
		RequireSudo(),
		func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) },
	)
	return e
}

func doSudoRequest(e *gin.Engine) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/gated", nil)
	e.ServeHTTP(rec, req)
	return rec
}

func decodeSudoError(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return body
}

func TestRequireSudo(t *testing.T) {
	t.Cleanup(func() { SetSudoChecker(nil) })

	t.Run("allows an elevated session", func(t *testing.T) {
		e := newSudoRouter(&fakeSudoChecker{elevated: true, hint: "password,timed_code"})
		if rec := doSudoRequest(e); rec.Code != http.StatusOK {
			t.Fatalf("got %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("denies a non-elevated session with the factor hint", func(t *testing.T) {
		checker := &fakeSudoChecker{elevated: false, hint: "password,timed_code"}
		e := newSudoRouter(checker)
		rec := doSudoRequest(e)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("got %d, want 403 (body %s)", rec.Code, rec.Body.String())
		}
		body := decodeSudoError(t, rec)
		if body["code"] != "AUTH_SUDO_REQUIRED" {
			t.Fatalf("code = %v, want AUTH_SUDO_REQUIRED", body["code"])
		}
		if body["message"] != "This action requires re-authentication." {
			t.Fatalf("message = %v", body["message"])
		}
		if body["detail"] != "password,timed_code" {
			t.Fatalf("detail = %v, want password,timed_code", body["detail"])
		}
		if checker.failures != 1 {
			t.Fatalf("failure records = %d, want 1", checker.failures)
		}
	})

	t.Run("fails closed with 503 when the elevation store errors", func(t *testing.T) {
		e := newSudoRouter(&fakeSudoChecker{elevErr: errors.New("redis down")})
		rec := doSudoRequest(e)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("got %d, want 503 (body %s)", rec.Code, rec.Body.String())
		}
		if body := decodeSudoError(t, rec); body["code"] != "SERVICE_UNAVAILABLE" {
			t.Fatalf("code = %v, want SERVICE_UNAVAILABLE", body["code"])
		}
	})

	t.Run("fails closed with 503 when no checker is wired", func(t *testing.T) {
		e := newSudoRouter(nil)
		if rec := doSudoRequest(e); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("got %d, want 503 (body %s)", rec.Code, rec.Body.String())
		}
	})
}
