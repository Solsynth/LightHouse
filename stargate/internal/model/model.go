// Package model defines the wire types for the Stargate API. All JSON is
// snake_case with nulls omitted; enums serialize as integers; times serialize
// as UTC RFC3339 with second precision via Time.
package model

import (
	"time"

	models "src.solsynth.dev/sosys/go/pkg/models"
)

// Time is the fleet Instant type (UTC RFC3339, seconds precision), shared via
// Golaunch pkg/models. Null is represented by a nil *Time and omitted from
// JSON.
type Time = models.Time

// NewTime converts a time.Time into a *Time (nil for the zero value).
func NewTime(t time.Time) *Time { return models.NewTime(t) }

// GeoPoint mirrors the C# GeoPoint jsonb shape.
type GeoPoint struct {
	Latitude    *float64 `json:"latitude,omitempty"`
	Longitude   *float64 `json:"longitude,omitempty"`
	CountryCode string   `json:"country_code,omitempty"`
	Country     string   `json:"country,omitempty"`
	City        string   `json:"city,omitempty"`
}

// SnCloudFileReferenceObject mirrors
// DysonNetwork.Shared.CloudFileReferenceObject; the shared definition lives
// in Golaunch pkg/models (the full jsonb file-cache shape).
type SnCloudFileReferenceObject = models.SnCloudFileReferenceObject

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
	// BasePrice/FinalPrice are wire strings ("19.99"); the C# model parses
	// them with decimal.Parse, so they must be present once the reference is.
	BasePrice  string `json:"base_price,omitempty"`
	FinalPrice string `json:"final_price,omitempty"`
	AccountId  string `json:"account_id"`
	CreatedAt  *Time  `json:"created_at,omitempty"`
	UpdatedAt  *Time  `json:"updated_at,omitempty"`
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
