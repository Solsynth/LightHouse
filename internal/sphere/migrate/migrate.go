// Package migrate applies the extensions the Sphere schema depends on. Tables
// are owned by modules (see each module's schema hook), so this package only
// creates what AutoMigrate cannot: the vector and pg_trgm extensions used by
// post embeddings and trigram search.
package migrate

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// Run applies the core schema prerequisites.
func Run(ctx context.Context, database *gorm.DB) error {
	for _, statement := range []string{
		`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`,
		`CREATE EXTENSION IF NOT EXISTS vector`,
		`CREATE EXTENSION IF NOT EXISTS pg_trgm`,
	} {
		if err := database.WithContext(ctx).Exec(statement).Error; err != nil {
			return fmt.Errorf("apply %q: %w", statement, err)
		}
	}
	return nil
}

// Tables is the legacy Sphere schema, in the order the C# DbContext declares
// it. The importer copies these tables verbatim (column names are unchanged)
// and skips any the legacy database does not have.
var Tables = []string{
	"publishers",
	"publisher_members",
	"publisher_subscriptions",
	"publisher_features",
	"publisher_rating_records",
	"publisher_verified_domains",
	"posts",
	"post_indices",
	"post_bookmarks",
	"post_watch_preferences",
	"post_reactions",
	"post_awards",
	"post_tags",
	"post_categories",
	"post_tag_links",
	"post_category_links",
	"post_collections",
	"post_collection_items",
	"post_featured_records",
	"post_sponsor_bids",
	"post_sponsor_placements",
	"post_aggregated_stats",
	"post_category_subscriptions",
	"post_interest_profiles",
	"discovery_preferences",
	"publishing_settings",
	"surveys",
	"survey_questions",
	"survey_answers",
	"survey_subscriptions",
	"stickers",
	"sticker_packs",
	"sticker_pack_ownerships",
	"realm_post_moderation_logs",
	"fediverse_instances",
	"fediverse_moderation_rules",
	"fediverse_keys",
	"activity_pub_deliveries",
	"delivery_dead_letters",
	"boosts",
	"quote_authorizations",
	"live_streams",
	"live_stream_chat_messages",
	"live_stream_awards",
	"automod_rules",
}
