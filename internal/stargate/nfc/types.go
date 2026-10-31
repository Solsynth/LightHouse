package nfc

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/model"
)

// Errors returned by the service. Their messages are the exact strings
// Passport put on the wire (InvalidOperationException.Message).
var (
	ErrTagExists           = errors.New("This NFC tag is already registered.")
	ErrReplay              = errors.New("Replay detected")
	ErrEncryptedTagByUID   = errors.New("Encrypted tag not found with this UID.")
	ErrEncryptedTagByID    = errors.New("Encrypted tag not found with this ID.")
	ErrTagClaimedByAnother = errors.New("This tag has already been claimed by another account.")
	ErrTagNotFound         = errors.New("NFC tag not found.")
	ErrInvalidSunKey       = errors.New("SUN key must be 16 bytes (AES-128) or 32 bytes (AES-256).")
	ErrEmptySunKey         = errors.New("SUN key cannot be empty.")
	ErrUndecodableSunKey   = errors.New("Cannot decode sun_key. Provide hex (32 or 64 chars) or Base64.")
)

// ClaimStatus is NfcTagClaimStatus.
type ClaimStatus int

const (
	// ClaimHasOwner means the tag has an owner and resolves normally.
	ClaimHasOwner ClaimStatus = iota
	// ClaimJustClaimed means the tag was unclaimed and is now claimed by the
	// observer (Passport never auto-claims; kept for wire parity).
	ClaimJustClaimed
	// ClaimPreAssigned means the tag is pre-assigned to a different user.
	ClaimPreAssigned
	// ClaimUnclaimed means the tag is unclaimed and the observer is authenticated.
	ClaimUnclaimed
	// ClaimNeedsAuth means the tag is unclaimed and no observer was provided.
	ClaimNeedsAuth
	// ClaimPreAssignedMismatch means the tag is pre-assigned to another user.
	ClaimPreAssignedMismatch
)

// ResolveResult is NfcResolveResult.
type ResolveResult struct {
	ID        uuid.UUID
	Account   *model.Account
	IsFriend  bool
	IsClaimed bool
	Actions   []string
}

// ValidationResult is NfcValidationResult.
type ValidationResult struct {
	Tag         *TagEntity
	Account     *model.Account
	Profile     *model.Profile
	IsFriend    bool
	IsClaimed   bool
	ClaimStatus ClaimStatus
	Actions     []string
}

// ResolveResponse is the HTTP NfcResolveResponse.
type ResolveResponse struct {
	ID        string         `json:"id"`
	Account   *model.Account `json:"account"`
	IsFriend  bool           `json:"is_friend"`
	IsClaimed bool           `json:"is_claimed"`
	Actions   []string       `json:"actions"`
}

// TagResponse is the HTTP NfcTagResponse.
type TagResponse struct {
	ID          string      `json:"id"`
	UID         string      `json:"uid"`
	Label       *string     `json:"label,omitempty"`
	IsActive    bool        `json:"is_active"`
	IsLocked    bool        `json:"is_locked"`
	IsEncrypted bool        `json:"is_encrypted"`
	SunKey      *string     `json:"sun_key,omitempty"`
	UserID      *string     `json:"user_id,omitempty"`
	LastSeenAt  *model.Time `json:"last_seen_at,omitempty"`
	CreatedAt   *model.Time `json:"created_at"`
}

// EncryptedTagDTO is the admin NfcAdminController.EncryptedTagDto.
type EncryptedTagDTO struct {
	ID         string      `json:"id"`
	UID        string      `json:"uid"`
	UserID     *string     `json:"user_id,omitempty"`
	IsActive   bool        `json:"is_active"`
	IsLocked   bool        `json:"is_locked"`
	LastSeenAt *model.Time `json:"last_seen_at,omitempty"`
	CreatedAt  *model.Time `json:"created_at"`
}

// NewTagResponse builds the user-facing tag payload.
func NewTagResponse(tag *TagEntity) TagResponse {
	response := TagResponse{
		ID:          tag.ID.String(),
		UID:         tag.UID,
		Label:       tag.Label,
		IsActive:    tag.IsActive,
		IsLocked:    tag.LockedAt != nil,
		IsEncrypted: tag.IsEncrypted,
		CreatedAt:   model.NewTime(tag.CreatedAt),
	}
	if tag.AccountID != nil {
		id := tag.AccountID.String()
		response.UserID = &id
	}
	if tag.LastSeenAt != nil {
		response.LastSeenAt = model.NewTime(*tag.LastSeenAt)
	}
	return response
}

// NewEncryptedTagDTO builds the admin tag payload.
func NewEncryptedTagDTO(tag *TagEntity) EncryptedTagDTO {
	response := EncryptedTagDTO{
		ID:        tag.ID.String(),
		UID:       tag.UID,
		IsActive:  tag.IsActive,
		IsLocked:  tag.LockedAt != nil,
		CreatedAt: model.NewTime(tag.CreatedAt),
	}
	if tag.AccountID != nil {
		id := tag.AccountID.String()
		response.UserID = &id
	}
	if tag.LastSeenAt != nil {
		response.LastSeenAt = model.NewTime(*tag.LastSeenAt)
	}
	return response
}

// ParseSolianURL extracts picc_data, e and cmac from a
// solian://phpass?picc_data=...&e=...&cmac=... URL.
func ParseSolianURL(raw string) (piccData, encData, cmac string, ok bool) {
	if !strings.HasPrefix(strings.ToLower(raw), "solian://") {
		return "", "", "", false
	}
	parsed, err := url.Parse("http://" + raw[len("solian://"):])
	if err != nil {
		return "", "", "", false
	}
	query := parsed.Query()
	piccData = query.Get("picc_data")
	if piccData == "" {
		return "", "", "", false
	}
	return piccData, query.Get("e"), query.Get("cmac"), true
}

// ParseNfcInput accepts either a solian:// URL or a raw hex string. The
// boolean reports whether the input was a URL carrying picc_data (Passport's
// "new format"); a false result means the input is a legacy/raw value.
func ParseNfcInput(input string) (piccData, encData, cmac string, isSdmURL bool) {
	return ParseSolianURL(input)
}

// ParseSunKey decodes a SUN key supplied either as hex (32 or 64 characters)
// or as Base64, mirroring NfcAdminController.ParseSunKey.
func ParseSunKey(sunKey string) ([]byte, error) {
	sunKey = strings.TrimSpace(sunKey)
	if sunKey == "" {
		return nil, ErrEmptySunKey
	}
	isHex := true
	for _, c := range sunKey {
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'F') || (c >= 'a' && c <= 'f')) {
			isHex = false
			break
		}
	}
	if isHex && (len(sunKey) == 32 || len(sunKey) == 64) {
		return hex.DecodeString(sunKey)
	}
	decoded, err := base64.StdEncoding.DecodeString(sunKey)
	if err != nil {
		return nil, ErrUndecodableSunKey
	}
	return decoded, nil
}

// IsHexString mirrors NfcController.IsHexString.
func IsHexString(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'F') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
