package spell

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// smokeDSN mirrors config.example.toml.
const smokeDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

// activationFixture seeds an account, the `verified` group, an unverified
// email contact and a contact-verification spell.
type activationFixture struct {
	accountID string
	contactID string
	groupID   string
}

func seedActivationFixture(t *testing.T, pool *pgxpool.Pool, ctx context.Context) activationFixture {
	t.Helper()
	accountID := uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, "activation_smoke_"+uuid.NewString()[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, accountID) })

	var groupID string
	err := pool.QueryRow(ctx, `SELECT id FROM permission_groups WHERE "key" = 'verified' AND deleted_at IS NULL`).Scan(&groupID)
	if err != nil {
		groupID = uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO permission_groups (id, "key", created_at, updated_at)
			VALUES ($1, 'verified', now(), now())`, groupID); err != nil {
			t.Fatalf("seed verified group: %v", err)
		}
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM permission_group_members WHERE actor = $1`, accountID) })

	contactID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO account_contacts (id, account_id, content, created_at, is_primary, is_public, type, updated_at)
		VALUES ($1, $2, $3, now(), true, false, $4, now())`,
		contactID, accountID, "activation@example.com", int(model.ContactTypeEmail)); err != nil {
		t.Fatalf("seed contact: %v", err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM magic_spells WHERE account_id = $1`, accountID) })

	return activationFixture{accountID: accountID, contactID: contactID, groupID: groupID}
}

func seedContactVerificationSpell(t *testing.T, pool *pgxpool.Pool, ctx context.Context, fx activationFixture) *model.MagicSpell {
	t.Helper()
	now := time.Now().UTC()
	spell := &model.MagicSpell{
		Id:        uuid.NewString(),
		Spell:     "word-" + uuid.NewString(),
		Type:      model.MagicSpellTypeContactVerification,
		Meta:      map[string]any{"contact_id": fx.contactID, "contact_method": "activation@example.com"},
		AccountId: fx.accountID,
		CreatedAt: model.NewTime(now),
		UpdatedAt: model.NewTime(now),
	}
	if _, err := pool.Exec(ctx, `INSERT INTO magic_spells
		(id, spell, type, expires_at, affected_at, meta, account_id, created_at, updated_at, deleted_at)
		VALUES ($1, $2, $3, NULL, NULL, $4, $5, $6, $7, NULL)`,
		spell.Id, spell.Spell, int(spell.Type), mustJSON(t, spell.Meta), spell.AccountId,
		spell.CreatedAt, spell.UpdatedAt); err != nil {
		t.Fatalf("seed spell: %v", err)
	}
	return spell
}

func activationState(t *testing.T, pool *pgxpool.Pool, ctx context.Context, fx activationFixture) (verifiedAt, activatedAt *time.Time, member bool) {
	t.Helper()
	if err := pool.QueryRow(ctx, `SELECT verified_at FROM account_contacts WHERE id = $1`, fx.contactID).Scan(&verifiedAt); err != nil {
		t.Fatalf("load contact: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT activated_at FROM accounts WHERE id = $1`, fx.accountID).Scan(&activatedAt); err != nil {
		t.Fatalf("load account: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM permission_group_members WHERE group_id = $1 AND actor = $2 AND deleted_at IS NULL)`,
		fx.groupID, fx.accountID).Scan(&member); err != nil {
		t.Fatalf("check verified membership: %v", err)
	}
	return verifiedAt, activatedAt, member
}

// TestApplyContactVerificationSpellActivatesAccount pins the onboarding
// contract ported from Passport's tests-disabled branch of
// TestService.TryActivateAfterContactVerification: applying a
// contact-verification spell verifies the contact AND activates the account
// (sets activated_at, grants the `verified` group, consumes the spell).
func TestApplyContactVerificationSpellActivatesAccount(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), smokeDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	ctx = context.Background()

	fx := seedActivationFixture(t, pool, ctx)
	st := store.New(pool)
	svc := NewService(st, nil, nil, "", config.Default(), nil)

	// Apply the spell: contact verified + account activated in one step.
	if err := svc.ApplyMagicSpell(ctx, seedContactVerificationSpell(t, pool, ctx, fx)); err != nil {
		t.Fatalf("apply contact verification spell: %v", err)
	}

	verifiedAt, activatedAt, member := activationState(t, pool, ctx, fx)
	if verifiedAt == nil {
		t.Fatal("contact was not verified by spell application")
	}
	if activatedAt == nil {
		t.Fatal("account was not activated after contact verification")
	}
	if !member {
		t.Fatal("account was not added to the verified group")
	}

	var spellCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM magic_spells WHERE account_id = $1`, fx.accountID).Scan(&spellCount); err != nil {
		t.Fatalf("count spells: %v", err)
	}
	if spellCount != 0 {
		t.Fatalf("applied spell was not consumed, %d spells remain", spellCount)
	}

	// Re-applying a fresh spell is idempotent: no error, activated_at and
	// group membership unchanged.
	if err := svc.ApplyMagicSpell(ctx, seedContactVerificationSpell(t, pool, ctx, fx)); err != nil {
		t.Fatalf("re-apply contact verification spell: %v", err)
	}
	_, again, _ := activationState(t, pool, ctx, fx)
	if again == nil || !again.Equal(*activatedAt) {
		t.Fatalf("activated_at moved on re-apply: before %v, after %v", activatedAt, again)
	}
}

// TestContactVerificationDefersActivationWhenTestsRequired pins the
// tests-enabled branch of TryActivateAfterContactVerification: when
// [accountActivation] requires entry tests, applying the spell verifies the
// contact but must NOT activate the account — Passport evaluates the
// attempts and publishes accounts.activated, which Stargate consumes.
func TestContactVerificationDefersActivationWhenTestsRequired(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), smokeDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	ctx = context.Background()

	fx := seedActivationFixture(t, pool, ctx)
	cfg := config.Default()
	cfg.AccountActivation.TestsEnabled = true
	cfg.AccountActivation.RequiredTestKeys = []string{"platform-entry"}
	st := store.New(pool)
	svc := NewService(st, nil, nil, "", cfg, nil)

	if err := svc.ApplyMagicSpell(ctx, seedContactVerificationSpell(t, pool, ctx, fx)); err != nil {
		t.Fatalf("apply contact verification spell: %v", err)
	}

	verifiedAt, activatedAt, member := activationState(t, pool, ctx, fx)
	if verifiedAt == nil {
		t.Fatal("contact was not verified by spell application")
	}
	if activatedAt != nil {
		t.Fatalf("account was activated although entry tests are required: %v", activatedAt)
	}
	if member {
		t.Fatal("account was added to the verified group although entry tests are required")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	return b
}
