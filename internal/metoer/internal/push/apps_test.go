package push

import (
	"errors"
	"testing"

	"src.solsynth.dev/sosys/metoer/internal/config"
)

func testApps() *PushService {
	cfg := &config.Config{}
	cfg.Notifications.Push.DefaultApp = "dev.solsynth.solian"
	cfg.Notifications.Push.Apps = map[string]config.PushAppConfig{
		"dev.solsynth.solian": {},
		"dev.solsynth.maid":   {},
	}
	return NewApps(cfg, nil)
}

// TestValidateAppIdAllowlist pins the send/registration guard: only
// configured app ids pass; empty resolves to the default; anything else is
// rejected with ErrUnknownAppId (no silent fallback to default senders).
func TestValidateAppIdAllowlist(t *testing.T) {
	svc := &Service{apps: testApps()}

	if err := svc.validateAppId("dev.solsynth.solian"); err != nil {
		t.Fatalf("configured app id must pass: %v", err)
	}
	if err := svc.validateAppId("dev.solsynth.maid"); err != nil {
		t.Fatalf("configured app id must pass: %v", err)
	}
	if err := svc.validateAppId(""); err != nil {
		t.Fatalf("empty app id must pass (default resolution applies): %v", err)
	}
	if err := svc.validateAppId("dev.solsynth.unknown"); !errors.Is(err, ErrUnknownAppId) {
		t.Fatalf("unconfigured app id must be rejected with ErrUnknownAppId, got %v", err)
	}
}

// TestResolveAppIdPinsDefaultFallback pins that empty app ids resolve to the
// configured default while provided ids pass through untouched (the
// allowlist rejection happens in validateAppId, not ResolveAppId — read
// paths rely on the pass-through).
func TestResolveAppIdPinsDefaultFallback(t *testing.T) {
	apps := testApps()

	if got := apps.ResolveAppId("", true); got != "dev.solsynth.solian" {
		t.Fatalf("empty app id resolved to %q, want dev.solsynth.solian", got)
	}
	if got := apps.ResolveAppId("", false); got != "" {
		t.Fatalf("empty app id without default resolved to %q, want empty", got)
	}
	if got := apps.ResolveAppId("dev.solsynth.maid", true); got != "dev.solsynth.maid" {
		t.Fatalf("provided app id resolved to %q, want dev.solsynth.maid", got)
	}
}
