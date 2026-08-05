package securityctl

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"regexp"
	"strings"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/webauthn"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/model"
)

// TestPasskeyCredentialJSON verifies registration stores the C#-compatible
// PascalCase PasskeyCredential shape (model.PasskeyCredential) — not go-webauthn's
// camelCase Credential — so the login assertion verifier (authctl.parsePasskeyCredential)
// and Padlock can both read it back.
func TestPasskeyCredentialJSON(t *testing.T) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	x := make([]byte, 32)
	y := make([]byte, 32)
	for i := range x {
		x[i] = byte(i)
		y[i] = byte(255 - i)
	}
	coseKey, err := cbor.Marshal(map[int64]any{
		1: int64(2), 3: int64(-7), -1: int64(1), -2: x, -3: y,
	})
	if err != nil {
		t.Fatal(err)
	}
	cred := &webauthn.Credential{
		ID:        id,
		PublicKey: coseKey,
		Authenticator: webauthn.Authenticator{
			SignCount: 42,
		},
	}
	raw, err := passkeyCredentialJSON(cred)
	if err != nil {
		t.Fatalf("passkeyCredentialJSON: %v", err)
	}

	var parsed model.PasskeyCredential
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("stored credential is not valid JSON: %v", err)
	}
	if want := base64.StdEncoding.EncodeToString(id); parsed.CredentialId != want {
		t.Errorf("CredentialId = %q, want %q", parsed.CredentialId, want)
	}
	if !bytes.Equal(parsed.PublicKeyX, x) {
		t.Errorf("PublicKeyX = %x, want %x", parsed.PublicKeyX, x)
	}
	if !bytes.Equal(parsed.PublicKeyY, y) {
		t.Errorf("PublicKeyY = %x, want %x", parsed.PublicKeyY, y)
	}
	if parsed.Counter != 42 {
		t.Errorf("Counter = %d, want 42", parsed.Counter)
	}
	// The stored shape must round-trip through the login parser's strict
	// PascalCase unmarshal: re-marshaling must produce the same document.
	remarshaled, err := json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if string(remarshaled) != raw {
		t.Errorf("re-marshal mismatch:\n got  %s\n want %s", remarshaled, raw)
	}
}

// TestPasskeyCredentialJSONAssertionRoundTrip simulates the full registration
// -> login loop: the stored PasskeyCredential (PascalCase JSON with X/Y) must
// reproduce the public key that verifies the ES256 assertion the browser
// signs, i.e. exactly what authctl.verifyPasskeyAssertion does at login.
func TestPasskeyCredentialJSONAssertionRoundTrip(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	xBytes := make([]byte, 32)
	yBytes := make([]byte, 32)
	priv.PublicKey.X.FillBytes(xBytes)
	priv.PublicKey.Y.FillBytes(yBytes)

	coseKey, err := cbor.Marshal(map[int64]any{
		1: int64(2), 3: int64(-7), -1: int64(1), -2: xBytes, -3: yBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	cred := &webauthn.Credential{
		ID:        []byte{9, 8, 7, 6, 5, 4, 3, 2, 1},
		PublicKey: coseKey,
	}
	raw, err := passkeyCredentialJSON(cred)
	if err != nil {
		t.Fatalf("passkeyCredentialJSON: %v", err)
	}

	// Login side: parsePasskeyCredential unmarshals into model.PasskeyCredential.
	var parsed model.PasskeyCredential
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("stored credential unparseable: %v", err)
	}

	// The browser signs rpIdHash(32) || flags || counter || sha256(clientDataJSON).
	authData := make([]byte, 37)
	authData[32] = 0x01 // UserPresent
	clientDataJSON := []byte(`{"type":"webauthn.get","challenge":"` +
		base64.RawURLEncoding.EncodeToString(make([]byte, 32)) + `"}`)
	clientDataHash := sha256.Sum256(clientDataJSON)
	signedData := make([]byte, 0, 37+32)
	signedData = append(signedData, authData...)
	signedData = append(signedData, clientDataHash[:]...)
	sig, err := ecdsa.SignASN1(rand.Reader, priv, signedData)
	if err != nil {
		t.Fatal(err)
	}

	pub := &ecdsa.PublicKey{Curve: elliptic.P256(),
		X: new(big.Int).SetBytes(parsed.PublicKeyX),
		Y: new(big.Int).SetBytes(parsed.PublicKeyY)}
	if !ecdsa.VerifyASN1(pub, signedData, sig) {
		t.Fatal("stored passkey public key failed to verify the ES256 assertion")
	}
}

// TestPasskeyCredentialJSONRejectsNonEC2 verifies non-EC2 COSE keys (e.g. RSA,
// key type 3) are rejected at registration rather than stored unusably.
func TestPasskeyCredentialJSONRejectsNonEC2(t *testing.T) {
	// RSA COSE key: {1: 3 (kty=RSA), 3: -257 (RS256), -1: modulus, -2: exponent}
	coseKey, err := cbor.Marshal(map[int64]any{
		1: int64(3), 3: int64(-257),
		-1: make([]byte, 256),
		-2: []byte{1, 0, 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	cred := &webauthn.Credential{ID: []byte{1, 2, 3}, PublicKey: coseKey}
	if _, err := passkeyCredentialJSON(cred); err == nil {
		t.Fatal("passkeyCredentialJSON accepted a non-EC2 key, want error")
	}
}

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
