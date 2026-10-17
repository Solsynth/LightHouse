package relationship

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// relSweepDSN mirrors the other store-level test DSNs; the suite skips when
// the database is unreachable rather than failing.
const relSweepDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

func sweepTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), relSweepDSN)
	if err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	return pool
}

// TestSweepExpiredAppliesDegradeAndSoftDelete pins the two outcomes of the
// expiry sweep that replaced Passport's RelationshipExpiryJob:
//
//   - a row carrying degrade_to_status lapses into that status (the timed
//     mute/block flow) and stops being treated as expired;
//   - every other expired row is soft-deleted rather than removed, which is
//     what keeps the change visible to GET /api/relationships/sync: the delta
//     reports Removed from `deleted_at IS NOT NULL AND deleted_at > since`, so
//     a hard delete would drop the removal from every client's incremental
//     sync without raising anything.
func TestSweepExpiredAppliesDegradeAndSoftDelete(t *testing.T) {
	pool := sweepTestPool(t)
	ctx := context.Background()
	now := time.Now().UTC()
	past := now.Add(-time.Hour)

	ids := make([]string, 4)
	for i := range ids {
		ids[i] = uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
			VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, ids[i], "relsweep_"+uuid.NewString()[:8], now); err != nil {
			t.Fatalf("seed account: %v", err)
		}
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM account_relationships WHERE account_id = ANY($1) OR related_id = ANY($1)`, ids); err != nil {
			t.Errorf("cleanup relationships: %v", err)
		}
		if _, err := pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = ANY($1)`, ids); err != nil {
			t.Errorf("cleanup accounts: %v", err)
		}
	})

	muter, muted, requester, requested := ids[0], ids[1], ids[2], ids[3]

	// Timed mute: expires back into Friends.
	if _, err := pool.Exec(ctx, `INSERT INTO account_relationships
		(account_id, related_id, status, expired_at, degrade_to_status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $6)`,
		muter, muted, int16(model.RelationshipMuted), past, int16(model.RelationshipFriends), now); err != nil {
		t.Fatalf("seed degrading row: %v", err)
	}
	// Lapsed friend request: no degrade target.
	if _, err := pool.Exec(ctx, `INSERT INTO account_relationships
		(account_id, related_id, status, expired_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $5)`,
		requester, requested, int16(model.RelationshipPending), past, now); err != nil {
		t.Fatalf("seed expiring row: %v", err)
	}
	// Not yet expired: must be left alone.
	if _, err := pool.Exec(ctx, `INSERT INTO account_relationships
		(account_id, related_id, status, expired_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $5)`,
		muted, requester, int16(model.RelationshipBlocked), now.Add(time.Hour), now); err != nil {
		t.Fatalf("seed live row: %v", err)
	}

	svc := New(store.New(pool), nil, nil)
	// Captured after the inserts so the sweep's writes are the only changes
	// in the delta window.
	since := time.Now().UTC()

	counts, err := svc.SweepExpired(ctx)
	if err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if counts.Degraded != 1 || counts.Deleted != 1 {
		t.Fatalf("counts = %+v, want 1 degraded and 1 deleted", counts)
	}

	// The mute lapsed into Friends and is no longer expired.
	degraded, err := svc.GetRelationship(ctx, uuid.MustParse(muter), uuid.MustParse(muted), nil, false, false)
	if err != nil {
		t.Fatalf("read degraded row: %v", err)
	}
	if degraded.Status != model.RelationshipFriends {
		t.Errorf("status = %d, want Friends (%d)", degraded.Status, model.RelationshipFriends)
	}
	if degraded.ExpiredAt != nil {
		t.Errorf("expired_at = %v, want cleared", degraded.ExpiredAt)
	}
	if degraded.DegradeToStatus != nil {
		t.Errorf("degrade_to_status = %v, want cleared", degraded.DegradeToStatus)
	}

	// The lapsed request is invisible to ordinary reads...
	if _, err := svc.GetRelationship(ctx, uuid.MustParse(requester), uuid.MustParse(requested), nil, false, false); err == nil {
		t.Error("soft-deleted request still visible to a non-including-deleted read")
	}
	// ...but the row survives in place: a re-send reuses it through
	// restoreRelationship instead of colliding on the composite primary key.
	var softDeleted bool
	if err := pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM account_relationships
		WHERE account_id = $1 AND related_id = $2`, requester, requested).Scan(&softDeleted); err != nil {
		t.Fatalf("read swept row: %v", err)
	}
	if !softDeleted {
		t.Error("expired request row was removed; want it soft-deleted in place")
	}

	// The unexpired block is untouched.
	live, err := svc.GetRelationship(ctx, uuid.MustParse(muted), uuid.MustParse(requester), nil, false, false)
	if err != nil {
		t.Fatalf("read live row: %v", err)
	}
	if live.Status != model.RelationshipBlocked || live.ExpiredAt == nil {
		t.Errorf("live row = %+v, want an untouched unexpired Blocked row", live)
	}

	// The sync surface carries both changes, each in its own account's delta:
	// the lapsed request shows up as a removal for the requester, and the
	// degraded mute as an update for the account that owns the row.
	removalDelta, err := svc.Store.GetRelationshipDelta(ctx, uuid.MustParse(requester), since)
	if err != nil {
		t.Fatalf("GetRelationshipDelta(requester): %v", err)
	}
	if !slices.Contains(removalDelta.Removed, requested) {
		t.Errorf("delta.Removed = %v, want the lapsed request %s", removalDelta.Removed, requested)
	}

	updateDelta, err := svc.Store.GetRelationshipDelta(ctx, uuid.MustParse(muter), since)
	if err != nil {
		t.Fatalf("GetRelationshipDelta(muter): %v", err)
	}
	var sawDegrade bool
	for _, row := range updateDelta.Updated {
		if row.AccountId == muter && row.RelatedId == muted && row.Status == model.RelationshipFriends {
			sawDegrade = true
		}
	}
	if !sawDegrade {
		t.Errorf("delta.Updated = %+v, want the degraded mute as Friends", updateDelta.Updated)
	}
}

// TestStartSweeperAppliesExpiryAndStops pins the wiring the boot path relies
// on: the ticker actually sweeps, and cancelling the context stops it. The
// interval is shortened for the test because the production value is five
// minutes; nothing else in the binary writes that var.
func TestStartSweeperAppliesExpiryAndStops(t *testing.T) {
	pool := sweepTestPool(t)
	ctx := context.Background()
	now := time.Now().UTC()
	past := now.Add(-time.Hour)

	ids := make([]string, 2)
	for i := range ids {
		ids[i] = uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
			VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, ids[i], "reltick_"+uuid.NewString()[:8], now); err != nil {
			t.Fatalf("seed account: %v", err)
		}
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM account_relationships WHERE account_id = ANY($1) OR related_id = ANY($1)`, ids); err != nil {
			t.Errorf("cleanup relationships: %v", err)
		}
		if _, err := pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = ANY($1)`, ids); err != nil {
			t.Errorf("cleanup accounts: %v", err)
		}
	})

	if _, err := pool.Exec(ctx, `INSERT INTO account_relationships
		(account_id, related_id, status, expired_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $5)`,
		ids[0], ids[1], int16(model.RelationshipPending), past, now); err != nil {
		t.Fatalf("seed expiring row: %v", err)
	}

	restore := sweepInterval
	sweepInterval = 20 * time.Millisecond
	t.Cleanup(func() { sweepInterval = restore })

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	New(store.New(pool), nil, nil).StartSweeper(runCtx)

	deadline := time.Now().Add(5 * time.Second)
	for {
		var deleted bool
		if err := pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM account_relationships
			WHERE account_id = $1 AND related_id = $2`, ids[0], ids[1]).Scan(&deleted); err != nil {
			t.Fatalf("poll swept row: %v", err)
		}
		if deleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sweeper never applied the expired row")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Cancelling must stop the loop; a second insert stays untouched.
	cancel()
	time.Sleep(100 * time.Millisecond)
	if _, err := pool.Exec(ctx, `INSERT INTO account_relationships
		(account_id, related_id, status, expired_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $5)
		ON CONFLICT (account_id, related_id) DO UPDATE SET status = $3, expired_at = $4, deleted_at = NULL`,
		ids[0], ids[1], int16(model.RelationshipPending), past, now); err != nil {
		t.Fatalf("re-seed row: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	var stillLive bool
	if err := pool.QueryRow(ctx, `SELECT deleted_at IS NULL FROM account_relationships
		WHERE account_id = $1 AND related_id = $2`, ids[0], ids[1]).Scan(&stillLive); err != nil {
		t.Fatalf("read row after cancel: %v", err)
	}
	if !stillLive {
		t.Error("sweeper kept running after its context was cancelled")
	}
}

// TestPurgeAccountRemovesBothDirections pins what the accounts.removal.confirmed
// consumer relies on: a removed account's rows go away in both directions, so
// its id stops answering from friends/blocked lists and stops satisfying the
// Unscoped existence check that gates a later relationship write.
func TestPurgeAccountRemovesBothDirections(t *testing.T) {
	pool := sweepTestPool(t)
	ctx := context.Background()
	now := time.Now().UTC()

	ids := make([]string, 3)
	for i := range ids {
		ids[i] = uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
			VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, ids[i], "relpurge_"+uuid.NewString()[:8], now); err != nil {
			t.Fatalf("seed account: %v", err)
		}
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM account_relationships WHERE account_id = ANY($1) OR related_id = ANY($1)`, ids); err != nil {
			t.Errorf("cleanup relationships: %v", err)
		}
		if _, err := pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = ANY($1)`, ids); err != nil {
			t.Errorf("cleanup accounts: %v", err)
		}
	})

	doomed, friend, blocker := ids[0], ids[1], ids[2]
	// Outgoing (doomed -> friend) and incoming (blocker -> doomed).
	for _, pair := range [][3]any{
		{doomed, friend, int16(model.RelationshipFriends)},
		{blocker, doomed, int16(model.RelationshipBlocked)},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO account_relationships
			(account_id, related_id, status, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $4)`, pair[0], pair[1], pair[2], now); err != nil {
			t.Fatalf("seed relationship: %v", err)
		}
	}

	svc := New(store.New(pool), nil, nil)
	removed, err := svc.PurgeAccount(ctx, uuid.MustParse(doomed))
	if err != nil {
		t.Fatalf("PurgeAccount: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want both rows", removed)
	}
	for _, pair := range [][2]string{{doomed, friend}, {blocker, doomed}} {
		var count int64
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM account_relationships
			WHERE account_id = $1 AND related_id = $2`, pair[0], pair[1]).Scan(&count); err != nil {
			t.Fatalf("count rows: %v", err)
		}
		if count != 0 {
			t.Errorf("row %s -> %s survived the purge", pair[0], pair[1])
		}
	}

	// The blocker can now relate to the id again, which is what the Unscoped
	// existence check gates on.
	exists, err := svc.HasExisting(ctx, uuid.MustParse(blocker), uuid.MustParse(doomed))
	if err != nil {
		t.Fatalf("HasExisting: %v", err)
	}
	if exists {
		t.Error("HasExisting = true after purge; a stale row still blocks a new relationship")
	}
}
