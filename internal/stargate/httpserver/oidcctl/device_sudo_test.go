package oidcctl

// Approving a device code binds it to the signed-in session, so it is gated on
// a live elevation grant exactly like challenge and QR approval: an unelevated
// session is refused with AUTH_SUDO_REQUIRED before the code is touched, and
// the approval itself never grants an elevation.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
)

const deviceSudoTestIP = "198.19.8.8"

type deviceSudoHarness struct {
	svc       *service
	token     string
	accountID string
	sessionID string
	router    *gin.Engine
	ctx       context.Context
}

func newDeviceSudoHarness(t *testing.T) *deviceSudoHarness {
	t.Helper()
	ctx := context.Background()
	svc, st, jwtSvc, pool := newRefreshTestService(t)
	middleware.SetSudoChecker(svc.authSvc)
	t.Cleanup(func() { middleware.SetSudoChecker(nil) })

	var accountID string
	if err := pool.QueryRow(ctx, `SELECT id FROM accounts ORDER BY created_at LIMIT 1`).Scan(&accountID); err != nil {
		t.Skipf("no local account: %v", err)
	}
	now := time.Now().UTC()
	// One enabled, trustworthy factor so SudoFactorHint can name a way up.
	factorID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO account_auth_factors
		(id, account_id, type, secret, config, trustworthy, enabled_at, created_at, updated_at)
		VALUES ($1, $2, $3, '', '{}', 1, $4, $4, $4)`,
		factorID, accountID, int(model.AuthFactorTypePassword), now); err != nil {
		t.Fatalf("seed factor: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM account_auth_factors WHERE id = $1`, factorID) })

	clientID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_clients
		(id, account_id, device_id, device_name, platform, created_at, updated_at)
		VALUES ($1, $2, $3, $3, $4, $5, $5)`,
		clientID, accountID, "device-sudo-test-device", int(model.ClientPlatformIos), now); err != nil {
		t.Fatalf("seed client: %v", err)
	}
	sessionID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions
		(id, account_id, audiences, scopes, epoch, type, expired_at, created_at, updated_at, client_id, last_granted_at)
		VALUES ($1, $2, '[]', '["*"]', 0, 0, $3, $4, $4, $5, $4)`,
		sessionID, accountID, now.Add(time.Hour), now, clientID); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	t.Cleanup(func() {
		_ = svc.redis.Cache.Remove(ctx, "accounts:"+sessionID+":sudo")
		_, _ = pool.Exec(ctx, `DELETE FROM auth_sessions WHERE id = $1`, sessionID)
	})

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

	// The production route table from Register, under the real auth middleware.
	authMW := middleware.Auth(middleware.AuthDeps{
		Token:        svc.token,
		Renewer:      svc.authSvc,
		CookieDomain: svc.cfg.Auth.CookieDomain,
		CookieSecure: svc.cfg.Auth.CookieSecure,
	})
	gin.SetMode(gin.TestMode)
	router := gin.New()
	api := router.Group("/api")
	api.Use(authMW)
	Register(api, Deps{
		Store: st,
		Redis: svc.redis,
		Cfg:   svc.cfg,
		JWT:   jwtSvc,
		Token: svc.token,
		Auth:  svc.authSvc,
		Log:   svc.log,
	})

	return &deviceSudoHarness{
		svc: svc, token: token, accountID: accountID, sessionID: sessionID,
		router: router, ctx: ctx,
	}
}

// seedDeviceCode issues a pending device code for a throwaway client id: the
// approve/decline handlers never resolve the client, they only move the code.
func (dh *deviceSudoHarness) seedDeviceCode(t *testing.T) (clientID, userCode string) {
	t.Helper()
	clientID = uuid.NewString()
	info, err := dh.svc.generateDeviceCode(dh.ctx, clientID, []string{"openid"}, nil, nil)
	if err != nil {
		t.Fatalf("generate device code: %v", err)
	}
	return clientID, info.UserCode
}

func (dh *deviceSudoHarness) resolve(t *testing.T, userCode, action string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/auth/open/device/code/"+userCode+"/"+action, nil)
	req.Header.Set("Authorization", "Bearer "+dh.token)
	req.Header.Set("X-Forwarded-For", deviceSudoTestIP)
	rec := httptest.NewRecorder()
	dh.router.ServeHTTP(rec, req)
	return rec
}

// TestDeviceApprovalRequiresElevation pins the gate on both actions: the
// refusal names the elevation, leaves the code pending, and grants nothing.
func TestDeviceApprovalRequiresElevation(t *testing.T) {
	dh := newDeviceSudoHarness(t)

	for _, action := range []string{"approve", "decline"} {
		t.Run(action, func(t *testing.T) {
			_, userCode := dh.seedDeviceCode(t)
			rec := dh.resolve(t, userCode, action)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s: status = %d, want 403 (body %s)", action, rec.Code, rec.Body.String())
			}
			info, err := dh.svc.getDeviceCodeByUserCode(dh.ctx, userCode)
			if err != nil {
				t.Fatalf("load device code: %v", err)
			}
			if info == nil || info.Status != deviceCodeStatusPending || info.AccountId != nil {
				t.Fatalf("%s: refusal touched the device code: %+v", action, info)
			}
			if elevated, err := dh.svc.authSvc.IsSudoElevated(dh.ctx, dh.sessionID); err != nil || elevated {
				t.Fatalf("%s: refusal left the session elevated=%v err=%v", action, elevated, err)
			}
		})
	}
}

// TestDeviceApprovalDoesNotLiftTheSession pins that approval is not a way to
// elevate: it goes through only once the caller elevated explicitly, and it
// does not extend the grant it presented.
func TestDeviceApprovalDoesNotLiftTheSession(t *testing.T) {
	dh := newDeviceSudoHarness(t)
	_, userCode := dh.seedDeviceCode(t)
	if _, err := dh.svc.authSvc.GrantSudo(dh.ctx, dh.sessionID); err != nil {
		t.Fatalf("grant sudo: %v", err)
	}
	before, err := dh.svc.redis.Raw.TTL(dh.ctx, "accounts:"+dh.sessionID+":sudo").Result()
	if err != nil {
		t.Fatalf("read grant ttl: %v", err)
	}

	rec := dh.resolve(t, userCode, "approve")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	info, err := dh.svc.getDeviceCodeByUserCode(dh.ctx, userCode)
	if err != nil {
		t.Fatalf("load device code: %v", err)
	}
	if info == nil || info.Status != deviceCodeStatusApproved || info.AccountId == nil {
		t.Fatalf("device code not approved: %+v", info)
	}

	after, err := dh.svc.redis.Raw.TTL(dh.ctx, "accounts:"+dh.sessionID+":sudo").Result()
	if err != nil {
		t.Fatalf("read grant ttl: %v", err)
	}
	if after > before {
		t.Fatalf("approving extended the elevation: ttl %v -> %v", before, after)
	}
}
