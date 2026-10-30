// Package config loads the Sphere service configuration.
package config

import (
	"strings"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/config"
)

// Config is the Sphere configuration: the shared base plus the sections that
// only the content service has.
type Config struct {
	config.Base

	// Posts controls post creation and search defaults.
	Posts struct {
		SearchEngineDefault    string `toml:"searchEngineDefault"`
		AutoChainWindowMinutes int    `toml:"autoChainWindowMinutes"`
	} `toml:"posts"`

	// ActivityPub is the federation identity of this deployment.
	ActivityPub struct {
		Domain        string `toml:"domain"`
		FileBaseURL   string `toml:"fileBaseUrl"`
		ServerKeyPath string `toml:"serverKeyPath"`
	} `toml:"activitypub"`

	ActivityPubDelivery struct {
		MaxRetries    int `toml:"maxRetries"`
		ConsumerCount int `toml:"consumerCount"`
	} `toml:"activityPubDelivery"`

	// LiveStream configures the LiveKit project used for streams.
	LiveStream struct {
		Endpoint    string `toml:"endpoint"`
		APIKey      string `toml:"apiKey"`
		APISecret   string `toml:"apiSecret"`
		PlaybackURL string `toml:"playbackUrl"`
	} `toml:"liveStream"`

	// Translation selects the outbound translation provider.
	Translation struct {
		Provider  string `toml:"provider"`
		Region    string `toml:"region"`
		ProjectID string `toml:"projectId"`
		SecretID  string `toml:"secretId"`
		SecretKey string `toml:"secretKey"`
	} `toml:"translation"`

	// Automod seeds the automod_rules table on boot.
	Automod struct {
		Rules []AutomodRuleSeed `toml:"rules"`
	} `toml:"automod"`

	// FediverseModeration seeds the fediverse_moderation_rules table on boot.
	FediverseModeration struct {
		Rules []FediverseRuleSeed `toml:"rules"`
	} `toml:"fediverseModeration"`

	// Scrap guards the outbound web reader.
	Scrap struct {
		AllowedSchemes []string `toml:"allowedSchemes"`
	} `toml:"scrap"`
}

// AutomodRuleSeed mirrors the C# Automod:R rules configuration block.
type AutomodRuleSeed struct {
	Name         string `toml:"name"`
	Description  string `toml:"description"`
	Type         string `toml:"type"`
	Action       string `toml:"action"`
	Pattern      string `toml:"pattern"`
	IsRegex      bool   `toml:"isRegex"`
	DerankWeight int    `toml:"derankWeight"`
	IsEnabled    bool   `toml:"isEnabled"`
	Priority     int    `toml:"priority"`
}

// FediverseRuleSeed mirrors the C# FediverseModeration:R rules block.
type FediverseRuleSeed struct {
	Name        string `toml:"name"`
	Description string `toml:"description"`
	Type        string `toml:"type"`
	Action      string `toml:"action"`
	Domain      string `toml:"domain"`
	IsEnabled   bool   `toml:"isEnabled"`
	Priority    int    `toml:"priority"`
}

// Load reads the configuration from path (or $CONFIG_PATH).
func Load(path string) (*Config, error) {
	cfg := &Config{}
	err := config.Load(path, "SPHERE", cfg, applyDefaults, applyEnv, validate)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func applyDefaults(cfg *Config) {
	// Ports default to the C# service's ports; Base.ApplyDefaults only fills
	// empty values, so a configured port always wins.
	cfg.HTTP.Port = "5071"
	cfg.GRPC.Port = "7099"
	cfg.Base.ApplyDefaults("sphere")
	if cfg.Posts.SearchEngineDefault == "" {
		cfg.Posts.SearchEngineDefault = "semantic"
	}
	if cfg.Posts.AutoChainWindowMinutes <= 0 {
		cfg.Posts.AutoChainWindowMinutes = 5
	}
	if cfg.ActivityPubDelivery.MaxRetries <= 0 {
		cfg.ActivityPubDelivery.MaxRetries = 5
	}
	if cfg.ActivityPubDelivery.ConsumerCount <= 0 {
		cfg.ActivityPubDelivery.ConsumerCount = 4
	}
}

func applyEnv(cfg *Config) {
	config.SetStr("SPHERE_SITE_URL", &cfg.SiteUrl)
	config.SetStr("SPHERE_BASE_URL", &cfg.BaseUrl)
	config.SetStr("SPHERE_HTTP_PORT", &cfg.HTTP.Port)
	config.SetStr("SPHERE_GRPC_PORT", &cfg.GRPC.Port)
	config.SetBool("SPHERE_GRPC_USETLS", &cfg.GRPC.UseTLS)
	config.SetStr("SPHERE_GRPC_CERTFILE", &cfg.GRPC.CertFile)
	config.SetStr("SPHERE_GRPC_KEYFILE", &cfg.GRPC.KeyFile)
	config.SetStr("SPHERE_DATABASE__DSN", &cfg.Database.DSN)
	config.SetStr("SPHERE_REDIS_ADDR", &cfg.Redis.Addr)
	config.SetStr("SPHERE_REDIS_PASSWORD", &cfg.Redis.Password)
	config.SetStr("SPHERE_NATS_TARGET", &cfg.NATS.Target)
	config.SetStr("SPHERE_SERVICES_STARGATE__GRPC", &cfg.Services.Stargate.GRPC)
	config.SetStr("SPHERE_SERVICES_RING__GRPC", &cfg.Services.Ring.GRPC)
	config.SetStr("SPHERE_SERVICES_DRIVE__GRPC", &cfg.Services.Drive.GRPC)
	config.SetStr("SPHERE_SERVICES_WALLET__GRPC", &cfg.Services.Wallet.GRPC)
	config.SetStr("SPHERE_SERVICES_DEVELOP__GRPC", &cfg.Services.Develop.GRPC)
	config.SetStr("SPHERE_SERVICES_BLADE__GRPC", &cfg.Services.Blade.GRPC)
	config.SetStr("SPHERE_ACTIVITYPUB_DOMAIN", &cfg.ActivityPub.Domain)
	config.SetStr("SPHERE_ACTIVITYPUB_FILEBASEURL", &cfg.ActivityPub.FileBaseURL)
	config.SetStr("SPHERE_ACTIVITYPUB_SERVERKEYPATH", &cfg.ActivityPub.ServerKeyPath)
	config.SetStr("SPHERE_LIVESTREAM_ENDPOINT", &cfg.LiveStream.Endpoint)
	config.SetStr("SPHERE_LIVESTREAM_APIKEY", &cfg.LiveStream.APIKey)
	config.SetStr("SPHERE_LIVESTREAM_APISECRET", &cfg.LiveStream.APISecret)
	config.SetStr("SPHERE_LIVESTREAM_PLAYBACKURL", &cfg.LiveStream.PlaybackURL)
	config.SetStr("SPHERE_TRANSLATION_PROVIDER", &cfg.Translation.Provider)
	config.SetInt("SPHERE_SECURITY_TRUSTEDPROXYHOPS", &cfg.Security.TrustedProxyHops)
	config.SetInt("SPHERE_SECURITY_MAXTAKE", &cfg.Security.MaxTake)
	config.SetBool("SPHERE_DISCOVERY_ENABLED", &cfg.Discovery.Enabled)
	config.SetStr("SPHERE_DISCOVERY_TARGET", &cfg.Discovery.Target)
	config.SetStr("SPHERE_DISCOVERY_REGISTRATION_TOKEN", &cfg.Discovery.RegistrationToken)
	config.SetStr("SPHERE_DISCOVERY_SERVICE", &cfg.Discovery.Service)
	config.SetStr("SPHERE_DISCOVERY_INSTANCE_ID", &cfg.Discovery.InstanceID)
	config.SetStr("SPHERE_DISCOVERY_HTTP_ENDPOINT", &cfg.Discovery.HttpEndpoint)
	config.SetStr("SPHERE_DISCOVERY_GRPC_ENDPOINT", &cfg.Discovery.GrpcEndpoint)
	config.SetList("SPHERE_MODULES_ENABLED", &cfg.Modules.Enabled)
	config.SetList("SPHERE_MODULES_DISABLED", &cfg.Modules.Disabled)
}

func validate(cfg *Config) error {
	if strings.TrimSpace(cfg.Database.DSN) == "" {
		return errMissing("database.dsn")
	}
	return nil
}

type errMissing string

func (e errMissing) Error() string { return "missing required setting: " + string(e) }
