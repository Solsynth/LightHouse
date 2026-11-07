package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadLocalOAuthClients(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `[captcha]
# Stargate refuses to start without a usable captcha verifier unless the
# deployment opts out explicitly.
allow_disabled = true

[oidcProvider]
[[oidcProvider.clients]]
id = "local-client-id"
slug = "local-client"
name = "Local Client"
clientSecret = "local-secret"
status = 2
redirectUris = ["https://client.example/callback"]
allowedScopes = ["openid", "profile"]
isPublicClient = false
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	client := cfg.FindLocalOAuthClient("local-client")
	if client == nil {
		t.Fatal("local OAuth client was not loaded")
	}
	if client.Id != "local-client-id" || client.ClientSecret != "local-secret" {
		t.Fatalf("loaded client = %+v", client)
	}
	if len(client.RedirectUris) != 1 || client.RedirectUris[0] != "https://client.example/callback" {
		t.Fatalf("redirect URIs = %v", client.RedirectUris)
	}
}

func TestOptionalServiceTarget(t *testing.T) {
	if (ServiceTarget{}).Enabled() {
		t.Fatal("empty service target reported enabled")
	}
	if !(ServiceTarget{GRPC: " dns:9090 "}).Enabled() {
		t.Fatal("configured service target reported disabled")
	}
}

func TestLoadExampleConfiguration(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "config.example.toml"))
	if err != nil {
		t.Fatalf("Load example configuration: %v", err)
	}
	if cfg.Services.Wallet.Enabled() || cfg.Services.Develop.Enabled() {
		t.Fatal("example configuration must leave outbound services disabled")
	}
	// The shipped example cannot carry verifier credentials, so it must carry
	// the explicit opt-out instead of silently leaving captcha disabled: Load
	// rejects a captcha-gated deployment with no verifier and no opt-out.
	if cfg.CaptchaEnabled() {
		t.Fatal("example configuration must not look like a configured captcha")
	}
	if !cfg.Captcha.AllowDisabled {
		t.Fatal("example configuration must opt out of captcha explicitly ([captcha] allow_disabled)")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("example configuration must pass Validate: %v", err)
	}
	wantIssuers := []string{"solar-network", "https://nt.solian.app"}
	if len(cfg.Auth.ValidIssuers) != len(wantIssuers) {
		t.Fatalf("valid issuers = %v, want %v", cfg.Auth.ValidIssuers, wantIssuers)
	}
	for i, want := range wantIssuers {
		if cfg.Auth.ValidIssuers[i] != want {
			t.Fatalf("valid issuer %d = %q, want %q", i, cfg.Auth.ValidIssuers[i], want)
		}
	}
}

// TestCaptchaPolicyFailsClosed pins the fail-closed captcha policy: captcha is
// required for the captcha-gated flows unless the deployment has a usable
// verifier or opted out explicitly, and Load refuses to start otherwise.
func TestCaptchaPolicyFailsClosed(t *testing.T) {
	cases := []struct {
		name         string
		mutate       func(*Config)
		wantEnabled  bool
		wantRequired bool
	}{
		{
			name:         "unconfigured and no opt-out is required",
			mutate:       func(c *Config) {},
			wantEnabled:  false,
			wantRequired: true,
		},
		{
			name:         "explicit opt-out disables the requirement",
			mutate:       func(c *Config) { c.Captcha.AllowDisabled = true },
			wantEnabled:  false,
			wantRequired: false,
		},
		{
			name:         "configured verifier satisfies the requirement",
			mutate:       func(c *Config) { c.Captcha.Skip = false; c.Captcha.APISecret = "s3cr3t" },
			wantEnabled:  true,
			wantRequired: false,
		},
		{
			name:         "skip with a verifier configured still needs the opt-out",
			mutate:       func(c *Config) { c.Captcha.Skip = true; c.Captcha.APISecret = "s3cr3t" },
			wantEnabled:  false,
			wantRequired: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.Captcha.Skip = true
			tc.mutate(cfg)
			if got := cfg.CaptchaEnabled(); got != tc.wantEnabled {
				t.Fatalf("CaptchaEnabled = %v, want %v", got, tc.wantEnabled)
			}
			if got := cfg.CaptchaRequired(); got != tc.wantRequired {
				t.Fatalf("CaptchaRequired = %v, want %v", got, tc.wantRequired)
			}
			if err := cfg.Validate(); (err != nil) != tc.wantRequired {
				t.Fatalf("Validate error = %v, want error: %v", err, tc.wantRequired)
			}
		})
	}
}

// TestLoadRejectsUnconfiguredCaptcha pins that a configuration which silently
// disabled captcha is refused at load time instead of serving the gated flows.
func TestLoadRejectsUnconfiguredCaptcha(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[captcha]\nprovider = \"cloudflare\"\napiSecret = \"\"\nskip = true\n"), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted a captcha-gated configuration with no verifier and no opt-out")
	}

	optedOut := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(optedOut, []byte("[captcha]\nskip = true\nallow_disabled = true\n"), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := Load(optedOut); err != nil {
		t.Fatalf("Load rejected an explicit captcha opt-out: %v", err)
	}
}

// TestTrustedProxyHopCount pins the proxy-depth normalization: absent or
// nonsensical values mean one trusted hop, never zero (which would let a
// client-supplied leftmost X-Forwarded-For entry through).
func TestTrustedProxyHopCount(t *testing.T) {
	cases := map[int]int{0: 1, -3: 1, 1: 1, 2: 2}
	for in, want := range cases {
		cfg := Default()
		cfg.Security.TrustedProxyHops = in
		if got := cfg.Security.TrustedProxyHopCount(); got != want {
			t.Fatalf("TrustedProxyHopCount(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestModuleListsParse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `
[captcha]
allow_disabled = true

[modules]
enabled = ["app.solian.id.auth", "app.solian.id.accounts"]
disabled = ["app.solian.e2ee"]

[ports]
files = "app.solian.drive"
`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Modules.Enabled) != 2 || cfg.Modules.Enabled[0] != "app.solian.id.auth" {
		t.Fatalf("enabled = %v", cfg.Modules.Enabled)
	}
	if len(cfg.Modules.Disabled) != 1 || cfg.Modules.Disabled[0] != "app.solian.e2ee" {
		t.Fatalf("disabled = %v", cfg.Modules.Disabled)
	}
	if cfg.Ports["files"] != "app.solian.drive" {
		t.Fatalf("ports = %v", cfg.Ports)
	}
}

func TestSingleUserValidation(t *testing.T) {
	cfg := Default()
	cfg.Captcha.AllowDisabled = true
	cfg.Auth.Provider = "single-user"
	cfg.Auth.SingleUser.Username = "admin"
	if err := cfg.Validate(); err == nil {
		t.Fatal("single-user without passwordHash accepted")
	}
	cfg.Auth.SingleUser.PasswordHash = "$2a$10$abcdefghijklmnopqrstuv"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid single-user config rejected: %v", err)
	}
}

// TestPushSubjectMatchesGatewaySubscription pins the websocket push subject
// against Blade's `<prefix><suffix>` construction. Blade's gateway subscribes
// to TrimSpace(nats.websocketSubjectPrefix) + "push"; a dotted subject
// ("websocket.push") is a different token space, so pushes published there
// were never delivered and failed silently on both sides.
func TestPushSubjectMatchesGatewaySubscription(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "config.example.toml"))
	if err != nil {
		t.Fatalf("load example config: %v", err)
	}

	const bladeSubject = "websocket_" + "push" // prefix default + suffix
	if cfg.NATS.WebsocketPushSubject != bladeSubject {
		t.Fatalf("websocketPushSubject = %q, want %q (the C# fleet and Metoer "+
			"publish to the same subject Blade subscribes to)",
			cfg.NATS.WebsocketPushSubject, bladeSubject)
	}
}

// TestWebAuthnOrigins pins the set both registration verification and
// GET /.well-known/webauthn publish. The regression: the defaults named only
// the legacy dev port (3000), so a passkey ceremony on the Vite dev server
// (5173) or the SPA Stargate serves (8080) failed go-webauthn's origin check
// with PADLOCK_PASSKEY_REGISTRATION_FAILED.
func TestWebAuthnOrigins(t *testing.T) {
	cfg := Default()
	for _, want := range []string{
		"http://localhost:3000",
		"http://localhost:5173",
		"http://localhost:8080",
		"http://localhost:5011", // baseUrl
	} {
		if !originIn(cfg.WebAuthnOrigins(), want) {
			t.Fatalf("default WebAuthnOrigins() lacks %q: %v", want, cfg.WebAuthnOrigins())
		}
	}

	cfg.SiteUrl = "https://web.solian.app/"
	cfg.BaseUrl = "https://api.solian.app"
	cfg.WebAuthn.RelatedOrigins = []string{
		"HTTPS://SOLIAN.APP:443",
		"https://solian.app",
		"not a url",
	}
	origins := cfg.WebAuthnOrigins()
	for _, want := range []string{
		"https://web.solian.app",
		"https://api.solian.app",
		"https://solian.app",
	} {
		if !originIn(origins, want) {
			t.Fatalf("WebAuthnOrigins() lacks %q: %v", want, origins)
		}
	}
	if countOrigin(origins, "https://solian.app") != 1 {
		t.Fatalf("duplicate normalized origin: %v", origins)
	}
	if originIn(origins, "not a url") {
		t.Fatalf("invalid origin accepted: %v", origins)
	}
}

func TestWebAuthnEnvOverrides(t *testing.T) {
	t.Setenv("STARGATE_WEBAUTHN_RPID", "solian.app")
	t.Setenv("STARGATE_WEBAUTHN_RPNAME", "Solar Network")
	t.Setenv("STARGATE_WEBAUTHN_RELATEDORIGINS", "https://web.solian.app, https://solian.app")
	cfg := Default()
	applyEnvOverrides(cfg)
	if cfg.WebAuthn.RpId != "solian.app" {
		t.Fatalf("rpId = %q", cfg.WebAuthn.RpId)
	}
	if len(cfg.WebAuthn.RelatedOrigins) != 2 || cfg.WebAuthn.RelatedOrigins[0] != "https://web.solian.app" {
		t.Fatalf("relatedOrigins = %v", cfg.WebAuthn.RelatedOrigins)
	}
}

func originIn(origins []string, want string) bool {
	return countOrigin(origins, want) > 0
}

func countOrigin(origins []string, want string) int {
	count := 0
	for _, origin := range origins {
		if origin == want {
			count++
		}
	}
	return count
}
