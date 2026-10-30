// Package store holds the GORM-backed Postgres access layer.
package store

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// Store wraps the shared GORM handle.
type Store struct {
	DB *gorm.DB
}

// ErrNotFound is returned when a row lookup misses.
var ErrNotFound = errors.New("store: record not found")

// New creates a Store over the database handle.
func New(database *gorm.DB) *Store {
	return &Store{DB: database}
}

func (s *Store) db(ctx context.Context) *gorm.DB {
	return s.DB.WithContext(ctx)
}

func notFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

func applyAppFilter(query *gorm.DB, column string, resolvedAppID, defaultAppID *string) *gorm.DB {
	if resolvedAppID == nil || *resolvedAppID == "" {
		return query
	}
	if defaultAppID != nil && *resolvedAppID == *defaultAppID {
		return query.Where(column+" = ? OR "+column+" IS NULL OR "+column+" = ''", *resolvedAppID)
	}
	return query.Where(column+" = ?", *resolvedAppID)
}
