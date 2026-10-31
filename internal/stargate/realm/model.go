package realm

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"

	"src.solsynth.dev/sosys/stargate/model"
)

// Wire types for the realm HTTP surface. Field order and names follow
// DysonNetwork.Shared.Models (snake_case, nulls omitted) so an Island client
// parses the same payload it got from Passport.

// Realm is SnRealm. boost_points serializes as a bare number and boost_level /
// resource_identifier are the C# [NotMapped] computed properties, which
// System.Text.Json does emit.
type Realm struct {
	Id                 uuid.UUID                         `json:"id"`
	Slug               string                            `json:"slug"`
	Name               string                            `json:"name"`
	Description        string                            `json:"description"`
	IsCommunity        bool                              `json:"is_community"`
	IsPublic           bool                              `json:"is_public"`
	Picture            *model.SnCloudFileReferenceObject `json:"picture,omitempty"`
	Background         *model.SnCloudFileReferenceObject `json:"background,omitempty"`
	Verification       *model.SnVerificationMark         `json:"verification,omitempty"`
	AccountId          uuid.UUID                         `json:"account_id"`
	BoostPoints        Decimal                           `json:"boost_points"`
	BoostLevel         int                               `json:"boost_level"`
	ResourceIdentifier string                            `json:"resource_identifier"`
	CreatedAt          *model.Time                       `json:"created_at"`
	UpdatedAt          *model.Time                       `json:"updated_at"`
	DeletedAt          *model.Time                       `json:"deleted_at"`
}

// Label is SnRealmLabel. The realm navigation is [JsonIgnore] in C#.
type Label struct {
	Id                 uuid.UUID   `json:"id"`
	RealmId            uuid.UUID   `json:"realm_id"`
	Name               string      `json:"name"`
	Description        *string     `json:"description,omitempty"`
	Color              *string     `json:"color,omitempty"`
	Icon               *string     `json:"icon,omitempty"`
	CreatedByAccountId uuid.UUID   `json:"created_by_account_id"`
	CreatedAt          *model.Time `json:"created_at"`
	UpdatedAt          *model.Time `json:"updated_at"`
	DeletedAt          *model.Time `json:"deleted_at"`
}

// Member is SnRealmMember. Level and leveling_progress are the C# [NotMapped]
// computed properties over the realm XP curve; Account, Realm, Label and
// Status are the hydrated navigations.
type Member struct {
	RealmId          uuid.UUID   `json:"realm_id"`
	AccountId        uuid.UUID   `json:"account_id"`
	Nick             *string     `json:"nick,omitempty"`
	Bio              *string     `json:"bio,omitempty"`
	LabelId          *uuid.UUID  `json:"label_id,omitempty"`
	Experience       int         `json:"experience"`
	Level            int         `json:"level"`
	LevelingProgress float64     `json:"leveling_progress"`
	Role             int         `json:"role"`
	JoinedAt         *model.Time `json:"joined_at,omitempty"`
	LeaveAt          *model.Time `json:"leave_at,omitempty"`
	CreatedAt        *model.Time `json:"created_at"`
	UpdatedAt        *model.Time `json:"updated_at"`
	DeletedAt        *model.Time `json:"deleted_at"`

	// Hydrated navigations.

	Account *model.Account       `json:"account,omitempty"`
	Realm   *Realm               `json:"realm,omitempty"`
	Label   *Label               `json:"label,omitempty"`
	Status  *model.AccountStatus `json:"status,omitempty"`
}

// RolePermission is SnRealmRolePermission.
type RolePermission struct {
	Id               uuid.UUID   `json:"id"`
	RealmId          uuid.UUID   `json:"realm_id"`
	RoleLevel        int         `json:"role_level"`
	CanChat          bool        `json:"can_chat"`
	CanPost          bool        `json:"can_post"`
	CanComment       bool        `json:"can_comment"`
	CanUploadMedia   bool        `json:"can_upload_media"`
	CanModeratePosts bool        `json:"can_moderate_posts"`
	CanModerateChat  bool        `json:"can_moderate_chat"`
	CanManageMembers bool        `json:"can_manage_members"`
	CanManageRealm   bool        `json:"can_manage_realm"`
	CreatedAt        *model.Time `json:"created_at"`
	UpdatedAt        *model.Time `json:"updated_at"`
	DeletedAt        *model.Time `json:"deleted_at"`
}

// UserPermission is SnRealmUserPermission. Nil flags mean "fall through to the
// role permission", and the C# property is nullable for exactly that reason.
type UserPermission struct {
	Id               uuid.UUID   `json:"id"`
	RealmId          uuid.UUID   `json:"realm_id"`
	AccountId        uuid.UUID   `json:"account_id"`
	CanChat          *bool       `json:"can_chat,omitempty"`
	CanPost          *bool       `json:"can_post,omitempty"`
	CanComment       *bool       `json:"can_comment,omitempty"`
	CanUploadMedia   *bool       `json:"can_upload_media,omitempty"`
	CanModeratePosts *bool       `json:"can_moderate_posts,omitempty"`
	CanModerateChat  *bool       `json:"can_moderate_chat,omitempty"`
	CanManageMembers *bool       `json:"can_manage_members,omitempty"`
	CanManageRealm   *bool       `json:"can_manage_realm,omitempty"`
	CreatedAt        *model.Time `json:"created_at"`
	UpdatedAt        *model.Time `json:"updated_at"`
	DeletedAt        *model.Time `json:"deleted_at"`
}

// ExperienceRecord is SnRealmExperienceRecord.
type ExperienceRecord struct {
	Id         uuid.UUID   `json:"id"`
	RealmId    uuid.UUID   `json:"realm_id"`
	AccountId  uuid.UUID   `json:"account_id"`
	ReasonType string      `json:"reason_type"`
	Reason     string      `json:"reason"`
	Delta      int         `json:"delta"`
	CreatedAt  *model.Time `json:"created_at"`
	UpdatedAt  *model.Time `json:"updated_at"`
	DeletedAt  *model.Time `json:"deleted_at"`
}

// BoostStatus is the GET /{slug}/boosts payload.
type BoostStatus struct {
	BoostPoints         Decimal  `json:"boost_points"`
	BoostLevel          int      `json:"boost_level"`
	LabelCap            int      `json:"label_cap"`
	ExpiresAfterDays    int      `json:"expires_after_days"`
	SupportedCurrencies []string `json:"supported_currencies"`
	DefaultCurrency     string   `json:"default_currency"`
}

// BoostResponse is the POST /{slug}/boosts payload.
type BoostResponse struct {
	OrderId  uuid.UUID `json:"order_id"`
	Shares   int       `json:"shares"`
	Currency string    `json:"currency"`
	Amount   Decimal   `json:"amount"`
}

// LeaderboardRow is one GET /{slug}/boosts/leaderboard entry.
type LeaderboardRow struct {
	AccountId     uuid.UUID      `json:"account_id"`
	Account       *model.Account `json:"account"`
	AmountGolds   Decimal        `json:"amount_golds"`
	AmountPoints  Decimal        `json:"amount_points"`
	Shares        Decimal        `json:"shares"`
	Boosts        int64          `json:"boosts"`
	LastBoostedAt *model.Time    `json:"last_boosted_at,omitempty"`
}

// AdminRealmDetail is RealmAdminController.AdminRealmDetail.
type AdminRealmDetail struct {
	Realm                        *Realm `json:"realm"`
	MemberCount                  int64  `json:"member_count"`
	PendingInviteCount           int64  `json:"pending_invite_count"`
	LabelCount                   int64  `json:"label_count"`
	ActiveBoostContributionCount int64  `json:"active_boost_contribution_count"`
}

// ─────────────────────────── converters ───────────────────────────

// RealmFromEntity projects a storage row into the wire shape. The JSON
// columns are decoded lazily; a malformed value is dropped rather than
// failing the whole response, matching how the profile read model behaves.
func RealmFromEntity(entity *RealmEntity) *Realm {
	if entity == nil {
		return nil
	}
	return &Realm{
		Id: entity.ID, Slug: entity.Slug, Name: entity.Name, Description: entity.Description,
		IsCommunity: entity.IsCommunity, IsPublic: entity.IsPublic,
		Picture:            decodeFileRef(entity.Picture),
		Background:         decodeFileRef(entity.Background),
		Verification:       decodeVerification(entity.Verification),
		AccountId:          entity.AccountID,
		BoostPoints:        entity.BoostPoints,
		BoostLevel:         boostLevel(entity.BoostPoints.Decimal),
		ResourceIdentifier: "realm:" + entity.ID.String(),
		CreatedAt:          model.NewTime(entity.CreatedAt),
		UpdatedAt:          model.NewTime(entity.UpdatedAt),
		DeletedAt:          deletedAt(entity.DeletedAt.Time, entity.DeletedAt.Valid),
	}
}

// LabelFromEntity projects a label row into the wire shape.
func LabelFromEntity(entity *RealmLabelEntity) *Label {
	if entity == nil {
		return nil
	}
	return &Label{
		Id: entity.ID, RealmId: entity.RealmID, Name: entity.Name,
		Description: entity.Description, Color: entity.Color, Icon: entity.Icon,
		CreatedByAccountId: entity.CreatedByAccountID,
		CreatedAt:          model.NewTime(entity.CreatedAt),
		UpdatedAt:          model.NewTime(entity.UpdatedAt),
		DeletedAt:          deletedAt(entity.DeletedAt.Time, entity.DeletedAt.Valid),
	}
}

// MemberFromEntity projects a membership row. The Account/Realm/Label/Status
// navigations are filled by the handler that loaded them.
func MemberFromEntity(entity *RealmMemberEntity) *Member {
	if entity == nil {
		return nil
	}
	return &Member{
		RealmId: entity.RealmID, AccountId: entity.AccountID,
		Nick: entity.Nick, Bio: entity.Bio, LabelId: entity.LabelID,
		Experience:       entity.Experience,
		Level:            realmLevelFromExp(entity.Experience),
		LevelingProgress: realmLevelingProgress(entity.Experience),
		Role:             entity.Role,
		JoinedAt:         model.NewTime(timeOrZero(entity.JoinedAt)),
		LeaveAt:          model.NewTime(timeOrZero(entity.LeaveAt)),
		CreatedAt:        model.NewTime(entity.CreatedAt),
		UpdatedAt:        model.NewTime(entity.UpdatedAt),
		DeletedAt:        deletedAt(entity.DeletedAt.Time, entity.DeletedAt.Valid),
		Account:          entity.Account,
	}
}

// RolePermissionFromEntity projects a role permission row.
func RolePermissionFromEntity(entity *RealmRolePermissionEntity) *RolePermission {
	if entity == nil {
		return nil
	}
	return &RolePermission{
		Id: entity.ID, RealmId: entity.RealmID, RoleLevel: entity.RoleLevel,
		CanChat: entity.CanChat, CanPost: entity.CanPost, CanComment: entity.CanComment,
		CanUploadMedia: entity.CanUploadMedia, CanModeratePosts: entity.CanModeratePosts,
		CanModerateChat: entity.CanModerateChat, CanManageMembers: entity.CanManageMembers,
		CanManageRealm: entity.CanManageRealm,
		CreatedAt:      model.NewTime(entity.CreatedAt),
		UpdatedAt:      model.NewTime(entity.UpdatedAt),
		DeletedAt:      deletedAt(entity.DeletedAt.Time, entity.DeletedAt.Valid),
	}
}

// UserPermissionFromEntity projects a user override row.
func UserPermissionFromEntity(entity *RealmUserPermissionEntity) *UserPermission {
	if entity == nil {
		return nil
	}
	return &UserPermission{
		Id: entity.ID, RealmId: entity.RealmID, AccountId: entity.AccountID,
		CanChat: entity.CanChat, CanPost: entity.CanPost, CanComment: entity.CanComment,
		CanUploadMedia: entity.CanUploadMedia, CanModeratePosts: entity.CanModeratePosts,
		CanModerateChat: entity.CanModerateChat, CanManageMembers: entity.CanManageMembers,
		CanManageRealm: entity.CanManageRealm,
		CreatedAt:      model.NewTime(entity.CreatedAt),
		UpdatedAt:      model.NewTime(entity.UpdatedAt),
		DeletedAt:      deletedAt(entity.DeletedAt.Time, entity.DeletedAt.Valid),
	}
}

// ExperienceFromEntity projects an XP record.
func ExperienceFromEntity(entity *RealmExperienceRecordEntity) *ExperienceRecord {
	if entity == nil {
		return nil
	}
	return &ExperienceRecord{
		Id: entity.ID, RealmId: entity.RealmID, AccountId: entity.AccountID,
		ReasonType: entity.ReasonType, Reason: entity.Reason, Delta: entity.Delta,
		CreatedAt: model.NewTime(entity.CreatedAt),
		UpdatedAt: model.NewTime(entity.UpdatedAt),
		DeletedAt: deletedAt(entity.DeletedAt.Time, entity.DeletedAt.Valid),
	}
}

func decodeFileRef(raw *datatypes.JSON) *model.SnCloudFileReferenceObject {
	if raw == nil || len(*raw) == 0 || string(*raw) == "null" {
		return nil
	}
	var ref model.SnCloudFileReferenceObject
	if err := json.Unmarshal(*raw, &ref); err != nil {
		return nil
	}
	return &ref
}

func decodeVerification(raw *datatypes.JSON) *model.SnVerificationMark {
	if raw == nil || len(*raw) == 0 || string(*raw) == "null" {
		return nil
	}
	var mark model.SnVerificationMark
	if err := json.Unmarshal(*raw, &mark); err != nil {
		return nil
	}
	return &mark
}

// deletedAt renders a soft-delete timestamp, nil when the row is live.
func deletedAt(t time.Time, valid bool) *model.Time {
	if !valid || t.IsZero() {
		return nil
	}
	return model.NewTime(t)
}

// timeOrZero dereferences an optional column time; model.NewTime turns the
// zero value back into a JSON null.
func timeOrZero(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

// ModerationLog is SnRealmPostModerationLog.
type ModerationLog struct {
	Id                 uuid.UUID   `json:"id"`
	RealmId            uuid.UUID   `json:"realm_id"`
	PostId             uuid.UUID   `json:"post_id"`
	ModeratorAccountId uuid.UUID   `json:"moderator_account_id"`
	Reason             *string     `json:"reason,omitempty"`
	ModeratedAt        *model.Time `json:"moderated_at,omitempty"`
	CreatedAt          *model.Time `json:"created_at"`
	UpdatedAt          *model.Time `json:"updated_at"`
	DeletedAt          *model.Time `json:"deleted_at"`
}

// ModerationLogFromEntity projects a moderation log row.
func ModerationLogFromEntity(entity *RealmPostModerationLogEntity) ModerationLog {
	if entity == nil {
		return ModerationLog{}
	}
	return ModerationLog{
		Id: entity.ID, RealmId: entity.RealmID, PostId: entity.PostID,
		ModeratorAccountId: entity.ModeratorAccountID, Reason: entity.Reason,
		ModeratedAt: model.NewTime(entity.ModeratedAt),
		CreatedAt:   model.NewTime(entity.CreatedAt),
		UpdatedAt:   model.NewTime(entity.UpdatedAt),
		DeletedAt:   deletedAt(entity.DeletedAt.Time, entity.DeletedAt.Valid),
	}
}
