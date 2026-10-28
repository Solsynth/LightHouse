// Package config loads a Lighthouse service's TOML configuration with
// environment overrides. The file format follows the house pattern shared by
// Stargate and the rest of the fleet.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// ServiceTarget is one outbound gRPC dependency.
type ServiceTarget struct {
	GRPC string `toml:"grpc"`
}

// Enabled reports whether an outbound target was configured.
func (s ServiceTarget) Enabled() bool {
	return strings.TrimSpace(s.GRPC) != ""
}

// Base is the configuration every Lighthouse service shares. Each service
// embeds it and adds its own sections.
type Base struct {
	SiteUrl string `toml:"siteUrl"`
	BaseUrl string `toml:"baseUrl"`

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
		// WebsocketPushSubject must stay the subject Blade's gateway
		// subscribes to; the C# fleet and every other service publish there.
		WebsocketPushSubject string `toml:"websocketPushSubject"`
		// WebsocketPushStream is the JetStream stream Stargate declares over
		// the same subject. Empty keeps the push on core NATS, like the C#
		// fleet.
		WebsocketPushStream string `toml:"websocketPushStream"`
	} `toml:"nats"`

	// Auth is the outbound token-validation dependency. Sphere and Messager do
	// not validate JWTs themselves: the fleet's DysonTokenAuthHandler asks the
	// auth service (Stargate's DyAuthService) for the session behind a token.
	Auth struct {
		// CacheTTL is how long a validated session is reused, e.g. "1h".
		CacheTTL string `toml:"cacheTTL"`
		// TouchInterval throttles the last-seen write-back, e.g. "1m".
		TouchInterval string `toml:"touchInterval"`
	} `toml:"auth"`

	Services struct {
		Stargate ServiceTarget `toml:"stargate"`
		Sphere   ServiceTarget `toml:"sphere"`
		Messager ServiceTarget `toml:"messager"`
		Ring     ServiceTarget `toml:"ring"`
		Drive    ServiceTarget `toml:"drive"`
		Wallet   ServiceTarget `toml:"wallet"`
		Develop  ServiceTarget `toml:"develop"`
		Blade    ServiceTarget `toml:"blade"`
	} `toml:"services"`

	// Modules selects the compiled-in modules. An empty Enabled list means
	// "every module whose DefaultEnabled is true". Disabled wins over Enabled;
	// required modules reject a disable.
	Modules struct {
		Enabled  []string                  `toml:"enabled"`
		Disabled []string                  `toml:"disabled"`
		Settings map[string]map[string]any `toml:"settings"`
	} `toml:"modules"`

	// Ports selects a provider by module ID when more than one enabled module
	// provides the same port.
	Ports map[string]string `toml:"ports"`

	// Discovery registers this instance with Blade's service discovery so
	// Blade's capability aggregator and proxy can resolve it.
	Discovery struct {
		Enabled           bool   `toml:"enabled"`
		Target            string `toml:"target"`
		RegistrationToken string `toml:"registrationToken"`
		Service           string `toml:"service"`
		InstanceID        string `toml:"instanceId"`
		HttpEndpoint      string `toml:"httpEndpoint"`
		GrpcEndpoint      string `toml:"grpcEndpoint"`
		LeaseSeconds      int    `toml:"leaseSeconds"`
		Weight            int    `toml:"weight"`
	} `toml:"discovery"`

	Security struct {
		// TrustedProxyHops is how many reverse proxies in front of the service
		// append to X-Forwarded-For. Default 1 (the Blade edge).
		TrustedProxyHops int `toml:"trustedProxyHops"`
		// MaxTake caps pagination's take parameter. Default 100, matching the
		// C# PaginationOptions.DefaultMaxTake.
		MaxTake int `toml:"maxTake"`
	} `toml:"security"`
}

// ApplyDefaults fills the base defaults shared by every service.
func (b *Base) ApplyDefaults(serviceName string) {
	if b.SiteUrl == "" {
		b.SiteUrl = "http://localhost:3000"
	}
	if b.HTTP.Port == "" {
		b.HTTP.Port = "8080"
	}
	if b.GRPC.Port == "" {
		b.GRPC.Port = "9090"
	}
	if b.Auth.CacheTTL == "" {
		b.Auth.CacheTTL = "1h"
	}
	if b.Auth.TouchInterval == "" {
		b.Auth.TouchInterval = "1m"
	}
	if b.NATS.WebsocketPushSubject == "" {
		b.NATS.WebsocketPushSubject = "websocket_push"
	}
	if b.Security.TrustedProxyHops < 1 {
		b.Security.TrustedProxyHops = 1
	}
	if b.Security.MaxTake <= 0 {
		b.Security.MaxTake = 100
	}
	if b.Discovery.Service == "" {
		b.Discovery.Service = serviceName
	}
	if b.Discovery.LeaseSeconds <= 0 {
		b.Discovery.LeaseSeconds = 30
	}
	if b.Discovery.Weight <= 0 {
		b.Discovery.Weight = 1
	}
}

// Load reads the TOML file at path (default: $CONFIG_PATH, then
// config.example.toml) into cfg, applies defaults, then the envPrefix
// environment overrides through applyEnv.
func Load[T any](path, envPrefix string, cfg *T, defaults func(*T), applyEnv func(*T), validate func(*T) error) error {
	if path == "" {
		path = os.Getenv("CONFIG_PATH")
	}
	if path == "" {
		path = "config.example.toml"
	}
	if defaults != nil {
		defaults(cfg)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read config %s: %w", path, err)
		}
	} else if err := toml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	if applyEnv != nil {
		applyEnv(cfg)
	}
	if validate != nil {
		if err := validate(cfg); err != nil {
			return fmt.Errorf("invalid configuration: %w", err)
		}
	}
	return nil
}

// Env helpers. Each service lists its overrides explicitly so a typo in an
// environment variable is visible in the code rather than silently ignored.

// SetStr sets dst from the environment when the variable is non-empty.
func SetStr(key string, dst *string) {
	if v := os.Getenv(key); v != "" {
		*dst = v
	}
}

// SetBool sets dst from the environment when the variable is non-empty.
func SetBool(key string, dst *bool) {
	if v := os.Getenv(key); v != "" {
		*dst = v == "true" || v == "1"
	}
}

// SetInt sets dst from the environment when the variable parses.
func SetInt(key string, dst *int) {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*dst = n
		}
	}
}

// SetList sets dst from a comma-separated environment variable.
func SetList(key string, dst *[]string) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	*dst = out
}
