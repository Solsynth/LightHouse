package autocomplete

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// fediversePublisherType is PublisherType.Fediverse; remote mirrors are not
// offered as local publisher mentions.
const fediversePublisherType = 2

// completePublisher matches publishers by name or nick prefix, skipping the
// fediverse mirrors, exactly like the C# service.
func (i *instance) completePublisher(ctx context.Context, query string, limit int) []model.Autocompletion {
	stmt := i.db.WithContext(ctx).Model(&store.PublisherEntity{}).
		Where("type <> ?", fediversePublisherType)
	if query != "" {
		pattern := escapeLike(query) + "%"
		stmt = stmt.Where("name ILIKE ? OR nick ILIKE ?", pattern, pattern)
	}

	var rows []store.PublisherEntity
	if err := stmt.Order("name ASC").Limit(limit).Find(&rows).Error; err != nil {
		if i.log != nil {
			i.log.Warn("autocomplete publishers", "error", err)
		}
		return []model.Autocompletion{}
	}

	results := make([]model.Autocompletion, 0, len(rows))
	for idx := range rows {
		row := &rows[idx]
		results = append(results, model.Autocompletion{
			Type:    "publisher",
			Keyword: "@p/" + row.Name,
			Data:    store.PublisherToModel(row),
		})
	}
	return results
}

// completeSticker matches the `pack+slug` placeholder of every sticker, which
// is what a composer inserts after typing ":". These are the emoji-style
// suggestions of the Sphere surface.
func (i *instance) completeSticker(ctx context.Context, query string, limit int) []model.Autocompletion {
	stmt := i.db.WithContext(ctx).
		Model(&store.StickerEntity{}).
		Select("stickers.*").
		Joins("JOIN sticker_packs ON sticker_packs.id = stickers.pack_id AND sticker_packs.deleted_at IS NULL").
		Where("stickers.deleted_at IS NULL")
	if query != "" {
		stmt = stmt.Where("(sticker_packs.prefix || '+' || stickers.slug) ILIKE ?", escapeLike(query)+"%")
	}

	var rows []store.StickerEntity
	if err := stmt.Order(`sticker_packs.popularity DESC, stickers."order" ASC`).
		Limit(limit).Find(&rows).Error; err != nil {
		if i.log != nil {
			i.log.Warn("autocomplete stickers", "error", err)
		}
		return []model.Autocompletion{}
	}

	packIDs := make([]uuid.UUID, 0, len(rows))
	for idx := range rows {
		packIDs = append(packIDs, rows[idx].PackID)
	}
	packsByID := map[uuid.UUID]store.StickerPackEntity{}
	if len(packIDs) > 0 {
		var packs []store.StickerPackEntity
		if err := i.db.WithContext(ctx).
			Where("id IN ? AND deleted_at IS NULL", packIDs).
			Find(&packs).Error; err != nil {
			if i.log != nil {
				i.log.Warn("autocomplete sticker packs", "error", err)
			}
			return []model.Autocompletion{}
		}
		for idx := range packs {
			packsByID[packs[idx].ID] = packs[idx]
		}
	}

	results := make([]model.Autocompletion, 0, len(rows))
	for idx := range rows {
		row := &rows[idx]
		pack, ok := packsByID[row.PackID]
		if !ok {
			continue
		}
		packModel := store.StickerPackToModel(&pack)
		sticker := store.StickerToModel(row)
		sticker.Pack = &packModel
		results = append(results, model.Autocompletion{
			Type:    "sticker",
			Keyword: ":" + pack.Prefix + "+" + row.Slug + ":",
			Data:    sticker,
		})
	}
	return results
}

// completeTag matches the tag slug of local post tags for "#" completions.
func (i *instance) completeTag(ctx context.Context, query string, limit int) []model.Autocompletion {
	stmt := i.db.WithContext(ctx).Model(&store.PostTagEntity{})
	if query != "" {
		pattern := escapeLike(query) + "%"
		stmt = stmt.Where("slug ILIKE ? OR name ILIKE ?", pattern, pattern)
	}

	var rows []store.PostTagEntity
	if err := stmt.Order("slug ASC").Limit(limit).Find(&rows).Error; err != nil {
		if i.log != nil {
			i.log.Warn("autocomplete tags", "error", err)
		}
		return []model.Autocompletion{}
	}

	results := make([]model.Autocompletion, 0, len(rows))
	for idx := range rows {
		row := &rows[idx]
		name := row.Slug
		if row.Name != nil && *row.Name != "" {
			name = *row.Name
		}
		results = append(results, model.Autocompletion{
			Type:    "tag",
			Keyword: "#" + row.Slug,
			Data: map[string]any{
				"id":   row.ID.String(),
				"slug": row.Slug,
				"name": name,
			},
		})
	}
	return results
}

// escapeLike escapes the LIKE wildcards so a query is matched literally.
func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)
	return replacer.Replace(value)
}
