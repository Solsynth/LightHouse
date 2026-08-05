package securityctl

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/model"
)

// TestNewRecoveryCode verifies the C# Guid.ToString("N") shape: 32 lowercase
// hex chars, stored plaintext for later comparison.
func TestNewRecoveryCode(t *testing.T) {
	code := newRecoveryCode()
	if len(code) != 32 {
		t.Fatalf("recovery code length = %d, want 32", len(code))
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(code) {
		t.Fatalf("recovery code %q is not 32 lowercase hex chars", code)
	}
	if code == newRecoveryCode() {
		t.Fatal("two recovery codes collided")
	}
}

// TestBase32Encode verifies the RFC 4648 unpadded base32 encoding used for
// TOTP secrets (Otp.NET Base32Encoding.ToString(20 bytes) => 32 chars).
func TestBase32Encode(t *testing.T) {
	// RFC 4648 test vectors.
	cases := map[string]string{
		"":        "",
		"f":       "MY",
		"fo":      "MZXQ",
		"foo":     "MZXW6",
		"foob":    "MZXW6YQ",
		"fooba":   "MZXW6YTB",
		"foobar":  "MZXW6YTBOI",
		"foobarb": "MZXW6YTBOJRA",
	}
	for input, want := range cases {
		if got := base32Encode([]byte(input)); got != want {
			t.Errorf("base32Encode(%q) = %q, want %q", input, got, want)
		}
	}
	secret := newTotpSecret()
	if len(secret) != 32 {
		t.Fatalf("totp secret length = %d, want 32", len(secret))
	}
	if strings.Contains(secret, "=") {
		t.Fatalf("totp secret %q has padding; C# Base32Encoding omits it", secret)
	}
}

// TestBuildAuthFactorRecoveryCode verifies the recovery factor stores the
// plaintext code and surfaces it once via created_response.
func TestBuildAuthFactorRecoveryCode(t *testing.T) {
	c := &controller{}
	account := &model.Account{Id: "11111111-1111-1111-1111-111111111111", Name: "testuser"}
	factor, err := c.buildAuthFactor(context.Background(), account, model.AuthFactorTypeRecoveryCode, nil)
	if err != nil {
		t.Fatal(err)
	}
	if factor.Secret == "" || len(factor.Secret) != 32 {
		t.Fatalf("recovery factor secret = %q, want 32-hex plaintext", factor.Secret)
	}
	if factor.EnabledAt == nil {
		t.Fatal("recovery factor must be created enabled")
	}
	created, ok := factor.CreatedResponse["recovery_code"].(string)
	if !ok || created != factor.Secret {
		t.Fatalf("created_response.recovery_code = %q, want the stored plaintext %q", created, factor.Secret)
	}
}

// TestBuildAuthFactorPassword verifies Password/PinCode secrets are bcrypt
// hashed (cost 12) so they verify via auth.VerifyFactorPassword.
func TestBuildAuthFactorPassword(t *testing.T) {
	c := &controller{}
	account := &model.Account{Id: "11111111-1111-1111-1111-111111111111", Name: "testuser"}
	secret := "s3cret-password"
	factor, err := c.buildAuthFactor(context.Background(), account, model.AuthFactorTypePassword, &secret)
	if err != nil {
		t.Fatal(err)
	}
	if factor.Secret == secret || !strings.HasPrefix(factor.Secret, "$2") {
		t.Fatalf("password factor secret was not bcrypt-hashed: %q", factor.Secret)
	}
	valid, err := auth.VerifyFactorPassword(factor, secret)
	if err != nil || !valid {
		t.Fatalf("bcrypt hash does not verify: valid=%v err=%v", valid, err)
	}
	if factor.Trustworthy != 1 || factor.EnabledAt == nil {
		t.Fatalf("password factor wrong trust/enabled state: %+v", factor)
	}
}

// TestBuildAuthFactorTimedCode verifies the otpauth URI shape matches the C#
// CreateTimedCodeFactor output.
func TestBuildAuthFactorTimedCode(t *testing.T) {
	c := &controller{}
	account := &model.Account{Id: "11111111-1111-1111-1111-111111111111", Name: "testuser"}
	factor, err := c.buildAuthFactor(context.Background(), account, model.AuthFactorTypeTimedCode, nil)
	if err != nil {
		t.Fatal(err)
	}
	uri, ok := factor.CreatedResponse["uri"].(string)
	if !ok {
		t.Fatal("timed-code factor missing created_response.uri")
	}
	wantPrefix := "otpauth://totp/SolarNetwork:testuser?secret=" + factor.Secret +
		"&issuer=SolarNetwork&digits=6&period=30"
	if uri != wantPrefix {
		t.Fatalf("uri = %q, want %q", uri, wantPrefix)
	}
}

// TestBuildAuthFactorInvalidType verifies unsupported types yield the
// PADLOCK_AUTH_FACTOR_INVALID path (nil factor).
func TestBuildAuthFactorInvalidType(t *testing.T) {
	c := &controller{}
	account := &model.Account{Id: "11111111-1111-1111-1111-111111111111", Name: "testuser"}
	if _, err := c.buildAuthFactor(context.Background(), account, model.AuthFactorType(99), nil); err == nil {
		t.Fatal("unsupported factor type should fail")
	}
}
