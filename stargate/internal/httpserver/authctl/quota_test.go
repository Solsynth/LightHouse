package authctl

// Pins the HTTP contract of the per-IP new-challenge quota: an IP may create
// cfg.Security.MaxChallengesPerIp challenges per window, the next one is
// rejected with 429, and other IPs are unaffected.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/geo"
	"src.solsynth.dev/sosys/stargate/internal/grpcclient"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

func TestCreateChallengeQuotaPerIP(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), smokeDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	ctx := context.Background()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	rc, err := redis.Connect(ctx, "localhost:6379", "", 0)
	if err != nil {
		t.Skipf("redis unavailable: %v", err)
	}
	t.Cleanup(func() { _ = rc.Raw.Close() })

	cfg := config.Default()
	cfg.Security.MaxChallengesPerIp = 3
	cfg.Security.ChallengeWindow = "1h"
	h := &handler{d: Deps{
		Store:   store.New(pool),
		Redis:   rc,
		Geo:     &geo.Service{},
		Cfg:     cfg,
		Clients: &grpcclient.Clients{Ring: &fakeRing{}},
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}

	accountID := seedRiskAccount(t, ctx, pool, model.AuthFactorTypePassword)
	account, err := h.d.Store.GetAccountByID(ctx, uuid.MustParse(accountID))
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	ip := "203.0.113.93"
	otherIP := "198.51.100.61"
	t.Cleanup(func() {
		_ = rc.Raw.Del(ctx, "auth:challenge:ip:"+ip).Err()
		_ = rc.Raw.Del(ctx, "auth:challenge:ip:"+otherIP).Err()
	})

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/auth/challenge", h.createChallenge)

	// A fresh device id per request, otherwise FindLiveChallenge hands back the
	// live challenge instead of creating a new one.
	post := func(from string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{
			"account":   account.Name,
			"device_id": uuid.NewString(),
		})
		req := httptest.NewRequest(http.MethodPost, "/auth/challenge", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", from)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	for i := range cfg.Security.MaxChallengesPerIp {
		if rec := post(ip); rec.Code != http.StatusOK {
			t.Fatalf("challenge %d from %s status = %d, want 200 (body %s)", i+1, ip, rec.Code, rec.Body.String())
		}
	}

	rec := post(ip)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("challenge %d from %s status = %d, want 429 (body %s)",
			cfg.Security.MaxChallengesPerIp+1, ip, rec.Code, rec.Body.String())
	}

	if rec := post(otherIP); rec.Code != http.StatusOK {
		t.Fatalf("challenge from %s status = %d, want 200 (quota is per IP)", otherIP, rec.Code)
	}
}

// TestGenerateQrChallengeQuotaPerIP pins that the quota is global across the
// challenge-creating endpoints: QR login challenges consume the same per-IP
// budget as username challenges.
func TestGenerateQrChallengeQuotaPerIP(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), smokeDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	ctx := context.Background()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	rc, err := redis.Connect(ctx, "localhost:6379", "", 0)
	if err != nil {
		t.Skipf("redis unavailable: %v", err)
	}
	t.Cleanup(func() { _ = rc.Raw.Close() })

	cfg := config.Default()
	cfg.Security.MaxChallengesPerIp = 1
	cfg.Security.ChallengeWindow = "1h"
	h := &handler{d: Deps{
		Store: store.New(pool),
		Redis: rc,
		Geo:   &geo.Service{},
		Cfg:   cfg,
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}

	ip := "203.0.113.94"
	t.Cleanup(func() { _ = rc.Raw.Del(ctx, "auth:challenge:ip:"+ip).Err() })

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/auth/qr/generate", h.generateQrChallenge)

	post := func() *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"device_id": uuid.NewString()})
		req := httptest.NewRequest(http.MethodPost, "/auth/qr/generate", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", ip)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("first QR challenge status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if rec := post(); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second QR challenge status = %d, want 429 (body %s)", rec.Code, rec.Body.String())
	}
}
