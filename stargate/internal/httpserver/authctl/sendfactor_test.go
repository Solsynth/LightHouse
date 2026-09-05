package authctl

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/config"
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

// TestSendFactorCodeInAppCodePublishesPrompt pins the InAppCode contract: the
// factor no longer generates/stores/steps a code. Instead it records a
// prompt_requested_at marker on the challenge and publishes the pending
// challenge to the trusted device (Ring auth.login_attempt + WS
// auth.challenge.pending). No authfactor:...:code Redis key is written. A
// second request while a prompt is outstanding is rejected without a new push.
func TestSendFactorCodeInAppCodePublishesPrompt(t *testing.T) {
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
	h, rc := newFactorHandler(t, ctx, pool, ring)
	accountID, factorID := seedFactorAccount(t, ctx, pool, model.AuthFactorTypeInAppCode, false)
	account, _ := h.d.Store.GetAccountByID(ctx, uuid.MustParse(accountID))
	factor, _ := h.d.Store.GetAuthFactorByID(ctx, accountID, uuid.MustParse(factorID))

	now := time.Now().UTC()
	challenge := &model.AuthChallenge{
		Id:               uuid.NewString(),
		AccountId:        accountID,
		StepTotal:        2,
		StepRemain:       2,
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

	if err := h.sendFactorCode(ctx, account, factor, challenge); err != nil {
		t.Fatalf("sendFactorCode: %v", err)
	}
	if len(ring.pushes) != 1 || ring.pushes[0].Topic != "auth.login_attempt" {
		t.Fatalf("push not delivered as auth.login_attempt: %+v", ring.pushes)
	}
	var cached string
	if found, _ := rc.Cache.Get(ctx, authFactorCodePrefix+factor.Id+":code", &cached); found {
		t.Fatalf("authfactor code key written for in-app prompt: %q", cached)
	}
	reloaded, err := h.d.Store.GetAuthChallenge(ctx, uuid.MustParse(challenge.Id))
	if err != nil {
		t.Fatalf("reload challenge: %v", err)
	}
	if reloaded.PromptRequestedAt == nil {
		t.Fatal("prompt_requested_at not persisted after prompt published")
	}

	// A second request while a prompt is outstanding must be rejected without
	// publishing another push.
	if err := h.sendFactorCode(ctx, account, factor, challenge); err == nil {
		t.Fatal("second sendFactorCode succeeded, want error while prompt pending")
	} else if err.Error() != "An in-app login request is already pending." {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ring.pushes) != 1 {
		t.Fatalf("pushes after duplicate request = %d, want 1", len(ring.pushes))
	}
}
