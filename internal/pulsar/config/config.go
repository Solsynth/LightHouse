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
		// terms require.
		UserAgent              string `toml:"userAgent"`
		MusicBrainzBaseURL     string `toml:"musicBrainzBaseUrl"`
		MusicBrainzCoverArtURL string `toml:"musicBrainzCoverArtUrl"`
		AniListEndpoint        string `toml:"anilistEndpoint"`
		SteamBaseURL           string `toml:"steamBaseUrl"`
		// TMDbAPIKey enables movie and TV search. Empty leaves the video
		// domain with only locally cached catalog rows.
		TMDbAPIKey  string `toml:"tmdbApiKey"`
		TMDbBaseURL string `toml:"tmdbBaseUrl"`
	} `toml:"metadata"`

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

	Scrobbler struct {
		AutoScrobbleThresholdPercent int `toml:"autoScrobbleThresholdPercent"`
		AutoScrobbleMinSeconds       int `toml:"autoScrobbleMinSeconds"`
		MaxBatchScrobble             int `toml:"maxBatchScrobble"`
	} `toml:"scrobbler"`
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
	config.SetStr("PULSAR_METADATA_TMDBAPIKEY", &cfg.Metadata.TMDbAPIKey)
	config.SetStr("PULSAR_METADATA_TMDBBASEURL", &cfg.Metadata.TMDbBaseURL)
	config.SetStr("PULSAR_SERVICES_STARGATE__GRPC", &cfg.Services.Stargate.GRPC)
	config.SetStr("PULSAR_SERVICES_BLADE__GRPC", &cfg.Services.Blade.GRPC)
	config.SetInt("PULSAR_LIBRARYSYNC_RETENTIONDAYS", &cfg.LibrarySync.RetentionDays)
	config.SetInt("PULSAR_LIBRARYSYNC_MAXBATCH", &cfg.LibrarySync.MaxBatch)
	config.SetStr("PULSAR_LIBRARYSYNC_DEVACCOUNTHEADER", &cfg.LibrarySync.DevAccountHeader)
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
}

func validate(cfg *Config) error {
	if strings.TrimSpace(cfg.Database.DSN) == "" {
		return errMissing("database.dsn")
	}
	return nil
}

type errMissing string

func (e errMissing) Error() string { return "missing required setting: " + string(e) }
