package config

import (
	"strings"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/config"
)

// Config holds the Pulsar service configuration.
type Config struct {
	config.Base

	// Metadata configures the upstream services the metadata engine queries.
	// Empty endpoint values select the provider's public upstream; the values
	// exist so a deployment can point at a mirror or a local test server.
	Metadata struct {
		// UserAgent is sent to every upstream provider. The default names
		// Pulsar and carries the fleet's contact URL, which MusicBrainz's
		// terms and Bangumi's client guidance require.
		UserAgent              string `toml:"userAgent"`
		MusicBrainzBaseURL     string `toml:"musicBrainzBaseUrl"`
		MusicBrainzCoverArtURL string `toml:"musicBrainzCoverArtUrl"`
		AniListEndpoint        string `toml:"anilistEndpoint"`
		SteamBaseURL           string `toml:"steamBaseUrl"`
		// BangumiBaseURL points at the bgm.tv API. Its access token is
		// optional: the public endpoints work without one, but a token raises
		// the account's quota and lets NSFW subjects be fetched.
		BangumiBaseURL     string `toml:"bangumiBaseUrl"`
		BangumiAccessToken string `toml:"bangumiAccessToken"`
		// TMDbAPIKey enables movie and TV search. Empty leaves the video
		// domain with only locally cached catalog rows.
		TMDbAPIKey  string `toml:"tmdbApiKey"`
		TMDbBaseURL string `toml:"tmdbBaseUrl"`
	} `toml:"metadata"`

	// Bangumi configures the collection mirror. Account linking itself lives
	// in Stargate, which owns the OAuth app and refreshes the token; Pulsar
	// only reads the API with the token Stargate hands it.
	Bangumi struct {
		// APIBaseURL is the api.bgm.tv base. Empty falls back to the metadata
		// Bangumi base and then to the public API.
		APIBaseURL string `toml:"apiBaseUrl"`
		// UserAgent identifies Pulsar to Bangumi. Empty falls back to the
		// metadata UserAgent.
		UserAgent string `toml:"userAgent"`
		// SyncIntervalMinutes is how often linked accounts are reconciled.
		// Zero selects the 30-minute default; a negative value disables the
		// background job (manual sync still works).
		SyncIntervalMinutes int `toml:"syncIntervalMinutes"`
		// ConflictPolicy decides what happens when both sides changed since
		// the last reconciliation: "local", "remote" (default) or "newest".
		ConflictPolicy string `toml:"conflictPolicy"`
	} `toml:"bangumi"`

	// LibrarySync configures the per-account library mirror GroovyBox syncs.
	LibrarySync struct {
		// RetentionDays is how long a tombstone stays visible to pulls before
		// the purge job removes it.
		RetentionDays int `toml:"retentionDays"`
		// MaxBatch caps one push request and one pull page.
		MaxBatch int `toml:"maxBatch"`
		// DevAccountHeader names a request header whose UUID value is trusted
		// as the account id. It bypasses fleet auth entirely and is a local
		// development switch: it must stay empty in any deployed config.
		DevAccountHeader string `toml:"devAccountHeader"`
	} `toml:"librarySync"`

	// Scrobbler gates when a played session or a synced library play becomes a
	// scrobble in the history.
	Scrobbler Scrobbler `toml:"scrobbler"`

	// Web configures the SSR templates and Vue island assets (web/dist).
	Web struct {
		// DistPath is the built Vite output directory containing .vite/manifest.json
		// and assets/. Empty or missing disables static asset serving while keeping
		// SSR templates operational.
		DistPath string `toml:"distPath"`
		// DevViteURL points at a running Vite dev server (e.g. http://localhost:5173)
		// so SSR templates load live HMR scripts during local development.
		DevViteURL string `toml:"devViteUrl"`
		// StargateURL is the base URL of Stargate for sign-in links (e.g.
		// https://id.example.com). Empty defaults to "/sign-in" on the same origin.
		StargateURL string `toml:"stargateUrl"`
	} `toml:"web"`
}

// Scrobbler carries the auto-scrobble thresholds shared by the sessions and
// library sync modules.
type Scrobbler struct {
	AutoScrobbleThresholdPercent int `toml:"autoScrobbleThresholdPercent"`
	AutoScrobbleMinSeconds       int `toml:"autoScrobbleMinSeconds"`
	MaxBatchScrobble             int `toml:"maxBatchScrobble"`
}

// Load loads configuration from path or environment.
func Load(path string) (*Config, error) {
	cfg := &Config{}
	err := config.Load(path, "PULSAR", cfg, applyDefaults, applyEnv, validate)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func applyDefaults(cfg *Config) {
	cfg.HTTP.Port = "5073"
	cfg.GRPC.Port = "7101"
	cfg.Base.ApplyDefaults("pulsar")

	if cfg.LibrarySync.RetentionDays <= 0 {
		cfg.LibrarySync.RetentionDays = 90
	}
	if cfg.LibrarySync.MaxBatch <= 0 {
		cfg.LibrarySync.MaxBatch = 500
	}
	if cfg.Scrobbler.AutoScrobbleThresholdPercent <= 0 {
		cfg.Scrobbler.AutoScrobbleThresholdPercent = 50
	}
	if cfg.Scrobbler.AutoScrobbleMinSeconds <= 0 {
		cfg.Scrobbler.AutoScrobbleMinSeconds = 240
	}
	if cfg.Scrobbler.MaxBatchScrobble <= 0 {
		cfg.Scrobbler.MaxBatchScrobble = 50
	}
	if strings.TrimSpace(cfg.Web.DistPath) == "" {
		cfg.Web.DistPath = "web/dist"
	}
}

// normalizeBangumi fills the OAuth and API defaults from the deployment's
// public URLs. It runs after TOML and environment overrides so an explicit
// value is never replaced.
func normalizeBangumi(cfg *Config) {
	b := &cfg.Bangumi
	if strings.TrimSpace(b.APIBaseURL) == "" {
		b.APIBaseURL = strings.TrimSpace(cfg.Metadata.BangumiBaseURL)
	}
	if strings.TrimSpace(b.APIBaseURL) == "" {
		b.APIBaseURL = "https://api.bgm.tv"
	}
	if strings.TrimSpace(b.UserAgent) == "" {
		b.UserAgent = cfg.Metadata.UserAgent
	}
	if b.SyncIntervalMinutes == 0 {
		b.SyncIntervalMinutes = 30
	}
	switch strings.ToLower(strings.TrimSpace(b.ConflictPolicy)) {
	case "local", "remote", "newest":
		b.ConflictPolicy = strings.ToLower(strings.TrimSpace(b.ConflictPolicy))
	default:
		b.ConflictPolicy = "remote"
	}
}

func applyEnv(cfg *Config) {
	config.SetStr("PULSAR_SITE_URL", &cfg.SiteUrl)
	config.SetStr("PULSAR_BASE_URL", &cfg.BaseUrl)
	config.SetStr("PULSAR_HTTP_PORT", &cfg.HTTP.Port)
	config.SetStr("PULSAR_GRPC_PORT", &cfg.GRPC.Port)
	config.SetBool("PULSAR_GRPC_USETLS", &cfg.GRPC.UseTLS)
	config.SetStr("PULSAR_GRPC_CERTFILE", &cfg.GRPC.CertFile)
	config.SetStr("PULSAR_GRPC_KEYFILE", &cfg.GRPC.KeyFile)
	config.SetStr("PULSAR_DATABASE__DSN", &cfg.Database.DSN)
	config.SetStr("PULSAR_REDIS_ADDR", &cfg.Redis.Addr)
	config.SetStr("PULSAR_REDIS_PASSWORD", &cfg.Redis.Password)
	config.SetStr("PULSAR_NATS_TARGET", &cfg.NATS.Target)
	config.SetStr("PULSAR_METADATA_USERAGENT", &cfg.Metadata.UserAgent)
	config.SetStr("PULSAR_METADATA_MUSICBRAINZBASEURL", &cfg.Metadata.MusicBrainzBaseURL)
	config.SetStr("PULSAR_METADATA_MUSICBRAINZCOVERARTURL", &cfg.Metadata.MusicBrainzCoverArtURL)
	config.SetStr("PULSAR_METADATA_ANILISTENDPOINT", &cfg.Metadata.AniListEndpoint)
	config.SetStr("PULSAR_METADATA_STEAMBASEURL", &cfg.Metadata.SteamBaseURL)
	config.SetStr("PULSAR_METADATA_BANGUMIBASEURL", &cfg.Metadata.BangumiBaseURL)
	config.SetStr("PULSAR_METADATA_BANGUMIACCESSTOKEN", &cfg.Metadata.BangumiAccessToken)
	config.SetStr("PULSAR_METADATA_TMDBAPIKEY", &cfg.Metadata.TMDbAPIKey)
	config.SetStr("PULSAR_METADATA_TMDBBASEURL", &cfg.Metadata.TMDbBaseURL)
	config.SetStr("PULSAR_SERVICES_STARGATE__GRPC", &cfg.Services.Stargate.GRPC)
	config.SetStr("PULSAR_SERVICES_BLADE__GRPC", &cfg.Services.Blade.GRPC)
	config.SetInt("PULSAR_LIBRARYSYNC_RETENTIONDAYS", &cfg.LibrarySync.RetentionDays)
	config.SetInt("PULSAR_LIBRARYSYNC_MAXBATCH", &cfg.LibrarySync.MaxBatch)
	config.SetStr("PULSAR_LIBRARYSYNC_DEVACCOUNTHEADER", &cfg.LibrarySync.DevAccountHeader)
	config.SetStr("PULSAR_BANGUMI_APIBASEURL", &cfg.Bangumi.APIBaseURL)
	config.SetStr("PULSAR_BANGUMI_USERAGENT", &cfg.Bangumi.UserAgent)
	config.SetInt("PULSAR_BANGUMI_SYNCINTERVALMINUTES", &cfg.Bangumi.SyncIntervalMinutes)
	config.SetStr("PULSAR_BANGUMI_CONFLICTPOLICY", &cfg.Bangumi.ConflictPolicy)
	config.SetInt("PULSAR_SECURITY_TRUSTEDPROXYHOPS", &cfg.Security.TrustedProxyHops)
	config.SetInt("PULSAR_SECURITY_MAXTAKE", &cfg.Security.MaxTake)
	config.SetBool("PULSAR_DISCOVERY_ENABLED", &cfg.Discovery.Enabled)
	config.SetStr("PULSAR_DISCOVERY_TARGET", &cfg.Discovery.Target)
	config.SetStr("PULSAR_DISCOVERY_SERVICE", &cfg.Discovery.Service)
	config.SetStr("PULSAR_DISCOVERY_INSTANCE_ID", &cfg.Discovery.InstanceID)
	config.SetStr("PULSAR_DISCOVERY_HTTP_ENDPOINT", &cfg.Discovery.HttpEndpoint)
	config.SetStr("PULSAR_DISCOVERY_GRPC_ENDPOINT", &cfg.Discovery.GrpcEndpoint)
	config.SetList("PULSAR_MODULES_ENABLED", &cfg.Modules.Enabled)
	config.SetList("PULSAR_MODULES_DISABLED", &cfg.Modules.Disabled)
	config.SetStr("PULSAR_WEB_DISTPATH", &cfg.Web.DistPath)
	config.SetStr("PULSAR_WEB_DEVVITEURL", &cfg.Web.DevViteURL)
	config.SetStr("PULSAR_WEB_STARGATEURL", &cfg.Web.StargateURL)
	normalizeBangumi(cfg)
}

func validate(cfg *Config) error {
	if strings.TrimSpace(cfg.Database.DSN) == "" {
		return errMissing("database.dsn")
	}
	return nil
}

type errMissing string

func (e errMissing) Error() string { return "missing required setting: " + string(e) }
