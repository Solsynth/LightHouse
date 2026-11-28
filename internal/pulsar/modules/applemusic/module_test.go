package applemusic

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/config"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

func generateTestP8Key(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate P-256 key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal PKCS8: %v", err)
	}
	block := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	return key, string(block)
}

func mountModule(t *testing.T, cfg *config.Config) (*gin.Engine, *instance) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set(authn.ContextKeyUser, &gen.DyAccount{Id: uuid.NewString()})
		c.Next()
	})
	host := &module.Host[core.Services]{
		Core:   &core.Services{Config: cfg},
		API:    engine.Group("/api"),
		Engine: engine,
	}
	inst := &instance{}
	if err := inst.Configure(nil, host); err != nil {
		t.Fatalf("configure applemusic module: %v", err)
	}
	return engine, inst
}

func TestDeveloperTokenEndpoint_MintsVerifiableES256JWT(t *testing.T) {
	privKey, pemStr := generateTestP8Key(t)

	cfg := &config.Config{}
	cfg.AppleMusic.TeamID = "TEAM123456"
	cfg.AppleMusic.KeyID = "KEY1234567"
	cfg.AppleMusic.PrivateKeyPEM = pemStr
	cfg.AppleMusic.TokenTTLHours = 6

	engine, inst := mountModule(t, cfg)
	fixedNow := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	inst.signer.now = func() time.Time { return fixedNow }

	req := httptest.NewRequest(http.MethodGet, "/api/apple-music/developer-token", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body developerTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	parts := strings.Split(body.Token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 JWT segments, got %d", len(parts))
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var header map[string]string
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if header["alg"] != "ES256" || header["kid"] != "KEY1234567" {
		t.Fatalf("unexpected header: %+v", header)
	}

	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	if claims.Iss != "TEAM123456" {
		t.Fatalf("expected iss TEAM123456, got %q", claims.Iss)
	}
	if claims.Exp-claims.Iat != int64((6 * time.Hour).Seconds()) {
		t.Fatalf("expected 6h TTL, got %ds", claims.Exp-claims.Iat)
	}

	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sigBytes) != 64 {
		t.Fatalf("expected 64-byte raw signature, got len=%d err=%v", len(sigBytes), err)
	}
	r := new(big.Int).SetBytes(sigBytes[:32])
	s := new(big.Int).SetBytes(sigBytes[32:])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(&privKey.PublicKey, digest[:], r, s) {
		t.Fatal("ES256 signature verification failed")
	}

	// Second call within TTL must return the cached token.
	rec2 := httptest.NewRecorder()
	engine.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/api/apple-music/developer-token", nil))
	var body2 developerTokenResponse
	_ = json.Unmarshal(rec2.Body.Bytes(), &body2)
	if body2.Token != body.Token {
		t.Fatal("expected cached token on immediate second request")
	}

	// Advancing near expiry must rotate the token.
	inst.signer.now = func() time.Time { return fixedNow.Add(5*time.Hour + 40*time.Minute) }
	rec3 := httptest.NewRecorder()
	engine.ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, "/api/apple-music/developer-token", nil))
	var body3 developerTokenResponse
	_ = json.Unmarshal(rec3.Body.Bytes(), &body3)
	if body3.Token == body.Token {
		t.Fatal("expected rotated token when approaching expiry")
	}
}

func TestDeveloperTokenEndpoint_ReadsP8FileFromDisk(t *testing.T) {
	_, pemStr := generateTestP8Key(t)
	keyPath := filepath.Join(t.TempDir(), "AuthKey_TESTKEY123.p8")
	if err := os.WriteFile(keyPath, []byte(pemStr), 0o600); err != nil {
		t.Fatalf("write p8 file: %v", err)
	}

	cfg := &config.Config{}
	cfg.AppleMusic.TeamID = "TEAM999999"
	cfg.AppleMusic.KeyID = "TESTKEY123"
	cfg.AppleMusic.PrivateKeyPath = keyPath
	cfg.AppleMusic.TokenTTLHours = 12

	engine, _ := mountModule(t, cfg)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/apple-music/developer-token", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDeveloperTokenEndpoint_UnconfiguredReturns503(t *testing.T) {
	engine, _ := mountModule(t, &config.Config{})
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/apple-music/developer-token", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when unconfigured, got %d", rec.Code)
	}
}
