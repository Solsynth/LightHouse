package spell

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// smokeDSN mirrors config.example.toml.
const smokeDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

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

	// Seed an account, the `verified` group, an unverified email contact and
	// a contact-verification spell.
	accountID := uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, "activation_smoke_"+uuid.NewString()[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	defer pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, accountID)

	var groupID string
	err = pool.QueryRow(ctx, `SELECT id FROM permission_groups WHERE "key" = 'verified' AND deleted_at IS NULL`).Scan(&groupID)
	if err != nil {
		groupID = uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO permission_groups (id, "key", created_at, updated_at)
			VALUES ($1, 'verified', now(), now())`, groupID); err != nil {
			t.Fatalf("seed verified group: %v", err)
		}
	}
	defer pool.Exec(ctx, `DELETE FROM permission_group_members WHERE actor = $1`, accountID)

	contactID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO account_contacts (id, account_id, content, created_at, is_primary, is_public, type, updated_at)
		VALUES ($1, $2, $3, now(), true, false, $4, now())`,
		contactID, accountID, "activation@example.com", int(model.ContactTypeEmail)); err != nil {
		t.Fatalf("seed contact: %v", err)
	}

	seedSpell := func() *model.MagicSpell {
		spell := &model.MagicSpell{
			Id:        uuid.NewString(),
			Spell:     "word-" + uuid.NewString(),
			Type:      model.MagicSpellTypeContactVerification,
			Meta:      map[string]any{"contact_id": contactID, "contact_method": "activation@example.com"},
			AccountId: accountID,
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
	defer pool.Exec(ctx, `DELETE FROM magic_spells WHERE account_id = $1`, accountID)

	st := store.New(pool)
	svc := NewService(st, nil, nil, "", nil)

	// Apply the spell: contact verified + account activated in one step.
	if err := svc.ApplyMagicSpell(ctx, seedSpell()); err != nil {
		t.Fatalf("apply contact verification spell: %v", err)
	}

	var verifiedAt, activatedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT verified_at FROM account_contacts WHERE id = $1`, contactID).Scan(&verifiedAt); err != nil {
		t.Fatalf("load contact: %v", err)
	}
	if verifiedAt == nil {
		t.Fatal("contact was not verified by spell application")
	}
	if err := pool.QueryRow(ctx, `SELECT activated_at FROM accounts WHERE id = $1`, accountID).Scan(&activatedAt); err != nil {
		t.Fatalf("load account: %v", err)
	}
	if activatedAt == nil {
		t.Fatal("account was not activated after contact verification")
	}

	var member bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM permission_group_members WHERE group_id = $1 AND actor = $2 AND deleted_at IS NULL)`,
		groupID, accountID).Scan(&member); err != nil {
		t.Fatalf("check verified membership: %v", err)
	}
	if !member {
		t.Fatal("account was not added to the verified group")
	}

	var spellCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM magic_spells WHERE account_id = $1`, accountID).Scan(&spellCount); err != nil {
		t.Fatalf("count spells: %v", err)
	}
	if spellCount != 0 {
		t.Fatalf("applied spell was not consumed, %d spells remain", spellCount)
	}

	// Re-applying a fresh spell is idempotent: no error, activated_at and
	// group membership unchanged.
	if err := svc.ApplyMagicSpell(ctx, seedSpell()); err != nil {
		t.Fatalf("re-apply contact verification spell: %v", err)
	}
	var again *time.Time
	if err := pool.QueryRow(ctx, `SELECT activated_at FROM accounts WHERE id = $1`, accountID).Scan(&again); err != nil {
		t.Fatalf("reload account: %v", err)
	}
	if again == nil || !again.Equal(*activatedAt) {
		t.Fatalf("activated_at moved on re-apply: before %v, after %v", activatedAt, again)
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
