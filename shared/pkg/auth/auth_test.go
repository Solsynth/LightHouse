package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/cache"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestExtractTokenPriorityQueryOverHeader(t *testing.T) {
	req := httptest.NewRequest("GET", "/ws?tk=query-token", nil)
	req.Header.Set("Authorization", "Bearer header-token")

	got, ok := ExtractToken(req)
	if !ok {
		t.Fatal("expected token")
	}
	if got.Token != "query-token" {
		t.Fatalf("expected query token, got %q", got.Token)
	}
	if got.Type != TokenTypeAuthKey {
		t.Fatalf("expected auth key, got %q", got.Type)
	}
}

func TestExtractTokenBearerScheme(t *testing.T) {
	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("Authorization", "Bearer my-token")

	got, ok := ExtractToken(req)
	if !ok {
		t.Fatal("expected token")
	}
	if got.Token != "my-token" {
		t.Fatalf("expected my-token, got %q", got.Token)
	}
	if got.Type != TokenTypeAuthKey {
		t.Fatalf("expected auth key, got %q", got.Type)
	}
}

func TestExtractTokenBearerWithApiKeyClaim(t *testing.T) {
	// JWT payload: {"type":"api_key","jti":"test-id"}
	// base64url encoded: eyJ0eXBlIjoiYXBpX2tleSIsImp0aSI6InRlc3QtaWQifQ
	token := "header.eyJ0eXBlIjoiYXBpX2tleSIsImp0aSI6InRlc3QtaWQifQ.signature"

	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	got, ok := ExtractToken(req)
	if !ok {
		t.Fatal("expected token")
	}
	if got.Token != token {
		t.Fatalf("expected token %q, got %q", token, got.Token)
	}
	if got.Type != TokenTypeApiKey {
		t.Fatalf("expected api key, got %q", got.Type)
	}
}

func TestExtractTokenBearerFromForwardedAuthorization(t *testing.T) {
	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("X-Forwarded-Authorization", "Bearer fwd-token")

	got, ok := ExtractToken(req)
	if !ok {
		t.Fatal("expected token")
	}
	if got.Token != "fwd-token" {
		t.Fatalf("expected fwd-token, got %q", got.Token)
	}
	if got.Type != TokenTypeAuthKey {
		t.Fatalf("expected auth key, got %q", got.Type)
	}
}

func TestExtractTokenNormalizesWrappedAuthorizationHeader(t *testing.T) {
	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("X-Original-Authorization", "[Bearer wrapped-token]")

	got, ok := ExtractToken(req)
	if !ok {
		t.Fatal("expected token")
	}
	if got.Token != "wrapped-token" {
		t.Fatalf("expected normalized token, got %q", got.Token)
	}
	if got.Type != TokenTypeAuthKey {
		t.Fatalf("expected auth key, got %q", got.Type)
	}
}

func TestExtractTokenRejectsNonBearerScheme(t *testing.T) {
	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("Authorization", "Basic some-credentials")

	_, ok := ExtractToken(req)
	if ok {
		t.Fatal("expected no token for non-Bearer scheme")
	}
}

func TestExtractTokenCookieFallback(t *testing.T) {
	req := httptest.NewRequest("GET", "/ws", nil)
	req.AddCookie(&http.Cookie{Name: "AuthToken", Value: "cookie-token"})

	got, ok := ExtractToken(req)
	if !ok {
		t.Fatal("expected token")
	}
	if got.Token != "cookie-token" {
		t.Fatalf("expected cookie token, got %q", got.Token)
	}
	if got.Type != TokenTypeAuthKey {
		t.Fatalf("expected auth key, got %q", got.Type)
	}
}

func TestExtractTokenNoToken(t *testing.T) {
	req := httptest.NewRequest("GET", "/ws", nil)

	_, ok := ExtractToken(req)
	if ok {
		t.Fatal("expected no token")
	}
}

func TestNormalizeAuthGRPCTargetWithHTTPS(t *testing.T) {
	target, useTLS := NormalizeAuthGRPCTarget("https://localhost:7003", false)
	if target != "localhost:7003" {
		t.Fatalf("expected localhost:7003, got %q", target)
	}
	if !useTLS {
		t.Fatal("expected TLS to be enabled for https target")
	}
}

func TestNormalizeAuthGRPCTargetWithPlainHost(t *testing.T) {
	target, useTLS := NormalizeAuthGRPCTarget("localhost:7003", true)
	if target != "localhost:7003" {
		t.Fatalf("expected localhost:7003, got %q", target)
	}
	if !useTLS {
		t.Fatal("expected TLS flag to preserve explicit config for plain host target")
	}
}

// --- middleware tests ---

type mockAuthenticator struct {
	result *AuthResult
	err    error
}

func (m *mockAuthenticator) Authenticate(_ context.Context, _ TokenInfo, _ *http.Request) (*AuthResult, error) {
	return m.result, m.err
}

type mockProfileService struct{}

func (m *mockProfileService) GetProfile(_ context.Context, _ *gen.DyGetProfileRequest, _ ...grpc.CallOption) (*gen.DyAccountProfile, error) {
	return &gen.DyAccountProfile{}, nil
}

func (m *mockProfileService) UpdateProfile(_ context.Context, _ *gen.DyUpdateProfileRequest, _ ...grpc.CallOption) (*gen.DyAccountProfile, error) {
	return &gen.DyAccountProfile{}, nil
}

func (m *mockProfileService) GetAccount(_ context.Context, _ *gen.DyGetAccountRequest, _ ...grpc.CallOption) (*gen.DyAccount, error) {
	return &gen.DyAccount{}, nil
}

func (m *mockProfileService) GetBotAccount(_ context.Context, _ *gen.DyGetBotAccountRequest, _ ...grpc.CallOption) (*gen.DyAccount, error) {
	return &gen.DyAccount{}, nil
}

func (m *mockProfileService) GetAccountBatch(_ context.Context, _ *gen.DyGetAccountBatchRequest, _ ...grpc.CallOption) (*gen.DyGetAccountBatchResponse, error) {
	return &gen.DyGetAccountBatchResponse{}, nil
}

func (m *mockProfileService) GetBotAccountBatch(_ context.Context, _ *gen.DyGetBotAccountBatchRequest, _ ...grpc.CallOption) (*gen.DyGetAccountBatchResponse, error) {
	return &gen.DyGetAccountBatchResponse{}, nil
}

func (m *mockProfileService) LookupAccountBatch(_ context.Context, _ *gen.DyLookupAccountBatchRequest, _ ...grpc.CallOption) (*gen.DyGetAccountBatchResponse, error) {
	return &gen.DyGetAccountBatchResponse{}, nil
}

func (m *mockProfileService) SearchAccount(_ context.Context, _ *gen.DySearchAccountRequest, _ ...grpc.CallOption) (*gen.DyGetAccountBatchResponse, error) {
	return &gen.DyGetAccountBatchResponse{}, nil
}

func (m *mockProfileService) ListAccounts(_ context.Context, _ *gen.DyListAccountsRequest, _ ...grpc.CallOption) (*gen.DyListAccountsResponse, error) {
	return &gen.DyListAccountsResponse{}, nil
}

func (m *mockProfileService) GetAccountStatus(_ context.Context, _ *gen.DyGetAccountRequest, _ ...grpc.CallOption) (*gen.DyAccountStatus, error) {
	return &gen.DyAccountStatus{}, nil
}

func (m *mockProfileService) GetAccountStatusBatch(_ context.Context, _ *gen.DyGetAccountBatchRequest, _ ...grpc.CallOption) (*gen.DyGetAccountStatusBatchResponse, error) {
	return &gen.DyGetAccountStatusBatchResponse{}, nil
}

func (m *mockProfileService) ListBadges(_ context.Context, _ *gen.DyListBadgesRequest, _ ...grpc.CallOption) (*gen.DyListBadgesResponse, error) {
	return &gen.DyListBadgesResponse{}, nil
}

func (m *mockProfileService) GrantBadge(_ context.Context, _ *gen.DyGrantBadgeRequest, _ ...grpc.CallOption) (*gen.DyGrantBadgeResponse, error) {
	return &gen.DyGrantBadgeResponse{}, nil
}

func (m *mockProfileService) GetBadge(_ context.Context, _ *gen.DyGetBadgeRequest, _ ...grpc.CallOption) (*gen.DyGetBadgeResponse, error) {
	return &gen.DyGetBadgeResponse{}, nil
}

func (m *mockProfileService) UpdateBadge(_ context.Context, _ *gen.DyUpdateBadgeRequest, _ ...grpc.CallOption) (*gen.DyUpdateBadgeResponse, error) {
	return &gen.DyUpdateBadgeResponse{}, nil
}

func (m *mockProfileService) GetRelationship(_ context.Context, _ *gen.DyGetRelationshipRequest, _ ...grpc.CallOption) (*gen.DyGetRelationshipResponse, error) {
	return &gen.DyGetRelationshipResponse{}, nil
}

func (m *mockProfileService) HasRelationship(_ context.Context, _ *gen.DyGetRelationshipRequest, _ ...grpc.CallOption) (*wrapperspb.BoolValue, error) {
	return wrapperspb.Bool(false), nil
}

func (m *mockProfileService) ListFriends(_ context.Context, _ *gen.DyListRelationshipSimpleRequest, _ ...grpc.CallOption) (*gen.DyListRelationshipSimpleResponse, error) {
	return &gen.DyListRelationshipSimpleResponse{}, nil
}

func (m *mockProfileService) ListBlocked(_ context.Context, _ *gen.DyListRelationshipSimpleRequest, _ ...grpc.CallOption) (*gen.DyListRelationshipSimpleResponse, error) {
	return &gen.DyListRelationshipSimpleResponse{}, nil
}

func (m *mockProfileService) GetCountdownEvents(_ context.Context, _ *gen.DyGetCountdownEventsRequest, _ ...grpc.CallOption) (*gen.DyGetCountdownEventsResponse, error) {
	return &gen.DyGetCountdownEventsResponse{}, nil
}

func (m *mockProfileService) UpdateBoardItemPayload(_ context.Context, _ *gen.DyUpdateBoardItemPayloadRequest, _ ...grpc.CallOption) (*gen.DyAccountBoardItem, error) {
	return &gen.DyAccountBoardItem{}, nil
}

func (m *mockProfileService) ListMuted(_ context.Context, _ *gen.DyListRelationshipSimpleRequest, _ ...grpc.CallOption) (*gen.DyListRelationshipSimpleResponse, error) {
	return &gen.DyListRelationshipSimpleResponse{}, nil
}

func (m *mockProfileService) ListCloseFriends(_ context.Context, _ *gen.DyListRelationshipSimpleRequest, _ ...grpc.CallOption) (*gen.DyListRelationshipSimpleResponse, error) {
	return &gen.DyListRelationshipSimpleResponse{}, nil
}

func (m *mockProfileService) GetNotableDays(_ context.Context, _ *gen.DyGetNotableDaysRequest, _ ...grpc.CallOption) (*gen.DyGetNotableDaysResponse, error) {
	return &gen.DyGetNotableDaysResponse{}, nil
}

func (m *mockProfileService) GetUserCalendarEvents(_ context.Context, _ *gen.DyGetUserCalendarEventsRequest, _ ...grpc.CallOption) (*gen.DyGetUserCalendarEventsResponse, error) {
	return &gen.DyGetUserCalendarEventsResponse{}, nil
}

func TestAuthMiddlewareStoresResult(t *testing.T) {
	gin.SetMode(gin.TestMode)

	c := cache.NewMemoryCacheService(100)
	defer c.Close()

	expected := &AuthResult{
		Session: &gen.DyAuthSession{
			Id:        "test-session",
			AccountId: "test-account",
			Account:   &gen.DyAccount{Id: "test-account"},
		},
		Account: &gen.DyAccount{Id: "test-account"},
	}
	auth := &mockAuthenticator{result: expected}
	profiles := &mockProfileService{}

	r := gin.New()
	r.Use(AuthMiddleware(auth, c, profiles))
	r.GET("/test", func(ctx *gin.Context) {
		result, token, ok := GetAuth(ctx)
		if !ok {
			t.Fatal("expected auth in context")
		}
		if result != expected {
			t.Fatal("unexpected auth result")
		}
		if token.Token != "tok" {
			t.Fatalf("expected token %q, got %q", "tok", token.Token)
		}
		ctx.Status(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestAuthMiddlewareRejectsMissingToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	c := cache.NewMemoryCacheService(100)
	defer c.Close()

	auth := &mockAuthenticator{result: &AuthResult{Account: &gen.DyAccount{Id: "test-account"}, Session: &gen.DyAuthSession{Id: "test-session", Account: &gen.DyAccount{Id: "test-account"}}}}
	profiles := &mockProfileService{}

	r := gin.New()
	r.Use(AuthMiddleware(auth, c, profiles))
	r.GET("/test", func(ctx *gin.Context) {
		ctx.Status(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestOptionalAuthMiddlewareProceedsWithoutToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	c := cache.NewMemoryCacheService(100)
	defer c.Close()

	auth := &mockAuthenticator{result: &AuthResult{}}
	profiles := &mockProfileService{}

	r := gin.New()
	r.Use(OptionalAuthMiddleware(auth, c, profiles))
	r.GET("/test", func(ctx *gin.Context) {
		_, _, ok := GetAuth(ctx)
		if ok {
			t.Fatal("expected no auth in context")
		}
		ctx.Status(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestOptionalAuthMiddlewareWithNilCacheDoesNotPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)

	auth := &mockAuthenticator{result: &AuthResult{}}

	r := gin.New()
	r.Use(OptionalAuthMiddleware(auth, nil, nil))
	r.GET("/test", func(ctx *gin.Context) {
		result, token, ok := GetAuth(ctx)
		if !ok {
			t.Fatal("expected auth in context")
		}
		if result == nil {
			t.Fatal("expected authenticated result")
		}
		if token.Token != "tok" {
			t.Fatalf("expected token %q, got %q", "tok", token.Token)
		}
		ctx.Status(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}
