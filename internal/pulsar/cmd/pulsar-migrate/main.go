// pulsar-migrate copies the legacy Stargate presence tables into Pulsar's
// canonical playback tables:
//
//	presence_catalog_items -> pulsar_catalog_items
//	presence_activities    -> pulsar_active_sessions (open rows)
//	                          pulsar_scrobbles       (ended rows)
//
// Rows keep their UUIDs, so catalog references and client history stay stable.
// The copy is idempotent: existing ids and open (account_id, manual_id)
// sessions are left untouched.
//
// Usage:
//
//	pulsar-migrate --source-dsn "postgres://.../light_auth"
//
// The target defaults to the Pulsar config [database] section.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/config"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

func main() {
	var sourceDSN, targetDSN string
	flag.StringVar(&sourceDSN, "source-dsn", os.Getenv("STARGATE_DSN"), "source Stargate postgres DSN")
	flag.StringVar(&targetDSN, "target-dsn", os.Getenv("PULSAR_DSN"), "target Pulsar postgres DSN (defaults to config)")
	flag.Parse()

	if strings.TrimSpace(sourceDSN) == "" {
		log.Fatal("--source-dsn is required")
	}
	if strings.TrimSpace(targetDSN) == "" {
		cfg, err := config.Load("")
		if err != nil {
			log.Fatalf("load Pulsar config: %v", err)
		}
		targetDSN = cfg.Database.DSN
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	source, err := pgxpool.New(ctx, sourceDSN)
	if err != nil {
		log.Fatalf("connect source: %v", err)
	}
	defer source.Close()

	target, err := gorm.Open(postgres.Open(targetDSN), &gorm.Config{})
	if err != nil {
		log.Fatalf("connect target: %v", err)
	}

	catalogTags, err := loadCatalogTags(ctx, source)
	if err != nil {
		log.Fatalf("catalog tags: %v", err)
	}
	activityTags, err := loadActivityTags(ctx, source)
	if err != nil {
		log.Fatalf("activity tags: %v", err)
	}
	catalogs, err := migrateCatalogs(ctx, source, target, catalogTags)
	if err != nil {
		log.Fatalf("catalogs: %v", err)
	}
	active, scrobbles, skipped, err := migrateActivities(ctx, source, target, activityTags)
	if err != nil {
		log.Fatalf("activities: %v", err)
	}
	log.Printf("catalogs=%d active=%d scrobbles=%d skipped=%d", catalogs, active, scrobbles, skipped)
}

func loadCatalogTags(ctx context.Context, source *pgxpool.Pool) (map[string][]string, error) {
	rows, err := source.Query(ctx, `SELECT catalog_id::text, slug FROM presence_catalog_tags ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := make(map[string][]string)
	for rows.Next() {
		var catalogID, slug string
		if err := rows.Scan(&catalogID, &slug); err != nil {
			return nil, err
		}
		tags[catalogID] = append(tags[catalogID], slug)
	}
	return tags, rows.Err()
}

func loadActivityTags(ctx context.Context, source *pgxpool.Pool) (map[string][]map[string]any, error) {
	rows, err := source.Query(ctx, `SELECT activity_id::text, slug, name FROM presence_activity_tags ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := make(map[string][]map[string]any)
	for rows.Next() {
		var activityID, slug string
		var name *string
		if err := rows.Scan(&activityID, &slug, &name); err != nil {
			return nil, err
		}
		tag := map[string]any{"slug": slug}
		if name != nil {
			tag["name"] = *name
		}
		tags[activityID] = append(tags[activityID], tag)
	}
	return tags, rows.Err()
}

type catalogRow struct {
	ID          string
	AccountID   string
	Name        *string
	Subtitle    *string
	Caption     *string
	CatalogKey  *string
	Provider    *string
	ReferenceID *string
	LargeImage  *string
	SmallImage  *string
	TitleURL    *string
	SubtitleURL *string
	Meta        *string
	CreatedAt   *time.Time
	UpdatedAt   *time.Time
}

func migrateCatalogs(ctx context.Context, source *pgxpool.Pool, target *gorm.DB, tags map[string][]string) (int, error) {
	rows, err := source.Query(ctx, `
		SELECT id::text, account_id::text, name, subtitle, caption, catalog_key,
		       provider, reference_id, large_image, small_image, title_url,
		       subtitle_url, meta::text, created_at, updated_at
		FROM presence_catalog_items
		WHERE deleted_at IS NULL`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var row catalogRow
		if err := rows.Scan(
			&row.ID, &row.AccountID, &row.Name, &row.Subtitle, &row.Caption,
			&row.CatalogKey, &row.Provider, &row.ReferenceID, &row.LargeImage,
			&row.SmallImage, &row.TitleURL, &row.SubtitleURL, &row.Meta,
			&row.CreatedAt, &row.UpdatedAt,
		); err != nil {
			return count, err
		}
		id, err := uuid.Parse(row.ID)
		if err != nil {
			return count, fmt.Errorf("catalog %s: %w", row.ID, err)
		}
		meta := decodeMeta(row.Meta)
		putMeta(meta, "account_id", row.AccountID)
		putMeta(meta, "reference_id", row.ReferenceID)
		putMeta(meta, "queryable_terms", nil)
		putMeta(meta, "title_url", row.TitleURL)
		putMeta(meta, "subtitle_url", row.SubtitleURL)
		putMeta(meta, "small_image", row.SmallImage)
		putMeta(meta, "last_active_at", nil)

		item := &model.CatalogItem{
			ID:          id,
			Domain:      model.DomainCustom,
			Provider:    deref(row.Provider),
			ProviderKey: deref(row.CatalogKey),
			Title:       firstNonEmpty(deref(row.Name), deref(row.CatalogKey), id.String()),
			Subtitle:    row.Subtitle,
			Caption:     row.Caption,
			ArtworkURL:  row.LargeImage,
			Metadata:    model.ToJSON(meta),
			CreatedAt:   valueOrNow(row.CreatedAt),
			UpdatedAt:   valueOrNow(row.UpdatedAt),
		}
		if slugs := tags[row.ID]; len(slugs) > 0 {
			item.Genres = model.ToJSON(slugs)
		}
		result := target.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(item)
		if result.Error != nil {
			return count, result.Error
		}
		if result.RowsAffected > 0 {
			count++
		}
	}
	return count, rows.Err()
}

type activityRow struct {
	ID             string
	AccountID      string
	CatalogID      *string
	Type           *string
	ManualID       *string
	Title          *string
	Subtitle       *string
	Caption        *string
	LargeImage     *string
	SmallImage     *string
	Provider       *string
	ReferenceID    *string
	QueryableTerms *string
	Meta           *string
	LeaseMinutes   int
	LeaseExpiresAt time.Time
	StartedAt      *time.Time
	EndedAt        *time.Time
	CreatedAt      *time.Time
	UpdatedAt      *time.Time
	TitleURL       *string
	SubtitleURL    *string
}

func migrateActivities(ctx context.Context, source *pgxpool.Pool, target *gorm.DB, tags map[string][]map[string]any) (active, scrobbles, skipped int, err error) {
	rows, err := source.Query(ctx, `
		SELECT id::text, account_id::text, catalog_id::text, type, manual_id,
		       title, subtitle, caption, large_image, small_image, provider,
		       reference_id, queryable_terms::text, meta::text, lease_minutes,
		       lease_expires_at, started_at, ended_at, created_at, updated_at,
		       title_url, subtitle_url
		FROM presence_activities
		WHERE deleted_at IS NULL
		ORDER BY updated_at DESC`)
	if err != nil {
		return 0, 0, 0, err
	}
	defer rows.Close()

	now := time.Now().UTC()
	for rows.Next() {
		var row activityRow
		if err := rows.Scan(
			&row.ID, &row.AccountID, &row.CatalogID, &row.Type, &row.ManualID,
			&row.Title, &row.Subtitle, &row.Caption, &row.LargeImage,
			&row.SmallImage, &row.Provider, &row.ReferenceID,
			&row.QueryableTerms, &row.Meta, &row.LeaseMinutes,
			&row.LeaseExpiresAt, &row.StartedAt, &row.EndedAt,
			&row.CreatedAt, &row.UpdatedAt, &row.TitleURL, &row.SubtitleURL,
		); err != nil {
			return active, scrobbles, skipped, err
		}
		id, err := uuid.Parse(row.ID)
		if err != nil {
			return active, scrobbles, skipped, fmt.Errorf("activity %s: %w", row.ID, err)
		}
		accountID, err := uuid.Parse(row.AccountID)
		if err != nil {
			return active, scrobbles, skipped, fmt.Errorf("activity %s account: %w", row.ID, err)
		}
		var catalogID *uuid.UUID
		if row.CatalogID != nil {
			parsed, err := uuid.Parse(*row.CatalogID)
			if err == nil {
				catalogID = &parsed
			}
		}
		meta := decodeMeta(row.Meta)
		if legacyTags := tags[row.ID]; len(legacyTags) > 0 {
			meta["tags"] = legacyTags
		}
		putMeta(meta, "provider", row.Provider)
		putMeta(meta, "reference_id", row.ReferenceID)
		putMeta(meta, "queryable_terms", decodeAny(row.QueryableTerms))
		putMeta(meta, "title_url", row.TitleURL)
		putMeta(meta, "subtitle_url", row.SubtitleURL)
		putMeta(meta, "small_image", row.SmallImage)
		putMeta(meta, "visibility", nil)

		domain := firstNonEmpty(deref(row.Type), model.DomainCustom)
		manualID := firstNonEmpty(deref(row.ManualID), "default")
		title := firstNonEmpty(deref(row.Title), manualID)

		if row.EndedAt != nil {
			playedAt := valueOrNow(row.StartedAt)
			if row.StartedAt == nil && row.EndedAt != nil {
				playedAt = *row.EndedAt
			}
			listened := 0
			if row.StartedAt != nil && row.EndedAt.After(*row.StartedAt) {
				listened = int(row.EndedAt.Sub(*row.StartedAt) / time.Second)
			}
			scrobble := &model.Scrobble{
				ID:              id,
				AccountID:       accountID,
				CatalogID:       catalogID,
				Domain:          domain,
				ClientName:      firstNonEmpty(deref(row.Provider), "legacy"),
				Title:           title,
				Subtitle:        row.Subtitle,
				Caption:         row.Caption,
				ArtworkURL:      row.LargeImage,
				ListenedSeconds: listened,
				PlayedAt:        playedAt,
				IsImported:      true,
				Meta:            model.ToJSON(meta),
				CreatedAt:       valueOrNow(row.CreatedAt),
			}
			result := target.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(scrobble)
			if result.Error != nil {
				return active, scrobbles, skipped, result.Error
			}
			if result.RowsAffected > 0 {
				scrobbles++
			}
			continue
		}

		if !row.LeaseExpiresAt.After(now) {
			skipped++
			continue
		}
		session := &model.ActiveSession{
			ID:             id,
			AccountID:      accountID,
			CatalogID:      catalogID,
			Domain:         domain,
			ManualID:       manualID,
			Title:          title,
			Subtitle:       row.Subtitle,
			Caption:        row.Caption,
			ArtworkURL:     row.LargeImage,
			LargeImage:     row.LargeImage,
			SmallImage:     row.SmallImage,
			LeaseMinutes:   row.LeaseMinutes,
			LeaseExpiresAt: row.LeaseExpiresAt,
			StartedAt:      valueOrNow(row.StartedAt),
			Meta:           model.ToJSON(meta),
			CreatedAt:      valueOrNow(row.CreatedAt),
			UpdatedAt:      valueOrNow(row.UpdatedAt),
		}
		result := target.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "account_id"}, {Name: "manual_id"}},
			DoNothing: true,
		}).Create(session)
		if result.Error != nil {
			return active, scrobbles, skipped, result.Error
		}
		if result.RowsAffected > 0 {
			active++
		} else {
			skipped++
		}
	}
	return active, scrobbles, skipped, rows.Err()
}

func decodeMeta(raw *string) map[string]any {
	out := make(map[string]any)
	if raw == nil || *raw == "" {
		return out
	}
	_ = json.Unmarshal([]byte(*raw), &out)
	return out
}

func decodeAny(raw *string) any {
	if raw == nil || *raw == "" {
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(*raw), &value); err != nil {
		return nil
	}
	return value
}

func putMeta(meta map[string]any, key string, value any) {
	if value == nil {
		return
	}
	switch typed := value.(type) {
	case *string:
		if typed != nil && strings.TrimSpace(*typed) != "" {
			meta[key] = *typed
		}
	case string:
		if strings.TrimSpace(typed) != "" {
			meta[key] = typed
		}
	default:
		meta[key] = value
	}
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func valueOrNow(value *time.Time) time.Time {
	if value == nil || value.IsZero() {
		return time.Now().UTC()
	}
	return *value
}
