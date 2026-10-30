// Package realm is the ported Passport realm domain: communities with
// membership, roles, per-role and per-user permission overrides, labels,
// paid boosts, member experience and post-moderation logs.
//
// Entities keep the legacy Passport table and column names, so an existing
// dyson_passport database can be pointed at this module without a data copy
// (see Import for the case where it cannot).
package realm

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/model"
)

// EntityBase mirrors the C# ModelBase (timestamps plus soft delete). It is
// local rather than store.EntityBase because the realm indexes are declared by
// the module's schema hooks, not by struct tags.
type EntityBase struct {
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at"`
}

// RealmEntity is `realms` (SnRealm).
type RealmEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	Slug         string          `gorm:"column:slug;size:1024;not null"`
	Name         string          `gorm:"column:name;size:1024;not null"`
	Description  string          `gorm:"column:description;size:4096;not null"`
	IsCommunity  bool            `gorm:"column:is_community"`
	IsPublic     bool            `gorm:"column:is_public"`
	Picture      *datatypes.JSON `gorm:"column:picture;type:jsonb"`
	Background   *datatypes.JSON `gorm:"column:background;type:jsonb"`
	Verification *datatypes.JSON `gorm:"column:verification;type:jsonb"`
	AccountID    uuid.UUID       `gorm:"column:account_id"`
	BoostPoints  Decimal         `gorm:"column:boost_points;type:numeric;not null"`
}

func (RealmEntity) TableName() string { return "realms" }

// RealmMemberEntity is `realm_members`. The primary key is (realm_id,
// account_id); a membership row survives a leave so a rejoin reuses it.
type RealmMemberEntity struct {
	RealmID   uuid.UUID `gorm:"column:realm_id;primaryKey"`
	AccountID uuid.UUID `gorm:"column:account_id;primaryKey"`
	EntityBase
	Nick       *string    `gorm:"column:nick;size:1024"`
	Bio        *string    `gorm:"column:bio;size:4096"`
	LabelID    *uuid.UUID `gorm:"column:label_id"`
	Experience int        `gorm:"column:experience"`
	Role       int        `gorm:"column:role"`
	JoinedAt   *time.Time `gorm:"column:joined_at"`
	LeaveAt    *time.Time `gorm:"column:leave_at"`

	// Account and Realm are hydrated payloads for the member listings; they
	// are not columns. Passport filled its [NotMapped] navigations the same
	// way.
	Account *model.Account `gorm:"-"`
	Realm   *RealmEntity   `gorm:"-"`
}

func (RealmMemberEntity) TableName() string { return "realm_members" }

// RealmLabelEntity is `realm_labels`.
type RealmLabelEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	RealmID            uuid.UUID `gorm:"column:realm_id"`
	Name               string    `gorm:"column:name;size:1024;not null"`
	Description        *string   `gorm:"column:description;size:4096"`
	Color              *string   `gorm:"column:color;size:64"`
	Icon               *string   `gorm:"column:icon;size:256"`
	CreatedByAccountID uuid.UUID `gorm:"column:created_by_account_id"`
}

func (RealmLabelEntity) TableName() string { return "realm_labels" }

// RealmBoostContributionEntity is `realm_boost_contributions`: one paid boost.
// Shares are derived from (currency, amount); a contribution counts toward the
// realm's boost level for 30 days.
type RealmBoostContributionEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	RealmID       uuid.UUID `gorm:"column:realm_id"`
	AccountID     uuid.UUID `gorm:"column:account_id"`
	Currency      string    `gorm:"column:currency;size:128;not null"`
	Amount        Decimal   `gorm:"column:amount;type:numeric;not null"`
	OrderID       uuid.UUID `gorm:"column:order_id"`
	TransactionID uuid.UUID `gorm:"column:transaction_id"`
}

func (RealmBoostContributionEntity) TableName() string { return "realm_boost_contributions" }

// RealmExperienceRecordEntity is `realm_experience_records`: one realm XP
// grant. (reason_type, reason) is the idempotency key for non-cooldown grants.
type RealmExperienceRecordEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	RealmID    uuid.UUID `gorm:"column:realm_id"`
	AccountID  uuid.UUID `gorm:"column:account_id"`
	ReasonType string    `gorm:"column:reason_type;size:1024;not null"`
	Reason     string    `gorm:"column:reason;size:1024;not null"`
	Delta      int       `gorm:"column:delta"`
}

func (RealmExperienceRecordEntity) TableName() string { return "realm_experience_records" }

// RealmRolePermissionEntity is `realm_role_permissions`: the permission set
// applied to every member of one role level.
type RealmRolePermissionEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	RealmID          uuid.UUID `gorm:"column:realm_id"`
	RoleLevel        int       `gorm:"column:role_level"`
	CanChat          bool      `gorm:"column:can_chat"`
	CanPost          bool      `gorm:"column:can_post"`
	CanComment       bool      `gorm:"column:can_comment"`
	CanUploadMedia   bool      `gorm:"column:can_upload_media"`
	CanModeratePosts bool      `gorm:"column:can_moderate_posts"`
	CanModerateChat  bool      `gorm:"column:can_moderate_chat"`
	CanManageMembers bool      `gorm:"column:can_manage_members"`
	CanManageRealm   bool      `gorm:"column:can_manage_realm"`
}

func (RealmRolePermissionEntity) TableName() string { return "realm_role_permissions" }

// RealmUserPermissionEntity is `realm_user_permissions`: per-account
// overrides. A nil flag falls through to the role permission.
type RealmUserPermissionEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	RealmID          uuid.UUID `gorm:"column:realm_id"`
	AccountID        uuid.UUID `gorm:"column:account_id"`
	CanChat          *bool     `gorm:"column:can_chat"`
	CanPost          *bool     `gorm:"column:can_post"`
	CanComment       *bool     `gorm:"column:can_comment"`
	CanUploadMedia   *bool     `gorm:"column:can_upload_media"`
	CanModeratePosts *bool     `gorm:"column:can_moderate_posts"`
	CanModerateChat  *bool     `gorm:"column:can_moderate_chat"`
	CanManageMembers *bool     `gorm:"column:can_manage_members"`
	CanManageRealm   *bool     `gorm:"column:can_manage_realm"`
}

func (RealmUserPermissionEntity) TableName() string { return "realm_user_permissions" }

// RealmPostModerationLogEntity is `realm_post_moderation_logs`.
type RealmPostModerationLogEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
	EntityBase
	RealmID            uuid.UUID `gorm:"column:realm_id"`
	PostID             uuid.UUID `gorm:"column:post_id"`
	ModeratorAccountID uuid.UUID `gorm:"column:moderator_account_id"`
	Reason             *string   `gorm:"column:reason;size:4096"`
	ModeratedAt        time.Time `gorm:"column:moderated_at"`
}

func (RealmPostModerationLogEntity) TableName() string { return "realm_post_moderation_logs" }
