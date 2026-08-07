package store

// Regression tests for the admin punishment and OIDC session queries.
//
// These pin two production failures:
//   - AdminPunishment* passed single-element []uuid.UUID slices for scalar
//     `= $1` predicates, which pgx cannot encode into a scalar uuid
//     ("cannot find encode plan", OID 2950).
//   - FindValidOauthSession selected the account columns unqualified, so the
//     JOIN made `id` (and created_at/updated_at/deleted_at) ambiguous
//     (SQLSTATE 42702).
//
// Both bugs errored before returning rows; a sentinel UUID exercises the
// query end-to-end and must come back as ErrNotFound (query executed, no
// rows), not an encoding/ambiguity error. Mirrors the lastactive_test.go
// convention: skip when Postgres is unavailable.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const adminRegressionDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

var sentinelUUID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

func adminRegressionStore(t *testing.T) *Store {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), adminRegressionDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(pool.Close)
	return New(pool)
}

// TestAdminPunishmentScalarArgs exercises every adminQueryPunishments call
// site that predicates on a single uuid: the arg must be a scalar uuid.UUID,
// never a slice.
func TestAdminPunishmentScalarArgs(t *testing.T) {
	s := adminRegressionStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if _, err := s.AdminPunishmentGet(ctx, sentinelUUID, sentinelUUID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AdminPunishmentGet: got err %v, want ErrNotFound (query must execute)", err)
	}
	if _, total, err := s.AdminPunishmentsCreatedBy(ctx, sentinelUUID, 10, 0); err != nil || total != 0 {
		t.Fatalf("AdminPunishmentsCreatedBy: got err %v, total %d", err, total)
	}
	if _, total, err := s.AdminActivePunishmentsForAccount(ctx, sentinelUUID, now, 10, 0); err != nil || total != 0 {
		t.Fatalf("AdminActivePunishmentsForAccount: got err %v, total %d", err, total)
	}
	if _, total, err := s.AdminAllPunishmentsForAccount(ctx, sentinelUUID, 10, 0); err != nil || total != 0 {
		t.Fatalf("AdminAllPunishmentsForAccount: got err %v, total %d", err, total)
	}
	if p, err := s.AdminPunishmentOverview(ctx, sentinelUUID, now); err != nil || p != nil {
		t.Fatalf("AdminPunishmentOverview: got err %v, punishment %v", err, p)
	}
}

// TestFindValidOauthSessionQualifiedColumns runs the OIDC authorization-code
// session lookup; it joins accounts, so every selected column must be
// qualified (the unqualified accountColumns made `id` ambiguous).
func TestFindValidOauthSessionQualifiedColumns(t *testing.T) {
	s := adminRegressionStore(t)
	_, err := s.FindValidOauthSession(context.Background(), sentinelUUID.String(), sentinelUUID.String())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("FindValidOauthSession: got err %v, want ErrNotFound (query must execute)", err)
	}
}
