package config

import "testing"

func TestNormalizeBangumiDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.Metadata.UserAgent = "Pulsar/1.0 (contact@example.com)"
	cfg.Metadata.BangumiBaseURL = "https://api.bgm.tv"

	normalizeBangumi(cfg)

	if cfg.Bangumi.APIBaseURL != "https://api.bgm.tv" {
		t.Fatalf("api = %q", cfg.Bangumi.APIBaseURL)
	}
	if cfg.Bangumi.UserAgent != cfg.Metadata.UserAgent {
		t.Fatalf("user agent = %q", cfg.Bangumi.UserAgent)
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
	cfg.Bangumi.APIBaseURL = "https://api.example"
	cfg.Bangumi.UserAgent = "Custom/1.0"
	cfg.Bangumi.SyncIntervalMinutes = -1
	cfg.Bangumi.ConflictPolicy = "LOCAL"

	normalizeBangumi(cfg)

	if cfg.Bangumi.APIBaseURL != "https://api.example" || cfg.Bangumi.UserAgent != "Custom/1.0" {
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
	if cfg.Bangumi.APIBaseURL == "" {
		t.Fatalf("bangumi defaults missing: %+v", cfg.Bangumi)
	}
	if cfg.Bangumi.SyncIntervalMinutes == 0 {
		t.Fatal("bangumi sync interval was not defaulted")
	}
}

func TestNormalizeLyricsDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.Metadata.UserAgent = "Pulsar/1.0 (contact@example.com)"
	cfg.Lyrics.LRCLIBBaseURL = "https://lrclib.example/"

	normalizeLyrics(cfg)

	if cfg.Lyrics.LRCLIBBaseURL != "https://lrclib.example" {
		t.Fatalf("lrclib base = %q", cfg.Lyrics.LRCLIBBaseURL)
	}
	if cfg.Lyrics.UserAgent != cfg.Metadata.UserAgent {
		t.Fatalf("user agent = %q", cfg.Lyrics.UserAgent)
	}
	if cfg.Lyrics.PersonaBaseURL != "" {
		t.Fatalf("persona must stay disabled until configured: %q", cfg.Lyrics.PersonaBaseURL)
	}
}
