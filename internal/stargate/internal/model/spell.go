package model

// Spell models mirror DysonNetwork.Shared.Models.MagicSpell.cs. The spell
// word itself is never serialized ([JsonIgnore] in the C# entity).

// MagicSpellType mirrors the MagicSpellType enum (stored as int).
type MagicSpellType int

const (
	MagicSpellTypeAccountActivation   MagicSpellType = 0
	MagicSpellTypeAccountDeactivation MagicSpellType = 1
	MagicSpellTypeAccountRemoval      MagicSpellType = 2
	MagicSpellTypeAuthPasswordReset   MagicSpellType = 3
	MagicSpellTypeContactVerification MagicSpellType = 4
)

// MagicSpell mirrors SnMagicSpell.
type MagicSpell struct {
	Id         string         `json:"id"`
	Spell      string         `json:"-"` // the secret word; never serialized
	Type       MagicSpellType `json:"type"`
	ExpiresAt  *Time          `json:"expires_at,omitempty"`
	AffectedAt *Time          `json:"affected_at,omitempty"`
	Meta       map[string]any `json:"meta"`
	AccountId  string         `json:"account_id,omitempty"`
	Account    *Account       `json:"account,omitempty"` // hydrated on GET
	CreatedAt  *Time          `json:"created_at,omitempty"`
	UpdatedAt  *Time          `json:"updated_at,omitempty"`
	DeletedAt  *Time          `json:"deleted_at,omitempty"`
}

// AffiliationSpellType mirrors the AffiliationSpellType enum (stored as int).
type AffiliationSpellType int

const (
	AffiliationSpellTypeRegistrationInvite AffiliationSpellType = 0
)

// AffiliationSpell mirrors SnAffiliationSpell (registration invites and other
// marketing usage, distinct from the magic spells).
type AffiliationSpell struct {
	Id         string               `json:"id"`
	Spell      string               `json:"-"`
	Type       AffiliationSpellType `json:"type"`
	ExpiresAt  *Time                `json:"expires_at,omitempty"`
	AffectedAt *Time                `json:"affected_at,omitempty"`
	Meta       map[string]any       `json:"meta"`
	AccountId  string               `json:"account_id,omitempty"`
	CreatedAt  *Time                `json:"created_at,omitempty"`
	UpdatedAt  *Time                `json:"updated_at,omitempty"`
	DeletedAt  *Time                `json:"deleted_at,omitempty"`
}

// AffiliationResult mirrors SnAffiliationResult: who used an affiliation spell.
type AffiliationResult struct {
	Id                 string `json:"id"`
	ResourceIdentifier string `json:"resource_identifier"`
	SpellId            string `json:"spell_id"`
	CreatedAt          *Time  `json:"created_at,omitempty"`
	UpdatedAt          *Time  `json:"updated_at,omitempty"`
	DeletedAt          *Time  `json:"deleted_at,omitempty"`
}
