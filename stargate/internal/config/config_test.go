package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadLocalOAuthClients(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `[oidcProvider]
[[oidcProvider.clients]]
id = "local-client-id"
slug = "local-client"
name = "Local Client"
clientSecret = "local-secret"
status = 2
redirectUris = ["https://client.example/callback"]
allowedScopes = ["openid", "profile"]
isPublicClient = false
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	client := cfg.FindLocalOAuthClient("local-client")
	if client == nil {
		t.Fatal("local OAuth client was not loaded")
	}
	if client.Id != "local-client-id" || client.ClientSecret != "local-secret" {
		t.Fatalf("loaded client = %+v", client)
	}
	if len(client.RedirectUris) != 1 || client.RedirectUris[0] != "https://client.example/callback" {
		t.Fatalf("redirect URIs = %v", client.RedirectUris)
	}
}
