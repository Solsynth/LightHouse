// Package config loads the Stargate TOML configuration with environment
// overrides. The file format follows the house pattern shared by sibling
// services (see ElecPostal's config.example.toml).
package config

import (
	"fmt"
	"os"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Config is the root configuration for Stargate.
type Config struct {
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
		Target               string `toml:"target"`
		SessionEventsStream  string `toml:"sessionEventsStream"`
		SessionEventsSubject string `toml:"sessionEventsSubject"`
		WebsocketPushStream  string `toml:"websocketPushStream"`
		WebsocketPushSubject string `toml:"websocketPushSubject"`
	} `toml:"nats"`

	// Discovery registers this instance with Blade's service discovery
	// (DyServiceDiscoveryService gRPC) so Blade's /meta capability
	// aggregator and proxy can resolve and health-check Stargate.
	Discovery struct {
		Enabled           bool   `toml:"enabled"`
		Target            string `toml:"target"` // Blade gRPC endpoint (host:port)
		RegistrationToken string `toml:"registrationToken"`
		Service           string `toml:"service"`
		InstanceID        string `toml:"instanceId"`
		HttpEndpoint      string `toml:"httpEndpoint"` // absolute URL Blade probes /health on
		GrpcEndpoint      string `toml:"grpcEndpoint"` // where Blade fetches capabilities
		LeaseSeconds      int    `toml:"leaseSeconds"`
		Weight            int    `toml:"weight"`
	} `toml:"discovery"`

	Auth struct {
		Issuer               string   `toml:"issuer"`
		Audiences            []string `toml:"audiences"`
		PublicKeyPath        string   `toml:"publicKeyPath"`
		PrivateKeyPath       string   `toml:"privateKeyPath"`
		AccessTokenLifetime  string   `toml:"accessTokenLifetime"`
		RefreshTokenLifetime string   `toml:"refreshTokenLifetime"`
		CookieDomain         string   `toml:"cookieDomain"`
		CookieSecure         bool     `toml:"cookieSecure"`
	} `toml:"auth"`

	OidcProvider struct {
		IssuerUri                 string `toml:"issuerUri"`
		PublicKeyPath             string `toml:"publicKeyPath"`
		PrivateKeyPath            string `toml:"privateKeyPath"`
		AccessTokenLifetime       string `toml:"accessTokenLifetime"`
		RefreshTokenLifetime      string `toml:"refreshTokenLifetime"`
		AuthorizationCodeLifetime string `toml:"authorizationCodeLifetime"`
		RequireHttpsMetadata      bool   `toml:"requireHttpsMetadata"`
	} `toml:"oidcProvider"`

	Captcha struct {
		Provider  string `toml:"provider"`
		APIKey    string `toml:"apiKey"`
		APISecret string `toml:"apiSecret"`
		Skip      bool   `toml:"skip"`
	} `toml:"captcha"`

	WebAuthn struct {
		RpId           string   `toml:"rpId"`
		RpName         string   `toml:"rpName"`
		RelatedOrigins []string `toml:"relatedOrigins"`
	} `toml:"webauthn"`

	GeoIP struct {
		DatabasePath string `toml:"databasePath"`
	} `toml:"geoip"`

	Services struct {
		Drive   ServiceTarget `toml:"drive"`
		Wallet  ServiceTarget `toml:"wallet"`
		Pass    ServiceTarget `toml:"pass"`
		Blade   ServiceTarget `toml:"blade"`
		Ring    ServiceTarget `toml:"ring"`
		Develop ServiceTarget `toml:"develop"`
	} `toml:"services"`

	Oidc struct {
		Google    GoogleClient    `toml:"google"`
		Apple     AppleClient     `toml:"apple"`
		Microsoft MicrosoftClient `toml:"microsoft"`
		Steam     SteamClient     `toml:"steam"`
		Discord   DiscordClient   `toml:"discord"`
		GitHub    GitHubClient    `toml:"github"`
		Afdian    AfdianClient    `toml:"afdian"`
	} `toml:"oidc"`
}

// ServiceTarget is an outbound gRPC target.
type ServiceTarget struct {
	GRPC string `toml:"grpc"`
}

type GoogleClient struct {
	ClientId     string `toml:"clientId"`
	ClientSecret string `toml:"clientSecret"`
}

type AppleClient struct {
	ClientId       string `toml:"clientId"`
	TeamId         string `toml:"teamId"`
	KeyId          string `toml:"keyId"`
	PrivateKeyPath string `toml:"privateKeyPath"`
}

type MicrosoftClient struct {
	ClientId          string `toml:"clientId"`
	ClientSecret      string `toml:"clientSecret"`
	DiscoveryEndpoint string `toml:"discoveryEndpoint"`
}

type SteamClient struct {
	APIKey string `toml:"apiKey"`
}

type DiscordClient struct {
	ClientId     string `toml:"clientId"`
	ClientSecret string `toml:"clientSecret"`
}

type GitHubClient struct {
	ClientId     string `toml:"clientId"`
	ClientSecret string `toml:"clientSecret"`
}

type AfdianClient struct {
	ClientId     string `toml:"clientId"`
	ClientSecret string `toml:"clientSecret"`
}

// Default returns a config with production-shaped defaults so a missing
// optional section never zeroes a critical value.
func Default() *Config {
	cfg := &Config{}
	cfg.SiteUrl = "http://localhost:3000"
	cfg.BaseUrl = "http://localhost:5011"
	cfg.HTTP.Port = "8080"
	cfg.GRPC.Port = "9090"
	cfg.Auth.Issuer = "solar-network"
	cfg.Auth.Audiences = []string{"http://localhost:5071", "https://localhost:7099"}
	cfg.Auth.AccessTokenLifetime = "5m"
	cfg.Auth.RefreshTokenLifetime = "720h"
	cfg.Auth.CookieDomain = "localhost"
	cfg.OidcProvider.IssuerUri = "https://nt.solian.app"
	cfg.OidcProvider.AccessTokenLifetime = "5m"
	cfg.OidcProvider.RefreshTokenLifetime = "720h"
	cfg.OidcProvider.AuthorizationCodeLifetime = "30m"
	cfg.OidcProvider.RequireHttpsMetadata = true
	cfg.Captcha.Provider = "cloudflare"
	cfg.Captcha.Skip = true
	cfg.WebAuthn.RpId = "localhost"
	cfg.WebAuthn.RpName = "Solar Network"
	cfg.WebAuthn.RelatedOrigins = []string{"http://localhost:3000"}
	cfg.NATS.SessionEventsStream = "auth_session_events"
	cfg.NATS.SessionEventsSubject = "auth.session.revoked"
	cfg.NATS.WebsocketPushStream = "websocket_push"
	cfg.NATS.WebsocketPushSubject = "websocket.push"
	cfg.Discovery.Service = "stargate"
	cfg.Discovery.LeaseSeconds = 30
	cfg.Discovery.Weight = 1
	return cfg
}

// Load reads the TOML file at path (default config.example.toml) and applies
// STARGATE_* environment overrides. Overrides use double-underscore nesting,
// e.g. STARGATE_DATABASE__DSN, STARGATE_AUTH__ISSUER.
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
	setStr("STARGATE_SITE_URL", &cfg.SiteUrl)
	setStr("STARGATE_BASE_URL", &cfg.BaseUrl)
	setStr("STARGATE_HTTP_PORT", &cfg.HTTP.Port)
	setStr("STARGATE_GRPC_PORT", &cfg.GRPC.Port)
	setStr("STARGATE_DATABASE__DSN", &cfg.Database.DSN)
	setStr("STARGATE_REDIS_ADDR", &cfg.Redis.Addr)
	setStr("STARGATE_REDIS_PASSWORD", &cfg.Redis.Password)
	setStr("STARGATE_NATS_TARGET", &cfg.NATS.Target)
	setStr("STARGATE_AUTH_ISSUER", &cfg.Auth.Issuer)
	setStr("STARGATE_AUTH_PUBLIC_KEY", &cfg.Auth.PublicKeyPath)
	setStr("STARGATE_AUTH_PRIVATE_KEY", &cfg.Auth.PrivateKeyPath)
	setStr("STARGATE_AUTH_ACCESS_TOKEN_LIFETIME", &cfg.Auth.AccessTokenLifetime)
	setStr("STARGATE_AUTH_REFRESH_TOKEN_LIFETIME", &cfg.Auth.RefreshTokenLifetime)
	setStr("STARGATE_OIDC_PROVIDER_ISSUER", &cfg.OidcProvider.IssuerUri)
	setStr("STARGATE_SERVICES_DRIVE__GRPC", &cfg.Services.Drive.GRPC)
	setStr("STARGATE_SERVICES_WALLET__GRPC", &cfg.Services.Wallet.GRPC)
	setStr("STARGATE_SERVICES_PASS__GRPC", &cfg.Services.Pass.GRPC)
	setStr("STARGATE_SERVICES_BLADE__GRPC", &cfg.Services.Blade.GRPC)
	setStr("STARGATE_SERVICES_RING__GRPC", &cfg.Services.Ring.GRPC)
	setBool("STARGATE_CAPTCHA_SKIP", &cfg.Captcha.Skip)
	setBool("STARGATE_DISCOVERY_ENABLED", &cfg.Discovery.Enabled)
	setStr("STARGATE_DISCOVERY_TARGET", &cfg.Discovery.Target)
	setStr("STARGATE_DISCOVERY_REGISTRATION_TOKEN", &cfg.Discovery.RegistrationToken)
	setStr("STARGATE_DISCOVERY_SERVICE", &cfg.Discovery.Service)
	setStr("STARGATE_DISCOVERY_INSTANCE_ID", &cfg.Discovery.InstanceID)
	setStr("STARGATE_DISCOVERY_HTTP_ENDPOINT", &cfg.Discovery.HttpEndpoint)
	setStr("STARGATE_DISCOVERY_GRPC_ENDPOINT", &cfg.Discovery.GrpcEndpoint)
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

// AccessTokenLifetime parses the configured access-token lifetime.
func (c *Config) AccessTokenLifetime() time.Duration {
	if d, err := time.ParseDuration(c.Auth.AccessTokenLifetime); err == nil && d > 0 {
		return d
	}
	return time.Hour
}

// RefreshTokenLifetime parses the configured refresh-token lifetime.
func (c *Config) RefreshTokenLifetime() time.Duration {
	if d, err := time.ParseDuration(c.Auth.RefreshTokenLifetime); err == nil && d > 0 {
		return d
	}
	return 30 * 24 * time.Hour
}
