// Package model defines the wire types for the Stargate API. All JSON is
// snake_case with nulls omitted; enums serialize as integers; times serialize
// as UTC RFC3339 with second precision via Time.
package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// Time is a UTC instant serialized as ISO-8601 (RFC3339, seconds precision,
// e.g. 2026-07-27T15:32:00Z). Null is represented by a nil *Time and omitted
// from JSON.
type Time time.Time

func (t Time) Time() time.Time { return time.Time(t) }

func (t Time) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Time(t).UTC().Format(time.RFC3339))
}

func (t *Time) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		// NodaTime emits up to 7 fractional digits; Go's RFC3339 parser
		// accepts up to nanoseconds, so this fallback is defensive.
		parsed, err = time.Parse("2006-01-02T15:04:05.9999999Z07:00", s)
		if err != nil {
			return err
		}
	}
	*t = Time(parsed.UTC())
	return nil
}

// NewTime converts a time.Time into a *Time (nil for the zero value).
func NewTime(t time.Time) *Time {
	if t.IsZero() {
		return nil
	}
	v := Time(t.UTC())
	return &v
}

// Scan implements sql.Scanner so pgx can scan timestamptz columns into
// *Time fields (and &*Time targets).
func (t *Time) Scan(v any) error {
	switch x := v.(type) {
	case nil:
		*t = Time{}
	case time.Time:
		*t = Time(x.UTC())
	case string:
		return t.scanString(x)
	case []byte:
		return t.scanString(string(x))
	default:
		return fmt.Errorf("cannot scan %T into model.Time", v)
	}
	return nil
}

func (t *Time) scanString(s string) error {
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		parsed, err = time.Parse("2006-01-02T15:04:05.9999999Z07:00", s)
		if err != nil {
			return err
		}
	}
	*t = Time(parsed.UTC())
	return nil
}

// Value implements driver.Valuer so pgx can encode *Time arguments as
// timestamptz.
func (t *Time) Value() (driver.Value, error) {
	if t == nil {
		return nil, nil
	}
	return time.Time(*t).UTC(), nil
}

// MarshalText implements encoding.TextMarshaler (RFC3339 UTC).
func (t Time) MarshalText() ([]byte, error) {
	return []byte(time.Time(t).UTC().Format(time.RFC3339)), nil
}

// FromTime converts a *Time back to time.Time (zero when nil).
func (t *Time) FromTime() time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.Time()
}

// GeoPoint mirrors the C# GeoPoint jsonb shape.
type GeoPoint struct {
	Latitude    *float64 `json:"latitude,omitempty"`
	Longitude   *float64 `json:"longitude,omitempty"`
	CountryCode string   `json:"country_code,omitempty"`
	Country     string   `json:"country,omitempty"`
	City        string   `json:"city,omitempty"`
}

// SnCloudFileReferenceObject mirrors DysonNetwork.Shared.CloudFileReferenceObject.
type SnCloudFileReferenceObject struct {
	Id       string `json:"id"`
	Url      string `json:"url,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Blurhash string `json:"blurhash,omitempty"`
	Width    *int64 `json:"width,omitempty"`
	Height   *int64 `json:"height,omitempty"`
	Size     *int64 `json:"size,omitempty"`
}

// SnSubscriptionReferenceObject is the perk-subscription reference hydrated
// from the wallet service (mirrors SnSubscriptionReferenceObject).
type SnSubscriptionReferenceObject struct {
	Id          string  `json:"id"`
	Identifier  string  `json:"identifier"`
	DisplayName *string `json:"display_name,omitempty"`
	PerkLevel   int     `json:"perk_level"`
	IsActive    bool    `json:"is_active"`
	IsAvailable bool    `json:"is_available"`
	IsFreeTrial bool    `json:"is_free_trial"`
	Status      int     `json:"status"`
	BegunAt     *Time   `json:"begun_at,omitempty"`
	EndedAt     *Time   `json:"ended_at,omitempty"`
	RenewalAt   *Time   `json:"renewal_at,omitempty"`
	AccountId   string  `json:"account_id"`
	CreatedAt   *Time   `json:"created_at,omitempty"`
	UpdatedAt   *Time   `json:"updated_at,omitempty"`
}

// SnVerificationMark is the verification mark embedded in profiles.
type SnVerificationMark struct {
	Type        int     `json:"type"`
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	VerifiedBy  *string `json:"verified_by,omitempty"`
}

// UsernameColor mirrors the C# UsernameColor jsonb shape.
type UsernameColor struct {
	Type      string   `json:"type"`
	Value     *string  `json:"value,omitempty"`
	Direction *string  `json:"direction,omitempty"`
	Colors    []string `json:"colors,omitempty"`
}

// SnProfileLink mirrors the C# SnProfileLink.
type SnProfileLink struct {
	Name string `json:"name"`
	Url  string `json:"url"`
}

// ActionLogType mirrors DysonNetwork.Shared.Models.ActionLogType: string
// constants, not an enum.
type ActionLogType string

const (
	ActionLogNewLogin                      ActionLogType = "login"
	ActionLogAccountActive                 ActionLogType = "accounts.active"
	ActionLogChallengeAttempt              ActionLogType = "challenges.attempt"
	ActionLogChallengeSuccess              ActionLogType = "challenges.success"
	ActionLogChallengeFailure              ActionLogType = "challenges.failure"
	ActionLogAccountProfileUpdate          ActionLogType = "accounts.profile.update"
	ActionLogAuthFactorCreate              ActionLogType = "accounts.auth_factors.create"
	ActionLogAuthFactorEnable              ActionLogType = "accounts.auth_factors.enable"
	ActionLogAuthFactorDisable             ActionLogType = "accounts.auth_factors.disable"
	ActionLogAuthFactorDelete              ActionLogType = "accounts.auth_factors.delete"
	ActionLogAuthFactorResetPassword       ActionLogType = "accounts.auth_factors.reset_password"
	ActionLogAccountRecovery               ActionLogType = "accounts.recovery"
	ActionLogSessionRevoke                 ActionLogType = "developer.sessions.revoke"
	ActionLogDeviceRevoke                  ActionLogType = "developer.devices.revoke"
	ActionLogDeviceRename                  ActionLogType = "developer.devices.rename"
	ActionLogAuthorizedAppDeauthorize      ActionLogType = "developer.apps.deauthorize"
	ActionLogRelationshipFriendRequest     ActionLogType = "relationships.friends.request"
	ActionLogRelationshipFriendAccept      ActionLogType = "relationships.friends.accept"
	ActionLogRelationshipFriendEstablished ActionLogType = "relationships.friends.established"
	ActionLogRelationshipBlock             ActionLogType = "relationships.block"
	ActionLogRelationshipUnblock           ActionLogType = "relationships.unblock"
	ActionLogRelationshipMute              ActionLogType = "relationships.mute"
	ActionLogRelationshipUnmute            ActionLogType = "relationships.unmute"
	ActionLogRelationshipCloseFriend       ActionLogType = "relationships.close_friend.add"
	ActionLogRelationshipUnCloseFriend     ActionLogType = "relationships.close_friend.remove"
	ActionLogAccountAvatar                 ActionLogType = "accounts.profile.avatar"
	ActionLogAccountProfileComplete        ActionLogType = "accounts.profile.complete"
	ActionLogAccountConnectionLink         ActionLogType = "accounts.connection.link"
	ActionLogAccountPushEnable             ActionLogType = "accounts.push.enable"
)
