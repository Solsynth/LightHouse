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

// OAuthApp is the minimal custom OAuth client shape.
type OAuthApp struct {
	ID           string
	Slug         string
	Name         string
	ClientSecret string
	Status       int
	RedirectURIs []string
	Scopes       []string
}

// OAuthClients resolves custom OAuth/OIDC clients.
type OAuthClients interface {
	Available() bool
	Get(ctx context.Context, id string) (*OAuthApp, error)
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
