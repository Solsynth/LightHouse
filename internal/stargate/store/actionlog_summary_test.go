package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const actionLogSummaryDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

// TestSummarizeActionLogs verifies the dashboard rollup: all-time and rolling
// totals, per-action groups ordered by count, and per-day buckets covering
// only the requested window.
func TestSummarizeActionLogs(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), actionLogSummaryDSN)
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

	now := time.Now().UTC()
	accountID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, "action_summary_"+uuid.NewString()[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	defer pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, accountID)

	seed := func(action string, createdAt time.Time) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO action_logs (id, account_id, action, created_at, updated_at, meta)
			VALUES ($1, $2, $3, $4, $4, '{}')`, uuid.NewString(), accountID, action, createdAt); err != nil {
			t.Fatalf("seed action log: %v", err)
		}
	}
	seed("login", now)
	seed("login", now.Add(-2*24*time.Hour))
	seed("login", now.Add(-10*24*time.Hour))
	seed("accounts.profile.update", now.Add(-5*24*time.Hour))
	seed("old.action", now.Add(-40*24*time.Hour))

	s := New(pool)
	summary, err := s.SummarizeActionLogs(ctx, uuid.MustParse(accountID), now.AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("SummarizeActionLogs: %v", err)
	}

	if summary.Total != 5 {
		t.Errorf("Total = %d, want 5", summary.Total)
	}
	if summary.Last24h != 1 {
		t.Errorf("Last24h = %d, want 1", summary.Last24h)
	}
	if summary.Last7d != 3 {
		t.Errorf("Last7d = %d, want 3", summary.Last7d)
	}
	if summary.Last30d != 4 {
		t.Errorf("Last30d = %d, want 4", summary.Last30d)
	}
	if len(summary.Groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(summary.Groups))
	}
	if summary.Groups[0].Action != "login" || summary.Groups[0].Count != 3 {
		t.Errorf("first group = %+v, want login x3", summary.Groups[0])
	}
	if summary.Groups[1].Action != "accounts.profile.update" || summary.Groups[1].Count != 1 {
		t.Errorf("second group = %+v, want profile x1", summary.Groups[1])
	}
	var bucketTotal int64
	for i, bucket := range summary.Buckets {
		bucketTotal += bucket.Count
		if i > 0 && !bucket.Start.After(summary.Buckets[i-1].Start) {
			t.Errorf("buckets not ascending at %d: %v then %v", i, summary.Buckets[i-1].Start, bucket.Start)
		}
	}
	if bucketTotal != 4 {
		t.Errorf("bucket total = %d, want 4 (the 40-day-old row is outside the window)", bucketTotal)
	}
}
