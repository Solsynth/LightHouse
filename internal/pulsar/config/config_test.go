package config

import "testing"

func TestNormalizeBangumiDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.BaseUrl = "https://pulsar.example/"
	cfg.SiteUrl = "https://solian.example"
	cfg.Metadata.UserAgent = "Pulsar/1.0 (contact@example.com)"
	cfg.Metadata.BangumiBaseURL = "https://api.bgm.tv"

	normalizeBangumi(cfg)

	if cfg.Bangumi.AuthorizeURL != "https://bgm.tv/oauth/authorize" {
		t.Fatalf("authorize = %q", cfg.Bangumi.AuthorizeURL)
	}
	if cfg.Bangumi.TokenURL != "https://bgm.tv/oauth/access_token" {
		t.Fatalf("token = %q", cfg.Bangumi.TokenURL)
	}
	if cfg.Bangumi.APIBaseURL != "https://api.bgm.tv" {
		t.Fatalf("api = %q", cfg.Bangumi.APIBaseURL)
	}
	if cfg.Bangumi.UserAgent != cfg.Metadata.UserAgent {
		t.Fatalf("user agent = %q", cfg.Bangumi.UserAgent)
	}
	if cfg.Bangumi.RedirectURL != "https://pulsar.example/api/bangumi/oauth/callback" {
		t.Fatalf("redirect = %q", cfg.Bangumi.RedirectURL)
	}
	if cfg.Bangumi.PostAuthRedirect != "https://solian.example" {
		t.Fatalf("post auth redirect = %q", cfg.Bangumi.PostAuthRedirect)
	}
	if cfg.Bangumi.SyncIntervalMinutes != 30 {
		t.Fatalf("interval = %d", cfg.Bangumi.SyncIntervalMinutes)
	}
	if cfg.Bangumi.ConflictPolicy != "remote" {
		t.Fatalf("policy = %q", cfg.Bangumi.ConflictPolicy)
	}
}

func TestNormalizeBangumiPreservesExplicitValues(t *testing.T) {
	cfg := &Config{}
	cfg.BaseUrl = "https://pulsar.example"
	cfg.Bangumi.RedirectURL = "https://custom.example/callback"
	cfg.Bangumi.AuthorizeURL = "https://auth.example/authorize"
	cfg.Bangumi.TokenURL = "https://auth.example/token"
	cfg.Bangumi.APIBaseURL = "https://api.example"
	cfg.Bangumi.UserAgent = "Custom/1.0"
	cfg.Bangumi.PostAuthRedirect = "https://app.example/done"
	cfg.Bangumi.SyncIntervalMinutes = -1
	cfg.Bangumi.ConflictPolicy = "LOCAL"

	normalizeBangumi(cfg)

	if cfg.Bangumi.RedirectURL != "https://custom.example/callback" ||
		cfg.Bangumi.AuthorizeURL != "https://auth.example/authorize" ||
		cfg.Bangumi.TokenURL != "https://auth.example/token" ||
		cfg.Bangumi.APIBaseURL != "https://api.example" ||
		cfg.Bangumi.UserAgent != "Custom/1.0" ||
		cfg.Bangumi.PostAuthRedirect != "https://app.example/done" {
		t.Fatalf("explicit values changed: %+v", cfg.Bangumi)
	}
	if cfg.Bangumi.SyncIntervalMinutes != -1 {
		t.Fatalf("negative interval must disable the job, got %d", cfg.Bangumi.SyncIntervalMinutes)
	}
	if cfg.Bangumi.ConflictPolicy != "local" {
		t.Fatalf("policy = %q", cfg.Bangumi.ConflictPolicy)
	}
}

func TestExampleConfigurationParsesBangumi(t *testing.T) {
	cfg, err := Load("../config.example.toml")
	if err != nil {
		t.Fatalf("load example: %v", err)
	}
	if cfg.Bangumi.AuthorizeURL == "" || cfg.Bangumi.TokenURL == "" || cfg.Bangumi.APIBaseURL == "" {
		t.Fatalf("bangumi defaults missing: %+v", cfg.Bangumi)
	}
	if cfg.Bangumi.SyncIntervalMinutes == 0 {
		t.Fatal("bangumi sync interval was not defaulted")
	}
}
