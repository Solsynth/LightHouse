package authctl

// QR login approval is a gated action: the session that approves or declines a
// login must hold a live elevation grant, exactly like challenge approval.
// Granting the elevation is the caller's job (POST /auth/sudo) — approving a
// login must never lift the approving session itself.

import (
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

	"src.solsynth.dev/sosys/stargate/auth"
	"src.solsynth.dev/sosys/stargate/config"
	"src.solsynth.dev/sosys/stargate/geo"
	"src.solsynth.dev/sosys/stargate/grpcclient"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/redis"
	"src.solsynth.dev/sosys/stargate/store"
)

const qrSudoTestIP = "198.19.7.7"

// qrSudoHarness is the QR routes wired with the real auth + elevation
// middleware, one account holding the QR login factor and a trusted native
// session for it.
type qrSudoHarness struct {
	h         *handler
	router    *gin.Engine
	rc        *redis.Client
	authSvc   *auth.AuthService
	token     string
	accountID string
	sessionID string
	ctx       context.Context
}

func newQrSudoHarness(t *testing.T) *qrSudoHarness {
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
	jwtSvc, err := auth.NewJWTService(cfg)
	if err != nil {
		t.Fatalf("jwt service: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.New(pool)
	tokenSvc := auth.NewTokenAuthService(st, rc, jwtSvc, nil, nil, log)
	authSvc := auth.NewAuthService(st, rc, cfg, geo.NewService(""), jwtSvc, tokenSvc, nil, nil, log)
	h := &handler{d: Deps{
		Store:   st,
		Redis:   rc,
		Cfg:     cfg,
		Token:   tokenSvc,
		Auth:    authSvc,
		Geo:     &geo.Service{},
		Clients: &grpcclient.Clients{Ring: &fakeRing{}},
		Log:     log,
	}}
	middleware.SetSudoChecker(authSvc)
	t.Cleanup(func() { middleware.SetSudoChecker(nil) })

	accountID := seedRiskAccount(t, ctx, pool, model.AuthFactorTypeQrLogin)
	now := time.Now().UTC()
	// The seeded QR factor is only Trustworthy 1, and elevation demands >= 2
	// (auth.SudoMinTrustworthy), so add one that can satisfy it: the refusal
	// then names a real way up.
	if _, err := pool.Exec(ctx, `INSERT INTO account_auth_factors
		(id, account_id, type, secret, config, trustworthy, enabled_at, created_at, updated_at)
		VALUES ($1, $2, $3, '', '{}', 3, $4, $4, $4)`,
		uuid.NewString(), accountID, int(model.AuthFactorTypeTimedCode), now); err != nil {
		t.Fatalf("seed timed code factor: %v", err)
	}
	clientID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_clients
		(id, account_id, device_id, device_name, platform, created_at, updated_at)
		VALUES ($1, $2, $3, $3, $4, $5, $5)`,
		clientID, accountID, "qr-sudo-test-device", int(model.ClientPlatformIos), now); err != nil {
		t.Fatalf("seed client: %v", err)
	}
	sessionID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions
		(id, account_id, audiences, scopes, epoch, type, expired_at, created_at, updated_at, client_id, last_granted_at)
		VALUES ($1, $2, '[]', '["*"]', 0, 0, $3, $4, $4, $5, $4)`,
		sessionID, accountID, now.Add(time.Hour), now, clientID); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	account, err := st.GetAccountByID(ctx, uuid.MustParse(accountID))
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	token, err := jwtSvc.CreateUserToken(&model.AuthSession{
		Id:            sessionID,
		AccountId:     accountID,
		Type:          model.SessionTypeLogin,
		Scopes:        []string{"*"},
		Audiences:     []string{},
		ExpiredAt:     model.NewTime(now.Add(time.Hour)),
		LastGrantedAt: model.NewTime(now),
	}, account, now.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("create access token: %v", err)
	}

	t.Cleanup(func() {
		_ = rc.Cache.Remove(ctx, "accounts:"+sessionID+":sudo")
		_ = rc.Raw.Del(ctx,
			"auth:fail2ban:decline:"+qrSudoTestIP,
			"auth:fail2ban:decline-block:"+qrSudoTestIP,
			"auth:fail2ban:block:"+qrSudoTestIP,
			"auth:fail2ban:ip:"+qrSudoTestIP,
			"auth:challenge:ip:"+qrSudoTestIP).Err()
	})

	gin.SetMode(gin.TestMode)
	router := gin.New()
	authMW := middleware.Auth(middleware.AuthDeps{Token: tokenSvc, CookieDomain: cfg.Auth.CookieDomain, Log: log})
	api := router.Group("/api")
	api.Use(authMW)
	// The production route table, so the gate is pinned on the real stack.
	Register(api, Deps{
		Store:   st,
		Redis:   rc,
		Cfg:     cfg,
		Token:   tokenSvc,
		Auth:    authSvc,
		Geo:     &geo.Service{},
		Clients: &grpcclient.Clients{Ring: &fakeRing{}},
		Log:     log,
	})

	return &qrSudoHarness{
		h: h, router: router, rc: rc, authSvc: authSvc,
		token: token, accountID: accountID, sessionID: sessionID, ctx: ctx,
	}
}

// seedQrChallenge stores a pending QR challenge whose auth challenge was
// requested from qrSudoTestIP, and returns the QR id.
func (qh *qrSudoHarness) seedQrChallenge(t *testing.T) string {
	t.Helper()
	now := time.Now().UTC()
	authChallenge := &model.AuthChallenge{
		Id:         uuid.NewString(),
		AccountId:  uuid.Nil.String(),
		DeviceId:   "qr-sudo-requester",
		DeviceName: new("Requesting device"),
		IpAddress:  new(qrSudoTestIP),
		StepTotal:  1,
		StepRemain: 1,
		ExpiredAt:  model.NewTime(now.Add(qrChallengeTTL)),
		CreatedAt:  model.NewTime(now),
		UpdatedAt:  model.NewTime(now),
	}
	if err := qh.h.d.Store.CreateAuthChallenge(qh.ctx, authChallenge); err != nil {
		t.Fatalf("create auth challenge: %v", err)
	}
	qrID := uuid.NewString()
	blob, err := json.Marshal(&qrLoginChallenge{
		Id:              qrID,
		AuthChallengeId: authChallenge.Id,
		AccountId:       uuid.Nil.String(),
		DeviceId:        "qr-sudo-requester",
		Platform:        model.ClientPlatformWeb,
		Status:          qrStatusPending,
		CreatedAt:       model.NewTime(now),
		ExpiresAt:       model.NewTime(now.Add(qrChallengeTTL)),
	})
	if err != nil {
		t.Fatalf("marshal qr challenge: %v", err)
	}
	if err := qh.rc.Raw.Set(qh.ctx, qrCachePrefix+qrID, blob, qrChallengeTTL).Err(); err != nil {
		t.Fatalf("store qr challenge: %v", err)
	}
	t.Cleanup(func() {
		_ = qh.rc.Raw.Del(qh.ctx, qrCachePrefix+qrID, qrToAuthPrefix+authChallenge.Id).Err()
	})
	return qrID
}

func (qh *qrSudoHarness) resolve(t *testing.T, qrID, action string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/qr/"+qrID+"/"+action, nil)
	req.Header.Set("Authorization", "Bearer "+qh.token)
	req.Header.Set("X-Forwarded-For", qrSudoTestIP)
	rec := httptest.NewRecorder()
	qh.router.ServeHTTP(rec, req)
	return rec
}

func (qh *qrSudoHarness) qrStatus(t *testing.T, qrID string) qrLoginStatus {
	t.Helper()
	qr, err := qh.h.getQrChallenge(qh.ctx, qrID)
	if err != nil {
		t.Fatalf("load qr challenge: %v", err)
	}
	return qr.Status
}

// TestQrApprovalRequiresElevation pins the gate: an unelevated session is
// refused with AUTH_SUDO_REQUIRED before the QR record is touched, and the
// refusal grants nothing.
func TestQrApprovalRequiresElevation(t *testing.T) {
	qh := newQrSudoHarness(t)

	for _, action := range []string{"approve", "decline"} {
		t.Run(action, func(t *testing.T) {
			qrID := qh.seedQrChallenge(t)
			rec := qh.resolve(t, qrID, action)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s: status = %d, want 403 (body %s)", action, rec.Code, rec.Body.String())
			}
			if code := responseErrorCode(t, rec); code != "AUTH_SUDO_REQUIRED" {
				t.Fatalf("%s: error code = %q, want AUTH_SUDO_REQUIRED", action, code)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if detail, _ := body["detail"].(string); detail != "timed_code" {
				t.Fatalf("%s: detail = %v, want the account's timed_code hint", action, body["detail"])
			}
			if status := qh.qrStatus(t, qrID); status != qrStatusPending {
				t.Fatalf("%s: qr status = %d, want pending", action, status)
			}
			if elevated, err := qh.authSvc.IsSudoElevated(qh.ctx, qh.sessionID); err != nil || elevated {
				t.Fatalf("%s: refusal left the session elevated=%v err=%v", action, elevated, err)
			}
		})
	}
}

// TestQrApprovalDoesNotLiftTheSession pins that approving a login is not a way
// to elevate: the gated call only goes through once the caller elevated
// explicitly, and the approval neither grants a new grant nor extends the one
// that was presented.
func TestQrApprovalDoesNotLiftTheSession(t *testing.T) {
	qh := newQrSudoHarness(t)
	qrID := qh.seedQrChallenge(t)

	if _, err := qh.authSvc.GrantSudo(qh.ctx, qh.sessionID); err != nil {
		t.Fatalf("grant sudo: %v", err)
	}
	before, err := qh.rc.Raw.TTL(qh.ctx, "accounts:"+qh.sessionID+":sudo").Result()
	if err != nil {
		t.Fatalf("read grant ttl: %v", err)
	}

	rec := qh.resolve(t, qrID, "approve")
	if rec.Code != http.StatusOK {
		t.Fatalf("elevated approve: status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if status := qh.qrStatus(t, qrID); status != qrStatusApproved {
		t.Fatalf("qr status = %d, want approved", status)
	}

	after, err := qh.rc.Raw.TTL(qh.ctx, "accounts:"+qh.sessionID+":sudo").Result()
	if err != nil {
		t.Fatalf("read grant ttl: %v", err)
	}
	if after > before {
		t.Fatalf("approving extended the elevation: ttl %v -> %v", before, after)
	}
}
