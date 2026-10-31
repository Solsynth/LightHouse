// Package nearby ports Passport's BLE presence domain into Stargate: rotating
// HMAC presence tokens issued by nearby devices and token resolution into
// visible peers. It deliberately excludes the meet and location-pin domains.
//
// Entities keep Passport's legacy table and column names (nearby_devices,
// nearby_presence_tokens) so an existing database can be pointed at this
// module without a data copy.
package nearby

import (
	"errors"
	"strconv"
	"strings"

	"src.solsynth.dev/sosys/stargate/model"
)

// ModuleID is the feature module identifier.
const ModuleID = "app.solian.id.nearby"

const (
	// DefaultServiceUUID mirrors NearbyService.DefaultServiceUuid.
	DefaultServiceUUID = "FFF0"
	// DefaultSlotDurationSec mirrors NearbyService.DefaultSlotDurationSec.
	DefaultSlotDurationSec = 30
	// DefaultPrefetchSlots mirrors NearbyService.DefaultPrefetchSlots.
	DefaultPrefetchSlots = 10
	// MaxPrefetchSlots mirrors NearbyService.MaxPrefetchSlots.
	MaxPrefetchSlots = 30
	// TokenBytes is the number of HMAC bytes exposed as the token.
	TokenBytes = 16
	// TokenHashLength is the uppercase-hex SHA-256 length stored in the row.
	TokenHashLength = 64
)

// ErrUnavailable marks a context where the signing secret is not configured.
// Controllers translate it to SERVICE_UNAVAILABLE (503): without a secret the
// deterministic token cannot be generated or verified.
var ErrUnavailable = errors.New("nearby: presence token secret is not configured")

// DeviceStatus mirrors NearbyDeviceStatus.
type DeviceStatus int

const (
	DeviceActive   DeviceStatus = 0
	DeviceDisabled DeviceStatus = 1
)

// Settings is the [modules.settings."app.solian.id.nearby"] subtree.
type Settings struct {
	ServiceUUID         string
	SlotDurationSec     int
	PresenceTokenSecret string
}

// DefaultSettings carries the C# appsettings defaults. The presence secret has
// no built-in default: Passport's "dev-nearby-secret-change-me" fallback is
// intentionally not reproduced (a deployment must configure a real secret).
func DefaultSettings() Settings {
	return Settings{
		ServiceUUID:     DefaultServiceUUID,
		SlotDurationSec: DefaultSlotDurationSec,
	}
}

// SettingsFromMap reads the module settings. camelCase keys match Passport's
// Nearby section; snake_case is accepted for convenience.
func SettingsFromMap(raw map[string]any) Settings {
	settings := DefaultSettings()
	if raw == nil {
		return settings
	}
	if value, ok := stringSetting(raw, "serviceUuid", "service_uuid"); ok && strings.TrimSpace(value) != "" {
		settings.ServiceUUID = strings.TrimSpace(value)
	}
	if value, ok := intSetting(raw, "slotDurationSec", "slot_duration_sec"); ok {
		settings.SlotDurationSec = clampSlotDuration(value)
	}
	if value, ok := stringSetting(raw, "presenceTokenSecret", "presence_token_secret"); ok {
		settings.PresenceTokenSecret = strings.TrimSpace(value)
	}
	return settings
}

// normalizeSlotDuration treats a zero value as "unset" (the zero-value
// Settings a caller may construct) and clamps everything else.
func normalizeSlotDuration(value int) int {
	if value == 0 {
		return DefaultSlotDurationSec
	}
	return clampSlotDuration(value)
}

func clampSlotDuration(value int) int {
	if value < 15 {
		return 15
	}
	if value > 300 {
		return 300
	}
	return value
}

// IssueRequest is the normalized POST /api/nearby/presence-tokens body.
type IssueRequest struct {
	DeviceID      string
	Discoverable  bool
	FriendOnly    bool
	Capabilities  int
	PrefetchSlots int
}

// PresenceToken is one issued slot token.
type PresenceToken struct {
	Slot      int64
	Token     string
	ValidFrom int64 // unix seconds
	ValidTo   int64 // unix seconds
}

// IssueResult is the presence-token response payload.
type IssueResult struct {
	ServiceUUID     string
	SlotDurationSec int
	Tokens          []PresenceToken
}

// Observation is one scanned token reported by a device.
type Observation struct {
	Token       string
	Slot        int64
	AvgRSSI     int
	SeenCount   int
	DurationMS  int64
	FirstSeenAt *int64 // unix seconds
	LastSeenAt  *int64 // unix seconds
}

// Peer is one resolved nearby account.
type Peer struct {
	UserID      string
	DisplayName string
	Avatar      *model.SnCloudFileReferenceObject
	IsFriend    bool
	CanInvite   bool
	Visibility  string
	LastSeenAt  int64 // unix seconds
}

func stringSetting(raw map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		if typed, ok := value.(string); ok {
			return typed, true
		}
	}
	return "", false
}

func intSetting(raw map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case int:
			return typed, true
		case int64:
			return int(typed), true
		case float64:
			return int(typed), true
		case string:
			parsed, err := strconv.Atoi(strings.TrimSpace(typed))
			if err == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}
