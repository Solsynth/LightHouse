package migrate

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// Run applies core prerequisite database extensions.
func Run(ctx context.Context, database *gorm.DB) error {
	if database.Dialector == nil || database.Dialector.Name() != "postgres" {
		return nil
	}
	for _, statement := range []string{
		`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`,
		`CREATE EXTENSION IF NOT EXISTS pg_trgm`,
	} {
		if err := database.WithContext(ctx).Exec(statement).Error; err != nil {
			return fmt.Errorf("pulsar migrate %q: %w", statement, err)
		}
	}
	if err := renamePrefixedTables(ctx, database); err != nil {
		return fmt.Errorf("pulsar migrate rename: %w", err)
	}
	if err := dropLegacyBangumiOAuth(ctx, database); err != nil {
		return fmt.Errorf("pulsar migrate bangumi oauth: %w", err)
	}
	return nil
}

// dropLegacyBangumiOAuth removes credentials the first Pulsar-owned Bangumi
// linking implementation stored. Stargate now owns the grant, so these columns
// and the callback-state table would otherwise linger as unused secrets.
func dropLegacyBangumiOAuth(ctx context.Context, database *gorm.DB) error {
	for _, statement := range []string{
		`ALTER TABLE IF EXISTS sync_connections DROP COLUMN IF EXISTS access_token`,
		`ALTER TABLE IF EXISTS sync_connections DROP COLUMN IF EXISTS refresh_token`,
		`ALTER TABLE IF EXISTS sync_connections DROP COLUMN IF EXISTS token_expires_at`,
		`DROP TABLE IF EXISTS sync_oauth_states`,
	} {
		if err := database.WithContext(ctx).Exec(statement).Error; err != nil {
			return fmt.Errorf("%q: %w", statement, err)
		}
	}
	return nil
}

// renamePrefixedTables upgrades databases created before the table prefix was
// dropped: pulsar_* tables and their ix_pulsar_* indexes become unprefixed.
// Every statement is guarded by IF EXISTS, so fresh databases are untouched.
func renamePrefixedTables(ctx context.Context, database *gorm.DB) error {
	statements := []string{
		`ALTER TABLE IF EXISTS pulsar_catalog_items RENAME TO catalog_items`,
		`ALTER TABLE IF EXISTS pulsar_active_sessions RENAME TO active_sessions`,
		`ALTER TABLE IF EXISTS pulsar_scrobbles RENAME TO scrobbles`,
		`ALTER TABLE IF EXISTS pulsar_collections RENAME TO collections`,
		`ALTER TABLE IF EXISTS pulsar_collection_items RENAME TO collection_items`,
		`ALTER TABLE IF EXISTS pulsar_sync_connections RENAME TO sync_connections`,
		`ALTER TABLE IF EXISTS pulsar_library_cursors RENAME TO library_cursors`,
		`ALTER TABLE IF EXISTS pulsar_library_tracks RENAME TO library_tracks`,
		`ALTER TABLE IF EXISTS pulsar_library_links RENAME TO library_links`,
		`ALTER TABLE IF EXISTS pulsar_library_lyrics RENAME TO library_lyrics`,
		`ALTER TABLE IF EXISTS pulsar_library_plays RENAME TO library_plays`,
		`ALTER TABLE IF EXISTS pulsar_library_session RENAME TO library_session`,
		`ALTER TABLE IF EXISTS pulsar_library_settings RENAME TO library_settings`,
		`ALTER INDEX IF EXISTS ix_pulsar_catalog_domain RENAME TO ix_catalog_domain`,
		`ALTER INDEX IF EXISTS ix_pulsar_catalog_slug RENAME TO ix_catalog_slug`,
		`ALTER INDEX IF EXISTS ix_pulsar_catalog_urn RENAME TO ix_catalog_urn`,
		`ALTER INDEX IF EXISTS ix_pulsar_catalog_prov RENAME TO ix_catalog_prov`,
		`ALTER INDEX IF EXISTS ix_pulsar_catalog_provkey RENAME TO ix_catalog_provkey`,
		`ALTER INDEX IF EXISTS ix_pulsar_sess_acc_man RENAME TO ix_sess_acc_man`,
		`ALTER INDEX IF EXISTS ix_pulsar_session_catalog RENAME TO ix_session_catalog`,
		`ALTER INDEX IF EXISTS ix_pulsar_session_lease RENAME TO ix_session_lease`,
		`ALTER INDEX IF EXISTS ix_pulsar_scrobble_account_played RENAME TO ix_scrobble_account_played`,
		`ALTER INDEX IF EXISTS ix_pulsar_scrobble_catalog RENAME TO ix_scrobble_catalog`,
		`ALTER INDEX IF EXISTS ix_pulsar_scrobble_domain RENAME TO ix_scrobble_domain`,
		`ALTER INDEX IF EXISTS ix_pulsar_scrobble_played RENAME TO ix_scrobble_played`,
		`ALTER INDEX IF EXISTS ix_pulsar_coll_account RENAME TO ix_coll_account`,
		`ALTER INDEX IF EXISTS ix_pulsar_coll_item_coll RENAME TO ix_coll_item_coll`,
		`ALTER INDEX IF EXISTS ix_pulsar_coll_item_catalog RENAME TO ix_coll_item_catalog`,
		`ALTER INDEX IF EXISTS ix_pulsar_sync_acc_prov RENAME TO ix_sync_acc_prov`,
		`ALTER INDEX IF EXISTS ix_pulsar_library_tracks_clock RENAME TO ix_library_tracks_clock`,
		`ALTER INDEX IF EXISTS ix_pulsar_library_tracks_rev RENAME TO ix_library_tracks_rev`,
		`ALTER INDEX IF EXISTS ix_pulsar_library_tracks_deleted RENAME TO ix_library_tracks_deleted`,
		`ALTER INDEX IF EXISTS ix_pulsar_library_links_rev RENAME TO ix_library_links_rev`,
		`ALTER INDEX IF EXISTS ix_pulsar_library_links_deleted RENAME TO ix_library_links_deleted`,
		`ALTER INDEX IF EXISTS ix_pulsar_library_lyrics_rev RENAME TO ix_library_lyrics_rev`,
		`ALTER INDEX IF EXISTS ix_pulsar_library_lyrics_deleted RENAME TO ix_library_lyrics_deleted`,
		`ALTER INDEX IF EXISTS ix_pulsar_library_plays_rev RENAME TO ix_library_plays_rev`,
		`ALTER INDEX IF EXISTS ix_pulsar_library_session_rev RENAME TO ix_library_session_rev`,
		`ALTER INDEX IF EXISTS ix_pulsar_library_settings_rev RENAME TO ix_library_settings_rev`,
	}
	for _, statement := range statements {
		if err := database.WithContext(ctx).Exec(statement).Error; err != nil {
			return fmt.Errorf("%q: %w", statement, err)
		}
	}
	return nil
}
