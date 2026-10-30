package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

const profilePatchDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

func patchStore(t *testing.T) (*pgxpool.Pool, *Store) {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), profilePatchDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("postgres unavailable: %v", err)
	}
	return pool, New(pool)
}

// seedProfileAccount inserts an account plus a profile carrying the given
// active_badge and verification jsonb payloads (raw, may be NULL).
func seedProfileAccount(t *testing.T, pool *pgxpool.Pool, badge, verification string) string {
	t.Helper()
	ctx := context.Background()
	accountID := uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, "patch_"+uuid.NewString()[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO account_profiles (id, account_id, active_badge, verification, experience, social_credits, created_at, updated_at)
		VALUES ($1, $2, $3::jsonb, $4::jsonb, 10, 100, $5, $5)`,
		uuid.NewString(), accountID, badge, verification, now); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	return accountID
}

// readProfileColumns returns the raw active_badge and verification jsonb
// bytes for an account (nil when the column is NULL).
func readProfileColumns(t *testing.T, pool *pgxpool.Pool, accountID string) (badge, verification []byte) {
	t.Helper()
	if err := pool.QueryRow(context.Background(),
		`SELECT active_badge, verification FROM account_profiles WHERE account_id = $1`, accountID).
		Scan(&badge, &verification); err != nil {
		t.Fatalf("read profile columns: %v", err)
	}
	return badge, verification
}

// TestApplyProfileFieldPatchPreservesBadgeAndVerification is the regression
// for the purge: ApplyProfileFieldPatch used to load the row and rewrite every
// profile column from that snapshot, so a routine XP/last-seen patch racing a
// verification or badge write NULLed the fields the snapshot had not seen.
// The patch must only touch the columns it carries.
func TestApplyProfileFieldPatchPreservesBadgeAndVerification(t *testing.T) {
	pool, s := patchStore(t)
	defer pool.Close()
	ctx := context.Background()

	accountID := seedProfileAccount(t, pool,
		`{"id": "badge-1", "type": "pioneer", "label": "Pioneer", "meta": {}}`,
		`{"type": 2, "title": "Verified", "verified_by": "admin"}`)
	defer pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, accountID)

	// XP-only patch (the highest-frequency profile_updated event) must leave
	// verification and active_badge untouched.
	experience := 25
	if err := s.ApplyProfileFieldPatch(ctx, uuid.MustParse(accountID), &ProfileFieldPatch{
		Experience: &experience,
	}); err != nil {
		t.Fatalf("apply XP patch: %v", err)
	}
	badge, verification := readProfileColumns(t, pool, accountID)
	if len(badge) == 0 {
		t.Fatal("active_badge NULLed by XP-only patch")
	}
	if len(verification) == 0 {
		t.Fatal("verification NULLed by XP-only patch")
	}
	var mark struct {
		Type int `json:"type"`
	}
	if err := json.Unmarshal(verification, &mark); err != nil || mark.Type != 2 {
		t.Fatalf("verification = %s, want type 2", verification)
	}
	var gotExp int
	if err := pool.QueryRow(ctx, `SELECT experience FROM account_profiles WHERE account_id = $1`, accountID).Scan(&gotExp); err != nil {
		t.Fatalf("read experience: %v", err)
	}
	if gotExp != 25 {
		t.Fatalf("experience = %d, want 25", gotExp)
	}

	// Explicit JSON null still clears (the Passport "null to clear" contract),
	// and clearing verification must not touch active_badge.
	if err := s.ApplyProfileFieldPatch(ctx, uuid.MustParse(accountID), &ProfileFieldPatch{
		HasVerification: true,
	}); err != nil {
		t.Fatalf("apply verification clear: %v", err)
	}
	badge, verification = readProfileColumns(t, pool, accountID)
	if len(verification) != 0 {
		t.Fatalf("verification = %s, want cleared", verification)
	}
	if len(badge) == 0 {
		t.Fatal("active_badge NULLed by verification-clear patch")
	}

	// A badge replacement must not resurrect/clear verification.
	badgeValue := map[string]any{"id": "badge-2", "type": "founder"}
	if err := s.ApplyProfileFieldPatch(ctx, uuid.MustParse(accountID), &ProfileFieldPatch{
		HasActiveBadge: true,
		ActiveBadge:    badgeValue,
	}); err != nil {
		t.Fatalf("apply badge set: %v", err)
	}
	badge, _ = readProfileColumns(t, pool, accountID)
	var ref map[string]any
	if err := json.Unmarshal(badge, &ref); err != nil || ref["id"] != "badge-2" {
		t.Fatalf("active_badge = %s, want badge-2", badge)
	}
}

// TestSaveProfileDoesNotClobberUnsetColumns guards the second purge vector:
// SaveProfile used to rewrite every profile column from its read snapshot, so
// a user/admin profile edit whose snapshot predated an admin-verification or
// badge commit NULLed those columns. Pointer columns are now written only when
// set; a profile loaded before the commit and saved after must leave
// verification/active_badge untouched.
func TestSaveProfileDoesNotClobberUnsetColumns(t *testing.T) {
	pool, s := patchStore(t)
	defer pool.Close()
	ctx := context.Background()

	accountID := seedProfileAccount(t, pool,
		`{"id": "badge-1", "type": "pioneer", "label": "Pioneer", "meta": {}}`,
		`{"type": 2, "title": "Verified", "verified_by": "admin"}`)
	defer pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, accountID)
	id := uuid.MustParse(accountID)

	// A caller that loaded the profile before the badge/verification were set
	// carries them as nil in its snapshot.
	profile, err := s.GetProfileByAccount(ctx, id)
	if err != nil {
		t.Fatalf("load profile: %v", err)
	}
	profile.Verification = nil
	profile.ActiveBadge = nil
	firstName := "Renamed"
	profile.FirstName = &firstName
	if err := s.SaveProfile(ctx, profile); err != nil {
		t.Fatalf("save profile: %v", err)
	}

	badge, verification := readProfileColumns(t, pool, accountID)
	if len(verification) == 0 {
		t.Fatal("SaveProfile NULLed verification not present in its snapshot")
	}
	if len(badge) == 0 {
		t.Fatal("SaveProfile NULLed active_badge not present in its snapshot")
	}
	var gotFirstName *string
	if err := pool.QueryRow(ctx, `SELECT first_name FROM account_profiles WHERE account_id = $1`, accountID).Scan(&gotFirstName); err != nil {
		t.Fatalf("read first_name: %v", err)
	}
	if gotFirstName == nil || *gotFirstName != "Renamed" {
		t.Fatalf("first_name = %v, want Renamed", gotFirstName)
	}

	// A caller that DID see the committed values round-trips them.
	profile, err = s.GetProfileByAccount(ctx, id)
	if err != nil {
		t.Fatalf("reload profile: %v", err)
	}
	if err := s.SaveProfile(ctx, profile); err != nil {
		t.Fatalf("save unchanged profile: %v", err)
	}
	badge, verification = readProfileColumns(t, pool, accountID)
	if len(verification) == 0 || len(badge) == 0 {
		t.Fatalf("round-trip dropped verification/badge: badge=%d verification=%d", len(badge), len(verification))
	}
}

// TestApplyProfileFieldPatchExperienceSemantics pins the absolute/delta
// experience handling and create-on-missing.
func TestApplyProfileFieldPatchExperienceSemantics(t *testing.T) {
	pool, s := patchStore(t)
	defer pool.Close()
	ctx := context.Background()

	accountID := seedProfileAccount(t, pool, "null", "null")
	defer pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, accountID)
	id := uuid.MustParse(accountID)

	readExp := func() int {
		t.Helper()
		var v int
		if err := pool.QueryRow(ctx, `SELECT experience FROM account_profiles WHERE account_id = $1`, accountID).Scan(&v); err != nil {
			t.Fatalf("read experience: %v", err)
		}
		return v
	}

	// Delta applies on top of the stored value.
	delta := 5
	if err := s.ApplyProfileFieldPatch(ctx, id, &ProfileFieldPatch{ExperienceDelta: &delta}); err != nil {
		t.Fatalf("apply delta: %v", err)
	}
	if got := readExp(); got != 15 {
		t.Fatalf("experience after delta = %d, want 15", got)
	}

	// Absolute replaces the stored value.
	absolute := 100
	if err := s.ApplyProfileFieldPatch(ctx, id, &ProfileFieldPatch{Experience: &absolute}); err != nil {
		t.Fatalf("apply absolute: %v", err)
	}
	if got := readExp(); got != 100 {
		t.Fatalf("experience after absolute = %d, want 100", got)
	}

	// A zero delta is a no-op, even when an absolute is absent.
	zero := 0
	if err := s.ApplyProfileFieldPatch(ctx, id, &ProfileFieldPatch{ExperienceDelta: &zero}); err != nil {
		t.Fatalf("apply zero delta: %v", err)
	}
	if got := readExp(); got != 100 {
		t.Fatalf("experience after zero delta = %d, want 100", got)
	}

	// Create-on-missing: an account without a profile row gets one.
	missingID := uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, missingID, "patch_missing_"+uuid.NewString()[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	defer pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, missingID)
	if err := s.ApplyProfileFieldPatch(ctx, uuid.MustParse(missingID), &ProfileFieldPatch{
		LastSeenAt:  &now,
		HasVerification: true,
		Verification: &model.SnVerificationMark{Type: 1},
	}); err != nil {
		t.Fatalf("apply patch to profile-less account: %v", err)
	}
	_, verification := readProfileColumns(t, pool, missingID)
	var mark struct {
		Type int `json:"type"`
	}
	if err := json.Unmarshal(verification, &mark); err != nil || mark.Type != 1 {
		t.Fatalf("verification = %s, want type 1 on created row", verification)
	}
}
