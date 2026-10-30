// Package model mirrors DysonNetwork.Shared.Models plus the Ring entity
// models (Email, DeliveryObservability). JSON is snake_case with nulls
// INCLUDED (Ring's AddJsonOptions sets no DefaultIgnoreCondition — unlike
// Stargate, do NOT add omitempty to API-facing fields); enums serialize as
// integers; instants are RFC3339 UTC seconds via pkg/models.Time.
package model

import (
	"github.com/google/uuid"
	"src.solsynth.dev/sosys/go/pkg/models"
)

// ModelBase mirrors DysonNetwork.Shared.Models.ModelBase.
type ModelBase struct {
	Id        uuid.UUID      `json:"id"`
	CreatedAt models.Time    `json:"created_at"`
	UpdatedAt models.Time    `json:"updated_at"`
	DeletedAt *models.Time   `json:"deleted_at"`
}

// PushProvider mirrors the SnNotificationPushSubscription.Provider enum
// (declaration order = wire/DB values).
type PushProvider int

const (
	PushProviderApple PushProvider = iota // 0
	PushProviderGoogle                    // 1
	PushProviderSop                       // 2
	PushProviderUnifiedPush               // 3
	PushProviderAppk                      // 4
)

// String returns the C# provider name (GetProviderName / action-log payload).
func (p PushProvider) String() string {
	switch p {
	case PushProviderApple:
		return "Apple"
	case PushProviderGoogle:
		return "Google"
	case PushProviderSop:
		return "Sop"
	case PushProviderUnifiedPush:
		return "UnifiedPush"
	case PushProviderAppk:
		return "Appk"
	default:
		return "Unknown"
	}
}

// ProviderName returns the observability provider key (GetProviderName).
func (p PushProvider) ProviderName() string {
	switch p {
	case PushProviderApple:
		return "apple"
	case PushProviderGoogle:
		return "google"
	case PushProviderAppk:
		return "appk"
	case PushProviderSop:
		return "sop"
	case PushProviderUnifiedPush:
		return "unifiedpush"
	default:
		return lowerFirst(p.String())
	}
}

// NotificationPreferenceLevel mirrors SnNotificationPreference.Preference.
type NotificationPreferenceLevel int

const (
	NotificationPreferenceNormal NotificationPreferenceLevel = iota // 0
	NotificationPreferenceSilent                                    // 1
	NotificationPreferenceReject                                    // 2
)

// Notification mirrors SnNotification.
type Notification struct {
	ModelBase
	Topic    string         `json:"topic"`
	Title    *string        `json:"title"`
	Subtitle *string        `json:"subtitle"`
	Content  *string        `json:"content"`
	Meta     map[string]any `json:"meta"`
	Priority int            `json:"priority"`
	ViewedAt *models.Time   `json:"viewed_at"`
	AppId    *string        `json:"app_id"`
	PushType *string        `json:"push_type"`
	AccountId uuid.UUID     `json:"account_id"`
}

// NotificationPreference mirrors SnNotificationPreference.
type NotificationPreference struct {
	ModelBase
	AccountId  uuid.UUID                 `json:"account_id"`
	Topic      string                    `json:"topic"`
	Preference NotificationPreferenceLevel `json:"preference"`
}

// AppIdValue returns the AppId string (empty when nil).
func (n *Notification) AppIdValue() string {
	if n == nil || n.AppId == nil {
		return ""
	}
	return *n.AppId
}

// PushSubscription mirrors SnNotificationPushSubscription.
type PushSubscription struct {
	ModelBase
	AccountId      uuid.UUID     `json:"account_id"`
	DeviceId       string        `json:"device_id"`
	DeviceToken    string        `json:"device_token"`
	DeviceName     *string       `json:"device_name"`
	Provider       PushProvider  `json:"provider"`
	IsActivated    bool          `json:"is_activated"`
	AppId          *string       `json:"app_id"`
	CountDelivered int           `json:"count_delivered"`
	LastUsedAt     *models.Time  `json:"last_used_at"`
}

// AppIdValue returns the AppId string (empty when nil).
func (s *PushSubscription) AppIdValue() string {
	if s == nil || s.AppId == nil {
		return ""
	}
	return *s.AppId
}
