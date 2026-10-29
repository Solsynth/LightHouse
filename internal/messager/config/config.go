// Package config loads the Messager service configuration.
package config

import (
	"strings"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/config"
)

// Config is the Messager configuration: the shared base plus chat-specific
// sections.
type Config struct {
	config.Base

	// RealtimeChat configures the LiveKit project used for calls.
	RealtimeChat struct {
		Endpoint  string `toml:"endpoint"`
		APIKey    string `toml:"apiKey"`
		APISecret string `toml:"apiSecret"`
	} `toml:"realtimeChat"`

	// VoiceMessages configures voice-message retention and storage.
	VoiceMessages struct {
		KeyPrefix        string `toml:"keyPrefix"`
		MaxFileSizeBytes int64  `toml:"maxFileSizeBytes"`
		RetentionDays    int    `toml:"retentionDays"`
		CleanupCron      string `toml:"cleanupCron"`
		S3               struct {
			ServiceURL     string `toml:"serviceUrl"`
			Endpoint       string `toml:"endpoint"`
			PublicBaseURL  string `toml:"publicBaseUrl"`
			Region         string `toml:"region"`
			AccessKey      string `toml:"accessKey"`
			SecretKey      string `toml:"secretKey"`
			Bucket         string `toml:"bucket"`
			ForcePathStyle bool   `toml:"forcePathStyle"`
			EnableSSL      bool   `toml:"enableSsl"`
		} `toml:"s3"`
	} `toml:"voiceMessages"`
}

// Load reads the configuration from path (or $CONFIG_PATH).
func Load(path string) (*Config, error) {
	cfg := &Config{}
	if err := config.Load(path, "MESSAGER", cfg, applyDefaults, applyEnv, validate); err != nil {
		return nil, err
	}
	return cfg, nil
}

func applyDefaults(cfg *Config) {
	// Ports default to the C# service's ports; Base.ApplyDefaults only fills
	// empty values, so a configured port always wins.
	cfg.HTTP.Port = "5072"
	cfg.GRPC.Port = "7100"
	cfg.Base.ApplyDefaults("messager")
	if cfg.VoiceMessages.KeyPrefix == "" {
		cfg.VoiceMessages.KeyPrefix = "voice"
	}
	if cfg.VoiceMessages.MaxFileSizeBytes <= 0 {
		cfg.VoiceMessages.MaxFileSizeBytes = 10 * 1024 * 1024
	}
	if cfg.VoiceMessages.RetentionDays <= 0 {
		cfg.VoiceMessages.RetentionDays = 30
	}
	if cfg.VoiceMessages.CleanupCron == "" {
		cfg.VoiceMessages.CleanupCron = "0 15 * * * ?"
	}
}

func applyEnv(cfg *Config) {
	config.SetStr("MESSAGER_SITE_URL", &cfg.SiteUrl)
	config.SetStr("MESSAGER_BASE_URL", &cfg.BaseUrl)
	config.SetStr("MESSAGER_HTTP_PORT", &cfg.HTTP.Port)
	config.SetStr("MESSAGER_GRPC_PORT", &cfg.GRPC.Port)
	config.SetBool("MESSAGER_GRPC_USETLS", &cfg.GRPC.UseTLS)
	config.SetStr("MESSAGER_GRPC_CERTFILE", &cfg.GRPC.CertFile)
	config.SetStr("MESSAGER_GRPC_KEYFILE", &cfg.GRPC.KeyFile)
	config.SetStr("MESSAGER_DATABASE__DSN", &cfg.Database.DSN)
	config.SetStr("MESSAGER_REDIS_ADDR", &cfg.Redis.Addr)
	config.SetStr("MESSAGER_REDIS_PASSWORD", &cfg.Redis.Password)
	config.SetStr("MESSAGER_NATS_TARGET", &cfg.NATS.Target)
	config.SetStr("MESSAGER_SERVICES_STARGATE__GRPC", &cfg.Services.Stargate.GRPC)
	config.SetStr("MESSAGER_SERVICES_PASSPORT__GRPC", &cfg.Services.Passport.GRPC)
	config.SetStr("MESSAGER_SERVICES_SPHERE__GRPC", &cfg.Services.Sphere.GRPC)
	config.SetStr("MESSAGER_SERVICES_RING__GRPC", &cfg.Services.Ring.GRPC)
	config.SetStr("MESSAGER_SERVICES_DRIVE__GRPC", &cfg.Services.Drive.GRPC)
	config.SetStr("MESSAGER_SERVICES_WALLET__GRPC", &cfg.Services.Wallet.GRPC)
	config.SetStr("MESSAGER_SERVICES_DEVELOP__GRPC", &cfg.Services.Develop.GRPC)
	config.SetStr("MESSAGER_SERVICES_BLADE__GRPC", &cfg.Services.Blade.GRPC)
	config.SetStr("MESSAGER_REALTIMECHAT_ENDPOINT", &cfg.RealtimeChat.Endpoint)
	config.SetStr("MESSAGER_REALTIMECHAT_APIKEY", &cfg.RealtimeChat.APIKey)
	config.SetStr("MESSAGER_REALTIMECHAT_APISECRET", &cfg.RealtimeChat.APISecret)
	config.SetInt("MESSAGER_SECURITY_TRUSTEDPROXYHOPS", &cfg.Security.TrustedProxyHops)
	config.SetInt("MESSAGER_SECURITY_MAXTAKE", &cfg.Security.MaxTake)
	config.SetBool("MESSAGER_DISCOVERY_ENABLED", &cfg.Discovery.Enabled)
	config.SetStr("MESSAGER_DISCOVERY_TARGET", &cfg.Discovery.Target)
	config.SetStr("MESSAGER_DISCOVERY_REGISTRATION_TOKEN", &cfg.Discovery.RegistrationToken)
	config.SetStr("MESSAGER_DISCOVERY_SERVICE", &cfg.Discovery.Service)
	config.SetStr("MESSAGER_DISCOVERY_INSTANCE_ID", &cfg.Discovery.InstanceID)
	config.SetStr("MESSAGER_DISCOVERY_HTTP_ENDPOINT", &cfg.Discovery.HttpEndpoint)
	config.SetStr("MESSAGER_DISCOVERY_GRPC_ENDPOINT", &cfg.Discovery.GrpcEndpoint)
	config.SetList("MESSAGER_MODULES_ENABLED", &cfg.Modules.Enabled)
	config.SetList("MESSAGER_MODULES_DISABLED", &cfg.Modules.Disabled)
}

func validate(cfg *Config) error {
	if strings.TrimSpace(cfg.Database.DSN) == "" {
		return errMissing("database.dsn")
	}
	return nil
}

type errMissing string

func (e errMissing) Error() string { return "missing required setting: " + string(e) }
