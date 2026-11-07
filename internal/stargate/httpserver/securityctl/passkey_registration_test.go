package securityctl

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/config"
	"src.solsynth.dev/sosys/stargate/model"
)

// TestDecodeBase64OrURL pins the encoding tolerance of the registration
// payload: browsers and the Darwin plugin send unpadded base64url, other
// platform plugins keep the padding, and the C# clients sent standard base64.
func TestDecodeBase64OrURL(t *testing.T) {
	cases := map[string]string{
		"aGk=": "hi",
		"aGk":  "hi",
		"-_8=": "\xfb\xff",
		"-_8":  "\xfb\xff",
		"":     "",
	}
	for input, want := range cases {
		got, err := decodeBase64OrURL(input)
		if err != nil {
			t.Fatalf("decodeBase64OrURL(%q): %v", input, err)
		}
		if string(got) != want {
			t.Fatalf("decodeBase64OrURL(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestPasskeyRegistrationVerification exercises the complete-registration
// pipeline (base64url decode, CBOR unwrap, synthesized PublicKeyCredential,
// go-webauthn attestation verification) with a synthetic ES256 authenticator.
//
// The web client encodes every binary field as unpadded base64url, and
// go-webauthn enforces ClientData.origin against the configured origin set.
// The regression this guards: the shipped origin set named only
// http://localhost:3000 while the console runs on the Vite port or the SPA
// port Stargate serves itself, so every registration answered
// PADLOCK_PASSKEY_REGISTRATION_FAILED with an origin validation error.
func TestPasskeyRegistrationVerification(t *testing.T) {
	cfg := config.Default()
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          cfg.WebAuthn.RpId,
		RPDisplayName: cfg.WebAuthn.RpName,
		RPOrigins:     cfg.WebAuthnOrigins(),
	})
	if err != nil {
		t.Fatalf("webauthn.New: %v", err)
	}
	user := webauthnUser{account: &model.Account{Id: uuid.NewString(), Name: "test"}}

	for _, tc := range []struct {
		name    string
		origin  string
		wantErr bool
	}{
		{name: "vite dev server", origin: "http://localhost:5173"},
		{name: "stargate hosted SPA", origin: "http://localhost:8080"},
		{name: "unlisted origin", origin: "https://evil.example", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, session, err := wa.BeginRegistration(user)
			if err != nil {
				t.Fatalf("BeginRegistration: %v", err)
			}
			attestation, credentialID := syntheticAttestation(t, cfg.WebAuthn.RpId, session.Challenge, tc.origin)

			b64 := func(v []byte) string { return base64.RawURLEncoding.EncodeToString(v) }
			attestationBytes, err := decodeBase64OrURL(b64(attestation.attestationObject))
			if err != nil {
				t.Fatalf("decode attestation: %v", err)
			}
			clientData, err := decodeBase64OrURL(b64(attestation.clientData))
			if err != nil {
				t.Fatalf("decode client data: %v", err)
			}
			var attObj protocol.AttestationObject
			if err := webauthncbor.Unmarshal(attestationBytes, &attObj); err != nil {
				t.Fatalf("attestation CBOR: %v", err)
			}
			if err := attObj.AuthData.Unmarshal(attObj.RawAuthData); err != nil {
				t.Fatalf("authenticator data: %v", err)
			}
			if got := attObj.AuthData.AttData.CredentialID; !bytes.Equal(got, credentialID) {
				t.Fatalf("credential id = %x, want %x", got, credentialID)
			}
			body, err := json.Marshal(map[string]any{
				"id": b64(credentialID), "rawId": b64(credentialID), "type": "public-key",
				"response": map[string]any{
					"clientDataJSON":    b64(clientData),
					"attestationObject": b64(attestationBytes),
				},
			})
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			parsed, err := protocol.ParseCredentialCreationResponseBytes(body)
			if err != nil {
				t.Fatalf("parse response: %v", err)
			}
			credential, err := wa.CreateCredential(user, *session, parsed)
			if tc.wantErr {
				if err == nil {
					t.Fatal("CreateCredential accepted an origin outside the configured set")
				}
				return
			}
			if err != nil {
				t.Fatalf("CreateCredential: %v", err)
			}
			if _, err := passkeyCredentialJSON(credential); err != nil {
				t.Fatalf("passkeyCredentialJSON: %v", err)
			}
		})
	}
}

type syntheticRegistration struct {
	clientData        []byte
	attestationObject []byte
}

// syntheticAttestation builds a "none"-format ES256 registration for the given
// RP ID, challenge and client origin, matching what a browser returns to the
// web client before it base64url-encodes the parts.
func syntheticAttestation(t *testing.T, rpID, challenge, origin string) (syntheticRegistration, []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	credentialID := make([]byte, 16)
	if _, err := rand.Read(credentialID); err != nil {
		t.Fatalf("credential id: %v", err)
	}
	coseKey, err := cbor.Marshal(map[int64]any{
		1: int64(2), 3: int64(-7), -1: int64(1),
		-2: priv.X.Bytes(), -3: priv.Y.Bytes(),
	})
	if err != nil {
		t.Fatalf("COSE key: %v", err)
	}

	rpHash := sha256.Sum256([]byte(rpID))
	var authData bytes.Buffer
	authData.Write(rpHash[:])
	authData.Write([]byte{0x41}) // UserPresent | AttestedCredentialData
	_ = binary.Write(&authData, binary.BigEndian, uint32(0))
	authData.Write(make([]byte, 16)) // AAGUID
	_ = binary.Write(&authData, binary.BigEndian, uint16(len(credentialID)))
	authData.Write(credentialID)
	authData.Write(coseKey)

	attestationObject, err := cbor.Marshal(map[string]any{
		"fmt": "none", "attStmt": map[string]any{}, "authData": authData.Bytes(),
	})
	if err != nil {
		t.Fatalf("attestation CBOR: %v", err)
	}
	clientData := fmt.Sprintf(`{"type":"webauthn.create","challenge":%q,"origin":%q}`, challenge, origin)
	return syntheticRegistration{
		clientData:        []byte(clientData),
		attestationObject: attestationObject,
	}, credentialID
}
