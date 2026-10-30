// Package ports defines the interfaces Sphere feature modules use to reach the
// rest of the fleet without hard dependencies. The integrations module (or a
// local provider) publishes implementations through the module PortRegistry;
// a missing port means the dependent feature degrades rather than failing.
package ports

import (
	"context"
	"errors"

	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
)

// ErrUnavailable marks a port whose backend is disabled or not configured.
var ErrUnavailable = errors.New("port provider unavailable")

// Port IDs.
const (
	PortAccounts      = "accounts"
	PortFiles         = "files"
	PortNotify        = "notify"
	PortRealtime      = "realtime"
	PortPayments      = "payments"
	PortSubscriptions = "subscriptions"
	PortDomains       = "domains"
)

// Accounts resolves accounts and relationships from Stargate.
type Accounts interface {
	GetAccount(ctx context.Context, id string) (*gen.DyAccount, error)
	GetAccounts(ctx context.Context, ids []string) ([]*gen.DyAccount, error)
	SearchAccounts(ctx context.Context, query string, take int) ([]*gen.DyAccount, error)
	// Relationships reports the viewer's relationship with another account
	// (friend, blocked, muted, close friend).
	HasRelationship(ctx context.Context, accountID, relatedID string) (bool, error)
}

// Files resolves Drive file references for attachments, avatars and stickers.
type Files interface {
	GetFile(ctx context.Context, id string) (*model.CloudFileReferenceObject, error)
	GetFiles(ctx context.Context, ids []string) (map[string]model.CloudFileReferenceObject, error)
}

// Notify sends push notifications through Ring.
type Notify interface {
	SendPush(ctx context.Context, accountID, title, body string, meta map[string]string) error
	SendPushToAccounts(ctx context.Context, accountIDs []string, title, body string, meta map[string]string) error
}

// Realtime pushes websocket packets through Blade.
type Realtime interface {
	Push(ctx context.Context, accountIDs []string, event string, payload any) error
}

// Payments creates wallet orders for awards, sponsorships and settlements.
type Payments interface {
	// CreateOrder opens a payment order for a product identifier and returns
	// its id. The caller passes the award metadata the payment consumer reads.
	CreateOrder(ctx context.Context, accountID, productIdentifier, amount, currency string, meta map[string]any) (string, error)
}

// Subscriptions reports an account's perk level.
type Subscriptions interface {
	GetPerkSubscription(ctx context.Context, accountID string) (*model.SubscriptionRef, error)
}

// Domains validates outbound URLs against Passport's domain blocklist.
type Domains interface {
	ValidateURL(ctx context.Context, url string) (bool, error)
	IsDomainBlocked(ctx context.Context, domain string) (bool, error)
}

// Additional port IDs for cross-module collaboration inside the service.
const (
	// PortRealms resolves realm snapshots from Passport/Stargate.
	PortRealms = "realms"
	// PortFederation asks the fediverse module to deliver a post activity.
	PortFederation = "federation"
	// PortPostSignals lets other modules record view/interest signals and read
	// the sponsored post without importing the posts module.
	PortPostSignals = "postsignals"
	// PortAds exposes the sponsor read model to the timeline.
	PortAds = "ads"
)

// RealmInfo is the realm snapshot Sphere embeds in responses.
type RealmInfo struct {
	ID           string  `json:"id"`
	Slug         string  `json:"slug"`
	Name         string  `json:"name"`
	Description  *string `json:"description"`
	IsCommunity  bool    `json:"is_community"`
	IsPublic     bool    `json:"is_public"`
	Picture      any     `json:"picture"`
	Background   any     `json:"background"`
	Verification any     `json:"verification"`
	AccountID    string  `json:"account_id"`
	BoostPoints  string  `json:"boost_points"`
	BoostLevel   int     `json:"boost_level"`
}

// Realms resolves realm snapshots.
type Realms interface {
	GetRealm(ctx context.Context, id string) (*RealmInfo, error)
	GetRealmBySlug(ctx context.Context, slug string) (*RealmInfo, error)
}

// Federation publishes post lifecycle activities to the fediverse.
type Federation interface {
	// PublishPost enqueues the ActivityPub activities for a post. event is
	// "created", "updated" or "deleted".
	PublishPost(ctx context.Context, postID string, event string) error
	// Enabled reports whether the post's publisher federates.
	Enabled(ctx context.Context, postID string) (bool, error)
}

// PostSignals lets sibling modules record interaction signals on posts without
// importing the posts module.
type PostSignals interface {
	// IncrementViews records a view; accountID may be empty for anonymous
	// reads. The implementation deduplicates per account/hour.
	IncrementViews(ctx context.Context, postID, accountID string) error
	// RecordInterest bumps an account's interest profile for a reference.
	RecordInterest(ctx context.Context, accountID string, kind int, referenceID string, score float64, signal string) error
	// SponsoredPost returns the post currently placed as a sponsored slot, or
	// nil when none is active.
	SponsoredPost(ctx context.Context) (*model.Post, error)
	// MarkSponsoredShown records that a sponsored post was displayed.
	MarkSponsoredShown(ctx context.Context, postID string) error
}
