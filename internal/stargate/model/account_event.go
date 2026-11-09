package model

import (
	"time"

	"github.com/google/uuid"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

// Account status and presence-activity wire types. They mirror
// DysonNetwork.Shared.Models.AccountEvent.cs (SnAccountStatus,
// SnPresenceActivity, SnPresenceCatalogItem, SnPresenceTag and their tag
// join rows) so the existing Dart SDK (solar_network_sdk) keeps parsing the
// responses: enums are integers, instants are RFC3339 UTC with Z. The
// created_at/updated_at/deleted_at keys are always emitted (null when unset),
// because the SDK parses createdAt/updatedAt with DateTime.parse and requires
// them to be present.
//
// The status fields IsOnline, IsIdle, IsIdleSince, IsCustomized and
// OnlineDevices are computed at read time — they are [NotMapped] in C# and
// have no column here either.

// StatusAttitude mirrors StatusAttitude.
type StatusAttitude int

const (
	StatusAttitudePositive StatusAttitude = 0
	StatusAttitudeNegative StatusAttitude = 1
	StatusAttitudeNeutral  StatusAttitude = 2
)

// StatusType mirrors StatusType.
type StatusType int

const (
	StatusTypeDefault      StatusType = 0
	StatusTypeBusy         StatusType = 1
	StatusTypeDoNotDisturb StatusType = 2
	StatusTypeInvisible    StatusType = 3
)

// PresenceVisibility mirrors PresenceVisibility.
type PresenceVisibility int

const (
	PresenceVisibilityUnknown PresenceVisibility = 0
	PresenceVisibilityPublic  PresenceVisibility = 1
	PresenceVisibilityFriends PresenceVisibility = 2
	PresenceVisibilityPrivate PresenceVisibility = 3
)

// OnlineDevice mirrors SnOnlineDevice: an auth client with a live websocket
// connection, resolved from Stargate's own device-presence registry.
type OnlineDevice struct {
	Id            string  `json:"id"`
	DeviceId      string  `json:"device_id"`
	DeviceName    string  `json:"device_name"`
	DeviceLabel   *string `json:"device_label,omitempty"`
	Platform      int     `json:"platform"`
	LastGrantedAt *Time   `json:"last_granted_at,omitempty"`
}

// AccountStatus mirrors SnAccountStatus.
type AccountStatus struct {
	Id            string                      `json:"id"`
	Attitude      StatusAttitude              `json:"attitude"`
	Type          StatusType                  `json:"type"`
	Label         *string                     `json:"label,omitempty"`
	Symbol        *string                     `json:"symbol,omitempty"`
	Icon          *SnCloudFileReferenceObject `json:"icon,omitempty"`
	Background    *SnCloudFileReferenceObject `json:"background,omitempty"`
	Meta          map[string]any              `json:"meta,omitempty"`
	ClearedAt     *Time                       `json:"cleared_at,omitempty"`
	AppIdentifier *string                     `json:"app_identifier,omitempty"`
	IsAutomated   bool                        `json:"is_automated"`
	AccountId     string                      `json:"account_id"`

	// Read-time presence fields ([NotMapped] in C#).

	IsOnline bool `json:"is_online"`
	// OnlineDevices lists the account's live devices. It is omitted when the
	// viewer may not see them (C# writes JSON null and suppresses it, so the
	// key is absent either way); a hidden or empty list is therefore omitted
	// rather than rendered as [], which the SDK reads as "no devices".
	OnlineDevices []OnlineDevice `json:"online_devices,omitempty"`
	IsIdle        bool           `json:"is_idle"`
	IdleSince     *Time          `json:"idle_since,omitempty"`
	IsCustomized  bool           `json:"is_customized"`

	// Derived from Type at read time (C# [NotMapped] shadow properties the
	// gRPC mapping and the Dart client read off the status).
	IsInvisible  bool `json:"is_invisible"`
	IsNotDisturb bool `json:"is_not_disturb"`

	CreatedAt *Time `json:"created_at"`
	UpdatedAt *Time `json:"updated_at"`
	DeletedAt *Time `json:"deleted_at"`
}

// zeroTime returns a non-nil zero Instant. C#'s ModelBase declares CreatedAt
// and UpdatedAt non-nullable, so a synthesized status serializes them as
// 0001-01-01T00:00:00Z instead of omitting the keys. NewTime cannot be used
// here because it deliberately maps the zero time to nil.
func zeroTime() *Time {
	value := Time(time.Time{})
	return &value
}

// DefaultAccountStatus mirrors the SnAccountStatus that
// AccountEventService.GetStatus synthesizes when an account has no active
// row: neutral attitude, no customization, and the zero Instant for both
// timestamps. The Dart SDK parses id/created_at/updated_at as non-nullable,
// so every one of them must be populated; the id is deterministic so repeated
// reads of the same account keep a stable synthetic identity.
func DefaultAccountStatus(accountID string, online bool) *AccountStatus {
	label := "Offline"
	if online {
		label = "Online"
	}
	return &AccountStatus{
		Id: uuid.NewSHA1(
			uuid.NameSpaceURL,
			[]byte("stargate:default-status:"+accountID),
		).String(),
		Attitude:     StatusAttitudeNeutral,
		Label:        &label,
		IsCustomized: false,
		AccountId:    accountID,
		CreatedAt:    zeroTime(),
		UpdatedAt:    zeroTime(),
	}
}

// PresenceTag mirrors SnPresenceTag: the wire shape of a tag row.
type PresenceTag struct {
	Slug string  `json:"slug"`
	Name *string `json:"name,omitempty"`
}

// PresenceActivity mirrors SnPresenceActivity. Tags is the denormalized form
// of the presence_activity_tags rows (C# assigns it alongside ActivityTags
// before serializing).
type PresenceActivity struct {
	Id             string               `json:"id"`
	Type           *string              `json:"type,omitempty"`
	Tags           []PresenceTag        `json:"tags"`
	Visibility     PresenceVisibility   `json:"visibility"`
	Provider       *string              `json:"provider,omitempty"`
	ReferenceId    *string              `json:"reference_id,omitempty"`
	ManualId       *string              `json:"manual_id,omitempty"`
	Title          *string              `json:"title,omitempty"`
	Subtitle       *string              `json:"subtitle,omitempty"`
	Caption        *string              `json:"caption,omitempty"`
	LargeImage     *string              `json:"large_image,omitempty"`
	SmallImage     *string              `json:"small_image,omitempty"`
	TitleUrl       *string              `json:"title_url,omitempty"`
	SubtitleUrl    *string              `json:"subtitle_url,omitempty"`
	QueryableTerms []string             `json:"queryable_terms"`
	Meta           map[string]any       `json:"meta,omitempty"`
	LeaseMinutes   int                  `json:"lease_minutes"`
	LeaseExpiresAt *Time                `json:"lease_expires_at,omitempty"`
	StartedAt      *Time                `json:"started_at,omitempty"`
	EndedAt        *Time                `json:"ended_at,omitempty"`
	CatalogId      *string              `json:"catalog_id,omitempty"`
	Catalog        *PresenceCatalogItem `json:"catalog,omitempty"`
	AccountId      string               `json:"account_id"`
	CreatedAt      *Time                `json:"created_at"`
	UpdatedAt      *Time                `json:"updated_at"`
	DeletedAt      *Time                `json:"deleted_at"`
}

// PresenceCatalogItem mirrors SnPresenceCatalogItem; Tags is the wire form of
// the presence_catalog_tags rows.
type PresenceCatalogItem struct {
	Id             string             `json:"id"`
	AccountId      string             `json:"account_id"`
	Provider       *string            `json:"provider,omitempty"`
	CatalogKey     *string            `json:"catalog_key,omitempty"`
	ReferenceId    *string            `json:"reference_id,omitempty"`
	Name           *string            `json:"name,omitempty"`
	Subtitle       *string            `json:"subtitle,omitempty"`
	Caption        *string            `json:"caption,omitempty"`
	Visibility     PresenceVisibility `json:"visibility"`
	Tags           []PresenceTag      `json:"tags"`
	LargeImage     *string            `json:"large_image,omitempty"`
	SmallImage     *string            `json:"small_image,omitempty"`
	TitleUrl       *string            `json:"title_url,omitempty"`
	SubtitleUrl    *string            `json:"subtitle_url,omitempty"`
	QueryableTerms []string           `json:"queryable_terms"`
	Meta           map[string]any     `json:"meta"`
	TotalSeconds   int64              `json:"total_seconds"`
	SessionCount   int                `json:"session_count"`
	LastActiveAt   *Time              `json:"last_active_at,omitempty"`
	CreatedAt      *Time              `json:"created_at"`
	UpdatedAt      *Time              `json:"updated_at"`
	DeletedAt      *Time              `json:"deleted_at"`
}

// ProtoToPresenceActivity converts a DyPresenceActivity protobuf message to PresenceActivity.
func ProtoToPresenceActivity(act *gen.DyPresenceActivity) PresenceActivity {
	if act == nil {
		return PresenceActivity{}
	}
	out := PresenceActivity{
		Id:           act.Id,
		AccountId:    act.AccountId,
		Type:         act.Type,
		ManualId:     act.ManualId,
		Title:        act.Title,
		Subtitle:     act.Subtitle,
		Caption:      act.Caption,
		LargeImage:   act.LargeImage,
		SmallImage:   act.SmallImage,
		TitleUrl:     act.TitleUrl,
		SubtitleUrl:  act.SubtitleUrl,
		LeaseMinutes: int(act.LeaseMinutes),
		Visibility:   PresenceVisibility(act.Visibility),
	}
	if act.CatalogId != nil {
		out.CatalogId = act.CatalogId
	}
	if act.LeaseExpiresAt != nil {
		t := Time(act.LeaseExpiresAt.AsTime())
		out.LeaseExpiresAt = &t
	}
	if act.StartedAt != nil {
		t := Time(act.StartedAt.AsTime())
		out.StartedAt = &t
	}
	if act.EndedAt != nil {
		t := Time(act.EndedAt.AsTime())
		out.EndedAt = &t
	}
	if act.CreatedAt != nil {
		t := Time(act.CreatedAt.AsTime())
		out.CreatedAt = &t
	}
	if act.UpdatedAt != nil {
		t := Time(act.UpdatedAt.AsTime())
		out.UpdatedAt = &t
	}
	if act.Meta != nil {
		out.Meta = act.Meta.AsMap()
	}
	for _, tag := range act.Tags {
		if tag != nil {
			out.Tags = append(out.Tags, PresenceTag{
				Slug: tag.Slug,
				Name: tag.Name,
			})
		}
	}
	return out
}
