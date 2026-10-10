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
	cfg, err := Load(filepath.Join("..", "..", "config.example.toml"))
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
