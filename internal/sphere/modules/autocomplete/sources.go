package autocomplete

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"gorm.io/datatypes"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/filejson"
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
		label := row.Nick
		if label == "" {
			label = row.Name
		}
		identifier := row.ID.String()
		keyword := "@p/" + row.Name
		results = append(results, model.Autocompletion{
			Type:       "publisher",
			Value:      keyword,
			Label:      &label,
			Identifier: &identifier,
			Icon:       pictureURL(row.Picture),
		})
	}
	return results
}

// completeSticker matches the `pack+slug` placeholder of every sticker, which
// is what a composer inserts after typing ":". These are the emoji-style
// suggestions of the Sphere surface.
func (i *instance) completeSticker(ctx context.Context, query string, limit int) []model.Autocompletion {
	type stickerRow struct {
		ID         uuid.UUID      `gorm:"column:id"`
		Slug       string         `gorm:"column:slug"`
		Name       string         `gorm:"column:name"`
		Image      datatypes.JSON `gorm:"column:image"`
		PackPrefix string         `gorm:"column:pack_prefix"`
	}

	stmt := i.db.WithContext(ctx).
		Table("stickers").
		Select(`stickers.id, stickers.slug, stickers.name, stickers.image,
			sticker_packs.prefix AS pack_prefix`).
		Joins("JOIN sticker_packs ON sticker_packs.id = stickers.pack_id").
		Where("stickers.deleted_at IS NULL").
		Where("sticker_packs.deleted_at IS NULL")
	if query != "" {
		stmt = stmt.Where("(sticker_packs.prefix || '+' || stickers.slug) ILIKE ?", escapeLike(query)+"%")
	}

	var rows []stickerRow
	if err := stmt.Order(`sticker_packs.popularity DESC, stickers."order" ASC`).
		Limit(limit).Scan(&rows).Error; err != nil {
		if i.log != nil {
			i.log.Warn("autocomplete stickers", "error", err)
		}
		return []model.Autocompletion{}
	}

	results := make([]model.Autocompletion, 0, len(rows))
	for _, row := range rows {
		label := row.Name
		if label == "" {
			label = row.Slug
		}
		identifier := row.ID.String()
		results = append(results, model.Autocompletion{
			Type:       "sticker",
			Value:      ":" + row.PackPrefix + "+" + row.Slug + ":",
			Label:      &label,
			Identifier: &identifier,
			Icon:       pictureURL(row.Image),
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
		label := row.Slug
		if row.Name != nil && *row.Name != "" {
			label = *row.Name
		}
		identifier := row.ID.String()
		results = append(results, model.Autocompletion{
			Type:       "tag",
			Value:      "#" + row.Slug,
			Label:      &label,
			Identifier: &identifier,
		})
	}
	return results
}

// pictureURL extracts the URL of a jsonb cloud-file column.
func pictureURL(raw datatypes.JSON) *string {
	if len(raw) == 0 {
		return nil
	}
	file := filejson.Reference(raw)
	if file == nil || file.Url == "" {
		return nil
	}
	url := file.Url
	return &url
}

// escapeLike escapes the LIKE wildcards so a query is matched literally.
func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)
	return replacer.Replace(value)
}
