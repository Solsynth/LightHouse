package accountkeys

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"golang.org/x/crypto/ssh"
)

func testSSHKey(t *testing.T) (publicLine, fingerprint string) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public, err := ssh.NewPublicKey(private.Public())
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public))), ssh.FingerprintSHA256(public)
}

func armoredEntity(t *testing.T, private bool) (armored, fingerprint string) {
	t.Helper()
	entity, err := openpgp.NewEntity("Test User", "", "test@example.com", &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA})
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	blockType := openpgp.PublicKeyType
	if private {
		blockType = openpgp.PrivateKeyType
	}
	writer, err := armor.Encode(&buffer, blockType, nil)
	if err != nil {
		t.Fatal(err)
	}
	if private {
		err = entity.SerializePrivate(writer, nil)
	} else {
		err = entity.Serialize(writer)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.String(), strings.ToUpper(hex.EncodeToString(entity.PrimaryKey.Fingerprint))
}

func TestParseSSHKeyNormalizesTheLine(t *testing.T) {
	line, fingerprint := testSSHKey(t)
	info, err := ParseSSHKey("  " + line + " user@example.com\n")
	if err != nil {
		t.Fatal(err)
	}
	if info.Fingerprint != fingerprint {
		t.Fatalf("fingerprint = %q, want %q", info.Fingerprint, fingerprint)
	}
	if !strings.HasPrefix(info.Fingerprint, "SHA256:") {
		t.Fatalf("fingerprint = %q, want SHA256", info.Fingerprint)
	}
	if info.PublicKey != line {
		t.Fatalf("normalized key = %q, want %q", info.PublicKey, line)
	}
	if info.Algorithm != "ssh-ed25519" {
		t.Fatalf("algorithm = %q, want ssh-ed25519", info.Algorithm)
	}
}

func testPrivateKey(t *testing.T) string {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block))
}

func TestParseSSHKeyRejectsUnusableInput(t *testing.T) {
	line, _ := testSSHKey(t)
	privateKey := testPrivateKey(t)
	cases := map[string]string{
		"empty":    "",
		"garbage":  "not a key",
		"two keys": line + "\n" + line,
		"too long": strings.Repeat("a", MaxKeyBytes+1),
		"private":  privateKey,
	}
	for name, raw := range cases {
		if info, err := ParseSSHKey(raw); err == nil {
			t.Fatalf("%s: parsed unexpectedly: %+v", name, info)
		} else if !errors.Is(err, ErrInvalidSSHKey) {
			t.Fatalf("%s: error = %v, want ErrInvalidSSHKey", name, err)
		}
	}
}

func TestParsePGPKeyPublicBlock(t *testing.T) {
	armored, fingerprint := armoredEntity(t, false)
	info, err := ParsePGPKey("\n" + armored + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if info.Fingerprint != fingerprint {
		t.Fatalf("fingerprint = %q, want %q", info.Fingerprint, fingerprint)
	}
	if len(info.Fingerprint) != 40 {
		t.Fatalf("fingerprint length = %d, want 40", len(info.Fingerprint))
	}
	if len(info.KeyID) != 16 {
		t.Fatalf("key id = %q, want 16 hex characters", info.KeyID)
	}
	if len(info.UserIDs) == 0 || info.UserIDs[0] != "Test User <test@example.com>" {
		t.Fatalf("user ids = %q, want the test identity", info.UserIDs)
	}
	if info.Algorithm != "eddsa" {
		t.Fatalf("algorithm = %q, want eddsa", info.Algorithm)
	}
	if info.PublicKey != strings.TrimSpace(armored) {
		t.Fatal("stored block was not trimmed to the armored key")
	}
}

func TestParsePGPKeyRejectsPrivateAndGarbage(t *testing.T) {
	privateArmored, _ := armoredEntity(t, true)
	cases := []struct {
		name string
		raw  string
		want error
	}{
		{name: "garbage", raw: "not a key", want: ErrInvalidPGPKey},
		{name: "empty", raw: "", want: ErrInvalidPGPKey},
		{name: "private", raw: privateArmored, want: ErrPrivateKey},
	}
	for _, tc := range cases {
		if _, err := ParsePGPKey(tc.raw); !errors.Is(err, tc.want) {
			t.Fatalf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestLabelOrFallback(t *testing.T) {
	if label, err := LabelOrFallback("  ", "ssh-ed25519"); err != nil || label != "ssh-ed25519" {
		t.Fatalf("fallback = %q (err %v)", label, err)
	}
	if label, err := LabelOrFallback(" Work ", "unused"); err != nil || label != "Work" {
		t.Fatalf("trimmed = %q (err %v)", label, err)
	}
	if _, err := LabelOrFallback(strings.Repeat("a", 129), ""); !errors.Is(err, ErrLabelTooLong) {
		t.Fatalf("long label error = %v, want ErrLabelTooLong", err)
	}
}
