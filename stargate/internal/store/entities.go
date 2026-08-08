package store

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// EntityBase is embedded by every soft-deletable persisted entity.
type EntityBase struct {
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

type AccountEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	ActivatedAt *time.Time `gorm:"column:activated_at"`
	AutomatedID *uuid.UUID `gorm:"column:automated_id"`
	IsSuperuser bool       `gorm:"column:is_superuser"`
	Language    string     `gorm:"column:language"`
	Name        string     `gorm:"column:name"`
	Nick        string     `gorm:"column:nick"`
	Region      string     `gorm:"column:region"`
}

func (AccountEntity) TableName() string { return "accounts" }

type PermissionGroupEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	Key string `gorm:"column:key"`
}

func (PermissionGroupEntity) TableName() string { return "permission_groups" }

type AuthClientEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	AccountID   uuid.UUID `gorm:"column:account_id"`
	DeviceID    string    `gorm:"column:device_id"`
	DeviceLabel *string   `gorm:"column:device_label"`
	DeviceName  string    `gorm:"column:device_name"`
	Platform    int       `gorm:"column:platform"`
}

func (AuthClientEntity) TableName() string { return "auth_clients" }

type AuthSessionEntity struct {
	ID              uuid.UUID       `gorm:"column:id;primaryKey"`
	CreatedAt       time.Time       `gorm:"column:created_at"`
	UpdatedAt       time.Time       `gorm:"column:updated_at"`
	DeletedAt       gorm.DeletedAt  `gorm:"column:deleted_at;index"`
	AccountID       uuid.UUID       `gorm:"column:account_id"`
	AppID           *uuid.UUID      `gorm:"column:app_id"`
	Audiences       datatypes.JSON  `gorm:"column:audiences;type:jsonb"`
	ChallengeID     *uuid.UUID      `gorm:"column:challenge_id"`
	ClientID        *uuid.UUID      `gorm:"column:client_id"`
	Epoch           int             `gorm:"column:epoch"`
	ExpiredAt       *time.Time      `gorm:"column:expired_at"`
	IPAddress       *string         `gorm:"column:ip_address"`
	LastGrantedAt   *time.Time      `gorm:"column:last_granted_at"`
	Location        *datatypes.JSON `gorm:"column:location;type:jsonb"`
	ParentSessionID *uuid.UUID      `gorm:"column:parent_session_id"`
	Scopes          datatypes.JSON  `gorm:"column:scopes;type:jsonb"`
	Type            int             `gorm:"column:type"`
	UserAgent       *string         `gorm:"column:user_agent"`
}

func (AuthSessionEntity) TableName() string { return "auth_sessions" }

type APIKeyEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	AccountID uuid.UUID  `gorm:"column:account_id"`
	AppID     *uuid.UUID `gorm:"column:app_id"`
	Label     string     `gorm:"column:label"`
	SessionID uuid.UUID  `gorm:"column:session_id"`
}

func (APIKeyEntity) TableName() string { return "api_keys" }

type AuthFactorEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	AccountID   uuid.UUID       `gorm:"column:account_id"`
	Config      *datatypes.JSON `gorm:"column:config;type:jsonb"`
	EnabledAt   *time.Time      `gorm:"column:enabled_at"`
	ExpiredAt   *time.Time      `gorm:"column:expired_at"`
	Secret      *string         `gorm:"column:secret"`
	Trustworthy int             `gorm:"column:trustworthy"`
	Type        int             `gorm:"column:type"`
}

func (AuthFactorEntity) TableName() string { return "account_auth_factors" }

type ContactEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	AccountID  uuid.UUID  `gorm:"column:account_id"`
	Content    string     `gorm:"column:content"`
	IsPrimary  bool       `gorm:"column:is_primary"`
	IsPublic   bool       `gorm:"column:is_public"`
	Type       int        `gorm:"column:type"`
	VerifiedAt *time.Time `gorm:"column:verified_at"`
}

func (ContactEntity) TableName() string { return "account_contacts" }

type ConnectionEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	AccessToken        *string        `gorm:"column:access_token"`
	AccountID          uuid.UUID      `gorm:"column:account_id"`
	IsPublic           bool           `gorm:"column:is_public"`
	LastUsedAt         *time.Time     `gorm:"column:last_used_at"`
	Meta               datatypes.JSON `gorm:"column:meta;type:jsonb"`
	ProvidedIdentifier string         `gorm:"column:provided_identifier"`
	Provider           string         `gorm:"column:provider"`
	RefreshToken       *string        `gorm:"column:refresh_token"`
}

func (ConnectionEntity) TableName() string { return "account_connections" }

type PasskeyEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	AccountID    uuid.UUID      `gorm:"column:account_id"`
	Credential   datatypes.JSON `gorm:"column:credential;type:jsonb"`
	CredentialID string         `gorm:"column:credential_id"`
	Label        string         `gorm:"column:label"`
}

func (PasskeyEntity) TableName() string { return "account_passkeys" }

type PunishmentEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	AccountID          uuid.UUID       `gorm:"column:account_id"`
	BlockedPermissions *datatypes.JSON `gorm:"column:blocked_permissions;type:jsonb"`
	CreatorID          *uuid.UUID      `gorm:"column:creator_id"`
	ExpiredAt          *time.Time      `gorm:"column:expired_at"`
	Reason             string          `gorm:"column:reason"`
	Type               int             `gorm:"column:type"`
}

func (PunishmentEntity) TableName() string { return "punishments" }

type AuthorizedAppEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	AccountID        uuid.UUID      `gorm:"column:account_id"`
	AppID            uuid.UUID      `gorm:"column:app_id"`
	AppName          *string        `gorm:"column:app_name"`
	AppSlug          *string        `gorm:"column:app_slug"`
	LastAuthorizedAt time.Time      `gorm:"column:last_authorized_at"`
	LastUsedAt       *time.Time     `gorm:"column:last_used_at"`
	Scopes           datatypes.JSON `gorm:"column:scopes;type:jsonb"`
	Type             int            `gorm:"column:type"`
}

func (AuthorizedAppEntity) TableName() string { return "authorized_apps" }

type ActionLogEntity struct {
	ID        uuid.UUID       `gorm:"column:id;primaryKey"`
	CreatedAt time.Time       `gorm:"column:created_at"`
	UpdatedAt time.Time       `gorm:"column:updated_at"`
	DeletedAt gorm.DeletedAt  `gorm:"column:deleted_at;index"`
	AccountID uuid.UUID       `gorm:"column:account_id"`
	Action    string          `gorm:"column:action"`
	IPAddress *string         `gorm:"column:ip_address"`
	Location  *datatypes.JSON `gorm:"column:location;type:jsonb"`
	Meta      datatypes.JSON  `gorm:"column:meta;type:jsonb"`
	SessionID *uuid.UUID      `gorm:"column:session_id"`
	UserAgent *string         `gorm:"column:user_agent"`
}

func (ActionLogEntity) TableName() string { return "action_logs" }

type ChallengeEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	AccountID           *uuid.UUID      `gorm:"column:account_id"`
	ApprovedAt          *time.Time      `gorm:"column:approved_at"`
	ApprovedBySessionID *uuid.UUID      `gorm:"column:approved_by_session_id"`
	Audiences           datatypes.JSON  `gorm:"column:audiences;type:jsonb"`
	BlacklistFactors    datatypes.JSON  `gorm:"column:blacklist_factors;type:jsonb"`
	DeclinedAt          *time.Time      `gorm:"column:declined_at"`
	DeviceID            string          `gorm:"column:device_id"`
	DeviceName          *string         `gorm:"column:device_name"`
	ExpiredAt           *time.Time      `gorm:"column:expired_at"`
	FailedAttempts      int             `gorm:"column:failed_attempts"`
	IPAddress           *string         `gorm:"column:ip_address"`
	Location            *datatypes.JSON `gorm:"column:location;type:jsonb"`
	Nonce               *string         `gorm:"column:nonce"`
	Platform            int             `gorm:"column:platform"`
	Scopes              datatypes.JSON  `gorm:"column:scopes;type:jsonb"`
	StepRemain          int             `gorm:"column:step_remain"`
	StepTotal           int             `gorm:"column:step_total"`
	UserAgent           *string         `gorm:"column:user_agent"`
}

func (ChallengeEntity) TableName() string { return "auth_challenges" }

type E2EEDeviceEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	AccountID    uuid.UUID  `gorm:"column:account_id"`
	DeviceID     string     `gorm:"column:device_id"`
	DeviceLabel  *string    `gorm:"column:device_label"`
	IsRevoked    bool       `gorm:"column:is_revoked"`
	LastBundleAt *time.Time `gorm:"column:last_bundle_at"`
	RevokedAt    *time.Time `gorm:"column:revoked_at"`
}

func (E2EEDeviceEntity) TableName() string { return "e2ee_devices" }

type E2EEKeyBundleEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	AccountID             uuid.UUID       `gorm:"column:account_id"`
	Algorithm             string          `gorm:"column:algorithm"`
	DeviceID              string          `gorm:"column:device_id"`
	IdentityKey           []byte          `gorm:"column:identity_key"`
	Meta                  *datatypes.JSON `gorm:"column:meta;type:jsonb"`
	SignedPreKey          []byte          `gorm:"column:signed_pre_key"`
	SignedPreKeyExpiresAt *time.Time      `gorm:"column:signed_pre_key_expires_at"`
	SignedPreKeyID        *int            `gorm:"column:signed_pre_key_id"`
	SignedPreKeySignature []byte          `gorm:"column:signed_pre_key_signature"`
}

func (E2EEKeyBundleEntity) TableName() string { return "e2ee_key_bundles" }

type E2EEOneTimePreKeyEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	AccountID          uuid.UUID  `gorm:"column:account_id"`
	ClaimedAt          *time.Time `gorm:"column:claimed_at"`
	ClaimedByAccountID *uuid.UUID `gorm:"column:claimed_by_account_id"`
	DeviceID           string     `gorm:"column:device_id"`
	IsClaimed          bool       `gorm:"column:is_claimed"`
	KeyBundleID        uuid.UUID  `gorm:"column:key_bundle_id"`
	KeyID              int        `gorm:"column:key_id"`
	PublicKey          []byte     `gorm:"column:public_key"`
}

func (E2EEOneTimePreKeyEntity) TableName() string { return "e2ee_one_time_pre_keys" }

type E2EESessionEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	AccountAID    uuid.UUID       `gorm:"column:account_a_id"`
	AccountBID    uuid.UUID       `gorm:"column:account_b_id"`
	Hint          *string         `gorm:"column:hint"`
	InitiatedByID uuid.UUID       `gorm:"column:initiated_by_id"`
	LastMessageAt *time.Time      `gorm:"column:last_message_at"`
	Meta          *datatypes.JSON `gorm:"column:meta;type:jsonb"`
}

func (E2EESessionEntity) TableName() string { return "e2ee_sessions" }

type E2EEEnvelopeEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	AckedAt             *time.Time      `gorm:"column:acked_at"`
	Ciphertext          []byte          `gorm:"column:ciphertext"`
	ClientMessageID     *string         `gorm:"column:client_message_id"`
	DeliveredAt         *time.Time      `gorm:"column:delivered_at"`
	DeliveryStatus      int             `gorm:"column:delivery_status"`
	ExpiresAt           *time.Time      `gorm:"column:expires_at"`
	GroupID             *string         `gorm:"column:group_id"`
	Header              []byte          `gorm:"column:header"`
	LegacyAccountScoped bool            `gorm:"column:legacy_account_scoped"`
	Meta                *datatypes.JSON `gorm:"column:meta;type:jsonb"`
	RecipientAccountID  uuid.UUID       `gorm:"column:recipient_account_id"`
	RecipientDeviceID   *string         `gorm:"column:recipient_device_id"`
	RecipientID         uuid.UUID       `gorm:"column:recipient_id"`
	SenderDeviceID      *string         `gorm:"column:sender_device_id"`
	SenderID            uuid.UUID       `gorm:"column:sender_id"`
	Sequence            int64           `gorm:"column:sequence"`
	SessionID           *uuid.UUID      `gorm:"column:session_id"`
	Signature           []byte          `gorm:"column:signature"`
	Type                int             `gorm:"column:type"`
}

func (E2EEEnvelopeEntity) TableName() string { return "e2ee_envelopes" }

type MLSKeyPackageEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	AccountID           uuid.UUID       `gorm:"column:account_id"`
	Ciphersuite         string          `gorm:"column:ciphersuite"`
	ConsumedAt          *time.Time      `gorm:"column:consumed_at"`
	ConsumedByAccountID *uuid.UUID      `gorm:"column:consumed_by_account_id"`
	DeviceID            string          `gorm:"column:device_id"`
	DeviceLabel         *string         `gorm:"column:device_label"`
	IsConsumed          bool            `gorm:"column:is_consumed"`
	KeyPackage          []byte          `gorm:"column:key_package"`
	Meta                *datatypes.JSON `gorm:"column:meta;type:jsonb"`
}

func (MLSKeyPackageEntity) TableName() string { return "mls_key_packages" }

type MLSGroupStateEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	Epoch        int64           `gorm:"column:epoch"`
	GroupInfo    []byte          `gorm:"column:group_info"`
	LastCommitAt *time.Time      `gorm:"column:last_commit_at"`
	Meta         *datatypes.JSON `gorm:"column:meta;type:jsonb"`
	MLSGroupID   string          `gorm:"column:mls_group_id"`
	RatchetTree  []byte          `gorm:"column:ratchet_tree"`
	StateVersion int64           `gorm:"column:state_version"`
}

func (MLSGroupStateEntity) TableName() string { return "mls_group_states" }

type MLSDeviceMembershipEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	AccountID              uuid.UUID  `gorm:"column:account_id"`
	DeviceID               string     `gorm:"column:device_id"`
	JoinedEpoch            int64      `gorm:"column:joined_epoch"`
	LastReshareCompletedAt *time.Time `gorm:"column:last_reshare_completed_at"`
	LastReshareRequiredAt  *time.Time `gorm:"column:last_reshare_required_at"`
	LastSeenEpoch          *int64     `gorm:"column:last_seen_epoch"`
	MLSGroupID             string     `gorm:"column:mls_group_id"`
}

func (MLSDeviceMembershipEntity) TableName() string { return "mls_device_memberships" }

type PermissionGroupMemberEntity struct {
	GroupID uuid.UUID `gorm:"column:group_id;primaryKey"`
	Actor   string    `gorm:"column:actor;primaryKey"`
	EntityBase
	AffectedAt *time.Time `gorm:"column:affected_at"`
	ExpiredAt  *time.Time `gorm:"column:expired_at"`
}

func (PermissionGroupMemberEntity) TableName() string { return "permission_group_members" }

type PermissionNodeEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	Actor      string         `gorm:"column:actor"`
	AffectedAt *time.Time     `gorm:"column:affected_at"`
	ExpiredAt  *time.Time     `gorm:"column:expired_at"`
	GroupID    *uuid.UUID     `gorm:"column:group_id"`
	Key        string         `gorm:"column:key"`
	Type       int            `gorm:"column:type"`
	Value      datatypes.JSON `gorm:"column:value;type:jsonb"`
}

func (PermissionNodeEntity) TableName() string { return "permission_nodes" }

type ProfileEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	AccountID     uuid.UUID       `gorm:"column:account_id"`
	ActiveBadge   *datatypes.JSON `gorm:"column:active_badge;type:jsonb"`
	Background    *datatypes.JSON `gorm:"column:background;type:jsonb"`
	Bio           *string         `gorm:"column:bio"`
	Birthday      *time.Time      `gorm:"column:birthday"`
	Experience    int             `gorm:"column:experience"`
	FirstName     *string         `gorm:"column:first_name"`
	Gender        *string         `gorm:"column:gender"`
	LastName      *string         `gorm:"column:last_name"`
	LastSeenAt    *time.Time      `gorm:"column:last_seen_at"`
	Links         *datatypes.JSON `gorm:"column:links;type:jsonb"`
	Location      *string         `gorm:"column:location"`
	MiddleName    *string         `gorm:"column:middle_name"`
	Picture       *datatypes.JSON `gorm:"column:picture;type:jsonb"`
	Pronouns      *string         `gorm:"column:pronouns"`
	SocialCredits float64         `gorm:"column:social_credits"`
	TimeZone      *string         `gorm:"column:time_zone"`
	UsernameColor *datatypes.JSON `gorm:"column:username_color;type:jsonb"`
	Verification  *datatypes.JSON `gorm:"column:verification;type:jsonb"`
}

func (ProfileEntity) TableName() string { return "account_profiles" }

type RelationshipEntity struct {
	AccountID uuid.UUID `gorm:"column:account_id;primaryKey"`
	RelatedID uuid.UUID `gorm:"column:related_id;primaryKey"`
	EntityBase
	Alias           *string    `gorm:"column:alias"`
	DegradeToStatus *int16     `gorm:"column:degrade_to_status"`
	ExpiredAt       *time.Time `gorm:"column:expired_at"`
	Status          int16      `gorm:"column:status"`
}

func (RelationshipEntity) TableName() string { return "account_relationships" }

type MagicSpellEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	Spell      string         `gorm:"column:spell"`
	Type       int            `gorm:"column:type"`
	ExpiresAt  *time.Time     `gorm:"column:expires_at"`
	AffectedAt *time.Time     `gorm:"column:affected_at"`
	Meta       datatypes.JSON `gorm:"column:meta;type:jsonb;default:{}"`
	AccountID  *uuid.UUID     `gorm:"column:account_id"`
}

func (MagicSpellEntity) TableName() string { return "magic_spells" }

type AffiliationSpellEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	Spell      string         `gorm:"column:spell"`
	Type       int            `gorm:"column:type"`
	ExpiresAt  *time.Time     `gorm:"column:expires_at"`
	AffectedAt *time.Time     `gorm:"column:affected_at"`
	Meta       datatypes.JSON `gorm:"column:meta;type:jsonb;default:{}"`
	AccountID  *uuid.UUID     `gorm:"column:account_id"`
}

func (AffiliationSpellEntity) TableName() string { return "affiliation_spells" }

type AffiliationResultEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	ResourceIdentifier string    `gorm:"column:resource_identifier"`
	SpellID            uuid.UUID `gorm:"column:spell_id"`
}

func (AffiliationResultEntity) TableName() string { return "affiliation_results" }

type SchemaMigrationEntity struct {
	Version   string    `gorm:"column:version;primaryKey"`
	AppliedAt time.Time `gorm:"column:applied_at"`
}

func (SchemaMigrationEntity) TableName() string { return "schema_migrations" }

var allEntityTables = []string{
	"accounts", "permission_groups", "auth_clients", "auth_sessions", "api_keys", "account_auth_factors", "account_contacts", "account_connections", "account_passkeys", "punishments", "authorized_apps", "action_logs", "auth_challenges", "e2ee_devices", "e2ee_key_bundles", "e2ee_one_time_pre_keys", "e2ee_sessions", "e2ee_envelopes", "mls_key_packages", "mls_group_states", "mls_device_memberships", "permission_group_members", "permission_nodes", "account_profiles", "account_relationships", "magic_spells", "affiliation_spells", "affiliation_results",
}
