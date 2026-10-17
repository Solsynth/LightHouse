package oauthapps

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"src.solsynth.dev/sosys/stargate/internal/dbtest"
	"src.solsynth.dev/sosys/stargate/internal/module"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// TestOAuthAppImport covers the Develop -> Stargate registry move end to end
// against real Postgres instances: the dry run writes nothing, the import
// preserves ids, jsonb config, links, soft deletes and secret kinds, and a
// second run is a no-op.
func TestOAuthAppImport(t *testing.T) {
	baseDSN := os.Getenv("STARGATE_TEST_DSN")
	if baseDSN == "" {
		t.Skip("STARGATE_TEST_DSN is not configured")
	}
	ctx := context.Background()

	sourceDSN, cleanupSource, err := dbtest.NewDatabase(ctx, baseDSN)
	if err != nil {
		t.Skipf("cannot create source database: %v", err)
	}
	t.Cleanup(cleanupSource)
	targetDSN, cleanupTarget, err := dbtest.NewDatabase(ctx, baseDSN)
	if err != nil {
		t.Skipf("cannot create target database: %v", err)
	}
	t.Cleanup(cleanupTarget)

	source, err := connect(ctx, sourceDSN)
	if err != nil {
		t.Fatal(err)
	}
	target, err := connect(ctx, targetDSN)
	if err != nil {
		t.Fatal(err)
	}

	seedDevelopSource(t, ctx, source)
	if err := module.MigrateSchema(ctx, target, []module.Factory{Factory()}); err != nil {
		t.Fatalf("migrate target schema: %v", err)
	}
	seedTargetConflict(t, ctx, target)

	// Dry run: reads everything, writes nothing.
	dry, err := (&importer{}).Import(ctx, source, target, module.ImportOptions{DryRun: true, BatchSize: 2})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	// 7 apps + 3 secrets are read; 2 apps are declined for the reasons
	// asserted below, and the dry run writes none of them.
	if dry.Read != 10 {
		t.Fatalf("dry run read = %d; want 10", dry.Read)
	}
	if dry.Written != 0 {
		t.Fatalf("dry run wrote = %d; want 0", dry.Written)
	}
	if dry.Skipped != 3 {
		t.Fatalf("dry run skipped = %d; want 3", dry.Skipped)
	}
	for _, table := range dry.Tables {
		if table.Written != 0 {
			t.Fatalf("dry run wrote to %s: %d", table.Table, table.Written)
		}
	}
	// The only app present is the one seeded before the run.
	assertCount(t, target, "oauth_apps", 1)
	assertCount(t, target, "oauth_app_secrets", 0)

	stats, err := (&importer{}).Import(ctx, source, target, module.ImportOptions{BatchSize: 2})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	// 10 read: five importable apps, one without a project, one whose slug
	// is taken by a pre-existing app, and three secrets.
	if stats.Read != 10 {
		t.Fatalf("read = %d; want 10", stats.Read)
	}
	if stats.Skipped != 3 {
		t.Fatalf("skipped = %d; want 3 (no project, slug taken)", stats.Skipped)
	}
	if len(stats.Tables) != 2 {
		t.Fatalf("per-table breakdown = %+v", stats.Tables)
	}
	assertCount(t, target, "oauth_apps", 5)
	assertCount(t, target, "oauth_app_secrets", 3)

	// Ids, status, project owner and soft delete survive the move.
	var status int
	var deletedAt *time.Time
	err = target.Raw(`SELECT status, deleted_at FROM oauth_apps WHERE id = ?`,
		"aaaaaaaa-0000-0000-0000-00000000000d").Row().Scan(&status, &deletedAt)
	if err != nil {
		t.Fatalf("read soft-deleted app: %v", err)
	}
	if status != 3 {
		t.Fatalf("status = %d; want 3 (suspended)", status)
	}
	if deletedAt == nil {
		t.Fatal("soft-deleted app lost its deleted_at")
	}

	// oauth_config keeps its snake_case keys and values, and an absent
	// allow_offline_access falls back to Develop's default.
	var clientURI string
	var scopes string
	if err := target.Raw(`
		SELECT oauth_config->>'client_uri',
		       oauth_config->'allowed_scopes'
		FROM oauth_apps WHERE id = ?`,
		"aaaaaaaa-0000-0000-0000-000000000001").Row().Scan(&clientURI, &scopes); err != nil {
		t.Fatalf("read oauth_config: %v", err)
	}
	if clientURI != "https://client.example" {
		t.Fatalf("client_uri = %q", clientURI)
	}
	if scopes != `["openid", "profile"]` {
		t.Fatalf("allowed_scopes = %s", scopes)
	}

	// A config that stored PascalCase keys is read as well.
	var pascalScopes string
	if err := target.Raw(`SELECT oauth_config->'allowed_scopes' FROM oauth_apps WHERE id = ?`,
		"aaaaaaaa-0000-0000-0000-000000000002").Row().Scan(&pascalScopes); err != nil {
		t.Fatalf("read pascal-case config: %v", err)
	}
	if pascalScopes != `["openid", "email"]` {
		t.Fatalf("pascal-case allowed_scopes = %s", pascalScopes)
	}

	// A soft-deleted app is invisible to the store's normal queries, so the
	// provider never resolves it even though the row survived.
	if _, err := (&store.Store{DB: target}).GetOAuthAppByID(ctx, "aaaaaaaa-0000-0000-0000-00000000000d"); err == nil {
		t.Fatal("soft-deleted app is still resolvable")
	}

	// A null oauth_config stays empty rather than gaining default scopes.
	// Develop's provider reads through a null config and sees no scopes, so
	// the imported app must be equally unusable as a client.
	nullApp, err := (&store.Store{DB: target}).GetOAuthAppByID(ctx, "aaaaaaaa-0000-0000-0000-000000000003")
	if err != nil {
		t.Fatalf("read null-config app: %v", err)
	}
	if len(nullApp.OauthConfig.AllowedScopes) != 0 || len(nullApp.OauthConfig.RedirectUris) != 0 {
		t.Fatalf("null oauth_config gained authorization: %+v", nullApp.OauthConfig)
	}

	// Links carry over under their snake_case names.
	var homePage string
	if err := target.Raw(`SELECT links->>'home_page' FROM oauth_apps WHERE id = ?`,
		"aaaaaaaa-0000-0000-0000-000000000001").Row().Scan(&homePage); err != nil {
		t.Fatalf("read links: %v", err)
	}
	if homePage != "https://client.example" {
		t.Fatalf("home_page = %q", homePage)
	}

	// Secrets keep their kind, description, expiry and plaintext.
	var isOidc bool
	var secret string
	if err := target.Raw(`SELECT is_oidc, secret FROM oauth_app_secrets WHERE id = ?`,
		"bbbbbbbb-0000-0000-0000-000000000001").Row().Scan(&isOidc, &secret); err != nil {
		t.Fatalf("read secret: %v", err)
	}
	if !isOidc || secret != "oidc-secret-value" {
		t.Fatalf("secret = %q is_oidc = %v", secret, isOidc)
	}
	// The api-key secret is not an OIDC client secret.
	if err := target.Raw(`SELECT is_oidc FROM oauth_app_secrets WHERE id = ?`,
		"bbbbbbbb-0000-0000-0000-000000000002").Row().Scan(&isOidc); err != nil {
		t.Fatalf("read api key: %v", err)
	}
	if isOidc {
		t.Fatal("api key imported as an OIDC secret")
	}
	// The secret of the app without a project is not imported, because its
	// app is not.
	assertAbsent(t, target, "oauth_app_secrets", "bbbbbbbb-0000-0000-0000-000000000004")

	// The pre-existing app keeps its own row: the importer never overwrites
	// a slug or id the target already holds.
	var name string
	if err := target.Raw(`SELECT name FROM oauth_apps WHERE id = ?`,
		"cccccccc-0000-0000-0000-000000000001").Row().Scan(&name); err != nil {
		t.Fatalf("read pre-existing app: %v", err)
	}
	if name != "Already Here" {
		t.Fatalf("pre-existing app name = %q", name)
	}

	// Re-run: nothing duplicates, nothing errors.
	again, err := (&importer{}).Import(ctx, source, target, module.ImportOptions{BatchSize: 3})
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if again.Written != 0 {
		t.Fatalf("re-import wrote = %d; want 0", again.Written)
	}
	if again.Skipped != again.Read {
		t.Fatalf("re-import skipped = %d of %d read; want all", again.Skipped, again.Read)
	}
	assertCount(t, target, "oauth_apps", 5)
	assertCount(t, target, "oauth_app_secrets", 3)

	// The imported rows resolve through the very source the provider uses:
	// slug lookup finds the client and the imported plaintext verifies.
	resolver := newTestSource(target)
	client, err := resolver.GetBySlug(ctx, "alpha")
	if err != nil {
		t.Fatalf("resolve imported client by slug: %v", err)
	}
	if client == nil {
		t.Fatal("imported client is not resolvable by slug")
	}
	if client.ID != "aaaaaaaa-0000-0000-0000-000000000001" || client.Name != "Alpha" {
		t.Fatalf("resolved client = %+v", client)
	}
	if len(client.RedirectURIs) != 1 || client.RedirectURIs[0] != "https://client.example/cb" {
		t.Fatalf("resolved redirect URIs = %v", client.RedirectURIs)
	}
	valid, err := resolver.CheckSecret(ctx, client.ID, "oidc-secret-value")
	if err != nil {
		t.Fatalf("verify imported secret: %v", err)
	}
	if !valid {
		t.Fatal("imported OIDC secret does not verify")
	}
	// An OIDC client secret must not be accepted as an API key or vice versa.
	if valid, err := resolver.CheckSecret(ctx, client.ID, "api-key-value"); err != nil || valid {
		t.Fatalf("api key accepted as an OIDC secret: %v %v", valid, err)
	}
	// A soft-deleted client stays unresolvable after the move.
	if gone, err := resolver.GetBySlug(ctx, "delta"); err != nil || gone != nil {
		t.Fatalf("soft-deleted client resolved: %+v %v", gone, err)
	}

	// Progress is recorded per table.
	for _, table := range []string{"oauth_apps", "oauth_app_secrets"} {
		var recorded int64
		if err := target.Raw(`SELECT count(*) FROM module_import_state WHERE module = ? AND table_name = ?`,
			moduleID, table).Row().Scan(&recorded); err != nil {
			t.Fatalf("read import state: %v", err)
		}
		if recorded != 1 {
			t.Fatalf("module_import_state rows for %s = %d; want 1", table, recorded)
		}
	}
}

// TestOAuthAppImportMissingSourceSchema reports a clear error rather than
// half-importing when the source is not a Develop database.
func TestOAuthAppImportMissingSourceSchema(t *testing.T) {
	baseDSN := os.Getenv("STARGATE_TEST_DSN")
	if baseDSN == "" {
		t.Skip("STARGATE_TEST_DSN is not configured")
	}
	ctx := context.Background()
	dsn, cleanup, err := dbtest.NewDatabase(ctx, baseDSN)
	if err != nil {
		t.Skipf("cannot create database: %v", err)
	}
	t.Cleanup(cleanup)
	db, err := connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&importer{}).Import(ctx, db, db, module.ImportOptions{}); err == nil {
		t.Fatal("import succeeded without a source schema")
	}
}

func seedDevelopSource(t *testing.T, ctx context.Context, db *gorm.DB) {
	t.Helper()
	statements := []string{
		`CREATE TABLE custom_apps (
			id uuid PRIMARY KEY,
			slug varchar(1024) NOT NULL,
			name varchar(1024) NOT NULL,
			description varchar(4096),
			status integer NOT NULL DEFAULT 0,
			oauth_config jsonb,
			links jsonb,
			project_id uuid,
			created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now(),
			deleted_at timestamptz
		)`,
		`CREATE TABLE custom_app_secrets (
			id uuid PRIMARY KEY,
			secret varchar(1024) NOT NULL,
			description varchar(4096),
			expired_at timestamptz,
			is_oidc boolean NOT NULL DEFAULT false,
			app_id uuid NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now(),
			deleted_at timestamptz
		)`,
		// Fully specified, snake_case keys.
		`INSERT INTO custom_apps (id, slug, name, description, status, oauth_config, links, project_id) VALUES (
			'aaaaaaaa-0000-0000-0000-000000000001', 'alpha', 'Alpha', 'first', 2,
			'{"client_uri":"https://client.example","redirect_uris":["https://client.example/cb"],"allowed_scopes":["openid","profile"],"allowed_grant_types":["authorization_code"],"require_pkce":true}'::jsonb,
			'{"home_page":"https://client.example","privacy_policy":"https://client.example/privacy","terms_of_service":null}'::jsonb,
			'11111111-0000-0000-0000-000000000001')`,
		// Keys in the casing System.Text.Json may have written.
		`INSERT INTO custom_apps (id, slug, name, status, oauth_config, project_id) VALUES (
			'aaaaaaaa-0000-0000-0000-000000000002', 'beta', 'Beta', 0,
			'{"ClientUri":"https://beta.example","AllowedScopes":["openid","email"],"RequirePkce":false}'::jsonb,
			'11111111-0000-0000-0000-000000000001')`,
		// No oauth_config at all.
		`INSERT INTO custom_apps (id, slug, name, status, project_id) VALUES (
			'aaaaaaaa-0000-0000-0000-000000000003', 'gamma', 'Gamma', 1,
			'11111111-0000-0000-0000-000000000001')`,
		// Soft-deleted in Develop.
		`INSERT INTO custom_apps (id, slug, name, status, oauth_config, project_id, deleted_at) VALUES (
			'aaaaaaaa-0000-0000-0000-00000000000d', 'delta', 'Delta', 3,
			'{"redirect_uris":["https://delta.example/cb"],"allowed_scopes":["openid"]}'::jsonb,
			'11111111-0000-0000-0000-000000000001', now())`,
		// Same slug as an app the target already holds: must be skipped.
		`INSERT INTO custom_apps (id, slug, name, status, oauth_config, project_id) VALUES (
			'aaaaaaaa-0000-0000-0000-00000000000e', 'taken', 'Taken', 0,
			'{"redirect_uris":["https://taken.example/cb"],"allowed_scopes":["openid"]}'::jsonb,
			'11111111-0000-0000-0000-000000000001')`,
		// No project: not representable in the target and not an OAuth
		// client either.
		`INSERT INTO custom_apps (id, slug, name, status, oauth_config) VALUES (
			'aaaaaaaa-0000-0000-0000-00000000000f', 'orphan', 'Orphan', 0,
			'{"redirect_uris":["https://orphan.example/cb"],"allowed_scopes":["openid"]}'::jsonb)`,
		// Secrets of the importable app.
		`INSERT INTO custom_app_secrets (id, secret, description, is_oidc, app_id, expired_at) VALUES
			('bbbbbbbb-0000-0000-0000-000000000001', 'oidc-secret-value', 'primary', true,
			 'aaaaaaaa-0000-0000-0000-000000000001', now() + interval '30 days'),
			('bbbbbbbb-0000-0000-0000-000000000002', 'api-key-value', 'widget key', false,
			 'aaaaaaaa-0000-0000-0000-000000000001', NULL)`,
		// Secret whose app is not imported.
		`INSERT INTO custom_app_secrets (id, secret, description, is_oidc, app_id) VALUES
			('bbbbbbbb-0000-0000-0000-000000000004', 'orphan-secret', 'orphan', true,
			 'aaaaaaaa-0000-0000-0000-00000000000f')`,
		// Secret of the soft-deleted app: soft delete carries over.
		`INSERT INTO custom_app_secrets (id, secret, description, is_oidc, app_id, deleted_at) VALUES
			('bbbbbbbb-0000-0000-0000-000000000003', 'retired-secret', 'retired', true,
			 'aaaaaaaa-0000-0000-0000-00000000000d', now())`,
	}
	for _, statement := range statements {
		if err := db.WithContext(ctx).Exec(statement).Error; err != nil {
			t.Fatalf("seed source: %v\n%s", err, statement)
		}
	}
}

func seedTargetConflict(t *testing.T, ctx context.Context, db *gorm.DB) {
	t.Helper()
	// An app that already owns the slug 'taken', created outside the import.
	id := uuid.MustParse("cccccccc-0000-0000-0000-000000000001")
	projectID := uuid.MustParse("11111111-0000-0000-0000-000000000001")
	err := db.WithContext(ctx).Exec(`
		INSERT INTO oauth_apps (id, slug, name, description, status, project_id,
			oauth_config, links, created_at, updated_at)
		VALUES (?, 'taken', 'Already Here', '', 2, ?,
			'{"redirect_uris":["https://existing.example/cb"],"allowed_scopes":["openid"]}'::jsonb,
			'{}'::jsonb, now(), now())`, id, projectID).Error
	if err != nil {
		t.Fatalf("seed target: %v", err)
	}
}

func assertCount(t *testing.T, db *gorm.DB, table string, want int64) {
	t.Helper()
	var count int64
	if err := db.Table(table).Count(&count).Error; err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if count != want {
		t.Fatalf("%s rows = %d; want %d", table, count, want)
	}
}

func assertAbsent(t *testing.T, db *gorm.DB, table, id string) {
	t.Helper()
	var count int64
	if err := db.Table(table).Where("id = ?", id).Count(&count).Error; err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if count != 0 {
		t.Fatalf("%s unexpectedly holds %s", table, id)
	}
}

// newTestSource builds the module's own client source, so the assertions run
// through the same resolution path the provider uses. It takes the database
// as an argument because the caller's parameter named `source` shadows the
// package-level type.
func newTestSource(db *gorm.DB) *source {
	return &source{store: &store.Store{DB: db}}
}

func connect(ctx context.Context, dsn string) (*gorm.DB, error) {
	return gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
}
