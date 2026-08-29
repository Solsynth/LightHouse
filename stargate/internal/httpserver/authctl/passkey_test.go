package authctl

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// navigator.credentials.get: the 32-byte digest
//   SHA-256(authenticatorData || SHA-256(clientDataJSON))
// over the FULL authenticator data (WebAuthn §7.2 Step 16 — the same
// construction go-webauthn verifies). It returns the assertion fields in the
// base64url form the client submits to the complete endpoints.
func signAssertion(t *testing.T, priv *ecdsa.PrivateKey, authData []byte, clientDataJSON string, rawSignature bool) (clientDataJson, authenticatorData, signature string) {
	t.Helper()
	innerHash := sha256.Sum256([]byte(clientDataJSON))
	toSign := make([]byte, 0, len(authData)+len(innerHash))
	toSign = append(toSign, authData...)
	toSign = append(toSign, innerHash[:]...)
	digest := sha256.Sum256(toSign)
	var sig []byte
	if rawSignature {
		// Raw IEEE P1363 r||s layout (64 bytes), used by some authenticators.
		r, s, err := ecdsa.Sign(rand.Reader, priv, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		sig = make([]byte, 64)
		r.FillBytes(sig[:32])
		s.FillBytes(sig[32:])
	} else {
		var err error
		sig, err = ecdsa.SignASN1(rand.Reader, priv, digest[:])
		if err != nil {
			t.Fatal(err)
		}
	}
	return base64.RawURLEncoding.EncodeToString([]byte(clientDataJSON)),
		base64.RawURLEncoding.EncodeToString(authData),
		base64.RawURLEncoding.EncodeToString(sig)
}

// TestVerifyPasskeyAssertionSignature pins the assertion verification
// contract that was broken in prod ("fail assertion"): the signature is
// checked over the 32-byte digest SHA-256(authenticatorData ||
// SHA-256(clientDataJSON)) — the full authenticator data, hashed — the
// clientDataJSON must be a webauthn.get ceremony echoing the stored
// challenge, and both DER and raw r||s signature layouts are accepted.
func TestVerifyPasskeyAssertionSignature(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	x := make([]byte, 32)
	y := make([]byte, 32)
	priv.PublicKey.X.FillBytes(x)
	priv.PublicKey.Y.FillBytes(y)
	credentialID := "test-credential-id"
	cred := &model.PasskeyCredential{
		CredentialId: base64.StdEncoding.EncodeToString([]byte(credentialID)),
		PublicKeyX:   x,
		PublicKeyY:   y,
	}

	storedChallenge := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	authData := make([]byte, 37)
	authData[32] = 0x01 // UserPresent

	clientDataJSON, err := json.Marshal(map[string]any{
		"type":      "webauthn.get",
		"challenge": storedChallenge,
		"origin":    "https://app.solian.app",
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("accepts a valid DER assertion", func(t *testing.T) {
		cJSON, aData, sig := signAssertion(t, priv, authData, string(clientDataJSON), false)
		if !verifyPasskeyAssertionSignature(cred, cred.CredentialId, cJSON, aData, sig, storedChallenge) {
			t.Fatal("verifyPasskeyAssertionSignature rejected a valid assertion")
		}
	})

	t.Run("accepts a raw r||s signature", func(t *testing.T) {
		cJSON, aData, sig := signAssertion(t, priv, authData, string(clientDataJSON), true)
		if !verifyPasskeyAssertionSignature(cred, cred.CredentialId, cJSON, aData, sig, storedChallenge) {
			t.Fatal("verifyPasskeyAssertionSignature rejected a raw r||s signature")
		}
	})

	t.Run("rejects a tampered signature", func(t *testing.T) {
		cJSON, aData, sig := signAssertion(t, priv, authData, string(clientDataJSON), false)
		raw := base64.RawURLEncoding.DecodeString
		sigBytes, err := raw(sig)
		if err != nil {
			t.Fatal(err)
		}
		sigBytes[0] ^= 0x01
		tampered := base64.RawURLEncoding.EncodeToString(sigBytes)
		if verifyPasskeyAssertionSignature(cred, cred.CredentialId, cJSON, aData, tampered, storedChallenge) {
			t.Fatal("verifyPasskeyAssertionSignature accepted a tampered signature")
		}
	})

	t.Run("rejects a mismatched credential id", func(t *testing.T) {
		cJSON, aData, sig := signAssertion(t, priv, authData, string(clientDataJSON), false)
		other := base64.StdEncoding.EncodeToString([]byte("some-other-credential"))
		if verifyPasskeyAssertionSignature(cred, other, cJSON, aData, sig, storedChallenge) {
			t.Fatal("verifyPasskeyAssertionSignature accepted a mismatched credential id")
		}
	})

	t.Run("rejects a challenge mismatch echoed in clientDataJSON", func(t *testing.T) {
		otherChallenge := make([]byte, 32)
		otherChallenge[0] = 0x01
		badClientDataJSON, err := json.Marshal(map[string]any{
			"type":      "webauthn.get",
			"challenge": base64.RawURLEncoding.EncodeToString(otherChallenge),
		})
		if err != nil {
			t.Fatal(err)
		}
		cJSON, aData, sig := signAssertion(t, priv, authData, string(badClientDataJSON), false)
		if verifyPasskeyAssertionSignature(cred, cred.CredentialId, cJSON, aData, sig, storedChallenge) {
			t.Fatal("verifyPasskeyAssertionSignature accepted a challenge mismatch")
		}
	})

	t.Run("rejects a missing UserPresent flag", func(t *testing.T) {
		noUP := make([]byte, 37)
		cJSON, aData, sig := signAssertion(t, priv, noUP, string(clientDataJSON), false)
		if verifyPasskeyAssertionSignature(cred, cred.CredentialId, cJSON, aData, sig, storedChallenge) {
			t.Fatal("verifyPasskeyAssertionSignature accepted an assertion without UserPresent")
		}
	})

	t.Run("rejects a non-webauthn.get ceremony type", func(t *testing.T) {
		badType, err := json.Marshal(map[string]any{
			"type":      "webauthn.create",
			"challenge": storedChallenge,
		})
		if err != nil {
			t.Fatal(err)
		}
		cJSON, aData, sig := signAssertion(t, priv, authData, string(badType), false)
		if verifyPasskeyAssertionSignature(cred, cred.CredentialId, cJSON, aData, sig, storedChallenge) {
			t.Fatal("verifyPasskeyAssertionSignature accepted a non-assertion ceremony")
		}
	})

	t.Run("accepts authenticator data with extensions (ED flag)", func(t *testing.T) {
		// Authenticators that emit extension data (e.g. the platform
		// authenticator's uv/prf handling) return authData longer than 37
		// bytes; the signature covers the FULL data, so truncation to [:37]
		// must not happen.
		extAuthData := make([]byte, 37+8)
		extAuthData[32] = 0x01 | 0x80 // UP | ED
		copy(extAuthData[37:], []byte{1, 2, 3, 4, 5, 6, 7, 8})
		cJSON, aData, sig := signAssertion(t, priv, extAuthData, string(clientDataJSON), false)
		if !verifyPasskeyAssertionSignature(cred, cred.CredentialId, cJSON, aData, sig, storedChallenge) {
			t.Fatal("verifyPasskeyAssertionSignature rejected a valid assertion with extension data")
		}
	})
}

// TestVerifyPasskeyAssertionSignatureKeySize guards against signers whose
// P-256 public key coordinates are not exactly 32 bytes (leading zeros
// stripped): the byte-wise SetBytes construction must still verify.
func TestVerifyPasskeyAssertionSignatureKeySize(t *testing.T) {
	// Pick a secret whose public X coordinate has leading zero bytes, so the
	// stored x/y are shorter than 32 bytes.
	var priv *ecdsa.PrivateKey
	for {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if len(k.PublicKey.X.Bytes()) < 32 {
			priv = k
			break
		}
	}
	xBytes := priv.PublicKey.X.Bytes() // < 32 bytes
	yBytes := priv.PublicKey.Y.Bytes()
	if len(xBytes) >= 32 {
		t.Fatalf("expected short X coordinate, got %d bytes", len(xBytes))
	}
	cred := &model.PasskeyCredential{
		CredentialId: base64.StdEncoding.EncodeToString([]byte("id")),
		PublicKeyX:   xBytes,
		PublicKeyY:   yBytes,
	}
	storedChallenge := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	authData := make([]byte, 37)
	authData[32] = 0x01
	clientDataJSON, _ := json.Marshal(map[string]any{
		"type":      "webauthn.get",
		"challenge": storedChallenge,
	})
	cJSON, aData, sig := signAssertion(t, priv, authData, string(clientDataJSON), false)
	if !verifyPasskeyAssertionSignature(cred, cred.CredentialId, cJSON, aData, sig, storedChallenge) {
		t.Fatal("verifyPasskeyAssertionSignature rejected a valid assertion with short coordinates")
	}
}
