// Package ports defines the interfaces the Messager chat module uses to reach
// the rest of the fleet. A missing port means the dependent feature degrades
// rather than failing at boot.
package ports

import (
	"context"
	"errors"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/model"
)

// ErrUnavailable marks a port whose backend is disabled or not configured.
var ErrUnavailable = errors.New("port provider unavailable")

// Port IDs.
const (
	PortAccounts = "accounts"
	PortFiles    = "files"
	PortNotify   = "notify"
	PortRealtime = "realtime"
	PortSphere   = "sphere"
	// PortRealms resolves realm snapshots for realm chat.
	PortRealms = "realms"
)

// Accounts resolves accounts from Stargate.
type Accounts interface {
	GetAccount(ctx context.Context, id string) (*gen.DyAccount, error)
	GetAccounts(ctx context.Context, ids []string) ([]*gen.DyAccount, error)
	LookupAccounts(ctx context.Context, names []string) ([]*gen.DyAccount, error)
	// HasRelationship reports whether the two accounts have a relationship.
	HasRelationship(ctx context.Context, accountID, relatedID string) (bool, error)
}

// Files resolves Drive file references.
type Files interface {
	GetFiles(ctx context.Context, ids []string) (map[string]model.CloudFileReferenceObject, error)
}

// Notify sends push notifications through Ring.
type Notify interface {
	SendPush(ctx context.Context, accountID, topic, title, body string, meta map[string]string) error
	SendPushToAccounts(ctx context.Context, accountIDs []string, topic, title, body string, meta map[string]string) error
}

// Realtime pushes websocket packets through Blade.
type Realtime interface {
	Push(ctx context.Context, accountIDs []string, event string, payload any) error
}

// Sphere resolves content owned by the Sphere service.
type Sphere interface {
	GetSurvey(ctx context.Context, id string) (*gen.DySurvey, error)
	GetStickerByIdentifier(ctx context.Context, identifier string) (*gen.DySticker, error)
	Autocomplete(ctx context.Context, content, chatID, realmID string, limit int) ([]byte, error)
	GetLinkPreview(ctx context.Context, url string) ([]byte, error)
	IsPublisherMember(ctx context.Context, publisherID, accountID string) (bool, error)
}

// RealmInfo is the realm snapshot the chat module embeds in responses.
type RealmInfo struct {
	ID          string  `json:"id"`
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	IsPublic    bool    `json:"is_public"`
	Picture     any     `json:"picture"`
	Background  any     `json:"background"`
}

// Realms resolves realm snapshots.
type Realms interface {
	GetRealm(ctx context.Context, id string) (*RealmInfo, error)
	GetRealmBySlug(ctx context.Context, slug string) (*RealmInfo, error)
}
