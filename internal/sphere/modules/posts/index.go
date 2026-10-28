package posts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// postIndexBatchSize is the number of unindexed posts one backfill batch reads.
const postIndexBatchSize = 200

// searchableText is the text the post index hashes and stores.
func searchableText(ent *store.PostEntity) string {
	parts := make([]string, 0, 3)
	if ent.Title != nil {
		parts = append(parts, *ent.Title)
	}
	if ent.Description != nil {
		parts = append(parts, *ent.Description)
	}
	if ent.Content != nil {
		parts = append(parts, *ent.Content)
	}
	return strings.Join(parts, "\n")
}

// writePostIndex upserts the post_indices row for a post. Embedding stays null
// because no embedding client is configured.
func (h *handler) writePostIndex(ctx context.Context, ent *store.PostEntity) error {
	if ent == nil || ent.ID == uuid.Nil {
		return nil
	}
	text := searchableText(ent)
	hash := sha256.Sum256([]byte(text))
	row := store.PostIndexEntity{
		ID:         uuid.New(),
		PostID:     ent.ID,
		SourceHash: hex.EncodeToString(hash[:]),
		SourceText: text,
		IndexedAt:  time.Now().UTC(),
	}
	return h.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "post_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"source_hash", "source_text", "indexed_at"}),
	}).Create(&row).Error
}

// backfillPostIndices indexes posts that have no post_indices row yet, in
// batches of postIndexBatchSize.
func (h *handler) backfillPostIndices(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		var ents []store.PostEntity
		if err := h.db.WithContext(ctx).
			Where("NOT EXISTS (SELECT 1 FROM post_indices i WHERE i.post_id = posts.id)").
			Order("created_at ASC").
			Limit(postIndexBatchSize).
			Find(&ents).Error; err != nil {
			return err
		}
		if len(ents) == 0 {
			return nil
		}
		indexed := 0
		for i := range ents {
			if err := h.writePostIndex(ctx, &ents[i]); err != nil {
				h.log.Warn("posts: index backfill failed", "post", ents[i].ID, "error", err)
				continue
			}
			indexed++
		}
		if indexed == 0 {
			return nil
		}
		if len(ents) < postIndexBatchSize {
			return nil
		}
	}
}
