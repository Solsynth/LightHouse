// Package ports defines the interfaces modules use to collaborate without
// hard dependencies. Feature modules consume ports; integration modules (and
// local feature providers) publish them through the module PortRegistry.
package ports

import (
	"context"
	"errors"

	"github.com/shopspring/decimal"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/stargate/model"
)

// ErrUnavailable marks a port whose backend is disabled or not configured.
var ErrUnavailable = errors.New("port provider unavailable")

const (
	PortFiles              module.PortID = "files"
	PortBadges             module.PortID = "badges"
	PortBoards             module.PortID = "boards"
	PortVerification       module.PortID = "verification"
	PortPerks              module.PortID = "perks"
	PortEmail              module.PortID = "email"
	PortPush               module.PortID = "push"
	PortPresence           module.PortID = "presence"
	PortOAuthClients       module.PortID = "oauthClients"
	PortNfcTokens          module.PortID = "nfcTokens"
	PortVerificationSpells module.PortID = "verificationSpells"
	PortPayments           module.PortID = "payments"
	PortOrderEvents        module.PortID = "orderEvents"
)

// Files resolves a stored file reference (profile picture, background).
type Files interface {
	Available() bool
	Resolve(ctx context.Context, fileID string) (*model.SnCloudFileReferenceObject, error)
}

// Badges lists an account's badges.
type Badges interface {
	Available() bool
	List(ctx context.Context, accountID string) ([]any, error)
}

// Boards returns an account's profile board items.
type Boards interface {
	Available() bool
	List(ctx context.Context, accountID string) ([]any, error)
}

// Verification returns an account's verification mark.
type Verification interface {
	Available() bool
	Get(ctx context.Context, accountID string) (*model.SnVerificationMark, error)
}

// Perks returns an account's subscription/perk level.
type Perks interface {
	Available() bool
	Subscription(ctx context.Context, accountID string) (*model.SnSubscriptionReferenceObject, error)
}

// EmailMessage is the transport-neutral email payload.
type EmailMessage struct {
	To      string
	Subject string
	HTML    string
	Text    string
}

// Email sends transactional email.
type Email interface {
	Available() bool
	Send(ctx context.Context, msg EmailMessage) error
}

// Push sends a notification to one account.
type Push interface {
	Available() bool
	SendToUser(ctx context.Context, accountID, title, body string) error
}

// OnlineDevice is the minimal presence shape shared with feature modules.
type OnlineDevice struct {
	AccountID  string
	DeviceID   string
	ClientID   string
	SessionIDs []string
}

// Presence resolves live devices for accounts.
type Presence interface {
	Available() bool
	OnlineDevices(ctx context.Context, accountIDs []string, namespace string) (map[string][]OnlineDevice, error)
}

// OAuthApp is one custom OAuth/OIDC client. It is the merge of the local
// TOML client, the Stargate-owned OAuth app and the Develop custom app, so
// a provider fills only the fields it knows; nil optional fields mean the
// provider has nothing to say about them.
type OAuthApp struct {
	ID           string
	Slug         string
	Name         string
	Status       int
	ClientSecret string
	RedirectURIs []string
	Scopes       []string

	// PostLogoutRedirectURIs are the RFC 9700 RP-initiated logout targets.
	PostLogoutRedirectURIs []string
	RequirePkce            bool
	AllowOfflineAccess     bool
	IsPublicClient         bool

	HomeUri           *string
	PolicyUri         *string
	TermsOfServiceUri *string

	// Cloud-file references the consent screen renders.
	Picture    *model.SnCloudFileReferenceObject
	Background *model.SnCloudFileReferenceObject
}

// OAuthClients resolves custom OAuth/OIDC clients. A provider may serve
// several sources (configuration, a local table, a remote service); it must
// merge them in its own precedence order.
type OAuthClients interface {
	Available() bool
	// Get resolves a client by its ID. Lookup by slug is the caller's job:
	// iterate Slugged for a slug, or a provider may implement Slugged.
	Get(ctx context.Context, id string) (*OAuthApp, error)
	// CheckSecret verifies a presented client secret for an app.
	CheckSecret(ctx context.Context, appID, secret string) (bool, error)
}

// OAuthClientsSlugged is an optional extension a provider implements to
// resolve a client by its human-readable slug without scanning.
type OAuthClientsSlugged interface {
	GetBySlug(ctx context.Context, slug string) (*OAuthApp, error)
}

// NfcTokens validates a physical NFC token for login. tagUID is the optional
// printed tag identifier; uidHex carries the SDM payload.
type NfcTokens interface {
	Available() bool
	Validate(ctx context.Context, tagUID, uidHex string) (accountID, tagID string, err error)
}

// VerificationSpells issues and consumes contact-verification spells.
type VerificationSpells interface {
	Available() bool
	Notify(ctx context.Context, accountID, spellID string) error
}

// OrderRequest opens a wallet order for a product. The caller passes the
// metadata the order's paid hook reads back (the realm boost consumer needs
// realm_id, account_id, shares, currency and amount).
type OrderRequest struct {
	AccountID         string
	Currency          string
	Amount            decimal.Decimal
	ProductIdentifier *string
	AppIdentifier     *string
	Remarks           *string
	Meta              map[string]any
	Reuseable         bool
}

// CreatedOrder is the order id plus the amount the provider actually opened.
type CreatedOrder struct {
	ID     string
	Amount decimal.Decimal
}

// Payments opens wallet orders for modules that sell a product. The local
// wallet module provides it as a feature; app.solian.wallet.remote provides
// it over gRPC when the wallet lives in another deployment.
type Payments interface {
	Available() bool
	CreateOrder(ctx context.Context, req OrderRequest) (*CreatedOrder, error)
}

// OrderEvent is one paid order as a feature module sees it.
type OrderEvent struct {
	OrderID           string
	ProductIdentifier string
	Meta              map[string]any
}

// OrderEvents lets a feature module subscribe to paid orders of the local
// wallet in-process. It is the standalone counterpart of the fleet's
// payment_orders NATS event: when the wallet runs in this binary, consumers
// apply their product without an event bus. Subscribers must be idempotent,
// because the NATS path may deliver the same order when a bus is configured.
type OrderEvents interface {
	Available() bool
	OnOrderPaid(handler func(ctx context.Context, event OrderEvent) error)
}

// Creditor credits a currency to an account's wallet balance. It is the
// progression rewards path; the local wallet provides it as a feature.
type Creditor interface {
	Credit(ctx context.Context, accountID, currency, remarks string, amount int64) error
}

// BadgeGranter extends the badges port with a write. A read-only provider
// (Passport's gRPC surface only lists) implements only Badges.
type BadgeGranter interface {
	Grant(ctx context.Context, accountID, badgeType string, label, caption *string, meta map[string]any) error
}
