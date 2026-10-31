// Package accountrecords owns the Passport account-record domain that used to
// live in the C# Account/Credit/Leveling services: experience records, social
// credit records, account badges and the customisable account board.
//
// The tables keep their legacy Passport names and columns so an existing
// deployment needs no data copy. Besides the HTTP/gRPC surface the package
// exports a small Store API (`AddExperience`, `AddSocialCredit`,
// `EffectiveSocialCredit`, `GrantBadge`, `ListBadges`) so sibling feature
// modules such as check-in can write these records without going through
// another service.
package accountrecords

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/model"
)

// Board item kinds mirror SnAccountBoardItemKind.
const (
	BoardKindPrebuilt  = 0
	BoardKindCustomApp = 1
)

// Social credit record statuses mirror SocialCreditRecordStatus.
const (
	SocialCreditActive  = 0
	SocialCreditExpired = 1
)

// ErrBadgeNotFound mirrors Passport's "Badge was not found." contract.
var ErrBadgeNotFound = errors.New("Badge was not found.")

// ErrBoardItemNotFound mirrors Passport's "Board item not found." contract.
var ErrBoardItemNotFound = errors.New("Board item not found.")

// ValidationError carries a user-facing message for the 400 responses
// Passport returned from board validation.
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

// ExperienceRecord is the wire shape of SnExperienceRecord.
type ExperienceRecord struct {
	Id              uuid.UUID   `json:"id"`
	ReasonType      string      `json:"reason_type"`
	Reason          string      `json:"reason"`
	Delta           int64       `json:"delta"`
	BonusMultiplier float64     `json:"bonus_multiplier"`
	AccountId       uuid.UUID   `json:"account_id"`
	CreatedAt       *model.Time `json:"created_at"`
	UpdatedAt       *model.Time `json:"updated_at"`
	DeletedAt       *model.Time `json:"deleted_at"`
}

// SocialCreditRecord is the wire shape of SnSocialCreditRecord.
type SocialCreditRecord struct {
	Id         uuid.UUID   `json:"id"`
	ReasonType string      `json:"reason_type"`
	Reason     string      `json:"reason"`
	Delta      float64     `json:"delta"`
	Status     int         `json:"status"`
	ExpiredAt  *model.Time `json:"expired_at,omitempty"`
	AccountId  uuid.UUID   `json:"account_id"`
	CreatedAt  *model.Time `json:"created_at"`
	UpdatedAt  *model.Time `json:"updated_at"`
	DeletedAt  *model.Time `json:"deleted_at"`
}

// Badge is the wire shape of SnAccountBadge.
type Badge struct {
	Id          uuid.UUID      `json:"id"`
	Type        string         `json:"type"`
	Label       *string        `json:"label,omitempty"`
	Caption     *string        `json:"caption,omitempty"`
	Meta        map[string]any `json:"meta"`
	ActivatedAt *model.Time    `json:"activated_at,omitempty"`
	ExpiredAt   *model.Time    `json:"expired_at,omitempty"`
	AccountId   uuid.UUID      `json:"account_id"`
	CreatedAt   *model.Time    `json:"created_at"`
	UpdatedAt   *model.Time    `json:"updated_at"`
	DeletedAt   *model.Time    `json:"deleted_at"`
}

// BoardItem is the wire shape of SnAccountBoardItem.
type BoardItem struct {
	Id                 uuid.UUID      `json:"id"`
	AccountId          uuid.UUID      `json:"account_id"`
	Order              int            `json:"order"`
	Kind               int            `json:"kind"`
	WidgetKey          *string        `json:"widget_key,omitempty"`
	CustomAppId        *uuid.UUID     `json:"custom_app_id,omitempty"`
	CustomAppWidgetKey *string        `json:"custom_app_widget_key,omitempty"`
	IsEnabled          bool           `json:"is_enabled"`
	Payload            map[string]any `json:"payload"`
	CreatedAt          *model.Time    `json:"created_at"`
	UpdatedAt          *model.Time    `json:"updated_at"`
	DeletedAt          *model.Time    `json:"deleted_at"`
}

// BoardItemRequest is the PUT /board body (both the self and admin routes use
// the Passport request shape). IsEnabled defaults to true when omitted.
type BoardItemRequest struct {
	Id                 *uuid.UUID     `json:"id"`
	Order              int            `json:"order"`
	Kind               int            `json:"kind"`
	WidgetKey          *string        `json:"widget_key"`
	CustomAppId        *uuid.UUID     `json:"custom_app_id"`
	CustomAppWidgetKey *string        `json:"custom_app_widget_key"`
	IsEnabled          *bool          `json:"is_enabled"`
	Payload            map[string]any `json:"payload"`
}

// Input converts the request to the store input, applying the Passport
// default (`IsEnabled = true`).
func (r BoardItemRequest) Input() BoardItemInput {
	enabled := true
	if r.IsEnabled != nil {
		enabled = *r.IsEnabled
	}
	return BoardItemInput{
		Id:                 r.Id,
		Order:              r.Order,
		Kind:               r.Kind,
		WidgetKey:          r.WidgetKey,
		CustomAppId:        r.CustomAppId,
		CustomAppWidgetKey: r.CustomAppWidgetKey,
		IsEnabled:          enabled,
		Payload:            r.Payload,
	}
}

// BoardItemInput is a board item as submitted by a replace request.
type BoardItemInput struct {
	Id                 *uuid.UUID
	Order              int
	Kind               int
	WidgetKey          *string
	CustomAppId        *uuid.UUID
	CustomAppWidgetKey *string
	IsEnabled          bool
	Payload            map[string]any
}

// ValidateBoardPayload enforces the universal board payload envelope:
// every field must be an object with a value, a non-empty string label and an
// optional string format. It returns the normalized payload.
func ValidateBoardPayload(payload map[string]any) (map[string]any, error) {
	result := make(map[string]any, len(payload))
	for key, raw := range payload {
		value, label, format, err := readEnvelope(raw)
		if err != nil {
			return nil, fmt.Errorf("Payload field '%s' %s", key, err.Error())
		}
		if strings.TrimSpace(label) == "" {
			return nil, fmt.Errorf("Payload field '%s' must include a non-empty 'label'.", key)
		}
		envelope := map[string]any{
			"value": normalizeValue(value),
			"label": label,
		}
		if strings.TrimSpace(format) != "" {
			envelope["format"] = format
		}
		result[key] = envelope
	}
	return result, nil
}

func readEnvelope(raw any) (value any, label, format string, err error) {
	dict, ok := raw.(map[string]any)
	if !ok {
		return nil, "", "", errors.New("must be an object with 'value', 'label', and optional 'format'.")
	}
	value, ok = dict["value"]
	if !ok {
		return nil, "", "", errors.New("must be an object with 'value', 'label', and optional 'format'.")
	}
	labelRaw, ok := dict["label"]
	if !ok {
		return nil, "", "", errors.New("must include a string 'label'.")
	}
	label, ok = labelRaw.(string)
	if !ok {
		return nil, "", "", errors.New("must include a string 'label'.")
	}
	if formatRaw, ok := dict["format"]; ok {
		if value, isString := formatRaw.(string); isString {
			format = value
		}
	}
	return value, label, format, nil
}

// normalizeValue converts JSON-decoded values into their canonical Go shape:
// nested objects become map[string]any and arrays []any. It also normalizes
// json.Number to int64/float64 like the C# InfraObjectCoder.
func normalizeValue(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer
		}
		if number, err := typed.Float64(); err == nil {
			return number
		}
		return typed.String()
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = normalizeValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = normalizeValue(item)
		}
		return out
	default:
		return value
	}
}

func metaFromJSON(raw []byte) map[string]any {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil || meta == nil {
		return map[string]any{}
	}
	return meta
}

func payloadFromJSON(raw []byte) map[string]any {
	return metaFromJSON(raw)
}

func jsonFromPayload(payload map[string]any) ([]byte, error) {
	if payload == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(payload)
}

func jsonFromMeta(meta map[string]any) ([]byte, error) {
	if meta == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(meta)
}

func timePtr(value time.Time) *model.Time {
	return model.NewTime(value)
}

func optionalTimePtr(value *time.Time) *model.Time {
	if value == nil {
		return nil
	}
	return model.NewTime(*value)
}

// GetType lets the progression module's badge adapter recognize the local
// badge provider without importing this package (ports.Badges returns any).
func (b Badge) GetType() string { return b.Type }
