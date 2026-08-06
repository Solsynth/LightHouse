package permission

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// smokeDSN mirrors config.example.toml.
const smokeDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

// TestGrantPermissionGroup pins the Padlock consumer contract for
// accounts.tests.permission-group-granted: the account is added to the group
// by key; an existing membership is re-activated (expired_at cleared); a
// missing group returns false without error.
func TestGrantPermissionGroup(t *testing.T) {
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

	accountID := uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, "grant_smoke_"+uuid.NewString()[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, accountID) })
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM permission_group_members WHERE actor = $1`, accountID) })

	groupKey := "grant-group-" + uuid.NewString()[:8]
	var groupID string
	err = pool.QueryRow(ctx, `SELECT id FROM permission_groups WHERE "key" = $1 AND deleted_at IS NULL`, groupKey).Scan(&groupID)
	if err != nil {
		groupID = uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO permission_groups (id, "key", created_at, updated_at)
			VALUES ($1, $2, now(), now())`, groupID, groupKey); err != nil {
			t.Fatalf("seed group: %v", err)
		}
	}

	s := New(pool)
	id, err := uuid.Parse(accountID)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Fresh grant adds the membership.
	granted, err := s.GrantPermissionGroup(ctx, id, groupKey)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if !granted {
		t.Fatal("grant returned false for an existing group")
	}
	var member bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM permission_group_members WHERE group_id = $1 AND actor = $2 AND deleted_at IS NULL)`,
		groupID, accountID).Scan(&member); err != nil {
		t.Fatalf("check membership: %v", err)
	}
	if !member {
		t.Fatal("account was not added to the group")
	}

	// 2. Re-granting re-activates an expired membership.
	if _, err := pool.Exec(ctx, `UPDATE permission_group_members
		SET expired_at = now() - interval '1 hour' WHERE group_id = $1 AND actor = $2`,
		groupID, accountID); err != nil {
		t.Fatalf("expire membership: %v", err)
	}
	if _, err := s.GrantPermissionGroup(ctx, id, groupKey); err != nil {
		t.Fatalf("re-grant: %v", err)
	}
	var expiredAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT expired_at FROM permission_group_members
		WHERE group_id = $1 AND actor = $2`, groupID, accountID).Scan(&expiredAt); err != nil {
		t.Fatalf("load membership: %v", err)
	}
	if expiredAt != nil {
		t.Fatalf("re-grant did not clear expired_at: %v", expiredAt)
	}

	// 3. Missing group: false, no error.
	missing, err := s.GrantPermissionGroup(ctx, id, "no-such-group-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatalf("missing group grant errored: %v", err)
	}
	if missing {
		t.Fatal("grant returned true for a missing group")
	}
}
