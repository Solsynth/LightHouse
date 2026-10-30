// Package dbtest provides a dedicated-database helper for tests that run
// the destructive SQL migrations.
//
// The migrations start with `DROP TABLE IF EXISTS <table> CASCADE`. When a
// test runs them inside a schema of an existing database with `public` on
// the search_path (needed for the pg_trgm operator class), the unqualified
// DROPs fall through the search_path to the live `public` schema and drop
// the database's real tables. Running migrations in a fresh dedicated
// database removes that hazard entirely: `public` is empty there, and
// `CREATE EXTENSION IF NOT EXISTS pg_trgm` installs into the test schema
// (first on the search_path), so `gin_trgm_ops` resolves without exposing
// any live tables to the DROPs.
package dbtest

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// NewDatabase creates a fresh dedicated PostgreSQL database for isolated
// migration tests and returns its DSN plus a cleanup function that drops it.
// baseDSN may be keyword/value or postgres:// URL form.
func NewDatabase(ctx context.Context, baseDSN string) (string, func(), error) {
	name := "stargate_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	adminDSN := withDatabase(baseDSN, "postgres")

	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return "", nil, fmt.Errorf("dbtest: connect admin: %w", err)
	}
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		_ = admin.Close(ctx)
		return "", nil, fmt.Errorf("dbtest: create database %s: %w", name, err)
	}
	if err := admin.Close(ctx); err != nil {
		return "", nil, fmt.Errorf("dbtest: close admin: %w", err)
	}

	dsn := withDatabase(baseDSN, name)
	cleanup := func() { dropDatabase(adminDSN, name) }
	return dsn, cleanup, nil
}

// dropDatabase terminates any lingering connections before dropping, so a
// leaked pool from a failed test cannot keep the scratch database alive.
func dropDatabase(adminDSN, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return
	}
	defer admin.Close(ctx)
	_, _ = admin.Exec(ctx,
		`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, name)
	_, _ = admin.Exec(ctx, `DROP DATABASE IF EXISTS `+name)
}

// withDatabase swaps the database name in a keyword/value or postgres:// URL
// DSN, preserving everything else.
func withDatabase(dsn, database string) string {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		rest := dsn[strings.Index(dsn, "://")+3:]
		slash := strings.IndexByte(rest, '/')
		if slash < 0 {
			return dsn
		}
		head := dsn[:strings.Index(dsn, "://")+3+slash+1]
		tail := rest[slash+1:]
		if q := strings.IndexByte(tail, '?'); q >= 0 {
			tail = tail[q:]
		} else {
			tail = ""
		}
		return head + database + tail
	}
	parts := strings.Fields(dsn)
	for i, part := range parts {
		if strings.HasPrefix(part, "dbname=") {
			parts[i] = "dbname=" + database
			return strings.Join(parts, " ")
		}
	}
	return dsn + " dbname=" + database
}
