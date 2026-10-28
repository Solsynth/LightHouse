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
