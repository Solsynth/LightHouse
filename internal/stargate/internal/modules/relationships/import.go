package relationships

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/stargate/internal/module"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// importer copies the legacy Passport account_relationships table into the
// module-owned table. It is deliberately independent of column order: it
// selects the legacy columns by name, so the new table can evolve without
// rewriting the source query.
type importer struct{}

type legacyRelationship struct {
	AccountID       uuid.UUID  `gorm:"column:account_id"`
	RelatedID       uuid.UUID  `gorm:"column:related_id"`
	Alias           *string    `gorm:"column:alias"`
	ExpiredAt       *time.Time `gorm:"column:expired_at"`
	DegradeToStatus *int16     `gorm:"column:degrade_to_status"`
	Status          int16      `gorm:"column:status"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at"`
	DeletedAt       *time.Time `gorm:"column:deleted_at"`
}

func (i *importer) Import(ctx context.Context, source, target *gorm.DB, opts module.ImportOptions) (module.ImportStats, error) {
	stats := module.ImportStats{Module: "app.solian.id.relationships", Table: "account_relationships"}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 500
	}

	hasSource := source.Migrator().HasTable("account_relationships")
	if !hasSource {
		return stats, fmt.Errorf("source table account_relationships not found")
	}

	rows, err := source.WithContext(ctx).Raw(`
		SELECT account_id, related_id, alias, expired_at, degrade_to_status,
		       status, created_at, updated_at, deleted_at
		FROM account_relationships`).Rows()
	if err != nil {
		return stats, err
	}
	defer rows.Close()

	batch := make([]store.RelationshipEntity, 0, opts.BatchSize)
	flush := func() error {
		if len(batch) == 0 || opts.DryRun {
			batch = batch[:0]
			return nil
		}
		result := target.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "account_id"}, {Name: "related_id"}},
			DoNothing: true,
		}).CreateInBatches(batch, opts.BatchSize)
		if result.Error != nil {
			return result.Error
		}
		stats.Written += result.RowsAffected
		batch = batch[:0]
		return nil
	}

	for rows.Next() {
		var legacy legacyRelationship
		if err := source.ScanRows(rows, &legacy); err != nil {
			return stats, err
		}
		stats.Read++
		entity := store.RelationshipEntity{
			AccountID: legacy.AccountID,
			RelatedID: legacy.RelatedID,
			Alias:     legacy.Alias,
			ExpiredAt: legacy.ExpiredAt,
			Status:    legacy.Status,
			EntityBase: store.EntityBase{
				CreatedAt: legacy.CreatedAt,
				UpdatedAt: legacy.UpdatedAt,
			},
		}
		if legacy.DegradeToStatus != nil {
			entity.DegradeToStatus = legacy.DegradeToStatus
		}
		if legacy.DeletedAt != nil {
			entity.DeletedAt = gorm.DeletedAt{Time: *legacy.DeletedAt, Valid: true}
		}
		batch = append(batch, entity)
		if len(batch) >= opts.BatchSize {
			if err := flush(); err != nil {
				return stats, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return stats, err
	}
	if err := flush(); err != nil {
		return stats, err
	}

	if opts.DryRun {
		return stats, nil
	}

	return stats, module.RecordImport(ctx, target, stats)
}
