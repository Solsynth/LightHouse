package chat

import (
	"context"

	"gorm.io/gorm"
)

// chatIndexes adds the partial and unique indexes the legacy EF schema
// declares but AutoMigrate cannot express.
func chatIndexes(ctx context.Context, db *gorm.DB) error {
	statements := []string{
		// One sequence per room; the allocator in store.NextRoomSequence keeps
		// this monotonic under concurrency.
		`CREATE UNIQUE INDEX IF NOT EXISTS ix_chat_messages_chat_room_id_room_sequence
			ON chat_messages (chat_room_id, room_sequence)`,

		// One membership row per account and room.
		`CREATE UNIQUE INDEX IF NOT EXISTS ix_chat_members_chat_room_id_account_id
			ON chat_members (chat_room_id, account_id)`,

		// At most one live pin per (room, message).
		`CREATE UNIQUE INDEX IF NOT EXISTS ix_chat_message_pins_room_message_live
			ON chat_message_pins (chat_room_id, message_id) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS ix_chat_message_pins_room_expires_at
			ON chat_message_pins (chat_room_id, expires_at)`,

		// At most one active placeholder per member per room.
		`CREATE UNIQUE INDEX IF NOT EXISTS ix_chat_messages_active_placeholder
			ON chat_messages (chat_room_id, sender_id)
			WHERE type = 'placeholder' AND deleted_at IS NULL`,

		// Full-text-ish search over plaintext message bodies.
		`CREATE INDEX IF NOT EXISTS ix_chat_messages_content_trgm
			ON chat_messages USING gin (content gin_trgm_ops)
			WHERE type = 'text' AND encryption_meta IS NULL AND content IS NOT NULL AND deleted_at IS NULL`,

		// Client idempotency lookup.
		`CREATE INDEX IF NOT EXISTS ix_chat_messages_client_message_id
			ON chat_messages (chat_room_id, sender_id, client_message_id)
			WHERE client_message_id IS NOT NULL`,

		// Shareable slugs: unique per realm, or per owner account when the room
		// is not realm-owned.
		`CREATE UNIQUE INDEX IF NOT EXISTS ix_chat_rooms_realm_id_slug
			ON chat_rooms (realm_id, slug) WHERE realm_id IS NOT NULL AND slug IS NOT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ix_chat_rooms_account_id_slug
			ON chat_rooms (account_id, slug) WHERE realm_id IS NULL AND slug IS NOT NULL`,

		`CREATE INDEX IF NOT EXISTS ix_chat_groups_account_id_name
			ON chat_groups (account_id, name)`,
	}
	for _, statement := range statements {
		if err := db.WithContext(ctx).Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}
