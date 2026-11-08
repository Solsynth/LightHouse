package accountkeys

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/model"
)

// MaxKeyBytes bounds the request body a single key may carry. Armored PGP keys
// with many identities stay far below it.
const MaxKeyBytes = 256 * 1024

var (
	// ErrInvalidSSHKey reports input that is not a usable OpenSSH public key.
	ErrInvalidSSHKey = errors.New("accountkeys: invalid SSH public key")
	// ErrInvalidPGPKey reports input that is not a usable OpenPGP public key.
	ErrInvalidPGPKey = errors.New("accountkeys: invalid PGP public key")
	// ErrPrivateKey reports a block that carries private key material.
	ErrPrivateKey = errors.New("accountkeys: private key material is not accepted")
	// ErrLabelTooLong reports a label past the column limit.
	ErrLabelTooLong = errors.New("accountkeys: label is longer than 128 characters")
)

// SSHKeyInfo is the parsed, normalized form stored for an SSH key. The comment
// is kept only to seed a default label; the stored line is canonical.
type SSHKeyInfo struct {
	Algorithm   string
	PublicKey   string
	Fingerprint string
	Comment     string
}

// ParseSSHKey accepts one `authorized_keys` line (options allowed) and
// normalizes it to `type base64`. Certificates are rejected: they are not keys
// an account owns, and this deployment has no SSH CA.
func ParseSSHKey(raw string) (*SSHKeyInfo, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > MaxKeyBytes {
		return nil, ErrInvalidSSHKey
	}
	publicKey, comment, _, rest, err := ssh.ParseAuthorizedKey([]byte(raw))
	if err != nil {
		return nil, ErrInvalidSSHKey
	}
	if trailing := strings.TrimSpace(string(rest)); trailing != "" {
		return nil, fmt.Errorf("%w: only one key is accepted", ErrInvalidSSHKey)
	}
	if strings.HasSuffix(publicKey.Type(), "-cert-v01@openssh.com") {
		return nil, fmt.Errorf("%w: certificates are not supported", ErrInvalidSSHKey)
	}
	normalized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(publicKey)))
	return &SSHKeyInfo{
		Algorithm:   publicKey.Type(),
		PublicKey:   normalized,
		Fingerprint: ssh.FingerprintSHA256(publicKey),
		Comment:     comment,
	}, nil
}

// PGPKeyInfo is the parsed form stored for a PGP key. UserIds are the key's
// live identities in lexical order; the armored block is stored verbatim apart
// from surrounding whitespace.
type PGPKeyInfo struct {
	Algorithm   string
	PublicKey   string
	Fingerprint string
	KeyID       string
	UserIDs     []string
}

// ParsePGPKey accepts exactly one armored OpenPGP public key block. Private
// keys and revoked primary keys are rejected.
func ParsePGPKey(raw string) (*PGPKeyInfo, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > MaxKeyBytes {
		return nil, ErrInvalidPGPKey
	}
	entities, err := openpgp.ReadArmoredKeyRing(strings.NewReader(raw))
	if err != nil {
		return nil, ErrInvalidPGPKey
	}
	if len(entities) == 0 {
		return nil, ErrInvalidPGPKey
	}
	if len(entities) > 1 {
		return nil, fmt.Errorf("%w: only one key is accepted", ErrInvalidPGPKey)
	}
	entity := entities[0]
	if entity.PrimaryKey == nil {
		return nil, ErrInvalidPGPKey
	}
	if entity.PrivateKey != nil {
		return nil, ErrPrivateKey
	}
	for _, subkey := range entity.Subkeys {
		if subkey.PrivateKey != nil {
			return nil, ErrPrivateKey
		}
	}
	now := time.Now()
	if entity.Revoked(now) {
		return nil, fmt.Errorf("%w: the key is revoked", ErrInvalidPGPKey)
	}
	fingerprint := strings.ToUpper(hex.EncodeToString(entity.PrimaryKey.Fingerprint))
	if fingerprint == "" {
		return nil, ErrInvalidPGPKey
	}
	userIDs := make([]string, 0, len(entity.Identities))
	for _, identity := range entity.Identities {
		if !identity.Revoked(now) {
			userIDs = append(userIDs, identity.Name)
		}
	}
	sort.Strings(userIDs)
	return &PGPKeyInfo{
		Algorithm:   pgpAlgorithmName(entity.PrimaryKey.PubKeyAlgo),
		PublicKey:   raw,
		Fingerprint: fingerprint,
		KeyID:       entity.PrimaryKey.KeyIdString(),
		UserIDs:     userIDs,
	}, nil
}

// LabelOrFallback trims the requested label and replaces an empty one with a
// recognizable default derived from the key itself.
func LabelOrFallback(label, fallback string) (string, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		label = strings.TrimSpace(fallback)
	}
	if label == "" {
		label = "Unnamed key"
	}
	if len([]rune(label)) > 128 {
		return "", ErrLabelTooLong
	}
	return label, nil
}

// pgpAlgorithmName maps the packet algorithm id to the name shown in the
// console. Unknown ids keep their numeric form so nothing is silently lost.
func pgpAlgorithmName(algorithm packet.PublicKeyAlgorithm) string {
	switch algorithm {
	case packet.PubKeyAlgoRSA, packet.PubKeyAlgoRSAEncryptOnly, packet.PubKeyAlgoRSASignOnly:
		return "rsa"
	case packet.PubKeyAlgoDSA:
		return "dsa"
	case packet.PubKeyAlgoElGamal:
		return "elgamal"
	case packet.PubKeyAlgoECDSA:
		return "ecdsa"
	case packet.PubKeyAlgoECDH:
		return "ecdh"
	case packet.PubKeyAlgoEdDSA:
		return "eddsa"
	case packet.PubKeyAlgoEd25519:
		return "ed25519"
	case packet.PubKeyAlgoX25519:
		return "x25519"
	case packet.PubKeyAlgoEd448:
		return "ed448"
	case packet.PubKeyAlgoX448:
		return "x448"
	case packet.PubKeyAlgoMlkem768X25519, packet.PubKeyAlgoMlkem1024X448:
		return "mlkem"
	case packet.PubKeyAlgoMldsa65Ed25519, packet.PubKeyAlgoMldsa87Ed448:
		return "mldsa"
	case packet.PubKeyAlgoSlhdsaShake128s, packet.PubKeyAlgoSlhdsaShake128f, packet.PubKeyAlgoSlhdsaShake256s:
		return "slhdsa"
	default:
		return fmt.Sprintf("algorithm-%d", int(algorithm))
	}
}

// SSHKeyFromEntity maps a row to the wire type.
func SSHKeyFromEntity(entity *SSHKeyEntity) model.SSHKey {
	return model.SSHKey{
		Id:          entity.ID.String(),
		AccountId:   entity.AccountID.String(),
		Label:       entity.Label,
		Algorithm:   entity.Algorithm,
		PublicKey:   entity.PublicKey,
		Fingerprint: entity.Fingerprint,
		CreatedAt:   model.NewTime(entity.CreatedAt),
		UpdatedAt:   model.NewTime(entity.UpdatedAt),
		DeletedAt:   deletedAt(entity.DeletedAt),
	}
}

// PGPKeyFromEntity maps a row to the wire type.
func PGPKeyFromEntity(entity *PGPKeyEntity) model.PGPKey {
	return model.PGPKey{
		Id:          entity.ID.String(),
		AccountId:   entity.AccountID.String(),
		Label:       entity.Label,
		Algorithm:   entity.Algorithm,
		PublicKey:   entity.PublicKey,
		Fingerprint: entity.Fingerprint,
		KeyId:       entity.KeyID,
		UserIds:     splitUserIDs(entity.UserIDs),
		CreatedAt:   model.NewTime(entity.CreatedAt),
		UpdatedAt:   model.NewTime(entity.UpdatedAt),
		DeletedAt:   deletedAt(entity.DeletedAt),
	}
}

func deletedAt(value gorm.DeletedAt) *model.Time {
	if !value.Valid || value.Time.IsZero() {
		return nil
	}
	return model.NewTime(value.Time)
}

func splitUserIDs(joined string) []string {
	var userIDs []string
	if err := json.Unmarshal([]byte(joined), &userIDs); err != nil || userIDs == nil {
		return []string{}
	}
	return userIDs
}

func joinUserIDs(userIDs []string) string {
	if userIDs == nil {
		userIDs = []string{}
	}
	raw, err := json.Marshal(userIDs)
	if err != nil {
		return "[]"
	}
	return string(raw)
}
