package config

import (
	"reflect"
	"testing"
)

// The login page hands a finished sign-in back to whatever origin the caller
// named. That origin comes from a third-party page, so only cookie-sharing
// hosts may receive it; everything else has to be refused or the page becomes
// an open redirector.
func TestAllowsSignInTarget(t *testing.T) {
	cfg := Default()
	cfg.SiteUrl = "https://id.solian.app"
	cfg.Auth.CookieDomain = ".solian.app"
	cfg.Auth.CookieSecure = true

	allowed := []string{
		"https://app.solian.app/oauth/callback",
		"https://a.b.solian.app/cb?x=1",
		"https://solian.app/",
		"https://id.solian.app/settings",
		"/account",
		"/security/connections?linked=github",
	}
	for _, raw := range allowed {
		if !cfg.AllowsSignInTarget(raw) {
			t.Errorf("AllowsSignInTarget(%q) = false, want true", raw)
		}
	}

	refused := []string{
		"",
		"//evil.example.com/cb",
		"https://evil-solian.app/cb",
		"https://solian.app.evil.example/cb",
		"https://notsolian.app/cb",
		// The session cookie is Secure here, so an http hand-off arrives
		// signed out.
		"http://app.solian.app/cb",
		"javascript:alert(1)",
		"data:text/html,<script>",
		"https://app.solian.app@evil.example/cb",
		"mailto:someone@solian.app",
		"https://app.solian.app/\nSet-Cookie: x=y",
		"relative/path",
	}
	for _, raw := range refused {
		if cfg.AllowsSignInTarget(raw) {
			t.Errorf("AllowsSignInTarget(%q) = true, want false", raw)
		}
	}
}

// A deployment without cookieSecure serves http, so a plain-http hand-off to a
// cookie host must survive; the Secure rule is what rejects it otherwise.
func TestAllowsSignInTarget_PermitsHTTPWithoutSecureCookie(t *testing.T) {
	cfg := Default()
	cfg.SiteUrl = "http://localhost:3000"
	cfg.Auth.CookieDomain = "localhost"
	cfg.Auth.CookieSecure = false

	for _, raw := range []string{
		"http://localhost:3001/cb",
		"http://localhost/cb",
		"http://app.localhost/cb",
	} {
		if !cfg.AllowsSignInTarget(raw) {
			t.Errorf("AllowsSignInTarget(%q) = false, want true", raw)
		}
	}
}

// The hand-off set is what the cookie reaches, so a deployment that shares the
// cookie across a whole domain shares the login page with that whole domain.
func TestSignInRedirectHosts(t *testing.T) {
	cfg := Default()
	cfg.SiteUrl = "https://id.solian.app"
	cfg.Auth.CookieDomain = ".solian.app"

	want := []string{"solian.app", "id.solian.app"}
	if got := cfg.SignInRedirectHosts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("SignInRedirectHosts() = %v, want %v", got, want)
	}

	// The site host is deduplicated when it is the cookie domain itself.
	cfg.Auth.CookieDomain = "id.solian.app"
	if got := cfg.SignInRedirectHosts(); !reflect.DeepEqual(got, []string{"id.solian.app"}) {
		t.Fatalf("SignInRedirectHosts() = %v, want one entry", got)
	}

	// A port on either source is dropped: the cookie ignores it.
	cfg.Auth.CookieDomain = "localhost"
	cfg.SiteUrl = "http://localhost:8080"
	if got := cfg.SignInRedirectHosts(); !reflect.DeepEqual(got, []string{"localhost"}) {
		t.Fatalf("SignInRedirectHosts() = %v, want [localhost]", got)
	}
}

// A hand-off client's hosts are legitimate landing targets even though no
// cookie reaches them: the sign-in finishes there by code exchange. A host no
// client registered for is not, or the policy would advertise a destination
// the issuer would then refuse to mint a code for.
func TestSignInRedirectHostsIncludeHandoffClients(t *testing.T) {
	cfg := Default()
	cfg.SiteUrl = "https://id.solian.app"
	cfg.Auth.CookieDomain = ".solian.app"
	cfg.Auth.CookieSecure = true
	cfg.Auth.HandoffClients = []HandoffClient{{
		Id:            "app.solian.express",
		Secret:        "s3cr3t",
		RedirectHosts: []string{"solsynth.dev"},
	}}

	hosts := cfg.SignInRedirectHosts()
	if !reflect.DeepEqual(hosts, []string{"solian.app", "id.solian.app", "solsynth.dev"}) {
		t.Fatalf("SignInRedirectHosts() = %v", hosts)
	}
	if !cfg.AllowsSignInTarget("https://dx.solsynth.dev/releases") {
		t.Error("a registered hand-off host was refused as a landing target")
	}
	if cfg.AllowsSignInTarget("https://evil.example/") {
		t.Error("an unregistered host was accepted as a landing target")
	}
}

// A hand-off client that could never be handed anything, and a duplicate id
// that would make binding a code ambiguous, are configuration errors.
func TestValidateRejectsBrokenHandoffClients(t *testing.T) {
	broken := func() *Config {
		cfg := Default()
		cfg.Auth.CookieDomain = "localhost"
		// Validate checks the captcha opt-out first on purpose; these cases are
		// about the hand-off rules, so satisfy it.
		cfg.Captcha.AllowDisabled = true
		return cfg
	}

	noHosts := broken()
	noHosts.Auth.HandoffClients = []HandoffClient{{Id: "a", Secret: "s"}}
	if err := noHosts.Validate(); err == nil {
		t.Error("Validate accepted a hand-off client with no redirectHosts")
	}

	duplicate := broken()
	duplicate.Auth.HandoffClients = []HandoffClient{
		{Id: "a", Secret: "s", RedirectHosts: []string{"a.example"}},
		{Id: "a", Secret: "s", RedirectHosts: []string{"b.example"}},
	}
	if err := duplicate.Validate(); err == nil {
		t.Error("Validate accepted a duplicate hand-off client id")
	}

	badTTL := broken()
	badTTL.Auth.HandoffCodeTTL = "not-a-duration"
	if err := badTTL.Validate(); err == nil {
		t.Error("Validate accepted an unparsable handoffCodeTTL")
	}

	ok := broken()
	ok.Auth.HandoffClients = []HandoffClient{{Id: "a", Secret: "s", RedirectHosts: []string{"a.example"}}}
	ok.Auth.HandoffCodeTTL = "45s"
	if err := ok.Validate(); err != nil {
		t.Errorf("Validate rejected a valid hand-off client: %v", err)
	}
}
