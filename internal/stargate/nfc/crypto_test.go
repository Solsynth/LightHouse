package nfc

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"testing"
)

// RFC 4493 AES-CMAC test vectors.
func TestAESCMACRFC4493(t *testing.T) {
	key := mustHex(t, "2b7e151628aed2a6abf7158809cf4f3c")
	cases := []struct {
		message string
		cmac    string
	}{
		{"", "bb1d6929e95937287fa37d129b756746"},
		{"6bc1bee22e409f96e93d7e117393172a", "070a16b46b4d4144f79bdd9dd04a287c"},
		{"6bc1bee22e409f96e93d7e117393172aae2d8a571e03ac9c9eb76fac45af8e51" +
			"30c81c46a35ce411", "dfa66747de9ae63030ca32611497c827"},
		{"6bc1bee22e409f96e93d7e117393172aae2d8a571e03ac9c9eb76fac45af8e51" +
			"30c81c46a35ce411e5fbc1191a0a52eff69f2445df4f9b17ad2b417be66c3710",
			"51f0bebf7e3b9d92fc49741779363cfe"},
	}
	for _, tc := range cases {
		got, err := AESCMAC(key, mustHex(t, tc.message))
		if err != nil {
			t.Fatalf("AESCMAC(%d bytes): %v", len(tc.message), err)
		}
		if want := mustHex(t, tc.cmac); !bytes.Equal(got, want) {
			t.Fatalf("AESCMAC(%d bytes) = %x, want %x", len(tc.message), got, want)
		}
	}
}

func TestBuildSV(t *testing.T) {
	uid := mustHex(t, "04a1b2c3d4e5f6")
	ctr := mustHex(t, "010000")
	sv, ok := BuildSV([]byte{0xC3, 0x3C}, uid, ctr)
	if !ok {
		t.Fatal("BuildSV rejected a valid input")
	}
	want := mustHex(t, "c33c0001008004a1b2c3d4e5f6010000")
	if !bytes.Equal(sv, want) {
		t.Fatalf("SV1 = %x, want %x", sv, want)
	}

	if _, ok := BuildSV([]byte{0xC3}, uid, ctr); ok {
		t.Fatal("BuildSV accepted a 1-byte prefix")
	}
	if _, ok := BuildSV([]byte{0xC3, 0x3C}, uid[:6], ctr); ok {
		t.Fatal("BuildSV accepted a 6-byte UID")
	}
}

func TestDecryptPICCDataWithMetaKey(t *testing.T) {
	key := mustHex(t, "000102030405060708090a0b0c0d0e0f")
	plain := mustHex(t, "c704a1b2c3d4e5f6010000ffffffffff")
	encrypted := encryptCBC(t, key, zeroIV, plain)

	decrypted, ok := DecryptPICCDataWithMetaKey(key, encrypted)
	if !ok {
		t.Fatal("DecryptPICCDataWithMetaKey rejected a matching key")
	}
	if !bytes.Equal(decrypted, plain) {
		t.Fatalf("decrypted = %x, want %x", decrypted, plain)
	}

	// Passport rejects a 32-byte SDMMetaReadKey even though tag registration
	// accepts it as a SUN key.
	if _, ok := DecryptPICCDataWithMetaKey(append(append([]byte(nil), key...), key...), encrypted); ok {
		t.Fatal("DecryptPICCDataWithMetaKey accepted a 32-byte meta key")
	}
	if _, ok := DecryptPICCDataWithMetaKey(key, encrypted[:15]); ok {
		t.Fatal("DecryptPICCDataWithMetaKey accepted 15 bytes of PICCData")
	}
}

func TestParsePICCData(t *testing.T) {
	plain := mustHex(t, "c704a1b2c3d4e5f6010000ffffffffff")
	parsed, ok := ParsePICCData(plain)
	if !ok {
		t.Fatal("ParsePICCData rejected a valid block")
	}
	if parsed.Tag != 0xC7 || !parsed.UIDMirroring || !parsed.CtrMirroring {
		t.Fatalf("unexpected tag flags: %+v", parsed)
	}
	if want := mustHex(t, "04a1b2c3d4e5f6"); !bytes.Equal(parsed.UID, want) {
		t.Fatalf("UID = %x, want %x", parsed.UID, want)
	}
	if parsed.ReadCtr != 1 {
		t.Fatalf("ReadCtr = %d, want 1", parsed.ReadCtr)
	}
	if want := mustHex(t, "010000"); !bytes.Equal(parsed.ReadCtrLSB, want) {
		t.Fatalf("ReadCtrLSB = %x, want %x", parsed.ReadCtrLSB, want)
	}

	if _, ok := ParsePICCData(plain[:12]); ok {
		t.Fatal("ParsePICCData accepted a short block")
	}
	// UID length nibble 0x0 with UID mirroring set is invalid.
	if _, ok := ParsePICCData(mustHex(t, "80000000000000000000000000000000")); ok {
		t.Fatal("ParsePICCData accepted a zero UID length nibble")
	}
}

func TestSessionKeysAndFileData(t *testing.T) {
	fileKey := mustHex(t, "00112233445566778899aabbccddeeff")
	uid := mustHex(t, "04a1b2c3d4e5f6")
	ctr := mustHex(t, "010000")

	encKey, macKey, ok := DeriveSessionKeys(fileKey, uid, ctr)
	if !ok {
		t.Fatal("DeriveSessionKeys failed")
	}
	if len(encKey) != 16 || len(macKey) != 16 {
		t.Fatalf("session key lengths = %d/%d, want 16/16", len(encKey), len(macKey))
	}
	again, againMac, _ := DeriveSessionKeys(fileKey, uid, ctr)
	if !bytes.Equal(encKey, again) || !bytes.Equal(macKey, againMac) {
		t.Fatal("session key derivation is not deterministic")
	}

	ive, ok := BuildIVe(encKey, ctr)
	if !ok {
		t.Fatal("BuildIVe failed")
	}
	filePlain := []byte("hello ntag424 dn")
	fileCipher := encryptCBC(t, encKey, ive, filePlain)
	decrypted, ok := DecryptFileData(encKey, fileCipher, ive)
	if !ok {
		t.Fatal("DecryptFileData failed")
	}
	if !bytes.Equal(decrypted, filePlain) {
		t.Fatalf("file data = %q, want %q", decrypted, filePlain)
	}

	if _, ok := DecryptFileData(encKey, fileCipher[:15], ive); ok {
		t.Fatal("DecryptFileData accepted a partial block")
	}
}

func TestCMACTruncateAndVerify(t *testing.T) {
	key := mustHex(t, "2b7e151628aed2a6abf7158809cf4f3c")
	full, err := AESCMAC(key, []byte("verify me"))
	if err != nil {
		t.Fatal(err)
	}
	truncated, ok := TruncateSDMCMAC(full)
	if !ok {
		t.Fatal("TruncateSDMCMAC rejected a 16-byte CMAC")
	}
	for i := range 8 {
		if truncated[i] != full[i*2+1] {
			t.Fatalf("truncated[%d] = %x, want %x", i, truncated[i], full[i*2+1])
		}
	}
	if !VerifyCMAC(key, []byte("verify me"), truncated) {
		t.Fatal("VerifyCMAC rejected a valid CMAC")
	}
	if VerifyCMAC(key, []byte("verify me"), truncated[:7]) {
		t.Fatal("VerifyCMAC accepted a 7-byte CMAC")
	}
	if VerifyCMAC(key, []byte("different"), truncated) {
		t.Fatal("VerifyCMAC accepted a CMAC over different data")
	}
}

func TestTruncateSDMCMACRejectsWrongLength(t *testing.T) {
	if _, ok := TruncateSDMCMAC(make([]byte, 15)); ok {
		t.Fatal("TruncateSDMCMAC accepted 15 bytes")
	}
}

func TestParseSunKey(t *testing.T) {
	hexKey := "000102030405060708090a0b0c0d0e0f"
	decoded, err := ParseSunKey(hexKey)
	if err != nil {
		t.Fatalf("ParseSunKey(hex): %v", err)
	}
	if len(decoded) != 16 {
		t.Fatalf("hex key length = %d, want 16", len(decoded))
	}
	base64Key := "AAECAwQFBgcICQoLDA0ODw=="
	decoded, err = ParseSunKey(base64Key)
	if err != nil {
		t.Fatalf("ParseSunKey(base64): %v", err)
	}
	if len(decoded) != 16 {
		t.Fatalf("base64 key length = %d, want 16", len(decoded))
	}
	if _, err := ParseSunKey(""); err != ErrEmptySunKey {
		t.Fatalf("ParseSunKey(empty) error = %v, want ErrEmptySunKey", err)
	}
	if _, err := ParseSunKey("not base64!"); err != ErrUndecodableSunKey {
		t.Fatalf("ParseSunKey(invalid) error = %v, want ErrUndecodableSunKey", err)
	}
}

func TestParseSolianURL(t *testing.T) {
	picc, e, cmac, ok := ParseSolianURL("solian://phpass?picc_data=c704a1b2c3d4e5f6010000ffffffffff&e=0011&cmac=22")
	if !ok {
		t.Fatal("ParseSolianURL rejected a valid URL")
	}
	if picc != "c704a1b2c3d4e5f6010000ffffffffff" || e != "0011" || cmac != "22" {
		t.Fatalf("parsed = %q/%q/%q", picc, e, cmac)
	}
	if _, _, _, ok := ParseSolianURL("solian://phpass?e=0011"); ok {
		t.Fatal("ParseSolianURL accepted a URL without picc_data")
	}
	if _, _, _, ok := ParseSolianURL("https://example.com"); ok {
		t.Fatal("ParseSolianURL accepted a non-solian URL")
	}
}

func mustHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("decode %q: %v", value, err)
	}
	return decoded
}

func encryptCBC(t *testing.T, key, iv, plain []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}
	if len(plain)%aes.BlockSize != 0 {
		t.Fatalf("plaintext length %d is not a multiple of the block size", len(plain))
	}
	ciphertext := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, plain)
	return ciphertext
}
