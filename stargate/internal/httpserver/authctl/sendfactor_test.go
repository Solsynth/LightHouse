package authctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/geo"
	"src.solsynth.dev/sosys/stargate/internal/grpcclient"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/spell"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// fakeRing implements gen.DyRingServiceClient, recording outbound email and
// push calls so the smoke test can assert delivery without a live Ring.
type fakeRing struct {
	emails      []*gen.DyEmailMessage
	pushes      []*gen.DyPushNotification
	failEmail   error
	failPush    error
	sendTimeout time.Duration
}

func (f *fakeRing) SendEmail(_ context.Context, in *gen.DySendEmailRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	if f.sendTimeout > 0 {
		time.Sleep(f.sendTimeout)
	}
	if f.failEmail != nil {
		return nil, f.failEmail
	}
	f.emails = append(f.emails, in.Email)
	return &emptypb.Empty{}, nil
}

func (f *fakeRing) SendPushNotificationToUser(_ context.Context, in *gen.DySendPushNotificationToUserRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	if f.sendTimeout > 0 {
		time.Sleep(f.sendTimeout)
	}
	if f.failPush != nil {
		return nil, f.failPush
	}
	f.pushes = append(f.pushes, in.Notification)
	return &emptypb.Empty{}, nil
}

func (f *fakeRing) SendPushNotificationToUsers(context.Context, *gen.DySendPushNotificationToUsersRequest, ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (f *fakeRing) UnsubscribePushNotifications(context.Context, *gen.DyUnsubscribePushNotificationsRequest, ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

// seedFactorAccount inserts an account with a verified primary email contact
// and one enabled factor of the given type.
func seedFactorAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, factorType model.AuthFactorType, withContact bool) (accountID, factorID string) {
	t.Helper()
	accountID = uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, "factor_"+uuid.NewString()[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if withContact {
		if _, err := pool.Exec(ctx, `INSERT INTO account_contacts (id, account_id, type, content, is_primary, is_public, verified_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, true, false, $5, $5, $5)`,
			uuid.NewString(), accountID, int(model.ContactTypeEmail), "factor@example.com", now); err != nil {
			t.Fatalf("seed contact: %v", err)
		}
	}
	factorID = uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO account_auth_factors (id, account_id, type, secret, config, trustworthy, enabled_at, created_at, updated_at)
		VALUES ($1, $2, $3, '', '{}', 1, $4, $4, $4)`, factorID, accountID, int(factorType), now); err != nil {
		t.Fatalf("seed factor: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, accountID) })
	return accountID, factorID
}

func newFactorHandler(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ring *fakeRing) (*handler, *redis.Client) {
	t.Helper()
	rc, err := redis.Connect(ctx, "localhost:6379", "", 0)
	if err != nil {
		t.Skipf("redis unavailable: %v", err)
	}
	t.Cleanup(func() { _ = rc.Raw.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg, err := config.Load("/tmp/nonexistent-stargate.toml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	st := store.New(pool)
	spells := spell.NewService(st, rc, ring, cfg.SiteUrl, cfg, logger)
	return &handler{d: Deps{
		Store:   st,
		Redis:   rc,
		Spells:  spells,
		Clients: &grpcclient.Clients{Ring: ring},
		Log:     logger,
	}}, rc
}

// TestSendFactorCodeEmailCodeDeliversViaRing pins the EmailCode contract
// (AccountService.SendFactorCode): the code is emailed through Ring's
// SendEmail and stored for verification; a failed send or a missing verified
// contact leaves no code behind, so the user can retry instead of being
// locked out of a code that never arrived.
func TestSendFactorCodeEmailCodeDeliversViaRing(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), smokeDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	ctx := context.Background()

	t.Run("delivers and stores the code", func(t *testing.T) {
		ring := &fakeRing{}
		h, rc := newFactorHandler(t, ctx, pool, ring)
		accountID, factorID := seedFactorAccount(t, ctx, pool, model.AuthFactorTypeEmailCode, true)
		account, err := h.d.Store.GetAccountByID(ctx, uuid.MustParse(accountID))
		if err != nil {
			t.Fatalf("load account: %v", err)
		}
		factor, err := h.d.Store.GetAuthFactorByID(ctx, accountID, uuid.MustParse(factorID))
		if err != nil {
			t.Fatalf("load factor: %v", err)
		}
		challenge := &model.AuthChallenge{Id: uuid.NewString(), AccountId: accountID}

		if err := h.sendFactorCode(ctx, account, factor, challenge); err != nil {
			t.Fatalf("sendFactorCode: %v", err)
		}
		if len(ring.emails) != 1 {
			t.Fatalf("emails sent = %d, want 1", len(ring.emails))
		}
		email := ring.emails[0]
		if email.ToAddress != "factor@example.com" || email.Subject != "Your email verification code" {
			t.Fatalf("unexpected email: %+v", email)
		}
		if !strings.Contains(email.Body, "one-time code below") {
			t.Fatalf("email body is not the FactorCode template: %s", email.Body)
		}

		var cached string
		found, err := rc.Cache.Get(ctx, authFactorCodePrefix+factorID+":code", &cached)
		if err != nil || !found || len(cached) != 6 {
			t.Fatalf("cached code not stored (found=%v err=%v code=%q)", found, err, cached)
		}
		if !strings.Contains(email.Body, cached) {
			t.Fatalf("email body %q does not contain the stored code %q", email.Body, cached)
		}
	})

	t.Run("ring failure leaves no code stored", func(t *testing.T) {
		ring := &fakeRing{failEmail: errors.New("ring down")}
		h, rc := newFactorHandler(t, ctx, pool, ring)
		accountID, factorID := seedFactorAccount(t, ctx, pool, model.AuthFactorTypeEmailCode, true)
		account, _ := h.d.Store.GetAccountByID(ctx, uuid.MustParse(accountID))
		factor, _ := h.d.Store.GetAuthFactorByID(ctx, accountID, uuid.MustParse(factorID))
		challenge := &model.AuthChallenge{Id: uuid.NewString(), AccountId: accountID}

		if err := h.sendFactorCode(ctx, account, factor, challenge); err == nil {
			t.Fatal("sendFactorCode succeeded, want error on ring failure")
		}
		var cached string
		if found, _ := rc.Cache.Get(ctx, authFactorCodePrefix+factorID+":code", &cached); found {
			t.Fatalf("code stored despite failed send: %q", cached)
		}
	})

	t.Run("missing verified contact stores nothing", func(t *testing.T) {
		ring := &fakeRing{}
		h, rc := newFactorHandler(t, ctx, pool, ring)
		accountID, factorID := seedFactorAccount(t, ctx, pool, model.AuthFactorTypeEmailCode, false)
		account, _ := h.d.Store.GetAccountByID(ctx, uuid.MustParse(accountID))
		factor, _ := h.d.Store.GetAuthFactorByID(ctx, accountID, uuid.MustParse(factorID))
		challenge := &model.AuthChallenge{Id: uuid.NewString(), AccountId: accountID}

		if err := h.sendFactorCode(ctx, account, factor, challenge); err != nil {
			t.Fatalf("missing contact should not fail the request (mirrors C#): %v", err)
		}
		if len(ring.emails) != 0 {
			t.Fatalf("email sent without a contact: %+v", ring.emails)
		}
		var cached string
		if found, _ := rc.Cache.Get(ctx, authFactorCodePrefix+factorID+":code", &cached); found {
			t.Fatalf("code stored without delivery: %q", cached)
		}
	})
}

// TestCreateChallengePublishesPendingPrompt pins the replacement for the
// in-app notification factor: creating a challenge now publishes the
// approval prompt to the account's other devices (Ring auth.login_attempt)
// and leaves the challenge pending, so a trusted session can approve it. An
// in-app-only account — which has no pickable factor left — must still be
// able to start a challenge this way instead of being locked out.
func TestCreateChallengePublishesPendingPrompt(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), smokeDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	ctx := context.Background()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}

	ring := &fakeRing{}
	h := &handler{d: Deps{
		Store:   store.New(pool),
		Geo:     &geo.Service{},
		Cfg:     &config.Config{},
		Clients: &grpcclient.Clients{Ring: ring},
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}
	accountID, _ := seedFactorAccount(t, ctx, pool, model.AuthFactorTypeInAppCode, false)
	account, err := h.d.Store.GetAccountByID(ctx, uuid.MustParse(accountID))
	if err != nil {
		t.Fatalf("load account: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/auth/challenge", h.createChallenge)

	body, _ := json.Marshal(map[string]any{
		"account":     account.Name,
		"device_id":   "pending-prompt-device",
		"device_name": "Pending Prompt Test",
		"platform":    int(model.ClientPlatformIos),
	})
	req := httptest.NewRequest(http.MethodPost, "/auth/challenge", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("create challenge status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var created model.AuthChallenge
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode challenge: %v", err)
	}
	if created.StepRemain != 1 {
		t.Fatalf("StepRemain = %d, want 1 (in-app-only account)", created.StepRemain)
	}
	if len(ring.pushes) != 1 || ring.pushes[0].Topic != "auth.login_attempt" {
		t.Fatalf("pending prompt not pushed as auth.login_attempt: %+v", ring.pushes)
	}

	// The prompt Device B acts on comes from the pending-challenge endpoint.
	pending, err := h.d.Store.ListPendingChallenges(ctx, accountID)
	if err != nil {
		t.Fatalf("list pending challenges: %v", err)
	}
	if len(pending) != 1 || pending[0].Id != created.Id {
		t.Fatalf("pending challenges = %+v, want the created challenge %s", pending, created.Id)
	}
}

// TestRequestFactorCodeRejectsInAppCode pins the removal of the in-app
// notification factor's code path: POST /auth/challenge/{id}/factors/{id} is
// the only way to request a factor code, and for the in-app factor it must
// fail with AUTH_FACTOR_NOT_SUPPORTED instead of publishing an approval
// prompt — the prompt is already sent when the challenge is created.
func TestRequestFactorCodeRejectsInAppCode(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), smokeDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	ctx := context.Background()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}

	ring := &fakeRing{}
	h := &handler{d: Deps{
		Store:   store.New(pool),
		Clients: &grpcclient.Clients{Ring: ring},
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}
	accountID, factorID := seedFactorAccount(t, ctx, pool, model.AuthFactorTypeInAppCode, false)

	now := time.Now().UTC()
	challenge := &model.AuthChallenge{
		Id:               uuid.NewString(),
		AccountId:        accountID,
		DeviceId:         "reject-in-app-device",
		StepTotal:        1,
		StepRemain:       1,
		BlacklistFactors: []string{},
		Audiences:        []string{},
		Scopes:           []string{},
		ExpiredAt:        model.NewTime(now.Add(10 * time.Minute)),
		CreatedAt:        model.NewTime(now),
		UpdatedAt:        model.NewTime(now),
	}
	if err := h.d.Store.CreateAuthChallenge(ctx, challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/auth/challenge/:id/factors/:factorId", h.requestFactorCode)
	req := httptest.NewRequest(http.MethodPost,
		"/auth/challenge/"+challenge.Id+"/factors/"+factorID, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "AUTH_FACTOR_NOT_SUPPORTED") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
	if len(ring.pushes) != 0 {
		t.Fatalf("pushes sent for a rejected in-app request: %+v", ring.pushes)
	}
	if len(ring.emails) != 0 {
		t.Fatalf("emails sent for a rejected in-app request: %+v", ring.emails)
	}
}
