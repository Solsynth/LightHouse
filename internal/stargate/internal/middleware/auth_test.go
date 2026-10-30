package middleware

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/stargate/internal/auth"
)

// newAuthMiddleware builds the Auth middleware with a token service that can
// reject tokens without any backing store: a malformed token short-circuits
// in validateToken/validateOidcToken before the JWT key, Redis or store are
// touched, so nil deps are safe here.
func newAuthMiddleware() gin.HandlerFunc {
	svc := auth.NewTokenAuthService(nil, nil, &auth.JWTService{}, nil, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	return Auth(AuthDeps{
		Token: svc,
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

// TestAuthInvalidTokenDoesNotRejectAnonymousRoute pins the DysonTokenAuthHandler
// contract: an invalid token leaves the request unauthenticated but must not
// 401 anonymous endpoints (login / challenge start must work with a stale
// token attached). Only RequireAuth-protected routes reject.
func TestAuthInvalidTokenDoesNotRejectAnonymousRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(newAuthMiddleware())
	e.GET("/public", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/public", nil)
	req.Header.Set("Authorization", "Bearer not-a-valid-token")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("anonymous route with invalid token: got %d, want 200 (body %s)", w.Code, w.Body.String())
	}
}

// TestRequireAuthRejectsAnonymous pins the canonical 401 ApiError payload
// (mirrors ApiError.Unauthorized() with the C# default message).
func TestRequireAuthRejectsAnonymous(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.GET("/protected", RequireAuth(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/protected", nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("protected route: got %d, want 401", w.Code)
	}
	want := `{"code":"UNAUTHORIZED","message":"Authentication is required.","status":401}`
	if got := w.Body.String(); got != want {
		t.Fatalf("401 body: got %s, want %s", got, want)
	}
}
