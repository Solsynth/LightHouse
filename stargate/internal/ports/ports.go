// Package ports defines the interfaces modules use to collaborate without
// hard dependencies. Feature modules consume ports; integration modules (and
// local feature providers) publish them through the module PortRegistry.
package ports

import (
	"context"
	"errors"

	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/module"
)

// ErrUnavailable marks a port whose backend is disabled or not configured.
var ErrUnavailable = errors.New("port provider unavailable")

const (
	PortFiles              module.PortID = "files"
	PortBadges             module.PortID = "badges"
	PortVerification       module.PortID = "verification"
	PortPerks              module.PortID = "perks"
	PortEmail              module.PortID = "email"
	PortPush               module.PortID = "push"
	PortPresence           module.PortID = "presence"
	PortOAuthClients       module.PortID = "oauthClients"
	PortNfcTokens          module.PortID = "nfcTokens"
	PortVerificationSpells module.PortID = "verificationSpells"
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
	RequirePkce           bool
	AllowOfflineAccess    bool
	IsPublicClient        bool

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
