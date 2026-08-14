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

func TestCreateUserTokenSerializesAllScopesAsOAuthScopeString(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	svc := &JWTService{
		issuer:   "solar-network",
		audience: "https://auth.example",
		private:  key,
	}
	session := &model.AuthSession{Id: "session-1", Scopes: []string{"openid", "profile", "email"}}
	account := &model.Account{Id: "account-1", Name: "User"}

	tokenText, err := svc.CreateUserToken(session, account, 1, time.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatalf("create user token: %v", err)
	}
	parsed, err := jwt.Parse(tokenText, func(token *jwt.Token) (any, error) {
		return &key.PublicKey, nil
	})
	if err != nil || !parsed.Valid {
		t.Fatalf("parse user token: valid=%v err=%v", parsed.Valid, err)
	}
	claims := parsed.Claims.(jwt.MapClaims)
	if got, want := claims["scope"], "openid profile email"; got != want {
		t.Fatalf("scope claim = %#v, want %#v", got, want)
	}
}

func TestCreateBotTokenExpiryFollowsSession(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	svc := &JWTService{
		issuer:   "solar-network",
		audience: "https://auth.example",
		private:  key,
	}
	apiKey := &model.ApiKey{Id: "api-key-1", AccountId: "account-1"}

	t.Run("no session expiry omits JWT expiry", func(t *testing.T) {
		tokenText, err := svc.CreateBotToken(apiKey, &model.AuthSession{Id: "session-1"}, 1)
		if err != nil {
			t.Fatalf("create bot token: %v", err)
		}
		parsed, err := jwt.Parse(tokenText, func(token *jwt.Token) (any, error) {
			return &key.PublicKey, nil
		})
		if err != nil || !parsed.Valid {
			t.Fatalf("parse bot token: valid=%v err=%v", parsed.Valid, err)
		}
		claims := parsed.Claims.(jwt.MapClaims)
		if _, ok := claims["exp"]; ok {
			t.Fatalf("non-expiring bot token unexpectedly has exp claim: %v", claims["exp"])
		}
	})

	t.Run("session expiry becomes JWT expiry", func(t *testing.T) {
		expiredAt := time.Now().Add(time.Hour)
		tokenText, err := svc.CreateBotToken(apiKey, &model.AuthSession{
			Id: "session-2", ExpiredAt: model.NewTime(expiredAt),
		}, 1)
		if err != nil {
			t.Fatalf("create bot token: %v", err)
		}
		parsed, err := jwt.Parse(tokenText, func(token *jwt.Token) (any, error) {
			return &key.PublicKey, nil
		})
		if err != nil || !parsed.Valid {
			t.Fatalf("parse bot token: valid=%v err=%v", parsed.Valid, err)
		}
		claims := parsed.Claims.(jwt.MapClaims)
		exp, ok := claims["exp"].(float64)
		if !ok {
			t.Fatalf("exp claim = %T, want float64", claims["exp"])
		}
		if got, want := int64(exp), expiredAt.Unix(); got != want {
			t.Fatalf("exp = %d, want %d", got, want)
		}
	})
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
		wantIssuer, wantAudience, []string{"openid", "profile"}, map[string]any{"azp": wantAudience},
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
	if got, want := claims["scope"], "openid profile"; got != want {
		t.Fatalf("OIDC scope claim = %#v, want %#v", got, want)
	}
}

func TestValidateJwtAndOidcTokenAcceptConfiguredIssuerHistory(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	svc := &JWTService{
		issuer:       "new-issuer",
		validIssuers: []string{"legacy-issuer", "new-issuer"},
		audience:     "auth-service",
		private:      key,
		public:       &key.PublicKey,
	}

	sign := func(issuer, audience string) string {
		t.Helper()
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": issuer,
			"aud": audience,
			"sid": "11111111-1111-1111-1111-111111111111",
			"nbf": time.Now().Unix(),
		})
		text, err := token.SignedString(key)
		if err != nil {
			t.Fatalf("sign token: %v", err)
		}
		return text
	}

	if valid, _ := svc.ValidateJwt(sign("legacy-issuer", "auth-service")); !valid {
		t.Fatal("legacy issuer was rejected by ordinary JWT validation")
	}
	if valid, _ := svc.ValidateJwt(sign("unknown-issuer", "auth-service")); valid {
		t.Fatal("unknown issuer was accepted by ordinary JWT validation")
	}

	oidc := &TokenAuthService{jwt: svc}
	if _, _, _, _, valid := oidc.validateOidcToken(sign("legacy-issuer", "oidc-client")); !valid {
		t.Fatal("legacy issuer was rejected by OIDC validation")
	}
	if _, _, _, _, valid := oidc.validateOidcToken(sign("unknown-issuer", "oidc-client")); valid {
		t.Fatal("unknown issuer was accepted by OIDC validation")
	}
}
