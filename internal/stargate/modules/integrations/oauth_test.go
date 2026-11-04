package integrations

import (
	"testing"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

func TestOAuthAppFromProtoRequiresOAuthConfig(t *testing.T) {
	// Nil app
	if got := OAuthAppFromProto(nil); got != nil {
		t.Fatalf("OAuthAppFromProto(nil) = %v, want nil", got)
	}

	// App without OAuth config
	appWithoutOAuth := &gen.DyCustomApp{
		Id:   "app-1",
		Slug: "app-slug",
		Name: "App Name",
	}
	if got := OAuthAppFromProto(appWithoutOAuth); got != nil {
		t.Fatalf("OAuthAppFromProto(appWithoutOAuth) = %v, want nil", got)
	}

	// App with OAuth config
	appWithOAuth := &gen.DyCustomApp{
		Id:     "app-2",
		Slug:   "oauth-slug",
		Name:   "OAuth App",
		Status: gen.DyCustomAppStatus_DY_PRODUCTION,
		OauthConfig: &gen.DyCustomAppOauthConfig{
			ClientUri:              "https://example.com",
			RedirectUris:           []string{"https://example.com/callback"},
			PostLogoutRedirectUris: []string{"https://example.com/logout"},
			AllowedScopes:          []string{"openid", "profile"},
			RequirePkce:            true,
			AllowOfflineAccess:     true,
			IsPublicClient:         false,
		},
		Picture: &gen.DyCloudFile{
			Id:   "file-pic-1",
			Name: "pic.png",
		},
		Background: &gen.DyCloudFile{
			Id:   "file-bg-1",
			Name: "bg.png",
			Url:  "https://custom.example/bg.png",
		},
	}
	got := OAuthAppFromProto(appWithOAuth)
	if got == nil {
		t.Fatal("OAuthAppFromProto(appWithOAuth) = nil, want non-nil")
	}
	if got.ID != "app-2" || got.Slug != "oauth-slug" || got.Name != "OAuth App" {
		t.Fatalf("unexpected app fields: %+v", got)
	}
	if len(got.RedirectURIs) != 1 || got.RedirectURIs[0] != "https://example.com/callback" {
		t.Fatalf("unexpected RedirectURIs: %v", got.RedirectURIs)
	}
	if !got.RequirePkce || !got.AllowOfflineAccess || got.IsPublicClient {
		t.Fatalf("unexpected flags in oauth app: %+v", got)
	}
	if got.Picture == nil || got.Picture.Url != "/drive/files/file-pic-1" {
		t.Fatalf("unexpected picture: %+v", got.Picture)
	}
	if got.Background == nil || got.Background.Url != "https://custom.example/bg.png" {
		t.Fatalf("unexpected background: %+v", got.Background)
	}
}
