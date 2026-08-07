package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const activeBadgeDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

// TestGetAccountWithProfileCarriesActiveBadge guards the HTTP identity path:
// GetAccountWithProfile (used by GET /api/accounts/me and the public account
// reads) must surface active_badge in the canonical snake_case ref shape the
// Island SDK strict-casts (account.g.dart). Regression: scanAccountWithProfile
// dropped the column, so the merged identity never carried the badge while
// the gRPC scan (scanProfile) did.
//
// The column holds two shapes — legacy C# EF rows (PascalCase partial refs)
// and NATS-synced refs (snake_case from Passport's accounts.profile_updated)
// — and both must normalize identically.
func TestGetAccountWithProfileCarriesActiveBadge(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), activeBadgeDSN)
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

	s := New(pool)
	now := time.Now().UTC()
	seed := func(badge string) string {
		id := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
			VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, id, "badge_"+uuid.NewString()[:8], now); err != nil {
			t.Fatalf("seed account: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO account_profiles (id, account_id, active_badge, created_at, updated_at, experience, social_credits)
			VALUES ($1, $2, $3::jsonb, $4, $4, 0, 100)`, uuid.NewString(), id, badge, now); err != nil {
			t.Fatalf("seed profile: %v", err)
		}
		return id
	}
	// Legacy C# EF rows store PascalCase partial refs.
	legacy := seed(`{"Id": "badge-legacy", "Type": "pioneer", "Label": "Pioneer"}`)
	// NATS-synced refs are snake_case full refs (Passport's
	// ProfileFieldUpdatedEvent via InfraObjectCoder).
	natsSynced := seed(`{"id": "badge-nats", "type": "pioneer", "label": "Pioneer", "meta": {"k": "v"}, "activated_at": "2026-08-07T02:33:00Z", "account_id": "00000000-0000-0000-0000-000000000000"}`)
	defer pool.Exec(ctx, `DELETE FROM accounts WHERE id = ANY($1)`, []string{legacy, natsSynced})

	for _, tc := range []struct {
		name      string
		id        string
		wantBadge string
	}{
		{name: "legacy pascalcase", id: legacy, wantBadge: "badge-legacy"},
		{name: "nats snake_case", id: natsSynced, wantBadge: "badge-nats"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			acct, err := s.GetAccountWithProfile(ctx, uuid.MustParse(tc.id))
			if err != nil {
				t.Fatalf("GetAccountWithProfile: %v", err)
			}
			if acct.Profile == nil || acct.Profile.ActiveBadge == nil {
				t.Fatalf("account %s profile active_badge not carried: %+v", tc.id, acct.Profile)
			}
			ref, ok := (*acct.Profile.ActiveBadge).(map[string]any)
			if !ok {
				t.Fatalf("active_badge is %T, want map[string]any", *acct.Profile.ActiveBadge)
			}
			if ref["id"] != tc.wantBadge {
				t.Fatalf("active_badge id = %v, want %s", ref["id"], tc.wantBadge)
			}
			if ref["type"] != "pioneer" {
				t.Fatalf("active_badge type = %v, want pioneer", ref["type"])
			}
			if ref["label"] != "Pioneer" {
				t.Fatalf("active_badge label = %v, want Pioneer", ref["label"])
			}
			if _, ok := ref["meta"].(map[string]any); !ok {
				t.Fatalf("active_badge meta = %v, want map (SDK strict-casts it)", ref["meta"])
			}
			if ref["account_id"] != tc.id {
				t.Fatalf("active_badge account_id = %v, want %s", ref["account_id"], tc.id)
			}
			for _, key := range []string{"created_at", "updated_at"} {
				if v, ok := ref[key].(string); !ok || v == "" {
					t.Fatalf("active_badge %s = %v, want RFC3339 string", key, ref[key])
				}
			}
			// Wire check: the raw column keys (Id/Type) must never leak.
			raw, err := json.Marshal(acct)
			if err != nil {
				t.Fatalf("marshal account: %v", err)
			}
			var wire map[string]any
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatalf("unmarshal wire: %v", err)
			}
			profileWire, ok := wire["profile"].(map[string]any)
			if !ok {
				t.Fatalf("wire has no profile object: %s", raw)
			}
			badgeWire, ok := profileWire["active_badge"].(map[string]any)
			if !ok {
				t.Fatalf("wire active_badge missing/not an object: %s", raw)
			}
			if _, hasPascal := badgeWire["Id"]; hasPascal {
				t.Fatalf("wire active_badge leaks PascalCase keys: %s", raw)
			}
		})
	}
}

// TestScanProfileActiveBadgeNull guards the clear path: a null active_badge
// column stays nil (the SDK reads `json['active_badge'] == null` as "no
// badge").
func TestScanProfileActiveBadgeNull(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), activeBadgeDSN)
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

	s := New(pool)
	now := time.Now().UTC()
	id := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, id, "badge_null_"+uuid.NewString()[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	defer pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, id)
	if _, err := pool.Exec(ctx, `INSERT INTO account_profiles (id, account_id, created_at, updated_at, experience, social_credits)
		VALUES ($1, $2, $3, $3, 0, 100)`, uuid.NewString(), id, now); err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	acct, err := s.GetAccountWithProfile(ctx, uuid.MustParse(id))
	if err != nil {
		t.Fatalf("GetAccountWithProfile: %v", err)
	}
	if acct.Profile == nil || acct.Profile.ActiveBadge != nil {
		t.Fatalf("active_badge = %v, want nil", acct.Profile.ActiveBadge)
	}
}
