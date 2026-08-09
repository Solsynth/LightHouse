package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// TestClaimInt pins the string-or-number claim parsing: the C# minting
// writes ver/epoch as strings and validates with int.TryParse, so both
// serializations must be read (a float64-only assertion silently skips the
// epoch/version checks on fleet-issued tokens).
func TestClaimInt(t *testing.T) {
	claims := jwt.MapClaims{
		"epoch_string":  "1", // C#/Go minted token
		"epoch_number":  1.0, // a JSON-number minting
		"epoch_garbage": "abc",
		"epoch_zero":    "0",
	}
	cases := []struct {
		key  string
		want int
		ok   bool
	}{
		{"epoch_string", 1, true},
		{"epoch_number", 1, true},
		{"epoch_zero", 0, true},
		{"epoch_garbage", 0, false},
		{"missing", 0, false},
	}
	for _, tc := range cases {
		got, ok := ClaimInt(claims, tc.key)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ClaimInt(%q) = (%d, %v), want (%d, %v)", tc.key, got, ok, tc.want, tc.ok)
		}
	}
}

func TestCreateOidcUserTokenUsesProviderClaimsAndSigner(t *testing.T) {
	authKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate auth key: %v", err)
	}
	providerKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate provider key: %v", err)
	}
	svc := &JWTService{
		issuer:   "solar-network",
		audience: "https://auth.example",
		private:  authKey,
	}
	session := &model.AuthSession{Id: "session-1", Epoch: 2}
	account := &model.Account{Id: "account-1", Name: "User"}
	wantIssuer := "https://issuer.example"
	wantAudience := "forgejo"

	tokenText, err := svc.CreateOidcUserTokenWithSigner(
		providerKey, session, account, 3, time.Now().Add(5*time.Minute),
		wantIssuer, wantAudience, []string{"openid"}, map[string]any{"azp": wantAudience},
	)
	if err != nil {
		t.Fatalf("create OIDC token: %v", err)
	}
	parsed, err := jwt.Parse(tokenText, func(token *jwt.Token) (any, error) {
		return &providerKey.PublicKey, nil
	})
	if err != nil || !parsed.Valid {
		t.Fatalf("parse OIDC token with provider key: valid=%v err=%v", parsed.Valid, err)
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("claims type = %T, want jwt.MapClaims", parsed.Claims)
	}
	if claims["iss"] != wantIssuer || claims["aud"] != wantAudience {
		t.Fatalf("OIDC claims issuer=%v audience=%v, want %q/%q", claims["iss"], claims["aud"], wantIssuer, wantAudience)
	}
}
