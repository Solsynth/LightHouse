package realm

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

// importer copies the eight legacy Passport realm tables into this module's
// tables. It exists because the module owns the schema: a deployment that
// disables it has no realm tables at all, so re-enabling on a fresh database
// needs a way to bring the old rows back.
//
// The copy is column-name based and conflict-tolerant, so re-running it is a
// no-op and an already-populated target is never truncated.
type importer struct{}

// legacyTables pairs each source table with its target entity, in FK-safe
// order (realms first, then members and labels, then the dependent rows).
var legacyTables = []struct {
	source string
	model  func() any
	// conflictColumns is the natural key the target already enforces.
	conflictColumns []string
}{
	{"realms", func() any { return &RealmEntity{} }, []string{"id"}},
	{"realm_members", func() any { return &RealmMemberEntity{} }, []string{"realm_id", "account_id"}},
	{"realm_labels", func() any { return &RealmLabelEntity{} }, []string{"id"}},
	{"realm_boost_contributions", func() any { return &RealmBoostContributionEntity{} }, []string{"id"}},
	{"realm_experience_records", func() any { return &RealmExperienceRecordEntity{} }, []string{"id"}},
	{"realm_role_permissions", func() any { return &RealmRolePermissionEntity{} }, []string{"id"}},
	{"realm_user_permissions", func() any { return &RealmUserPermissionEntity{} }, []string{"id"}},
	{"realm_post_moderation_logs", func() any { return &RealmPostModerationLogEntity{} }, []string{"id"}},
}

// Import copies every realm table from source into target.
func (i *importer) Import(ctx context.Context, source, target *gorm.DB, opts module.ImportOptions) (module.ImportStats, error) {
	stats := module.ImportStats{Module: ModuleID, Table: "realms"}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 500
	}
	for _, table := range legacyTables {
		if !source.Migrator().HasTable(table.source) {
			return stats, fmt.Errorf("source table %s not found", table.source)
		}
		if !target.Migrator().HasTable(table.source) {
			return stats, fmt.Errorf("target table %s not found; enable the realm module and boot Stargate first", table.source)
		}
		tableStats, err := copyTable(ctx, source, target, table.source, table.model, table.conflictColumns, opts)
		if err != nil {
			return stats, fmt.Errorf("%s: %w", table.source, err)
		}
		stats = stats.Add(tableStats)
	}
	if opts.DryRun {
		return stats, nil
	}
	if err := module.RecordImport(ctx, target, stats); err != nil {
		return stats, err
	}
	return stats, nil
}

// copyTable reads the source table in ordered batches and inserts them into
// the target with a do-nothing conflict clause, so ids already present are
// skipped instead of rewritten. Soft-deleted source rows are carried over
// (Unscoped), because a realm delete is a soft delete and must survive the
// copy.
func copyTable(ctx context.Context, source, target *gorm.DB, table string, model func() any, conflict []string, opts module.ImportOptions) (module.ImportStats, error) {
	stats := module.ImportStats{Module: ModuleID, Table: table}
	sliceType := reflect.SliceOf(reflect.TypeOf(model()))
	order := strings.Join(conflict, ", ")
	columns := make([]clause.Column, 0, len(conflict))
	for _, name := range conflict {
		columns = append(columns, clause.Column{Name: name})
	}

	for offset := 0; ; offset += opts.BatchSize {
		batchPtr := reflect.New(sliceType)
		query := source.WithContext(ctx).Table(table).Unscoped().Order(order).
			Limit(opts.BatchSize).Offset(offset)
		if err := query.Find(batchPtr.Interface()).Error; err != nil {
			return stats, err
		}
		batch := batchPtr.Elem()
		if batch.Len() == 0 {
			break
		}
		for i := 0; i < batch.Len(); i++ {
			ensureID(batch.Index(i).Interface())
		}
		stats.Read += int64(batch.Len())
		if !opts.DryRun {
			result := target.WithContext(ctx).Model(model()).Clauses(clause.OnConflict{
				Columns: columns, DoNothing: true,
			}).CreateInBatches(batch.Interface(), opts.BatchSize)
			if result.Error != nil {
				return stats, result.Error
			}
			stats.Written += result.RowsAffected
		}
		if batch.Len() < opts.BatchSize {
			break
		}
	}
	return stats, nil
}

// ensureID fills the uuid primary key when the source row had none, which a
// never-booted Passport schema can produce for the rows GORM generated.
func ensureID(entity any) {
	switch typed := entity.(type) {
	case *RealmEntity:
		if typed.ID == uuid.Nil {
			typed.ID = uuid.New()
		}
	case *RealmLabelEntity:
		if typed.ID == uuid.Nil {
			typed.ID = uuid.New()
		}
	case *RealmBoostContributionEntity:
		if typed.ID == uuid.Nil {
			typed.ID = uuid.New()
		}
	case *RealmExperienceRecordEntity:
		if typed.ID == uuid.Nil {
			typed.ID = uuid.New()
		}
	case *RealmRolePermissionEntity:
		if typed.ID == uuid.Nil {
			typed.ID = uuid.New()
		}
	case *RealmUserPermissionEntity:
		if typed.ID == uuid.Nil {
			typed.ID = uuid.New()
		}
	case *RealmPostModerationLogEntity:
		if typed.ID == uuid.Nil {
			typed.ID = uuid.New()
		}
	}
}

// NewImporter returns the module's legacy-data importer.
func NewImporter() module.Importer { return &importer{} }
