package store

// Round-trip tests for the oauth_apps tables owned by app.solian.id.oauthapps.
// They skip when Postgres is unavailable, like the other store tests.

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

func oauthAppStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("STARGATE_TEST_DSN")
	if dsn == "" {
		t.Skip("STARGATE_TEST_DSN is not configured")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}

	st := New(pool)
	if err := st.DB.AutoMigrate(&OAuthAppEntity{}, &OAuthAppSecretEntity{}); err != nil {
		t.Fatalf("migrate oauth tables: %v", err)
	}
	t.Cleanup(func() {
		st.DB.Exec(`DELETE FROM oauth_app_secrets`)
		st.DB.Exec(`DELETE FROM oauth_apps`)
	})
	return st
}

func sampleApp(id, slug, accountID string) *model.OAuthApp {
	owner := accountID
	return &model.OAuthApp{
		Id: id, Slug: slug, Name: "Integration Test App",
		Description: "created by the oauthapps store test",
		Status:      2,
		AccountId:   &owner,
		OauthConfig: model.OAuthAppOauthConfig{
			ClientUri:              "https://client.example",
			RedirectUris:           []string{"https://client.example/callback"},
			PostLogoutRedirectUris: []string{"https://client.example/logout"},
			AllowedScopes:          []string{"openid", "profile"},
			AllowedGrantTypes:      []string{"authorization_code", "refresh_token"},
			RequirePkce:            true,
		},
		Links: model.OAuthAppLinks{
			HomePage:       "https://client.example",
			PrivacyPolicy:  "https://client.example/privacy",
			TermsOfService: "https://client.example/terms",
		},
	}
}

func TestOAuthAppStoreRoundTrip(t *testing.T) {
	st := oauthAppStore(t)
	ctx := context.Background()
	accountID := "55555555-5555-5555-5555-555555555555"
	app := sampleApp("66666666-6666-6666-6666-666666666666", "integration-app", accountID)

	if err := st.CreateOAuthApp(ctx, app); err != nil {
		t.Fatalf("create: %v", err)
	}

	loaded, err := st.GetOAuthAppByIdentifier(ctx, app.Id)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if loaded.Slug != app.Slug || loaded.AccountId == nil || *loaded.AccountId != accountID {
		t.Fatalf("loaded app = %+v", loaded)
	}
	if loaded.ProjectId != nil {
		t.Fatalf("individually owned app has project id %v", *loaded.ProjectId)
	}
	if len(loaded.OauthConfig.RedirectUris) != 1 || loaded.OauthConfig.RedirectUris[0] != "https://client.example/callback" {
		t.Fatalf("redirect uris = %v", loaded.OauthConfig.RedirectUris)
	}
	if loaded.Links.PrivacyPolicy != "https://client.example/privacy" {
		t.Fatalf("links = %+v", loaded.Links)
	}

	bySlug, err := st.GetOAuthAppBySlug(ctx, "integration-app")
	if err != nil || bySlug.Id != app.Id {
		t.Fatalf("get by slug = %+v, %v", bySlug, err)
	}

	owned, err := st.ListOAuthApps(ctx, &accountID, nil)
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 1 || owned[0].Id != app.Id {
		t.Fatalf("owned apps = %+v", owned)
	}

	// A slug stays reserved after a soft delete, so it cannot be silently
	// reused by a different owner.
	if count, err := st.CountOAuthAppsWithSlug(ctx, "integration-app"); err != nil || count != 1 {
		t.Fatalf("slug count = %d, %v", count, err)
	}
	// Updating writes the jsonb columns through a map, which GORM must
	// serialize correctly.
	loaded.Name = "Renamed App"
	loaded.OauthConfig.AllowedScopes = []string{"openid", "profile", "email"}
	loaded.OauthConfig.RedirectUris = []string{"https://client.example/new-callback"}
	loaded.Links.TermsOfService = "https://client.example/terms-v2"
	if err := st.UpdateOAuthApp(ctx, loaded); err != nil {
		t.Fatalf("update: %v", err)
	}
	updated, err := st.GetOAuthAppByIdentifier(ctx, app.Id)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if updated.Name != "Renamed App" {
		t.Fatalf("name after update = %q", updated.Name)
	}
	if len(updated.OauthConfig.AllowedScopes) != 3 {
		t.Fatalf("scopes after update = %v", updated.OauthConfig.AllowedScopes)
	}
	if len(updated.OauthConfig.RedirectUris) != 1 || updated.OauthConfig.RedirectUris[0] != "https://client.example/new-callback" {
		t.Fatalf("redirects after update = %v", updated.OauthConfig.RedirectUris)
	}
	if updated.Links.TermsOfService != "https://client.example/terms-v2" {
		t.Fatalf("links after update = %+v", updated.Links)
	}
	if err := st.DeleteOAuthApp(ctx, app.Id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.GetOAuthAppByID(ctx, app.Id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete = %v; want ErrNotFound", err)
	}

	if count, err := st.CountOAuthAppsWithSlug(ctx, "integration-app"); err != nil || count != 1 {
		t.Fatalf("slug count after delete = %d, %v", count, err)
	}
}

func TestOAuthAppSecretLifecycle(t *testing.T) {
	st := oauthAppStore(t)
	ctx := context.Background()
	app := sampleApp("77777777-7777-7777-7777-777777777777", "secret-app", "88888888-8888-8888-8888-888888888888")
	if err := st.CreateOAuthApp(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	expires := model.NewTime(time.Now().UTC().Add(time.Hour))
	secret := &model.OAuthAppSecret{
		Id: "99999999-9999-9999-9999-999999999999", AppId: app.Id,
		Secret: "initial-secret", Description: "test", IsOidc: true, ExpiredAt: expires,
	}
	if err := st.CreateOAuthAppSecret(ctx, secret); err != nil {
		t.Fatalf("create secret: %v", err)
	}

	valid, err := st.FindValidOAuthAppSecret(ctx, app.Id, "initial-secret", true)
	if err != nil || valid == nil {
		t.Fatalf("valid secret lookup = %+v, %v", valid, err)
	}
	// An OIDC secret must not authenticate as an API key.
	if mismatched, err := st.FindValidOAuthAppSecret(ctx, app.Id, "initial-secret", false); err != nil || mismatched != nil {
		t.Fatalf("api-key lookup of an oidc secret = %+v, %v", mismatched, err)
	}
	if wrong, err := st.FindValidOAuthAppSecret(ctx, app.Id, "not-the-secret", true); err != nil || wrong != nil {
		t.Fatalf("wrong secret lookup = %+v, %v", wrong, err)
	}

	listed, err := st.ListOAuthAppSecrets(ctx, app.Id)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list secrets = %+v, %v", listed, err)
	}
	if listed[0].Secret != "" {
		t.Fatal("list returned the plaintext secret")
	}
	if !listed[0].IsOidc {
		t.Fatal("secret is_oidc not persisted")
	}

	rotated, err := st.RotateOAuthAppSecret(ctx, app.Id, secret.Id, "rotated-secret", "rotated")
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rotated.Secret != "rotated-secret" {
		t.Fatalf("rotated plaintext = %q", rotated.Secret)
	}
	if stale, err := st.FindValidOAuthAppSecret(ctx, app.Id, "initial-secret", true); err != nil || stale != nil {
		t.Fatalf("old secret still valid = %+v, %v", stale, err)
	}
	if fresh, err := st.FindValidOAuthAppSecret(ctx, app.Id, "rotated-secret", true); err != nil || fresh == nil {
		t.Fatalf("rotated secret lookup = %+v, %v", fresh, err)
	}

	if err := st.DeleteOAuthAppSecret(ctx, app.Id, secret.Id); err != nil {
		t.Fatalf("delete secret: %v", err)
	}
	if after, err := st.FindValidOAuthAppSecret(ctx, app.Id, "rotated-secret", true); err != nil || after != nil {
		t.Fatalf("deleted secret still valid = %+v, %v", after, err)
	}
}
