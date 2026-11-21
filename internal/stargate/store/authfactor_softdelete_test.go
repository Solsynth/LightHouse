package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/model"
)

// TestAuthFactorSoftDeleteSemantics pins the Port's soft-delete contract for
// auth factors, which the C# service got from EF's global
// `DeletedAt == null` query filter. Stargate dropped that filter on several
// factor queries, so an admin-deleted factor stayed listed, stayed
// re-enableable, stayed usable as a challenge step and kept blocking the
// account from creating a fresh factor of the same type.
func TestAuthFactorSoftDeleteSemantics(t *testing.T) {
	pool, st := patchStore(t)
	defer pool.Close()
	ctx := context.Background()

	now := time.Now().UTC()
	accountID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, "factor_softdelete_"+uuid.NewString()[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	defer pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, accountID)

	factor, err := st.InsertAuthFactor(ctx, &model.AuthFactor{
		Type:        model.AuthFactorTypePasskey,
		Trustworthy: 4,
		AccountId:   accountID,
		EnabledAt:   model.NewTime(now),
	})
	if err != nil {
		t.Fatalf("insert factor: %v", err)
	}
	factorID := uuid.MustParse(factor.Id)

	// Baseline: the live factor is visible to every read path.
	if list, err := st.ListAllFactors(ctx, uuid.MustParse(accountID)); err != nil || len(list) != 1 {
		t.Fatalf("ListAllFactors before delete = %d, %v; want 1, nil", len(list), err)
	}
	if _, err := st.GetAuthFactorByID(ctx, accountID, factorID); err != nil {
		t.Fatalf("GetAuthFactorByID before delete: %v", err)
	}
	if ok, err := st.CheckAuthFactorExists(ctx, accountID, model.AuthFactorTypePasskey); err != nil || !ok {
		t.Fatalf("CheckAuthFactorExists before delete = %v, %v; want true, nil", ok, err)
	}

	if err := st.AdminDeleteAuthFactor(ctx, uuid.MustParse(accountID), factorID); err != nil {
		t.Fatalf("AdminDeleteAuthFactor: %v", err)
	}

	// A soft-deleted factor must disappear from the account's own list: the
	// console renders every row it receives, so a tombstone shows up as a
	// live factor the account cannot get rid of.
	if list, err := st.ListAllFactors(ctx, uuid.MustParse(accountID)); err != nil || len(list) != 0 {
		t.Fatalf("ListAllFactors after delete = %d, %v; want 0, nil", len(list), err)
	}

	// Deleting, enabling or using a tombstone must not be possible.
	if _, err := st.GetAuthFactorByID(ctx, accountID, factorID); err != ErrNotFound {
		t.Fatalf("GetAuthFactorByID after delete err = %v; want ErrNotFound", err)
	}
	if ok, err := st.CheckAuthFactorExists(ctx, accountID, model.AuthFactorTypePasskey); err != nil || ok {
		t.Fatalf("CheckAuthFactorExists after delete = %v, %v; want false, nil", ok, err)
	}

	// Re-enabling the stale model must not resurrect the row: the UPDATE is
	// scoped to live rows, so it finds nothing and leaves the tombstone alone.
	var before *time.Time
	if err := pool.QueryRow(ctx, `SELECT enabled_at FROM account_auth_factors WHERE id = $1`, factorID).
		Scan(&before); err != nil {
		t.Fatalf("read enabled_at: %v", err)
	}
	stale := *factor
	stale.EnabledAt = model.NewTime(now.Add(48 * time.Hour))
	if err := st.UpdateAuthFactor(ctx, &stale); err != ErrNotFound {
		t.Fatalf("UpdateAuthFactor on tombstone err = %v; want ErrNotFound", err)
	}
	var deletedAt *time.Time
	var enabledAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT deleted_at, enabled_at FROM account_auth_factors WHERE id = $1`, factorID).
		Scan(&deletedAt, &enabledAt); err != nil {
		t.Fatalf("read factor row: %v", err)
	}
	if deletedAt == nil {
		t.Fatal("tombstone lost its deleted_at")
	}
	if !equalTime(enabledAt, before) {
		t.Fatalf("tombstone enabled_at = %v, want %v (re-enable must not write)", enabledAt, before)
	}

	// The account must be able to create a fresh factor of the same type.
	recreated, err := st.InsertAuthFactor(ctx, &model.AuthFactor{
		Type:        model.AuthFactorTypePasskey,
		Trustworthy: 4,
		AccountId:   accountID,
		EnabledAt:   model.NewTime(now),
	})
	if err != nil {
		t.Fatalf("re-create factor after delete: %v", err)
	}
	if list, err := st.ListAllFactors(ctx, uuid.MustParse(accountID)); err != nil || len(list) != 1 {
		t.Fatalf("ListAllFactors after re-create = %d, %v; want 1, nil", len(list), err)
	}
	if recreated.Id == factor.Id {
		t.Fatalf("re-created factor reused the deleted id %s", recreated.Id)
	}
}

// TestPasskeySoftDeleteSemantics pins the same contract for account_passkeys:
// Padlock soft-deleted these rows through EF, so a revoked credential must not
// be listed, renamed, counter-updated or offered as an allow-credential.
func TestPasskeySoftDeleteSemantics(t *testing.T) {
	pool, st := patchStore(t)
	defer pool.Close()
	ctx := context.Background()

	now := time.Now().UTC()
	accountID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, "passkey_softdelete_"+uuid.NewString()[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	defer pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, accountID)

	credentialID := "softdelete-" + uuid.NewString()
	passkey, err := st.InsertPasskey(ctx, &model.Passkey{
		AccountId:    accountID,
		Label:        "soft-delete probe",
		CredentialId: credentialID,
		Credential:   "{}",
	})
	if err != nil {
		t.Fatalf("insert passkey: %v", err)
	}
	passkeyID := uuid.MustParse(passkey.Id)

	if list, err := st.ListPasskeys(ctx, accountID); err != nil || len(list) != 1 {
		t.Fatalf("ListPasskeys before delete = %d, %v; want 1, nil", len(list), err)
	}

	// EF Remove semantics: Postgres keeps the row, EF stamps deleted_at.
	if _, err := pool.Exec(ctx, `UPDATE account_passkeys SET deleted_at = $2 WHERE id = $1`, passkeyID, now); err != nil {
		t.Fatalf("soft-delete passkey: %v", err)
	}

	if list, err := st.ListPasskeys(ctx, accountID); err != nil || len(list) != 0 {
		t.Fatalf("ListPasskeys after delete = %d, %v; want 0, nil", len(list), err)
	}
	if _, err := st.GetPasskeyByID(ctx, accountID, passkeyID); err != ErrNotFound {
		t.Fatalf("GetPasskeyByID after delete err = %v; want ErrNotFound", err)
	}
	if _, err := st.UpdatePasskeyLabel(ctx, passkeyID, "renamed"); err != ErrNotFound {
		t.Fatalf("UpdatePasskeyLabel after delete err = %v; want ErrNotFound", err)
	}
	if _, err := st.UpdatePasskeyCredential(ctx, passkeyID, `{"Counter":7}`); err != ErrNotFound {
		t.Fatalf("UpdatePasskeyCredential after delete err = %v; want ErrNotFound", err)
	}
	if err := st.DeletePasskeyRow(ctx, passkeyID); err != ErrNotFound {
		t.Fatalf("DeletePasskeyRow after delete err = %v; want ErrNotFound", err)
	}

	// The credential id must be free for a fresh registration: the partial
	// unique index only covers live rows.
	if _, err := st.InsertPasskey(ctx, &model.Passkey{
		AccountId:    accountID,
		Label:        "re-registered",
		CredentialId: credentialID,
		Credential:   "{}",
	}); err != nil {
		t.Fatalf("re-register passkey after delete: %v", err)
	}
}

// equalTime reports whether two nullable timestamps denote the same instant.
func equalTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
