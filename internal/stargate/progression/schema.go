package progression

import (
	"context"

	"gorm.io/gorm"
)

// Models is the AutoMigrate input for the module factory.
func Models() []any {
	return []any{
		&AchievementDefinitionEntity{},
		&QuestDefinitionEntity{},
		&AccountAchievementEntity{},
		&AccountQuestProgressEntity{},
		&RewardGrantEntity{},
		&EventReceiptEntity{},
	}
}

// uniqueIndex is one uniqueness rule the module relies on.
type uniqueIndex struct {
	name    string
	table   string
	columns string
}

// uniqueIndexes are the indexes that make the seed sync idempotent and the
// reward tokens one-shot.
//
// Passport's EF model declared them as plain unique indexes over the columns
// plus deleted_at. That shape enforces nothing for live rows: every live row
// has deleted_at NULL, and Postgres treats NULLs as distinct, so two live rows
// with the same key would both be allowed. The indexes here are therefore
// partial (WHERE deleted_at IS NULL), which is the rule the code actually
// depends on: a soft-deleted row frees its key, a live duplicate is rejected.
var uniqueIndexes = []uniqueIndex{
	{"ix_achievement_definitions_identifier_live", "achievement_definitions", "identifier"},
	{"ix_quest_definitions_identifier_live", "quest_definitions", "identifier"},
	{"ix_account_achievements_account_definition_live", "account_achievements", "account_id, achievement_definition_id"},
	{"ix_account_quest_progresses_account_definition_period_live", "account_quest_progresses", "account_id, quest_definition_id, period_key"},
	{"ix_progress_reward_grants_reward_token_live", "progress_reward_grants", "reward_token"},
	{"ix_progress_event_receipts_event_definition_live", "progress_event_receipts", "event_id, definition_type, definition_identifier, period_key"},
}

// legacyIndexes are the EF-named indexes this module replaces. Dropping an
// index touches no data; a database migrated from Passport keeps its rows.
var legacyIndexes = []string{
	"ix_achievement_definitions_identifier_deleted_at",
	"ix_quest_definitions_identifier_deleted_at",
	"ix_account_achievements_account_id_achievement_definition_id_d",
	"ix_account_quest_progresses_account_id_quest_definition_id_per",
	"ix_progress_reward_grants_reward_token_deleted_at",
	"ix_progress_event_receipts_event_id_definition_type_definition",
	"ix_account_achievements_account_definition",
	"ix_account_quest_progresses_account_definition_period",
}

// Hooks applies the uniqueness rules and drops the superseded EF indexes.
func Hooks() []func(ctx context.Context, db *gorm.DB) error {
	hooks := make([]func(ctx context.Context, db *gorm.DB) error, 0, len(legacyIndexes)+len(uniqueIndexes))
	for _, name := range legacyIndexes {
		hooks = append(hooks, func(ctx context.Context, db *gorm.DB) error {
			return db.WithContext(ctx).Exec(`DROP INDEX IF EXISTS ` + name).Error
		})
	}
	for _, index := range uniqueIndexes {
		hooks = append(hooks, func(ctx context.Context, db *gorm.DB) error {
			return db.WithContext(ctx).Exec(`
				CREATE UNIQUE INDEX IF NOT EXISTS ` + index.name + `
				ON ` + index.table + ` (` + index.columns + `)
				WHERE deleted_at IS NULL`).Error
		})
	}
	return hooks
}
