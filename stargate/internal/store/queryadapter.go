package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

type rowScanner interface {
	Scan(dest ...any) error
}

type rowsScanner interface {
	Close() error
	Next() bool
	Scan(dest ...any) error
	Err() error
}

// These narrow adapters keep legacy query paths source-compatible while the
// backing handle is GORM-over-database/sql. New code should use typed GORM
// entities directly; the adapters are only used by the remaining complex
type rowAdapter struct{ row *sql.Row }

func (r rowAdapter) Scan(dest ...any) error {
	adapted := make([]any, len(dest))
	var stringSlices []struct {
		target *[]string
		raw    *[]byte
	}
	for index, value := range dest {
		if target, ok := value.(*[]string); ok {
			raw := []byte(nil)
			adapted[index] = &raw
			stringSlices = append(stringSlices, struct {
				target *[]string
				raw    *[]byte
			}{target, &raw})
			continue
		}
		adapted[index] = value
	}
	if err := r.row.Scan(adapted...); err != nil {
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		return err
	}
	for _, value := range stringSlices {
		if err := json.Unmarshal(*value.raw, value.target); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) queryRow(ctx context.Context, statement string, args ...any) rowScanner {
	database, err := s.DB.DB()
	if err != nil {
		return rowAdapter{row: &sql.Row{}}
	}
	return rowAdapter{row: database.QueryRowContext(ctx, statement, args...)}
}

func (s *Store) query(ctx context.Context, statement string, args ...any) (*sql.Rows, error) {
	database, err := s.DB.DB()
	if err != nil {
		return nil, err
	}
	return database.QueryContext(ctx, statement, args...)
}

type commandTag struct{ sql.Result }

func (r commandTag) RowsAffected() int64 {
	count, _ := r.Result.RowsAffected()
	return count
}

func (s *Store) exec(ctx context.Context, statement string, args ...any) (commandTag, error) {
	database, err := s.DB.DB()
	if err != nil {
		return commandTag{}, err
	}
	result, err := database.ExecContext(ctx, statement, args...)
	return commandTag{Result: result}, err
}

type sqlTx struct{ *sql.Tx }

func (tx *sqlTx) queryRow(ctx context.Context, statement string, args ...any) rowScanner {
	return rowAdapter{row: tx.Tx.QueryRowContext(ctx, statement, args...)}
}
func (tx *sqlTx) query(ctx context.Context, statement string, args ...any) (*sql.Rows, error) {
	return tx.Tx.QueryContext(ctx, statement, args...)
}
func (tx *sqlTx) exec(ctx context.Context, statement string, args ...any) (commandTag, error) {
	result, err := tx.Tx.ExecContext(ctx, statement, args...)
	return commandTag{Result: result}, err
}
func (tx *sqlTx) Commit(context.Context) error   { return tx.Tx.Commit() }
func (tx *sqlTx) Rollback(context.Context) error { return tx.Tx.Rollback() }

func (s *Store) begin(ctx context.Context) (*sqlTx, error) {
	database, err := s.DB.DB()
	if err != nil {
		return nil, err
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &sqlTx{Tx: tx}, nil
}

func (s *Store) beginSerializable(ctx context.Context) (*sqlTx, error) {
	database, err := s.DB.DB()
	if err != nil {
		return nil, err
	}
	tx, err := database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	return &sqlTx{Tx: tx}, nil
}

func (s *Store) Query(ctx context.Context, statement string, args ...any) (rowsScanner, error) {
	return s.query(ctx, statement, args...)
}
func (s *Store) QueryRow(ctx context.Context, statement string, args ...any) rowScanner {
	return s.queryRow(ctx, statement, args...)
}
func (s *Store) Exec(ctx context.Context, statement string, args ...any) (commandTag, error) {
	return s.exec(ctx, statement, args...)
}

func (tx *sqlTx) Query(ctx context.Context, statement string, args ...any) (rowsScanner, error) {
	return tx.query(ctx, statement, args...)
}
func (tx *sqlTx) QueryRow(ctx context.Context, statement string, args ...any) rowScanner {
	return tx.queryRow(ctx, statement, args...)
}
func (tx *sqlTx) Exec(ctx context.Context, statement string, args ...any) (commandTag, error) {
	return tx.exec(ctx, statement, args...)
}
func (s *Store) Begin(ctx context.Context) (*sqlTx, error) { return s.begin(ctx) }
