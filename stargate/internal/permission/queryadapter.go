package permission

import (
	"context"
	"database/sql"
)

type rowAdapter struct{ row *sql.Row }

func (r rowAdapter) Scan(dest ...any) error {
	err := r.row.Scan(dest...)
	if err == sql.ErrNoRows {
		return sql.ErrNoRows
	}
	return err
}

type rowsScanner interface {
	Close() error
	Next() bool
	Scan(...any) error
	Err() error
}
type rowScanner interface{ Scan(...any) error }
type commandTag struct{ sql.Result }

func (r commandTag) RowsAffected() int64 { n, _ := r.Result.RowsAffected(); return n }

type sqlTx struct{ *sql.Tx }

func (t *sqlTx) QueryRow(ctx context.Context, q string, args ...any) rowScanner {
	return rowAdapter{t.Tx.QueryRowContext(ctx, q, args...)}
}
func (t *sqlTx) Query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return t.Tx.QueryContext(ctx, q, args...)
}
func (t *sqlTx) Exec(ctx context.Context, q string, args ...any) (commandTag, error) {
	r, e := t.Tx.ExecContext(ctx, q, args...)
	return commandTag{r}, e
}
func (s *Service) QueryRow(ctx context.Context, q string, args ...any) rowScanner {
	db, e := s.DB.DB()
	if e != nil {
		return rowAdapter{&sql.Row{}}
	}
	return rowAdapter{db.QueryRowContext(ctx, q, args...)}
}
func (t *sqlTx) Commit(context.Context) error   { return t.Tx.Commit() }
func (t *sqlTx) Rollback(context.Context) error { return t.Tx.Rollback() }
func (s *Service) Query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	db, e := s.DB.DB()
	if e != nil {
		return nil, e
	}
	return db.QueryContext(ctx, q, args...)
}
func (s *Service) Exec(ctx context.Context, q string, args ...any) (commandTag, error) {
	db, e := s.DB.DB()
	if e != nil {
		return commandTag{}, e
	}
	r, e := db.ExecContext(ctx, q, args...)
	return commandTag{r}, e
}
func (s *Service) Begin(ctx context.Context) (*sqlTx, error) {
	db, e := s.DB.DB()
	if e != nil {
		return nil, e
	}
	t, e := db.BeginTx(ctx, nil)
	return &sqlTx{t}, e
}
