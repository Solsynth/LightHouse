// Package config loads the Metoer TOML configuration with environment
// overrides. The file format follows the house pattern shared by sibling
// services (see Stargate's config.example.toml).
package config

import (
	"fmt"
	"os"
	"runtime"

	"github.com/pelletier/go-toml/v2"
)

// Config is the root configuration for Metoer (the Go replacement of
// DysonNetwork.Ring).
type Config struct {
	SiteUrl string `toml:"siteUrl"`
	BaseUrl string `toml:"baseUrl"`

	// ConsumerCount mirrors the C# "ConsumerCount" top-level config
	// (QueueBackgroundService): number of concurrent queue consumers;
	// 0 means runtime.NumCPU().
	ConsumerCount int `toml:"consumerCount"`

	HTTP struct {
		Port string `toml:"port"`
	} `toml:"http"`
	GRPC struct {
		Port     string `toml:"port"`
		UseTLS   bool   `toml:"useTLS"`
		CertFile string `toml:"certFile"`
		KeyFile  string `toml:"keyFile"`
	} `toml:"grpc"`

	Database struct {
		DSN string `toml:"dsn"`
	} `toml:"database"`

	Redis struct {
		Addr     string `toml:"addr"`
		Password string `toml:"password"`
		DB       int    `toml:"db"`
	} `toml:"redis"`

	NATS struct {
		Target string `toml:"target"`
	} `toml:"nats"`

	Email struct {
		Server        string `toml:"server"`
		Port          int    `toml:"port"`
		UseSsl        bool   `toml:"useSsl"`
		Username      string `toml:"username"`
		Password      string `toml:"password"`
		FromAddress   string `toml:"fromAddress"`
		FromName      string `toml:"fromName"`
		SubjectPrefix string `toml:"subjectPrefix"`
	} `toml:"email"`

	// Notifications mirrors the C# "Notifications" section (appsettings):
	// a Push section with per-app configs, or a legacy flat layout.
	Notifications struct {
		Push struct {
			DefaultApp string `toml:"defaultApp"`
			Apps       map[string]PushAppConfig `toml:"apps"`
			// Legacy flat fallback (old configs without Apps):
			Production bool   `toml:"production"`
			Google     string `toml:"google"` // FCM service-account key path
			Apple      struct {
				PrivateKey      string `toml:"privateKey"`
				PrivateKeyId    string `toml:"privateKeyId"`
				TeamId          string `toml:"teamId"`
				BundleIdentifier string `toml:"bundleIdentifier"`
			} `toml:"apple"`
		} `toml:"push"`
	} `toml:"notifications"`

	Services struct {
		Stargate ServiceTarget `toml:"stargate"` // DyAccountService, DyAuthService, DyPermissionService, DyProfileService, DyActionLogService
		Blade    ServiceTarget `toml:"blade"`    // WebSocketService + service discovery
	} `toml:"services"`

	// Discovery registers this instance with Blade's service discovery
	// (DyServiceDiscoveryService gRPC), mirroring the C# Blade service.
	Discovery struct {
		Enabled           bool   `toml:"enabled"`
		Target            string `toml:"target"` // Blade gRPC endpoint (host:port)
		RegistrationToken string `toml:"registrationToken"`
		Service           string `toml:"service"`
		InstanceID        string `toml:"instanceId"`
		HttpEndpoint      string `toml:"httpEndpoint"`
		GrpcEndpoint      string `toml:"grpcEndpoint"`
		LeaseSeconds      int    `toml:"leaseSeconds"`
		Weight            int    `toml:"weight"`
	} `toml:"discovery"`
}

// PushAppConfig mirrors the C# PushAppConfig (Notifications:Push:Apps entries).
type PushAppConfig struct {
	Production bool              `toml:"production"`
	FcmKeyPath string            `toml:"fcmKeyPath"`
	Apns       *ApnsAppConfig    `toml:"apns"`
	Topics     map[string]string `toml:"topics"`
}

// ApnsAppConfig mirrors the C# ApnsAppConfig.
type ApnsAppConfig struct {
	PrivateKeyPath   string `toml:"privateKeyPath"`
	PrivateKeyId     string `toml:"privateKeyId"`
	TeamId           string `toml:"teamId"`
	BundleIdentifier string `toml:"bundleIdentifier"`
}

// ServiceTarget is an outbound gRPC target.
type ServiceTarget struct {
	GRPC string `toml:"grpc"`
}

// Default returns a config with production-shaped defaults so a missing
// optional section never zeroes a critical value.
func Default() *Config {
	cfg := &Config{}
	cfg.SiteUrl = "http://localhost:3000"
	cfg.BaseUrl = "http://localhost:5212"
	cfg.ConsumerCount = 0
	cfg.HTTP.Port = "8080"
	cfg.GRPC.Port = "9090"
	cfg.Email.Port = 25
	cfg.Email.FromName = "Alphabot"
	cfg.Email.SubjectPrefix = "Solar Network"
	cfg.Discovery.Service = "metoer"
	cfg.Discovery.LeaseSeconds = 30
	cfg.Discovery.Weight = 1
	return cfg
}

// ConsumerCountValue returns the number of queue consumers to run,
// defaulting to runtime.NumCPU() when the config value is 0 (the C#
// `?? Environment.ProcessorCount` fallback).
func (c *Config) ConsumerCountValue() int {
	if c.ConsumerCount > 0 {
		return c.ConsumerCount
	}
	return runtime.NumCPU()
}

// Load reads the TOML file at path (default config.example.toml) and applies
// METOER_* environment overrides. Overrides use double-underscore nesting,
// e.g. METOER_DATABASE__DSN, METOER_SERVICES_STARGATE__GRPC.
func Load(path string) (*Config, error) {
	if path == "" {
		path = os.Getenv("CONFIG_PATH")
	}
	if path == "" {
		path = "config.example.toml"
	}
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read config %s: %w", path, err)
		}
	} else if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	applyEnvOverrides(cfg)
	return cfg, nil
}

func applyEnvOverrides(cfg *Config) {
	// Each override maps to the TOML field; keep this list explicit.
	setStr("METOER_SITE_URL", &cfg.SiteUrl)
	setStr("METOER_BASE_URL", &cfg.BaseUrl)
	setStr("METOER_HTTP_PORT", &cfg.HTTP.Port)
	setStr("METOER_GRPC_PORT", &cfg.GRPC.Port)
	setBool("METOER_GRPC_USETLS", &cfg.GRPC.UseTLS)
	setStr("METOER_GRPC_CERT_FILE", &cfg.GRPC.CertFile)
	setStr("METOER_GRPC_KEY_FILE", &cfg.GRPC.KeyFile)
	setStr("METOER_DATABASE__DSN", &cfg.Database.DSN)
	setStr("METOER_REDIS_ADDR", &cfg.Redis.Addr)
	setStr("METOER_REDIS_PASSWORD", &cfg.Redis.Password)
	setStr("METOER_NATS_TARGET", &cfg.NATS.Target)
	setStr("METOER_EMAIL_SERVER", &cfg.Email.Server)
	setStr("METOER_EMAIL_USERNAME", &cfg.Email.Username)
	setStr("METOER_EMAIL_PASSWORD", &cfg.Email.Password)
	setStr("METOER_EMAIL_FROM_ADDRESS", &cfg.Email.FromAddress)
	setStr("METOER_SERVICES_STARGATE__GRPC", &cfg.Services.Stargate.GRPC)
	setStr("METOER_SERVICES_BLADE__GRPC", &cfg.Services.Blade.GRPC)
	setBool("METOER_DISCOVERY_ENABLED", &cfg.Discovery.Enabled)
	setStr("METOER_DISCOVERY_TARGET", &cfg.Discovery.Target)
	setStr("METOER_DISCOVERY_REGISTRATION_TOKEN", &cfg.Discovery.RegistrationToken)
	setStr("METOER_DISCOVERY_SERVICE", &cfg.Discovery.Service)
	setStr("METOER_DISCOVERY_INSTANCE_ID", &cfg.Discovery.InstanceID)
	setStr("METOER_DISCOVERY_HTTP_ENDPOINT", &cfg.Discovery.HttpEndpoint)
	setStr("METOER_DISCOVERY_GRPC_ENDPOINT", &cfg.Discovery.GrpcEndpoint)
}

func setStr(key string, dst *string) {
	if v := os.Getenv(key); v != "" {
		*dst = v
	}
}

func setBool(key string, dst *bool) {
	if v := os.Getenv(key); v != "" {
		*dst = v == "true" || v == "1"
	}
}
