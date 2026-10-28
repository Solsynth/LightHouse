// Package migrate applies the extensions the Messager schema depends on.
// Tables are owned by modules (see each module's schema hook); this package
// only creates what AutoMigrate cannot.
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
		`CREATE EXTENSION IF NOT EXISTS pg_trgm`,
	} {
		if err := database.WithContext(ctx).Exec(statement).Error; err != nil {
			return fmt.Errorf("apply %q: %w", statement, err)
		}
	}
	return nil
}

// Tables is the legacy Messager schema. The importer copies these tables
// verbatim (column names are unchanged) and skips any the legacy database does
// not have.
var Tables = []string{
	"chat_rooms",
	"chat_groups",
	"chat_members",
	"chat_messages",
	"chat_reactions",
	"chat_message_pins",
	"chat_room_counters",
	"chat_realtime_call",
	"chat_voice_clips",
}
