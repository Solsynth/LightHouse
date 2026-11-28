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
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// tokenSigner mints and caches Apple Music ES256 Developer Tokens from an
// Apple MusicKit .p8 (PKCS#8 ECDSA P-256) private key.
type tokenSigner struct {
	teamID string
	keyID  string
	key    *ecdsa.PrivateKey
	ttl    time.Duration
	now    func() time.Time

	mu        sync.Mutex
	cached    string
	expiresAt time.Time
}

type signerConfig struct {
	TeamID         string
	KeyID          string
	PrivateKeyPath string
	PrivateKeyPEM  string
	TTL            time.Duration
}

func newTokenSigner(cfg signerConfig) (*tokenSigner, error) {
	teamID := strings.TrimSpace(cfg.TeamID)
	keyID := strings.TrimSpace(cfg.KeyID)
	if teamID == "" || keyID == "" {
		return nil, nil
	}

	var pemBytes []byte
	if inline := strings.TrimSpace(cfg.PrivateKeyPEM); inline != "" {
		// Support single-line env vars with literal "\n" escapes.
		normalized := strings.ReplaceAll(inline, `\n`, "\n")
		pemBytes = []byte(normalized)
	} else if path := strings.TrimSpace(cfg.PrivateKeyPath); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("applemusic: read private key %q: %w", path, err)
		}
		pemBytes = raw
	} else {
		return nil, nil
	}

	key, err := parseP8PrivateKey(pemBytes)
	if err != nil {
		return nil, err
	}

	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	const maxTTL = 180 * 24 * time.Hour
	if ttl > maxTTL {
		ttl = maxTTL
	}

	return &tokenSigner{
		teamID: teamID,
		keyID:  keyID,
		key:    key,
		ttl:    ttl,
		now:    time.Now,
	}, nil
}

func parseP8PrivateKey(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("applemusic: invalid PEM block in .p8 private key")
	}

	var parsed any
	var err error
	if block.Type == "EC PRIVATE KEY" {
		parsed, err = x509.ParseECPrivateKey(block.Bytes)
	} else {
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	}
	if err != nil {
		return nil, fmt.Errorf("applemusic: parse private key: %w", err)
	}

	ecKey, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("applemusic: private key is not an ECDSA key")
	}
	if ecKey.Curve != elliptic.P256() {
		return nil, fmt.Errorf("applemusic: expected P-256 curve for ES256, got %s", ecKey.Curve.Params().Name)
	}
	return ecKey, nil
}

// Token returns a cached ES256 developer JWT, minting a fresh one when the
// cached token is absent or approaching expiry.
func (s *tokenSigner) Token() (token string, expiresAt time.Time, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now().UTC()
	// Refresh once 90% of the lifetime has elapsed (or within 5 minutes of expiry).
	refreshMargin := s.ttl / 10
	if refreshMargin < 5*time.Minute {
		refreshMargin = 5 * time.Minute
	}
	if refreshMargin >= s.ttl {
		refreshMargin = s.ttl / 2
	}

	if s.cached != "" && now.Add(refreshMargin).Before(s.expiresAt) {
		return s.cached, s.expiresAt, nil
	}

	exp := now.Add(s.ttl)
	signed, err := s.sign(now, exp)
	if err != nil {
		return "", time.Time{}, err
	}
	s.cached = signed
	s.expiresAt = exp
	return signed, exp, nil
}

func (s *tokenSigner) sign(iat, exp time.Time) (string, error) {
	headerJSON, err := json.Marshal(struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
		Typ string `json:"typ"`
	}{
		Alg: "ES256",
		Kid: s.keyID,
		Typ: "JWT",
	})
	if err != nil {
		return "", err
	}

	claimsJSON, err := json.Marshal(struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}{
		Iss: s.teamID,
		Iat: iat.Unix(),
		Exp: exp.Unix(),
	})
	if err != nil {
		return "", err
	}

	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(headerJSON) + "." + enc.EncodeToString(claimsJSON)

	digest := sha256.Sum256([]byte(signingInput))
	r, sVal, err := ecdsa.Sign(rand.Reader, s.key, digest[:])
	if err != nil {
		return "", fmt.Errorf("applemusic: sign ES256 JWT: %w", err)
	}

	// RFC 7518 §3.4: ES256 signature is the 64-byte IEEE P1363 concatenation r || s.
	var sig [64]byte
	r.FillBytes(sig[0:32])
	sVal.FillBytes(sig[32:64])

	return signingInput + "." + enc.EncodeToString(sig[:]), nil
}
