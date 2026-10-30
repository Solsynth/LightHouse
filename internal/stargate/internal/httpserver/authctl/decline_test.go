package authctl

// End-to-end pin for the declined-challenge fail2ban: a challenge declined by
// a trusted session charges the IP that *requested* it, exceeding
// cfg.Security.Fail2banDeclineMax blocks that IP for new challenges, and the
// decliner's own IP is left alone. Covers the in-app approval prompt and the
// QR login prompt.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/geo"
	"src.solsynth.dev/sosys/stargate/internal/grpcclient"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/risk"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

const (
	// IP that starts the login attempts (what the decline must be charged to).
	declineRequesterIP = "203.0.113.150"
	// IP of the trusted session that declines (must never be charged).
	declineDeclinerIP = "198.51.100.200"
)

// declineHarness is a handler wired with real middleware, DB, Redis, signing
// keys, one account and a trusted native session for it.
type declineHarness struct {
	h         *handler
	router    *gin.Engine
	rc        *redis.Client
	pool      *pgxpool.Pool
	cfg       *config.Config
	token     string
	account   *model.Account
	accountID string

	ctx context.Context
}

// newDeclineHarness builds the harness with cfg.Security.Fail2banDeclineMax
// set to declineMax and seeds accountFactors for the account.
func newDeclineHarness(t *testing.T, declineMax int, accountFactors ...model.AuthFactorType) *declineHarness {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, smokeDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	rc, err := redis.Connect(ctx, "localhost:6379", "", 0)
	if err != nil {
		t.Skipf("redis unavailable: %v", err)
	}
	t.Cleanup(func() { _ = rc.Raw.Close() })

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test file path")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(file))))
	priv := filepath.Join(root, "Keys", "PrivateKey.pem")
	pub := filepath.Join(root, "Keys", "PublicKey.pem")
	if _, err := os.Stat(priv); err != nil {
		t.Skipf("dev signing keys missing: %v", err)
	}

	cfg := config.Default()
	cfg.Auth.PublicKeyPath = pub
	cfg.Auth.PrivateKeyPath = priv
	cfg.Security.Fail2banDeclineMax = declineMax
	cfg.Security.Fail2banWindow = "1m"
	cfg.Security.Fail2banBlockFor = "1m"
	jwtSvc, err := auth.NewJWTService(cfg)
	if err != nil {
		t.Fatalf("jwt service: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.New(pool)
	tokenSvc := auth.NewTokenAuthService(st, rc, jwtSvc, nil, nil, log)
	h := &handler{d: Deps{
		Store:   st,
		Redis:   rc,
		Cfg:     cfg,
		Token:   tokenSvc,
		Auth:    auth.NewAuthService(st, rc, cfg, geo.NewService(""), jwtSvc, tokenSvc, nil, nil, log),
		Geo:     &geo.Service{},
		Clients: &grpcclient.Clients{Ring: &fakeRing{}},
		Log:     log,
	}}

	accountID := seedRiskAccount(t, ctx, pool, accountFactors...)
	account, err := h.d.Store.GetAccountByID(ctx, uuid.MustParse(accountID))
	if err != nil {
		t.Fatalf("load account: %v", err)
	}

	// Trusted session: native-platform client, granted just now.
	clientID := uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_clients
		(id, account_id, device_id, device_name, platform, created_at, updated_at)
		VALUES ($1, $2, $3, $3, $4, $5, $5)`,
		clientID, accountID, "decline-test-device", int(model.ClientPlatformIos), now); err != nil {
		t.Fatalf("seed client: %v", err)
	}
	sessionID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions
		(id, account_id, audiences, scopes, epoch, type, expired_at, created_at, updated_at, client_id, last_granted_at)
		VALUES ($1, $2, '[]', '["*"]', 0, 0, $3, $4, $4, $5, $4)`,
		sessionID, accountID, now.Add(time.Hour), now, clientID); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	session := &model.AuthSession{
		Id:            sessionID,
		AccountId:     accountID,
		Type:          model.SessionTypeLogin,
		Scopes:        []string{"*"},
		Audiences:     []string{},
		Epoch:         0,
		ExpiredAt:     model.NewTime(now.Add(time.Hour)),
		LastGrantedAt: model.NewTime(now),
	}
	token, err := jwtSvc.CreateUserToken(session, account, now.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("create access token: %v", err)
	}

	t.Cleanup(func() {
		for _, ip := range []string{declineRequesterIP, declineDeclinerIP} {
			_ = rc.Raw.Del(ctx,
				"auth:fail2ban:decline:"+ip, "auth:fail2ban:decline-block:"+ip,
				"auth:fail2ban:block:"+ip, "auth:fail2ban:ip:"+ip,
				"auth:challenge:ip:"+ip).Err()
		}
	})

	gin.SetMode(gin.TestMode)
	router := gin.New()
	authMW := middleware.Auth(middleware.AuthDeps{Token: tokenSvc, CookieDomain: cfg.Auth.CookieDomain, Log: log})
	router.POST("/auth/challenge/:id/decline", authMW, middleware.RequireAuth(), middleware.RequireInteractive(), h.declineChallenge)
	router.POST("/auth/qr/:id/decline", authMW, middleware.RequireAuth(), middleware.RequireInteractive(), h.declineQrChallenge)
	router.POST("/auth/challenge", h.createChallenge)

	return &declineHarness{
		h: h, router: router, rc: rc, pool: pool, cfg: cfg,
		token: token, account: account, accountID: accountID, ctx: ctx,
	}
}

// decline posts a decline from the trusted session (IP: declineDeclinerIP).
func (dr *declineHarness) decline(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+dr.token)
	req.Header.Set("X-Forwarded-For", declineDeclinerIP)
	rec := httptest.NewRecorder()
	dr.router.ServeHTTP(rec, req)
	return rec
}

// seedRequesterChallenge inserts a live challenge started from
// declineRequesterIP.
func (dr *declineHarness) seedRequesterChallenge(t *testing.T) string {
	t.Helper()
	now := time.Now().UTC()
	challenge := &model.AuthChallenge{
		Id:         uuid.NewString(),
		AccountId:  dr.accountID,
		DeviceId:   "decline-test-requester",
		DeviceName: new("Requesting device"),
		UserAgent:  new("decline-test-agent"),
		IpAddress:  new(declineRequesterIP),
		StepTotal:  1,
		StepRemain: 1,
		ExpiredAt:  model.NewTime(now.Add(time.Hour)),
		CreatedAt:  model.NewTime(now),
		UpdatedAt:  model.NewTime(now),
	}
	if err := dr.h.d.Store.CreateAuthChallenge(dr.ctx, challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	return challenge.Id
}

// TestDeclinedChallengeFail2bansRequestingIP pins the in-app prompt path: each
// decline is charged to the requesting IP, the block lands only once the count
// exceeds the limit, the decliner's IP is never charged, and the block rejects
// new challenges from the requesting IP with the fail2ban message.
func TestDeclinedChallengeFail2bansRequestingIP(t *testing.T) {
	dr := newDeclineHarness(t, 2, model.AuthFactorTypePassword)

	for i := range dr.cfg.Security.Fail2banDeclineMax + 1 {
		rec := dr.decline(t, "/auth/challenge/"+dr.seedRequesterChallenge(t)+"/decline")
		if rec.Code != http.StatusOK {
			t.Fatalf("decline %d status = %d, want 200 (body %s)", i+1, rec.Code, rec.Body.String())
		}
		blocked := !risk.IPAllowed(dr.ctx, dr.rc, dr.cfg, declineRequesterIP)
		if i < dr.cfg.Security.Fail2banDeclineMax && blocked {
			t.Fatalf("decline %d: requesting IP blocked at the exact limit", i+1)
		}
		if i == dr.cfg.Security.Fail2banDeclineMax && !blocked {
			t.Fatalf("decline %d: requesting IP not blocked after exceeding the limit", i+1)
		}
	}

	// The trusting device's own IP is never charged.
	if !risk.IPAllowed(dr.ctx, dr.rc, dr.cfg, declineDeclinerIP) {
		t.Fatal("the declining session's IP must not be blocked")
	}

	// The block is enforced on new challenges from the requesting IP, and it is
	// the fail2ban block (not the per-IP challenge quota) that rejects them.
	body, _ := json.Marshal(map[string]any{"account": dr.account.Name, "device_id": uuid.NewString()})
	req := httptest.NewRequest(http.MethodPost, "/auth/challenge", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", declineRequesterIP)
	rec := httptest.NewRecorder()
	dr.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("new challenge from a blocked IP status = %d, want 429 (body %s)", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("Too many failed sign-in attempts")) {
		t.Fatalf("429 was not the fail2ban block: %s", rec.Body.String())
	}
}

// TestDeclinedQrChallengeFail2bansRequestingIP pins the QR prompt path: a
// declined QR login charges the IP that generated the QR code.
func TestDeclinedQrChallengeFail2bansRequestingIP(t *testing.T) {
	dr := newDeclineHarness(t, 1, model.AuthFactorTypeQrLogin)

	for i := range dr.cfg.Security.Fail2banDeclineMax + 1 {
		authChallengeID := dr.seedRequesterChallenge(t)
		qrID := uuid.NewString()
		now := time.Now().UTC()
		qr := &qrLoginChallenge{
			Id:              qrID,
			AuthChallengeId: authChallengeID,
			AccountId:       uuid.Nil.String(),
			DeviceId:        "qr-test-device",
			Platform:        model.ClientPlatformWeb,
			Status:          qrStatusPending,
			CreatedAt:       model.NewTime(now),
			ExpiresAt:       model.NewTime(now.Add(qrChallengeTTL)),
		}
		blob, err := json.Marshal(qr)
		if err != nil {
			t.Fatalf("marshal qr challenge: %v", err)
		}
		if err := dr.rc.Raw.Set(dr.ctx, qrCachePrefix+qrID, blob, qrChallengeTTL).Err(); err != nil {
			t.Fatalf("store qr challenge: %v", err)
		}
		t.Cleanup(func() { _ = dr.rc.Raw.Del(dr.ctx, qrCachePrefix+qrID, qrToAuthPrefix+authChallengeID).Err() })

		rec := dr.decline(t, "/auth/qr/"+qrID+"/decline")
		if rec.Code != http.StatusOK {
			t.Fatalf("qr decline %d status = %d, want 200 (body %s)", i+1, rec.Code, rec.Body.String())
		}
		blocked := !risk.IPAllowed(dr.ctx, dr.rc, dr.cfg, declineRequesterIP)
		if i < dr.cfg.Security.Fail2banDeclineMax && blocked {
			t.Fatalf("qr decline %d: requesting IP blocked at the exact limit", i+1)
		}
		if i == dr.cfg.Security.Fail2banDeclineMax && !blocked {
			t.Fatalf("qr decline %d: requesting IP not blocked after exceeding the limit", i+1)
		}
	}

	if !risk.IPAllowed(dr.ctx, dr.rc, dr.cfg, declineDeclinerIP) {
		t.Fatal("the declining session's IP must not be blocked")
	}
}
