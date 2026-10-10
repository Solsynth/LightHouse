// Package config loads the Stargate TOML configuration with environment
// overrides. The file format follows the house pattern shared by sibling
// services (see ElecPostal's config.example.toml).
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
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
		ValidIssuers         []string `toml:"validIssuers"`
		Audiences            []string `toml:"audiences"`
		PublicKeyPath        string   `toml:"publicKeyPath"`
		PrivateKeyPath       string   `toml:"privateKeyPath"`
		AccessTokenLifetime  string   `toml:"accessTokenLifetime"`
		RefreshTokenLifetime string   `toml:"refreshTokenLifetime"`
		RefreshGracePeriod   string   `toml:"refreshGracePeriod"`
		CookieDomain         string   `toml:"cookieDomain"`
		CookieSecure         bool     `toml:"cookieSecure"`
	} `toml:"auth"`

	OidcProvider struct {
		IssuerUri                 string        `toml:"issuerUri"`
		PublicKeyPath             string        `toml:"publicKeyPath"`
		PrivateKeyPath            string        `toml:"privateKeyPath"`
		AccessTokenLifetime       string        `toml:"accessTokenLifetime"`
		RefreshTokenLifetime      string        `toml:"refreshTokenLifetime"`
		AuthorizationCodeLifetime string        `toml:"authorizationCodeLifetime"`
		RequireHttpsMetadata      bool          `toml:"requireHttpsMetadata"`
		Clients                   []OAuthClient `toml:"clients"`
	} `toml:"oidcProvider"`

	Captcha struct {
		Provider  string `toml:"provider"`
		APIKey    string `toml:"apiKey"`
		APISecret string `toml:"apiSecret"`
		Skip      bool   `toml:"skip"`
		// AllowDisabled is the explicit opt-out from captcha verification.
		// Stargate has captcha-gated flows (account creation, password reset
		// request, /auth/captcha/verify), so a deployment without a working
		// verifier must acknowledge that by setting allow_disabled = true;
		// otherwise the configuration is rejected at startup (see Validate).
		AllowDisabled bool `toml:"allow_disabled"`
	} `toml:"captcha"`

	WebAuthn struct {
		RpId           string   `toml:"rpId"`
		RpName         string   `toml:"rpName"`
		RelatedOrigins []string `toml:"relatedOrigins"`
	} `toml:"webauthn"`

	// AccountActivation mirrors Passport's AccountActivation settings. Entry
	// tests (exam logic) stay in Passport, so Stargate only needs to know
	// whether tests are required to defer activation to Passport — the exam
	// evaluation itself never runs here.
	AccountActivation struct {
		TestsEnabled     bool     `toml:"testsEnabled"`
		RequiredTestKeys []string `toml:"requiredTestKeys"`
	} `toml:"accountActivation"`

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
		Twitter   TwitterClient   `toml:"twitter"`
		LastFm    LastFmClient    `toml:"lastfm"`
	} `toml:"oidc"`

	Security SecurityConfig `toml:"security"`

	// SecurityTxt is the contact block of the RFC 9116
	// /.well-known/security.txt the HTTP layer serves.
	SecurityTxt struct {
		Contact string `toml:"contact"`
	} `toml:"securityTxt"`
}

// DefaultSecurityContact is the vulnerability-disclosure contact used when
// [securityTxt] contact is unset.
const DefaultSecurityContact = "mailto:security@solsynth.dev"

// SecurityContact returns the configured security.txt contact, falling back to
// DefaultSecurityContact.
func (c *Config) SecurityContact() string {
	if c == nil || strings.TrimSpace(c.SecurityTxt.Contact) == "" {
		return DefaultSecurityContact
	}
	return strings.TrimSpace(c.SecurityTxt.Contact)
}

// SecurityConfig controls fail2ban, challenge risk escalation, and trusted
// session thresholds. The defaults are production-shaped so a missing
// [security] section never weakens the deployment.
type SecurityConfig struct {
	Fail2banMaxFails int    `toml:"fail2banMaxFails"`
	Fail2banWindow   string `toml:"fail2banWindow"`
	Fail2banBlockFor string `toml:"fail2banBlockFor"`
	// Fail2banDeclineMax is how many declined challenges (declined by a
	// trusted session) the requesting IP may accumulate within Fail2banWindow
	// before it is blocked for Fail2banBlockFor. Declines are counted against
	// the IP that started the login, not the one that declined it. "0"
	// disables the tracking.
	Fail2banDeclineMax         int `toml:"fail2banDeclineMax"`
	ChallengeFailEscalateAfter int `toml:"challengeFailEscalateAfter"`
	// MaxChallengesPerIp caps how many new challenges a single IP may create
	// within ChallengeWindow before it is rate limited. A challenge that
	// completes into a session releases its slot, so successful logins never
	// count against the quota. "0" disables the quota.
	MaxChallengesPerIp int    `toml:"maxChallengesPerIp"`
	ChallengeWindow    string `toml:"challengeWindow"`
	// MaxChallengeAttempts caps how many failed credential submissions a
	// single challenge accepts before further verification is refused with
	// 429 (the IP-failure fail2ban block normally trips first). "0" disables
	// the cap.
	MaxChallengeAttempts int `toml:"maxChallengeAttempts"`
	// TrustedProxyHops is how many reverse proxies in front of Stargate
	// append to X-Forwarded-For. The client IP is read that many entries from
	// the right, so a client-supplied leftmost entry can never choose the
	// address the rate limiter sees. Defaults to 1 (one trusted edge proxy,
	// e.g. Blade).
	TrustedProxyHops int `toml:"trustedProxyHops"`
	// RecentLoginGrace is how long a completed login from the same client
	// user agent suppresses the IP-novelty risk terms of the next challenge
	// (see hint in config.example.toml). "0" disables the suppression.
	RecentLoginGrace     string `toml:"recentLoginGrace"`
	TrustedSessionMaxGap string `toml:"trustedSessionMaxGap"`
}

// Fail2banWindowDuration parses the fail2ban counting window.
func (s SecurityConfig) Fail2banWindowDuration() time.Duration {
	if d, err := time.ParseDuration(s.Fail2banWindow); err == nil && d > 0 {
		return d
	}
	return 15 * time.Minute
}

// Fail2banBlockForDuration parses the fail2ban block duration.
func (s SecurityConfig) Fail2banBlockForDuration() time.Duration {
	if d, err := time.ParseDuration(s.Fail2banBlockFor); err == nil && d > 0 {
		return d
	}
	return 30 * time.Minute
}

// ChallengeWindowDuration parses the new-challenge quota window.
func (s SecurityConfig) ChallengeWindowDuration() time.Duration {
	if d, err := time.ParseDuration(s.ChallengeWindow); err == nil && d > 0 {
		return d
	}
	return time.Hour
}

// RecentLoginGraceDuration parses the window in which a completed login from
// the same client user agent suppresses the IP-novelty risk terms. A missing
// or unparsable value falls back to 30 minutes; "0" disables the suppression.
func (s SecurityConfig) RecentLoginGraceDuration() time.Duration {
	if s.RecentLoginGrace == "" {
		return 30 * time.Minute
	}
	if d, err := time.ParseDuration(s.RecentLoginGrace); err == nil && d >= 0 {
		return d
	}
	return 30 * time.Minute
}

// TrustedSessionMaxGapDuration parses the maximum time since last granted
// activity for a session to be considered trusted.
func (s SecurityConfig) TrustedSessionMaxGapDuration() time.Duration {
	if d, err := time.ParseDuration(s.TrustedSessionMaxGap); err == nil && d > 0 {
		return d
	}
	return 720 * time.Hour // 30 days
}

type ServiceTarget struct {
	GRPC string `toml:"grpc"`
}

// Enabled reports whether an outbound service target was configured.
func (s ServiceTarget) Enabled() bool {
	return strings.TrimSpace(s.GRPC) != ""
}

// OAuthClient configures a custom OAuth/OIDC client without requiring
// DysonNetwork.Develop. IDs and secrets are deployment-owned values.
type OAuthClient struct {
	Id                string   `toml:"id"`
	Slug              string   `toml:"slug"`
	Name              string   `toml:"name"`
	ClientSecret      string   `toml:"clientSecret"`
	Status            int      `toml:"status"`
	HomeUri           string   `toml:"homeUri"`
	PolicyUri         string   `toml:"policyUri"`
	TermsOfServiceUri string   `toml:"termsOfServiceUri"`
	RedirectUris      []string `toml:"redirectUris"`
	AllowedScopes     []string `toml:"allowedScopes"`
	IsPublicClient    bool     `toml:"isPublicClient"`
}

// FindLocalOAuthClient finds a configured client by its ID or slug.
func (c *Config) FindLocalOAuthClient(identifier string) *OAuthClient {
	if c == nil || identifier == "" {
		return nil
	}
	for i := range c.OidcProvider.Clients {
		client := &c.OidcProvider.Clients[i]
		if client.Id == identifier || client.Slug == identifier {
			return client
		}
	}
	return nil
}

// FindLocalOAuthClientByID finds a configured client by its stable ID.
func (c *Config) FindLocalOAuthClientByID(id string) *OAuthClient {
	if c == nil || id == "" {
		return nil
	}
	for i := range c.OidcProvider.Clients {
		client := &c.OidcProvider.Clients[i]
		if client.Id == id {
			return client
		}
	}
	return nil
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

type TwitterClient struct {
	ClientId     string `toml:"clientId"`
	ClientSecret string `toml:"clientSecret"`
}

// LastFmClient configures the Last.fm web-application API account. Last.fm has
// no OAuth2 client registration: the API key is the 32-character public
// identifier and the shared secret signs every call. The callback URL must be
// registered on the Last.fm API account page and match
// {SiteUrl}/auth/callback/lastfm.
type LastFmClient struct {
	ApiKey    string `toml:"apiKey"`
	ApiSecret string `toml:"apiSecret"`
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
	cfg.Auth.ValidIssuers = []string{"solar-network", "https://nt.solian.app"}
	cfg.Auth.Audiences = []string{"http://localhost:5071", "https://localhost:7099"}
	cfg.Auth.AccessTokenLifetime = "5m"
	cfg.Auth.RefreshTokenLifetime = "720h"
	cfg.Auth.RefreshGracePeriod = "60s"
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
	cfg.Security.Fail2banMaxFails = 5
	cfg.Security.Fail2banWindow = "15m"
	cfg.Security.Fail2banBlockFor = "30m"
	cfg.Security.Fail2banDeclineMax = 3
	cfg.Security.ChallengeFailEscalateAfter = 2
	cfg.Security.MaxChallengesPerIp = 3
	cfg.Security.ChallengeWindow = "1h"
	cfg.Security.MaxChallengeAttempts = 5
	cfg.Security.TrustedProxyHops = 1
	cfg.Security.RecentLoginGrace = "30m"
	cfg.Security.TrustedSessionMaxGap = "720h"
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
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
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
	setStr("STARGATE_AUTH_REFRESH_GRACE_PERIOD", &cfg.Auth.RefreshGracePeriod)
	setStr("STARGATE_OIDC_PROVIDER_ISSUER", &cfg.OidcProvider.IssuerUri)
	setStr("STARGATE_OIDC_TWITTER_CLIENT_ID", &cfg.Oidc.Twitter.ClientId)
	setStr("STARGATE_OIDC_TWITTER_CLIENT_SECRET", &cfg.Oidc.Twitter.ClientSecret)
	setStr("STARGATE_OIDC_LASTFM_API_KEY", &cfg.Oidc.LastFm.ApiKey)
	setStr("STARGATE_OIDC_LASTFM_API_SECRET", &cfg.Oidc.LastFm.ApiSecret)
	setStr("STARGATE_SERVICES_DRIVE__GRPC", &cfg.Services.Drive.GRPC)
	setStr("STARGATE_SERVICES_WALLET__GRPC", &cfg.Services.Wallet.GRPC)
	setStr("STARGATE_SERVICES_PASS__GRPC", &cfg.Services.Pass.GRPC)
	setStr("STARGATE_SERVICES_BLADE__GRPC", &cfg.Services.Blade.GRPC)
	setStr("STARGATE_SERVICES_RING__GRPC", &cfg.Services.Ring.GRPC)
	setStr("STARGATE_SERVICES_DEVELOP__GRPC", &cfg.Services.Develop.GRPC)
	setBool("STARGATE_CAPTCHA_SKIP", &cfg.Captcha.Skip)
	setBool("STARGATE_CAPTCHA_ALLOWDISABLED", &cfg.Captcha.AllowDisabled)
	setBool("STARGATE_ACCOUNT_ACTIVATION__TESTS_ENABLED", &cfg.AccountActivation.TestsEnabled)
	setBool("STARGATE_DISCOVERY_ENABLED", &cfg.Discovery.Enabled)
	setStr("STARGATE_DISCOVERY_TARGET", &cfg.Discovery.Target)
	setStr("STARGATE_DISCOVERY_REGISTRATION_TOKEN", &cfg.Discovery.RegistrationToken)
	setStr("STARGATE_DISCOVERY_SERVICE", &cfg.Discovery.Service)
	setStr("STARGATE_DISCOVERY_INSTANCE_ID", &cfg.Discovery.InstanceID)
	setStr("STARGATE_DISCOVERY_HTTP_ENDPOINT", &cfg.Discovery.HttpEndpoint)
	setStr("STARGATE_DISCOVERY_GRPC_ENDPOINT", &cfg.Discovery.GrpcEndpoint)
	setInt("STARGATE_SECURITY_FAIL2BANMAXFAILS", &cfg.Security.Fail2banMaxFails)
	setStr("STARGATE_SECURITY_FAIL2BANWINDOW", &cfg.Security.Fail2banWindow)
	setStr("STARGATE_SECURITY_FAIL2BANBLOCKFOR", &cfg.Security.Fail2banBlockFor)
	setInt("STARGATE_SECURITY_FAIL2BANDECLINEMAX", &cfg.Security.Fail2banDeclineMax)
	setInt("STARGATE_SECURITY_CHALLENGEFAILESCALATEAFTER", &cfg.Security.ChallengeFailEscalateAfter)
	setInt("STARGATE_SECURITY_MAXCHALLENGESPERIP", &cfg.Security.MaxChallengesPerIp)
	setInt("STARGATE_SECURITY_MAXCHALLENGEATTEMPTS", &cfg.Security.MaxChallengeAttempts)
	setInt("STARGATE_SECURITY_TRUSTEDPROXYHOPS", &cfg.Security.TrustedProxyHops)
	setStr("STARGATE_SECURITY_CHALLENGEWINDOW", &cfg.Security.ChallengeWindow)
	setStr("STARGATE_SECURITY_TRUSTEDSESSIONMAXGAP", &cfg.Security.TrustedSessionMaxGap)
	setStr("STARGATE_SECURITY_RECENTLOGINGRACE", &cfg.Security.RecentLoginGrace)
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

func setInt(key string, dst *int) {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*dst = n
		}
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

// RefreshGracePeriod parses how long the immediately previous refresh token
// remains acceptable after a rotation. A zero/negative/invalid value disables
// the window (a replayed previous token is rejected immediately).
func (c *Config) RefreshGracePeriod() time.Duration {
	if d, err := time.ParseDuration(c.Auth.RefreshGracePeriod); err == nil && d > 0 {
		return d
	}
	return 0
}

// CaptchaEnabled reports whether an external captcha verifier is configured.
// Skip and incomplete credentials both mean the verifier is unusable, i.e.
// captcha tokens cannot be checked. Callers must not treat that as "verification
// passed": see CaptchaRequired and AuthService.ValidateCaptcha.
func (c *Config) CaptchaEnabled() bool {
	if c == nil || c.Captcha.Skip {
		return false
	}
	return strings.TrimSpace(c.Captcha.Provider) != "" &&
		strings.TrimSpace(c.Captcha.APISecret) != ""
}

// CaptchaRequired reports a misconfiguration: captcha-gated flows exist (they
// always do — account creation, the password-reset request and
// /auth/captcha/verify) but no verifier is usable and the deployment did not
// explicitly opt out with [captcha] allow_disabled = true. Callers must fail
// closed while this is true.
func (c *Config) CaptchaRequired() bool {
	if c == nil {
		return true
	}
	return !c.CaptchaEnabled() && !c.Captcha.AllowDisabled
}

// Validate rejects configurations that must never come up: today that is a
// captcha-gated deployment without a usable verifier and without the explicit
// [captcha] allow_disabled opt-out. Refusing to start beats silently serving
// login, registration and password-reset flows with an unconfigured captcha.
func (c *Config) Validate() error {
	if c.CaptchaRequired() {
		return fmt.Errorf("captcha is not configured: set [captcha] provider/apiSecret (or apiKey where the provider needs one), or opt out explicitly with [captcha] allow_disabled = true")
	}
	return nil
}

// TrustedProxyHopCount is the number of trusted reverse proxies in front of
// Stargate, at least 1 (a direct deployment still has its own listener as the
// last hop). It is what middleware.ClientIP indexes X-Forwarded-For by.
func (s SecurityConfig) TrustedProxyHopCount() int {
	if s.TrustedProxyHops < 1 {
		return 1
	}
	return s.TrustedProxyHops
}
