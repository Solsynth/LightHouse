// Package db provides the Postgres pool and shared query helpers.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/go/pkg/models"
)

// Connect creates a pgxpool from the DSN with sane production defaults. The
// type map wraps models.Time so it encodes as a plain time.Time
// (timestamptz) — pgx cannot encode the defined type directly.
func Connect(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database dsn: %w", err)
	}
	poolCfg.MaxConns = 20
	poolCfg.MaxConnLifetime = 60 * time.Second
	poolCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		conn.TypeMap().TryWrapEncodePlanFuncs = append(conn.TypeMap().TryWrapEncodePlanFuncs, wrapModelsTime)
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// wrapModelsTime converts models.Time values to time.Time before pgx plans
// the encode (the shared SDK type implements driver.Valuer on the pointer
// receiver only; wrapping keeps every store call site unchanged).
func wrapModelsTime(value any) (pgtype.WrappedEncodePlanNextSetter, any, bool) {
	switch v := value.(type) {
	case models.Time:
		return &timeWrapPlan{}, time.Time(v), true
	case *models.Time:
		if v == nil {
			return &timeWrapPlan{}, (*time.Time)(nil), true
		}
		t := time.Time(*v)
		return &timeWrapPlan{}, &t, true
	default:
		return nil, nil, false
	}
}

type timeWrapPlan struct {
	next pgtype.EncodePlan
}

func (p *timeWrapPlan) SetNext(next pgtype.EncodePlan) { p.next = next }

// Encode receives the ORIGINAL value (models.Time) and converts it before
// delegating to the time.Time codec plan.
func (p *timeWrapPlan) Encode(value any, buf []byte) ([]byte, error) {
	switch v := value.(type) {
	case models.Time:
		return p.next.Encode(time.Time(v), buf)
	case *models.Time:
		if v == nil {
			return p.next.Encode((*time.Time)(nil), buf)
		}
		t := time.Time(*v)
		return p.next.Encode(&t, buf)
	default:
		return p.next.Encode(value, buf)
	}
}
