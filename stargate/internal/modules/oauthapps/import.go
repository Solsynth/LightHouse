package oauthapps

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/stargate/internal/module"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// importer copies Develop's custom_apps and custom_app_secrets into the
// module-owned tables, so a deployment can stop running the Develop service
// without losing its OAuth clients.
//
// Only the OAuth-relevant subset moves: an app's board widgets, payment
// wallet, mini apps, products and bot accounts stay in Develop. Every app is
// copied, including one without an oauth_config, because Develop treats any
// custom app as a potential client and the two registries should agree.
//
// Both queries select columns by name, so the Develop schema can gain columns
// without touching this file.
type importer struct{}

const (
	sourceAppsTable    = "custom_apps"
	sourceSecretsTable = "custom_app_secrets"
)

// sourceApp is one custom_apps row. Nullable columns stay pointers so a null
// in the source is distinguishable from a zero value.
type sourceApp struct {
	ID          uuid.UUID
	Slug        string
	Name        string
	Description *string
	Status      int
	OauthConfig *string
	Links       *string
	ProjectID   *uuid.UUID
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time
}

// sourceSecret is one custom_app_secrets row.
type sourceSecret struct {
	ID          uuid.UUID
	Secret      string
	Description *string
	ExpiredAt   *time.Time
	IsOidc      bool
	AppID       uuid.UUID
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time
}

func (i *importer) Import(ctx context.Context, source, target *gorm.DB, opts module.ImportOptions) (module.ImportStats, error) {
	if opts.BatchSize <= 0 {
		opts.BatchSize = 500
	}
	stats := module.ImportStats{Module: moduleID, Table: "oauth_apps"}
	if !source.Migrator().HasTable(sourceAppsTable) {
		return stats, fmt.Errorf("source table %s not found", sourceAppsTable)
	}

	present, err := existingOAuthAppIDs(ctx, target)
	if err != nil {
		return stats, err
	}

	apps, owned, err := importApps(ctx, source, target, opts, present)
	if err != nil {
		return stats, err
	}
	// Secrets follow their app: a secret whose app was not imported would
	// be a row the provider can never resolve.
	secrets, err := importSecrets(ctx, source, target, opts, owned)
	if err != nil {
		return stats, err
	}

	stats.Tables = []module.TableStats{apps, secrets}
	for _, table := range stats.Tables {
		stats.Read += table.Read
		stats.Written += table.Written
		stats.Skipped += table.Skipped
	}
	if opts.DryRun {
		return stats, nil
	}
	for _, table := range stats.Tables {
		err := module.RecordImport(ctx, target, module.ImportStats{
			Module:  moduleID,
			Table:   table.Table,
			Read:    table.Read,
			Written: table.Written,
			Skipped: table.Skipped,
		})
		if err != nil {
			return stats, err
		}
	}
	return stats, nil
}

// importApps copies the app rows and reports both the counters and the ids
// that exist in the target afterwards, so the caller can decide which secrets
// are importable.
func importApps(ctx context.Context, source, target *gorm.DB, opts module.ImportOptions, present map[uuid.UUID]struct{}) (module.TableStats, map[uuid.UUID]struct{}, error) {
	stats := module.TableStats{Table: "oauth_apps"}
	owned := make(map[uuid.UUID]struct{}, len(present))
	for id := range present {
		owned[id] = struct{}{}
	}

	// Slugs already taken by a different app are refused rather than
	// imported: the provider resolves clients by slug with a first-match
	// lookup, so a duplicate would make authorization ambiguous.
	takenSlugs, err := existingOAuthAppSlugs(ctx, target)
	if err != nil {
		return stats, nil, err
	}

	rows, err := source.WithContext(ctx).Raw(`
		SELECT id, slug, name, description, status, oauth_config, links,
		       project_id, created_at, updated_at, deleted_at
		FROM custom_apps`).Rows()
	if err != nil {
		return stats, nil, err
	}
	defer rows.Close()

	batch := make([]store.OAuthAppEntity, 0, opts.BatchSize)
	flush := func() error {
		defer func() { batch = batch[:0] }()
		if len(batch) == 0 || opts.DryRun {
			return nil
		}
		result := target.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoNothing: true,
		}).CreateInBatches(batch, opts.BatchSize)
		if result.Error != nil {
			return result.Error
		}
		stats.Written += result.RowsAffected
		return nil
	}

	for rows.Next() {
		var app sourceApp
		if err := source.ScanRows(rows, &app); err != nil {
			return stats, nil, err
		}
		stats.Read++

		if _, exists := present[app.ID]; exists {
			stats.Skipped++
			continue
		}
		if app.ProjectID == nil {
			// Develop makes project_id non-null and the target's owner
			// constraint demands exactly one owner. An app with neither
			// cannot be represented, so it is reported, not guessed at.
			stats.Skipped++
			continue
		}
		slug := strings.TrimSpace(app.Slug)
		if other, taken := takenSlugs[slug]; taken && other != app.ID {
			stats.Skipped++
			continue
		}
		takenSlugs[slug] = app.ID

		entity, err := buildOAuthApp(app)
		if err != nil {
			return stats, nil, err
		}
		owned[app.ID] = struct{}{}
		batch = append(batch, *entity)
		if len(batch) >= opts.BatchSize {
			if err := flush(); err != nil {
				return stats, nil, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return stats, nil, err
	}
	if err := flush(); err != nil {
		return stats, nil, err
	}
	return stats, owned, nil
}

// importSecrets copies the secret rows of apps the caller reports as owned
// (imported in this run or already present).
func importSecrets(ctx context.Context, source, target *gorm.DB, opts module.ImportOptions, owned map[uuid.UUID]struct{}) (module.TableStats, error) {
	stats := module.TableStats{Table: "oauth_app_secrets"}

	if !source.Migrator().HasTable(sourceSecretsTable) || !target.Migrator().HasTable("oauth_app_secrets") {
		return stats, nil
	}
	existing, err := existingSecretIDs(ctx, target)
	if err != nil {
		return stats, err
	}

	rows, err := source.WithContext(ctx).Raw(`
		SELECT id, secret, description, expired_at, is_oidc, app_id,
		       created_at, updated_at, deleted_at
		FROM custom_app_secrets`).Rows()
	if err != nil {
		return stats, err
	}
	defer rows.Close()

	batch := make([]store.OAuthAppSecretEntity, 0, opts.BatchSize)
	flush := func() error {
		defer func() { batch = batch[:0] }()
		if len(batch) == 0 || opts.DryRun {
			return nil
		}
		result := target.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoNothing: true,
		}).CreateInBatches(batch, opts.BatchSize)
		if result.Error != nil {
			return result.Error
		}
		stats.Written += result.RowsAffected
		return nil
	}

	for rows.Next() {
		var secret sourceSecret
		if err := source.ScanRows(rows, &secret); err != nil {
			return stats, err
		}
		stats.Read++
		if _, exists := existing[secret.ID]; exists {
			stats.Skipped++
			continue
		}
		if _, ok := owned[secret.AppID]; !ok {
			stats.Skipped++
			continue
		}
		entity := store.OAuthAppSecretEntity{
			ID: secret.ID,
			EntityBase: store.EntityBase{
				CreatedAt: secret.CreatedAt,
				UpdatedAt: secret.UpdatedAt,
			},
			Secret:    secret.Secret,
			IsOidc:    secret.IsOidc,
			ExpiredAt: secret.ExpiredAt,
			AppID:     secret.AppID,
		}
		if secret.Description != nil {
			entity.Description = *secret.Description
		}
		if secret.DeletedAt != nil {
			entity.DeletedAt = gorm.DeletedAt{Time: *secret.DeletedAt, Valid: true}
		}
		batch = append(batch, entity)
		if len(batch) >= opts.BatchSize {
			if err := flush(); err != nil {
				return stats, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return stats, err
	}
	if err := flush(); err != nil {
		return stats, err
	}
	return stats, nil
}

// existingOAuthAppIDs loads the target's app ids, including soft-deleted
// ones, so a re-run reports its work as skipped instead of re-inserting.
func existingOAuthAppIDs(ctx context.Context, target *gorm.DB) (map[uuid.UUID]struct{}, error) {
	ids := make(map[uuid.UUID]struct{})
	if !target.Migrator().HasTable("oauth_apps") {
		return ids, nil
	}
	var rows []uuid.UUID
	if err := target.WithContext(ctx).Unscoped().
		Model(&store.OAuthAppEntity{}).Pluck("id", &rows).Error; err != nil {
		return nil, err
	}
	for _, id := range rows {
		ids[id] = struct{}{}
	}
	return ids, nil
}

func existingOAuthAppSlugs(ctx context.Context, target *gorm.DB) (map[string]uuid.UUID, error) {
	taken := make(map[string]uuid.UUID)
	if !target.Migrator().HasTable("oauth_apps") {
		return taken, nil
	}
	var rows []struct {
		ID   uuid.UUID
		Slug string
	}
	if err := target.WithContext(ctx).Unscoped().
		Model(&store.OAuthAppEntity{}).
		Select("id", "slug").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		taken[strings.TrimSpace(row.Slug)] = row.ID
	}
	return taken, nil
}

func existingSecretIDs(ctx context.Context, target *gorm.DB) (map[uuid.UUID]struct{}, error) {
	ids := make(map[uuid.UUID]struct{})
	var rows []uuid.UUID
	if err := target.WithContext(ctx).Unscoped().
		Model(&store.OAuthAppSecretEntity{}).Pluck("id", &rows).Error; err != nil {
		return nil, err
	}
	for _, id := range rows {
		ids[id] = struct{}{}
	}
	return ids, nil
}

func buildOAuthApp(app sourceApp) (*store.OAuthAppEntity, error) {
	oauthConfig, err := parseJSONObject(app.OauthConfig)
	if err != nil {
		return nil, fmt.Errorf("app %s: oauth_config: %w", app.ID, err)
	}
	links, err := parseJSONObject(app.Links)
	if err != nil {
		return nil, fmt.Errorf("app %s: links: %w", app.ID, err)
	}

	entity := &store.OAuthAppEntity{
		ID: app.ID,
		EntityBase: store.EntityBase{
			CreatedAt: app.CreatedAt,
			UpdatedAt: app.UpdatedAt,
		},
		Slug:        strings.TrimSpace(app.Slug),
		Name:        app.Name,
		Description: derefString(app.Description),
		Status:      app.Status,
		ProjectID:   app.ProjectID,
		OauthConfig: oauthConfigFromJSON(oauthConfig),
		Links: store.OAuthAppLinksEntity{
			HomePage:       links.str("home_page"),
			PrivacyPolicy:  links.str("privacy_policy"),
			TermsOfService: links.str("terms_of_service"),
		},
	}
	if app.DeletedAt != nil {
		entity.DeletedAt = gorm.DeletedAt{Time: *app.DeletedAt, Valid: true}
	}
	return entity, nil
}

// oauthConfigFromJSON maps a source oauth_config onto the target shape.
//
// A null oauth_config imports empty, because Develop's provider reads
// app.OauthConfig?.AllowedScopes and therefore sees no scopes for such a row;
// filling in defaults would grant authorization the client never had. Inside
// a present object, an absent key falls back to the C# property initializer
// on SnCustomAppOauthConfig, which is what Develop itself reads.
func oauthConfigFromJSON(object jsonObject) store.OAuthAppOauthConfigEntity {
	if object == nil {
		return store.OAuthAppOauthConfigEntity{}
	}
	config := store.OAuthAppOauthConfigEntity{
		AllowedScopes:     []string{"openid", "profile", "email"},
		AllowedGrantTypes: []string{"authorization_code", "refresh_token"},
		RequirePkce:       true,
	}
	config.ClientUri = object.str("client_uri")
	config.RedirectUris = object.strList("redirect_uris")
	config.PostLogoutRedirectUris = object.strList("post_logout_redirect_uris")
	if scopes, present := object.strListPresent("allowed_scopes"); present {
		config.AllowedScopes = scopes
	}
	if grants, present := object.strListPresent("allowed_grant_types"); present {
		config.AllowedGrantTypes = grants
	}
	if value, present := object.boolean("require_pkce"); present {
		config.RequirePkce = value
	}
	if value, present := object.boolean("allow_offline_access"); present {
		config.AllowOfflineAccess = value
	}
	if value, present := object.boolean("is_public_client"); present {
		config.IsPublicClient = value
	}
	return config
}

// ── Tolerant jsonb decoding ────────────────────────────────────────────────
//
// Develop persists these objects with System.Text.Json, whose key casing is
// decided by EF Core's JSON options rather than by the project's snake_case
// naming convention. Normalizing keys (lower case, underscores dropped) lets
// the importer read either `client_uri` or `ClientUri` without asserting
// which one the deployed schema holds.

type jsonObject map[string]json.RawMessage

func parseJSONObject(raw *string) (jsonObject, error) {
	if raw == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return nil, err
	}
	object := make(jsonObject, len(decoded))
	for key, value := range decoded {
		object[normalizeJSONKey(key)] = value
	}
	return object, nil
}

func normalizeJSONKey(key string) string {
	return strings.ToLower(strings.ReplaceAll(key, "_", ""))
}

func (o jsonObject) raw(names ...string) (json.RawMessage, bool) {
	if o == nil {
		return nil, false
	}
	for _, name := range names {
		if value, ok := o[normalizeJSONKey(name)]; ok {
			return value, true
		}
	}
	return nil, false
}

func (o jsonObject) str(names ...string) string {
	value, ok := o.raw(names...)
	if !ok {
		return ""
	}
	var out string
	if err := json.Unmarshal(value, &out); err != nil {
		return ""
	}
	return out
}

// strListPresent distinguishes an explicit null (present, empty result) from
// an absent key (not present), which callers need in order to apply only the
// defaults Develop itself would apply.
func (o jsonObject) strListPresent(names ...string) ([]string, bool) {
	value, ok := o.raw(names...)
	if !ok {
		return nil, false
	}
	var out []string
	if err := json.Unmarshal(value, &out); err != nil {
		return nil, false
	}
	return out, true
}

func (o jsonObject) strList(names ...string) []string {
	out, _ := o.strListPresent(names...)
	return out
}

func (o jsonObject) boolean(names ...string) (bool, bool) {
	value, ok := o.raw(names...)
	if !ok {
		return false, false
	}
	var out bool
	if err := json.Unmarshal(value, &out); err != nil {
		return false, false
	}
	return out, true
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
