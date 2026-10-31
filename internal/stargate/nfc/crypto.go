package nfc

// SDM (Secure Dynamic Messaging) crypto utilities for NFC tag validation.
// NTAG 424 DNA SDM decoder / verifier following NXP AN12196.
//
// This is a literal port of Passport's NfcCrypto.cs:
//   - PICCData is decrypted with the SDMMetaReadKey using AES-CBC with a zero IV
//   - Session keys are derived from SDMFileReadKey + UID + SDMReadCtr using AES-CMAC
//   - File data is decrypted with the session encryption key
//   - CMAC is verified with the session MAC key

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

var zeroIV = make([]byte, 16)

// PiccData is the parsed AN12196 PICCData structure.
type PiccData struct {
	Raw             []byte
	Tag             byte
	UID             []byte
	ReadCtrLSB      []byte
	ReadCtr         int
	RandomPadding   []byte
	UIDMirroring    bool
	CtrMirroring    bool
	UIDLengthNibble int
}

// SunValidationResult is the result of a successful legacy SUN validation.
type SunValidationResult struct {
	UID     []byte
	ReadCtr int
}

// ParseFullUIDData parses the uid parameter from the NFC scan URL. It requires
// at least 32 hex characters and ignores a trailing odd nibble.
func ParseFullUIDData(uidHex string) ([]byte, bool) {
	if len(uidHex) < 32 {
		return nil, false
	}
	hexLength := len(uidHex) - len(uidHex)%2
	data, err := hex.DecodeString(uidHex[:hexLength])
	if err != nil {
		return nil, false
	}
	return data, true
}

// ExtractPICCDataBlock extracts the encrypted PICCData (the first 16 bytes)
// from the full SDM data.
func ExtractPICCDataBlock(fullData []byte) ([]byte, bool) {
	if len(fullData) < 16 {
		return nil, false
	}
	block := make([]byte, 16)
	copy(block, fullData[:16])
	return block, true
}

// FormatUID renders a UID as the uppercase hex string stored on the tag row.
func FormatUID(uid []byte) string {
	return strings.ToUpper(hex.EncodeToString(uid))
}

// DecryptPICCData decrypts SDM encrypted PICCData using the SDMFileReadKey
// directly (the legacy path). It returns nil unless the PICCData tag is 0xC7
// and the UID starts with 0x04, mirroring NfcCrypto.DecryptPiccData.
func DecryptPICCData(fileReadKey, encryptedPICCData []byte) (*SunValidationResult, bool) {
	if len(encryptedPICCData) < 16 {
		return nil, false
	}
	if len(fileReadKey) != 16 && len(fileReadKey) != 32 {
		return nil, false
	}
	decrypted, ok := aesCBCDecrypt(fileReadKey, zeroIV, encryptedPICCData[:16])
	if !ok || len(decrypted) < 16 {
		return nil, false
	}
	if decrypted[0] != 0xC7 {
		return nil, false
	}
	uid := make([]byte, 7)
	copy(uid, decrypted[1:8])
	if uid[0] != 0x04 {
		return nil, false
	}
	counter := int(decrypted[8]) | int(decrypted[9])<<8 | int(decrypted[10])<<16
	return &SunValidationResult{UID: uid, ReadCtr: counter}, true
}

// DecryptPICCDataWithMetaKey decrypts PICCData using the SDMMetaReadKey with
// AES-CBC and a zero IV. The key must be 16 bytes: Passport's implementation
// rejects 32-byte SDMMetaReadKeys even though the tag registration accepts
// them as SUN keys.
func DecryptPICCDataWithMetaKey(metaKey, encryptedPICCData []byte) ([]byte, bool) {
	if len(encryptedPICCData) != 16 {
		return nil, false
	}
	if len(metaKey) != 16 {
		return nil, false
	}
	return aesCBCDecrypt(metaKey, zeroIV, encryptedPICCData)
}

// ParsePICCData parses the decrypted PICCData structure from AN12196.
//
// Layout:
//   - byte 0: PICCDataTag (bit 7 UID mirroring, bit 6 CTR mirroring, low
//     nibble UID length)
//   - next N: UID (if mirrored)
//   - next 3: SDMReadCtr (if mirrored, LSB first)
//   - rest: random padding
func ParsePICCData(plain []byte) (*PiccData, bool) {
	if len(plain) != 16 {
		return nil, false
	}
	tag := plain[0]
	uidMirroring := tag&0x80 != 0
	ctrMirroring := tag&0x40 != 0
	uidLengthNibble := int(tag & 0x0F)
	uidLen := 0
	if uidMirroring {
		uidLen = uidLengthNibble
	}

	pos := 1
	var uid []byte
	if uidMirroring {
		if uidLen <= 0 || pos+uidLen > len(plain) {
			return nil, false
		}
		uid = make([]byte, uidLen)
		copy(uid, plain[pos:pos+uidLen])
		pos += uidLen
	}

	var readCtrLSB []byte
	var readCtr int
	if ctrMirroring {
		if pos+3 > len(plain) {
			return nil, false
		}
		readCtrLSB = make([]byte, 3)
		copy(readCtrLSB, plain[pos:pos+3])
		readCtr = int(readCtrLSB[0]) | int(readCtrLSB[1])<<8 | int(readCtrLSB[2])<<16
		pos += 3
	}

	randomPadding := make([]byte, 16-pos)
	copy(randomPadding, plain[pos:])

	return &PiccData{
		Raw:             append([]byte(nil), plain...),
		Tag:             tag,
		UID:             uid,
		ReadCtrLSB:      readCtrLSB,
		ReadCtr:         readCtr,
		RandomPadding:   randomPadding,
		UIDMirroring:    uidMirroring,
		CtrMirroring:    ctrMirroring,
		UIDLengthNibble: uidLengthNibble,
	}, true
}

// BuildSV builds SV1/SV2 for session key derivation per AN12196:
// prefix (2 bytes) || 0001 || 0080 || UID (7) || SDMReadCtr (3, LSB first).
func BuildSV(prefix, uid, readCtrLSB []byte) ([]byte, bool) {
	if len(prefix) != 2 || len(uid) != 7 || len(readCtrLSB) != 3 {
		return nil, false
	}
	sv := make([]byte, 16)
	copy(sv[0:2], prefix)
	sv[2] = 0x00
	sv[3] = 0x01
	sv[4] = 0x00
	sv[5] = 0x80
	copy(sv[6:13], uid)
	copy(sv[13:16], readCtrLSB)
	return sv, true
}

// DeriveSessionKeys derives the session keys per AN12196:
// KSesSDMFileReadENC = AES-CMAC(FileReadKey, SV1)
// KSesSDMFileReadMAC = AES-CMAC(FileReadKey, SV2)
func DeriveSessionKeys(fileReadKey, uid, readCtrLSB []byte) ([]byte, []byte, bool) {
	sv1, ok := BuildSV([]byte{0xC3, 0x3C}, uid, readCtrLSB)
	if !ok {
		return nil, nil, false
	}
	sv2, ok := BuildSV([]byte{0x3C, 0xC3}, uid, readCtrLSB)
	if !ok {
		return nil, nil, false
	}
	ksesEnc, err := AESCMAC(fileReadKey, sv1)
	if err != nil {
		return nil, nil, false
	}
	ksesMac, err := AESCMAC(fileReadKey, sv2)
	if err != nil {
		return nil, nil, false
	}
	return ksesEnc, ksesMac, true
}

// BuildIVe builds the file-data IV per AN12196:
// IVe = AES-128(KSesSDMFileReadENC; SDMReadCtr || 13 zero bytes).
func BuildIVe(ksesEnc, readCtrLSB []byte) ([]byte, bool) {
	if len(readCtrLSB) != 3 {
		return nil, false
	}
	seed := make([]byte, 16)
	copy(seed, readCtrLSB)
	block, err := aes.NewCipher(ksesEnc)
	if err != nil {
		return nil, false
	}
	ive := make([]byte, 16)
	block.Encrypt(ive, seed)
	return ive, true
}

// DecryptFileData decrypts encrypted file data using the session encryption
// key. The ciphertext length must be a multiple of the AES block size.
func DecryptFileData(ksesEnc, encryptedData, ive []byte) ([]byte, bool) {
	if len(encryptedData) == 0 || len(encryptedData)%16 != 0 {
		return nil, false
	}
	return aesCBCDecrypt(ksesEnc, ive, encryptedData)
}

// AESCMAC computes AES-CMAC per RFC 4493. The key must be a valid AES key.
func AESCMAC(key, message []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	const blockSize = 16

	n := (len(message) + blockSize - 1) / blockSize
	lastBlockComplete := len(message)%blockSize == 0 && len(message) > 0
	if n == 0 {
		n = 1
		lastBlockComplete = false
	}

	// Subkeys K1/K2: L = AES(K, 0^128).
	k0 := make([]byte, blockSize)
	block.Encrypt(k0, make([]byte, blockSize))
	k1 := leftShiftOneBit(k0)
	if k0[0]&0x80 != 0 {
		k1[blockSize-1] ^= 0x87
	}
	k2 := leftShiftOneBit(k1)
	if k1[0]&0x80 != 0 {
		k2[blockSize-1] ^= 0x87
	}

	x := make([]byte, blockSize)
	for i := range n - 1 {
		blockInput := make([]byte, blockSize)
		copy(blockInput, message[i*blockSize:(i+1)*blockSize])
		xorBlock(x, blockInput)
		block.Encrypt(x, x)
	}

	lastBlock := make([]byte, blockSize)
	lastBlockLen := len(message) - (n-1)*blockSize
	if lastBlockLen > 0 {
		copy(lastBlock, message[(n-1)*blockSize:])
	}
	if !lastBlockComplete {
		lastBlock[lastBlockLen] = 0x80
		xorBlock(lastBlock, k2)
	} else {
		xorBlock(lastBlock, k1)
	}
	xorBlock(x, lastBlock)
	block.Encrypt(x, x)
	return x, nil
}

// TruncateSDMCMAC truncates a full 16-byte CMAC by taking the bytes at odd
// indices (NXP convention), yielding 8 bytes.
func TruncateSDMCMAC(fullCMAC []byte) ([]byte, bool) {
	if len(fullCMAC) != 16 {
		return nil, false
	}
	truncated := make([]byte, 8)
	for i := range 8 {
		truncated[i] = fullCMAC[i*2+1]
	}
	return truncated, true
}

// VerifyCMAC verifies the truncated SDM CMAC over macInput.
func VerifyCMAC(ksesMac, macInput, givenCMAC []byte) bool {
	if len(givenCMAC) != 8 {
		return false
	}
	full, err := AESCMAC(ksesMac, macInput)
	if err != nil {
		return false
	}
	truncated, ok := TruncateSDMCMAC(full)
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare(truncated, givenCMAC) == 1
}

func aesCBCDecrypt(key, iv, ciphertext []byte) ([]byte, bool) {
	if len(iv) != aes.BlockSize || len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, false
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, false
	}
	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ciphertext)
	return plain, true
}

func leftShiftOneBit(input []byte) []byte {
	output := make([]byte, len(input))
	var overflow byte
	for i := len(input) - 1; i >= 0; i-- {
		output[i] = input[i]<<1 | overflow
		if input[i]&0x80 != 0 {
			overflow = 1
		} else {
			overflow = 0
		}
	}
	return output
}

func xorBlock(a, b []byte) {
	for i := 0; i < len(a) && i < len(b); i++ {
		a[i] ^= b[i]
	}
}
