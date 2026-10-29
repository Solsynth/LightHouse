package model

// Account models mirror DysonNetwork.Shared.Models.Account.cs. JSON keys are
// snake_case and match what the Island SDK parses (account.g.dart).

// Account is the user account (SnAccount).
type Account struct {
	Id          string    `json:"id"`
	Name        string    `json:"name"`
	Nick        string    `json:"nick"`
	Language    string    `json:"language"`
	Region      string    `json:"region"`
	ActivatedAt *Time     `json:"activated_at,omitempty"`
	IsSuperuser bool      `json:"is_superuser"`
	AutomatedId *string   `json:"automated_id,omitempty"`
	Profile     *Profile  `json:"profile,omitempty"`
	Contacts    []Contact `json:"contacts,omitempty"`
	Badges      []any     `json:"badges,omitempty"`
	// PerkSubscription is hydrated from the wallet service when available.
	PerkSubscription *SnSubscriptionReferenceObject `json:"perk_subscription,omitempty"`
	PerkLevel        int                            `json:"perk_level"`
	CreatedAt        *Time                          `json:"created_at,omitempty"`
	UpdatedAt        *Time                          `json:"updated_at,omitempty"`
	DeletedAt        *Time                          `json:"deleted_at,omitempty"`
}

// Profile is the account profile (SnAccountProfile).
type Profile struct {
	Id                 string                      `json:"id"`
	FirstName          *string                     `json:"first_name,omitempty"`
	MiddleName         *string                     `json:"middle_name,omitempty"`
	LastName           *string                     `json:"last_name,omitempty"`
	Bio                *string                     `json:"bio,omitempty"`
	Gender             *string                     `json:"gender,omitempty"`
	Pronouns           *string                     `json:"pronouns,omitempty"`
	TimeZone           *string                     `json:"time_zone,omitempty"`
	Location           *string                     `json:"location,omitempty"`
	Links              []Link                      `json:"links,omitempty"`
	UsernameColor      *UsernameColor              `json:"username_color,omitempty"`
	Birthday           *Time                       `json:"birthday,omitempty"`
	LastSeenAt         *Time                       `json:"last_seen_at,omitempty"`
	Verification       *SnVerificationMark         `json:"verification,omitempty"`
	ActiveBadge        *any                        `json:"active_badge"`
	Experience         int                         `json:"experience"`
	Level              int                         `json:"level"`
	LevelingProgress   float64                     `json:"leveling_progress"`
	SocialCredits      float64                     `json:"social_credits"`
	SocialCreditsLevel int                         `json:"social_credits_level"`
	Picture            *SnCloudFileReferenceObject `json:"picture,omitempty"`
	Background         *SnCloudFileReferenceObject `json:"background,omitempty"`
	AccountId          string                      `json:"account_id,omitempty"`
	CreatedAt          *Time                       `json:"created_at,omitempty"`
	UpdatedAt          *Time                       `json:"updated_at,omitempty"`
	DeletedAt          *Time                       `json:"deleted_at,omitempty"`
}

// Link mirrors SnProfileLink.
type Link struct {
	Name string `json:"name"`
	Url  string `json:"url"`
}

// Contact mirrors SnAccountContact.
type Contact struct {
	Id         string `json:"id"`
	Type       int    `json:"type"`
	VerifiedAt *Time  `json:"verified_at,omitempty"`
	IsPrimary  bool   `json:"is_primary"`
	IsPublic   bool   `json:"is_public"`
	Content    string `json:"content"`
	AccountId  string `json:"account_id"`
	CreatedAt  *Time  `json:"created_at,omitempty"`
	UpdatedAt  *Time  `json:"updated_at,omitempty"`
	DeletedAt  *Time  `json:"deleted_at,omitempty"`
}

// ContactType mirrors AccountContactType.
type ContactType int

const (
	ContactTypeEmail       ContactType = 0
	ContactTypePhoneNumber ContactType = 1
	ContactTypeAddress     ContactType = 2
)

// AuthFactor mirrors SnAccountAuthFactor. Secret is never serialized.
type AuthFactor struct {
	Id              string         `json:"id"`
	Type            AuthFactorType `json:"type"`
	Trustworthy     int            `json:"trustworthy"`
	EnabledAt       *Time          `json:"enabled_at,omitempty"`
	ExpiredAt       *Time          `json:"expired_at,omitempty"`
	AccountId       string         `json:"account_id"`
	CreatedAt       *Time          `json:"created_at,omitempty"`
	UpdatedAt       *Time          `json:"updated_at,omitempty"`
	DeletedAt       *Time          `json:"deleted_at,omitempty"`
	CreatedResponse map[string]any `json:"created_response,omitempty"`
	// Secret is the bcrypt/TOTP/recovery secret; never serialized.
	Secret string `json:"-"`
	// Config is opaque factor configuration; never serialized.
	Config map[string]any `json:"-"`
}

// AuthFactorType mirrors AccountAuthFactorType.
type AuthFactorType int

const (
	AuthFactorTypePassword     AuthFactorType = 0
	AuthFactorTypeEmailCode    AuthFactorType = 1
	AuthFactorTypeInAppCode    AuthFactorType = 2
	AuthFactorTypeTimedCode    AuthFactorType = 3
	AuthFactorTypePinCode      AuthFactorType = 4
	AuthFactorTypeRecoveryCode AuthFactorType = 5
	AuthFactorTypeNfcToken     AuthFactorType = 6
	AuthFactorTypePasskey      AuthFactorType = 7
	AuthFactorTypeQrLogin      AuthFactorType = 8
)

// Connection mirrors SnAccountConnection.
type Connection struct {
	Id                 string         `json:"id"`
	Provider           string         `json:"provider"`
	ProvidedIdentifier string         `json:"provided_identifier"`
	Meta               map[string]any `json:"meta,omitempty"`
	LastUsedAt         *Time          `json:"last_used_at,omitempty"`
	IsPublic           bool           `json:"is_public"`
	AccountId          string         `json:"account_id"`
	CreatedAt          *Time          `json:"created_at,omitempty"`
	UpdatedAt          *Time          `json:"updated_at,omitempty"`
	DeletedAt          *Time          `json:"deleted_at,omitempty"`
	// RegisteredAt is set when this connection created the account (OIDC
	// registration); NULL for connections linked after account creation.
	// Registration connections cannot be removed by the account owner.
	RegisteredAt *Time `json:"registered_at,omitempty"`
	// AccessToken/RefreshToken are never serialized.
	AccessToken  string `json:"-"`
	RefreshToken string `json:"-"`
}

// Passkey mirrors SnAccountPasskey (Padlock Models).
type Passkey struct {
	Id           string `json:"id"`
	AccountId    string `json:"account_id"`
	Label        string `json:"label"`
	CredentialId string `json:"-"`
	Credential   string `json:"-"`
	CreatedAt    *Time  `json:"created_at,omitempty"`
	UpdatedAt    *Time  `json:"updated_at,omitempty"`
	DeletedAt    *Time  `json:"deleted_at,omitempty"`
}

// PasskeyCredential mirrors AccountService.PasskeyCredential (Padlock): the
// JSON shape stored in account_passkeys.credential. The C# side serializes it
// with System.Text.Json (PascalCase names; byte slices as base64), so Stargate
// must store exactly this shape for the assertion verifier and for DB rows to
// stay readable by Padlock.
type PasskeyCredential struct {
	CredentialId string `json:"CredentialId"`
	PublicKeyX   []byte `json:"PublicKeyX"`
	PublicKeyY   []byte `json:"PublicKeyY"`
	Counter      uint64 `json:"Counter"`
}

// Punishment mirrors SnAccountPunishment (Padlock Models).
type Punishment struct {
	Id                 string         `json:"id"`
	Reason             string         `json:"reason"`
	ExpiredAt          *Time          `json:"expired_at,omitempty"`
	Type               PunishmentType `json:"type"`
	BlockedPermissions []string       `json:"blocked_permissions,omitempty"`
	AccountId          string         `json:"account_id"`
	CreatorId          *string        `json:"creator_id,omitempty"`
	CreatedAt          *Time          `json:"created_at,omitempty"`
	UpdatedAt          *Time          `json:"updated_at,omitempty"`
	DeletedAt          *Time          `json:"deleted_at,omitempty"`
}

// PunishmentType mirrors PunishmentType.
type PunishmentType int

const (
	PunishmentPermissionModification PunishmentType = 0
	PunishmentBlockLogin             PunishmentType = 1
	PunishmentDisableAccount         PunishmentType = 2
	PunishmentStrike                 PunishmentType = 3
)
