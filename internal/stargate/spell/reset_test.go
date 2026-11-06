package spell

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/auth"
	"src.solsynth.dev/sosys/stargate/config"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/store"
)

// recordingRevoker records the accounts whose sessions a reset revoked.
type recordingRevoker struct {
	accounts []string
}

func (r *recordingRevoker) RevokeAllSessionsForAccount(_ context.Context, accountID string) (int, error) {
	r.accounts = append(r.accounts, accountID)
	return 1, nil
}

// seedResetFixture creates an account without any password factor and a live
// password-reset spell for it.
func seedResetFixture(t *testing.T, pool *pgxpool.Pool, ctx context.Context) (string, *model.MagicSpell) {
	t.Helper()
	accountID := uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, "reset_smoke_"+uuid.NewString()[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM account_auth_factors WHERE account_id = $1`, accountID)
		pool.Exec(ctx, `DELETE FROM magic_spells WHERE account_id = $1`, accountID)
		pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, accountID)
	})
	spell := &model.MagicSpell{
		Id:        uuid.NewString(),
		Spell:     "reset-" + uuid.NewString(),
		Type:      model.MagicSpellTypeAuthPasswordReset,
		Meta:      map[string]any{},
		AccountId: accountID,
		ExpiresAt: model.NewTime(now.Add(time.Hour)),
		CreatedAt: model.NewTime(now),
		UpdatedAt: model.NewTime(now),
	}
	if _, err := pool.Exec(ctx, `INSERT INTO magic_spells
		(id, spell, type, expires_at, affected_at, meta, account_id, created_at, updated_at, deleted_at)
		VALUES ($1, $2, $3, $4, NULL, $5, $6, $7, $8, NULL)`,
		spell.Id, spell.Spell, int(spell.Type), spell.ExpiresAt.Time(), mustJSON(t, spell.Meta),
		spell.AccountId, spell.CreatedAt, spell.UpdatedAt); err != nil {
		t.Fatalf("seed reset spell: %v", err)
	}
	return accountID, spell
}

func newResetService(t *testing.T, pool *pgxpool.Pool) (*Service, *store.Store, *recordingRevoker) {
	t.Helper()
	st := store.New(pool)
	svc := NewService(st, nil, nil, "", config.Default(), nil)
	revoker := &recordingRevoker{}
	svc.SetSessionRevoker(revoker)
	return svc, st, revoker
}

// TestApplyPasswordResetCreatesEnabledPassword pins the create-or-reset
// semantics: a reset on an account that never had a password installs an
// enabled, verifiable password factor, consumes the spell and revokes every
// session that predates it.
func TestApplyPasswordResetCreatesEnabledPassword(t *testing.T) {
	pool := resetPool(t)
	ctx := context.Background()
	accountID, spell := seedResetFixture(t, pool, ctx)
	svc, st, revoker := newResetService(t, pool)

	if err := svc.ApplyPasswordReset(ctx, spell, "brand-new-password"); err != nil {
		t.Fatalf("apply reset: %v", err)
	}

	factor, err := st.GetEnabledPasswordFactor(ctx, accountID)
	if err != nil {
		t.Fatalf("load password factor: %v", err)
	}
	if factor.EnabledAt == nil {
		t.Fatal("reset installed a disabled password factor")
	}
	ok, err := auth.VerifyFactorPassword(factor, "brand-new-password")
	if err != nil || !ok {
		t.Fatalf("new password does not verify: ok=%v err=%v", ok, err)
	}
	if len(revoker.accounts) != 1 || revoker.accounts[0] != accountID {
		t.Fatalf("sessions not revoked for %s: %v", accountID, revoker.accounts)
	}
	assertSpellCount(t, pool, ctx, accountID, 0)
}

// TestApplyPasswordResetRejectsShortPassword: the reset honours the
// registration length rule and leaves the spell usable, so the page can show
// the error instead of burning the link.
func TestApplyPasswordResetRejectsShortPassword(t *testing.T) {
	pool := resetPool(t)
	ctx := context.Background()
	_, spell := seedResetFixture(t, pool, ctx)
	svc, st, revoker := newResetService(t, pool)

	if err := svc.ApplyPasswordReset(ctx, spell, "abc"); err == nil {
		t.Fatal("short password was accepted")
	}
	if _, err := st.GetEnabledPasswordFactor(ctx, spell.AccountId); err == nil {
		t.Fatal("a rejected reset still installed a password")
	}
	if len(revoker.accounts) != 0 {
		t.Fatalf("a rejected reset revoked sessions: %v", revoker.accounts)
	}
	assertSpellCount(t, pool, ctx, spell.AccountId, 1)
}

// TestApplyPasswordResetRotatesExistingPassword pins that an existing
// password is replaced rather than duplicated, and the old secret stops
// working.
func TestApplyPasswordResetRotatesExistingPassword(t *testing.T) {
	pool := resetPool(t)
	ctx := context.Background()
	accountID, spell := seedResetFixture(t, pool, ctx)
	svc, st, _ := newResetService(t, pool)

	oldHash, err := auth.HashPassword("old-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO account_auth_factors
		(id, account_id, type, secret, trustworthy, enabled_at, created_at, updated_at, deleted_at)
		VALUES ($1, $2, $3, $4, 1, now(), now(), now(), NULL)`,
		uuid.NewString(), accountID, int(model.AuthFactorTypePassword), oldHash); err != nil {
		t.Fatalf("seed password factor: %v", err)
	}

	if err := svc.ApplyPasswordReset(ctx, spell, "rotated-password-456"); err != nil {
		t.Fatalf("apply reset: %v", err)
	}
	factor, err := st.GetEnabledPasswordFactor(ctx, accountID)
	if err != nil {
		t.Fatalf("load password factor: %v", err)
	}
	if ok, _ := auth.VerifyFactorPassword(factor, "rotated-password-456"); !ok {
		t.Fatal("rotated password does not verify")
	}
	if ok, _ := auth.VerifyFactorPassword(factor, "old-password-123"); ok {
		t.Fatal("old password still verifies after reset")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM account_auth_factors WHERE account_id = $1 AND type = $2`,
		accountID, int(model.AuthFactorTypePassword)).Scan(&count); err != nil {
		t.Fatalf("count password factors: %v", err)
	}
	if count != 1 {
		t.Fatalf("password factors = %d, want 1", count)
	}
}

func resetPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), smokeDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	return pool
}

func assertSpellCount(t *testing.T, pool *pgxpool.Pool, ctx context.Context, accountID string, want int) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM magic_spells WHERE account_id = $1`, accountID).Scan(&count); err != nil {
		t.Fatalf("count spells: %v", err)
	}
	if count != want {
		t.Fatalf("spells = %d, want %d", count, want)
	}
}
