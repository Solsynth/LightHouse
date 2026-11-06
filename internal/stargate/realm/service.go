package realm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/model"
)

// PerkProvider reports an account's perk level so XP grants can apply the C#
// bonus multiplier and the realm quota can add perk slots. *ports.Perks and
// auth.PerkProvider both satisfy it; a nil provider means "no subscription".
type PerkProvider interface {
	GetPerkSubscription(ctx context.Context, accountID string) (*model.SnSubscriptionReferenceObject, error)
}

// BoostContribution is one paid boost to record.
type BoostContribution struct {
	RealmID       uuid.UUID
	AccountID     uuid.UUID
	Currency      string
	Amount        Decimal
	OrderID       uuid.UUID
	TransactionID uuid.UUID
}

// Service is the realm domain logic: boost state, membership roles, label
// caps, XP grants, quotas and permissions.
type Service struct {
	Store *Store
	Perks PerkProvider
	Log   *slog.Logger
}

// NewService builds the realm service.
func NewService(store *Store, perks PerkProvider, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{Store: store, Perks: perks, Log: log}
}

// ─────────────────────────── boost state ───────────────────────────

// RefreshBoost recomputes a realm's boost points from the active window and
// returns its level. It never writes: Passport recomputed the value in memory
// on every read, so the stored column is only a snapshot, and persisting a
// read would let a plain GET overwrite an imported (or stale) value.
func (s *Service) RefreshBoost(ctx context.Context, entity *RealmEntity) (int, error) {
	points, err := s.Store.ActiveBoostPoints(ctx, []uuid.UUID{entity.ID}, boostCutoff(time.Now()))
	if err != nil {
		return 0, err
	}
	entity.BoostPoints = points[entity.ID]
	return boostLevel(entity.BoostPoints.Decimal), nil
}

// RefreshBoosts recomputes boost points for a batch with one windowed read.
func (s *Service) RefreshBoosts(ctx context.Context, entities []RealmEntity) error {
	if len(entities) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(entities))
	for _, entity := range entities {
		ids = append(ids, entity.ID)
	}
	points, err := s.Store.ActiveBoostPoints(ctx, ids, boostCutoff(time.Now()))
	if err != nil {
		return err
	}
	for i := range entities {
		entities[i].BoostPoints = points[entities[i].ID]
	}
	return nil
}

// BoostLevel is the level a realm's points unlock.
func BoostLevel(entity *RealmEntity) int { return boostLevel(entity.BoostPoints.Decimal) }

// boostCutoff is the start of the active contribution window.
func boostCutoff(now time.Time) time.Time {
	return now.AddDate(0, 0, -ExpirationDays)
}

// ─────────────────────────── roles ───────────────────────────

// IsMemberWithRole mirrors RealmService.IsMemberWithRole: the member must be
// joined and its role at least the highest requested one.
func (s *Service) IsMemberWithRole(ctx context.Context, realmID, accountID uuid.UUID, requiredRoles ...int) (bool, error) {
	if len(requiredRoles) == 0 {
		return false, nil
	}
	required := requiredRoles[0]
	for _, role := range requiredRoles[1:] {
		if role > required {
			required = role
		}
	}
	role, ok, err := s.Store.MemberRole(ctx, realmID, accountID)
	if err != nil {
		return false, err
	}
	return ok && role >= required, nil
}

// RoleDescription renders a role for the action-log payloads.
func RoleDescription(role int) string {
	switch role {
	case RoleOwner:
		return "owner"
	case RoleModerator:
		return "moderator"
	default:
		return "normal"
	}
}

// ─────────────────────────── permissions ───────────────────────────

// HasPermission mirrors RealmService.HasPermission: an active membership is
// required first, then a per-user override wins, then the realm's role
// permission row, then the built-in role defaults.
//
// The membership check must precede the override: an override row outlives
// the member (kick/leave), and evaluating it first let a removed member keep
// whatever the override granted.
func (s *Service) HasPermission(ctx context.Context, realmID, accountID uuid.UUID, permission string) (bool, error) {
	member, err := s.Store.GetActiveMember(ctx, realmID, accountID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	override, err := s.Store.GetUserPermission(ctx, realmID, accountID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return false, err
	}
	if override != nil {
		if value, ok := userPermissionValue(override, permission); ok {
			return value, nil
		}
	}
	roleRow, err := s.Store.GetRolePermission(ctx, realmID, member.Role)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return false, err
	}

	if roleRow != nil {
		value, ok := rolePermissionValue(roleRow, permission)
		if !ok {
			return false, nil
		}
		return value, nil
	}
	return defaultPermission(member.Role, permission), nil
}

func userPermissionValue(row *RealmUserPermissionEntity, permission string) (bool, bool) {
	value := func(flag *bool) (bool, bool) {
		if flag == nil {
			return false, false
		}
		return *flag, true
	}
	switch permission {
	case PermChatSend:
		return value(row.CanChat)
	case PermPostCreate:
		return value(row.CanPost)
	case PermPostComment:
		return value(row.CanComment)
	case PermMediaUpload:
		return value(row.CanUploadMedia)
	case PermPostModerate:
		return value(row.CanModeratePosts)
	case PermChatModerate:
		return value(row.CanModerateChat)
	case PermMembersManage:
		return value(row.CanManageMembers)
	case PermRealmManage:
		return value(row.CanManageRealm)
	default:
		return false, false
	}
}

func rolePermissionValue(row *RealmRolePermissionEntity, permission string) (bool, bool) {
	switch permission {
	case PermChatSend:
		return row.CanChat, true
	case PermPostCreate:
		return row.CanPost, true
	case PermPostComment:
		return row.CanComment, true
	case PermMediaUpload:
		return row.CanUploadMedia, true
	case PermPostModerate:
		return row.CanModeratePosts, true
	case PermChatModerate:
		return row.CanModerateChat, true
	case PermMembersManage:
		return row.CanManageMembers, true
	case PermRealmManage:
		return row.CanManageRealm, true
	default:
		return false, false
	}
}

// defaultPermission mirrors RealmService.GetDefaultPermission.
func defaultPermission(role int, permission string) bool {
	switch role {
	case RoleOwner:
		return true
	case RoleModerator:
		switch permission {
		case PermChatSend, PermPostCreate, PermPostComment, PermMediaUpload,
			PermPostModerate, PermChatModerate, PermMembersManage:
			return true
		default:
			return false
		}
	case RoleNormal:
		switch permission {
		case PermChatSend, PermPostCreate, PermPostComment, PermMediaUpload:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

// ─────────────────────────── experience ───────────────────────────

// Realm XP reasons and their amounts (RealmExperienceService constants).
const (
	TenureDailyXP  = 5
	ChatMessageXP  = 2
	PostCreatedXP  = 20
	BoostShareXP   = 100
	ReasonTenure   = "realm.tenure.daily"
	ReasonChat     = "realm.activity.chat_message"
	ReasonActivity = "realm.activity."
)

// AddExperience grants realm XP with the perk multiplier and the idempotency
// rules RealmExperienceService enforced: a cooldown window for repeated
// events, or an exact (reason_type, reason) check otherwise. It returns nil
// when the grant was skipped.
func (s *Service) AddExperience(ctx context.Context, realmID, accountID uuid.UUID, reasonType, reason string, delta int, cooldown *time.Duration) (*RealmExperienceRecordEntity, error) {
	if delta == 0 {
		return nil, nil
	}
	if _, err := s.Store.GetActiveMember(ctx, realmID, accountID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if cooldown != nil {
		recent, err := s.Store.ExperienceSince(ctx, realmID, accountID, reasonType, time.Now().UTC().Add(-*cooldown))
		if err != nil {
			return nil, err
		}
		if recent {
			return nil, nil
		}
	} else {
		exists, err := s.Store.ExperienceExists(ctx, realmID, accountID, reasonType, reason)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, nil
		}
	}

	finalDelta := delta
	if finalDelta > 0 {
		finalDelta = int(float64(finalDelta) * s.perkMultiplier(ctx, accountID))
	}
	record := &RealmExperienceRecordEntity{
		ID: uuid.New(), RealmID: realmID, AccountID: accountID,
		ReasonType: reasonType, Reason: reason, Delta: finalDelta,
	}
	if err := s.Store.AddExperience(ctx, record); err != nil {
		return nil, err
	}
	return record, nil
}

// perkMultiplier is the C# XP bonus for perk levels 1-3 (1.5x, 2x, 2.5x).
func (s *Service) perkMultiplier(ctx context.Context, accountID uuid.UUID) float64 {
	if s.Perks == nil {
		return 1
	}
	subscription, err := s.Perks.GetPerkSubscription(ctx, accountID.String())
	if err != nil || subscription == nil {
		return 1
	}
	switch subscription.PerkLevel {
	case 1:
		return 1.5
	case 2:
		return 2
	case 3:
		return 2.5
	default:
		return 1
	}
}

// ─────────────────────────── quota ───────────────────────────

// QuotaRecord is one owned realm in the quota response.
type QuotaRecord struct {
	ID   uuid.UUID `json:"id"`
	Slug string    `json:"slug"`
	Name string    `json:"name"`
}

// QuotaResponse mirrors ResourceQuotaResponse<RealmQuotaRecord>.
type QuotaResponse struct {
	Total     int           `json:"total"`
	Used      int           `json:"used"`
	Remaining int           `json:"remaining"`
	Level     int           `json:"level"`
	PerkLevel int           `json:"perk_level"`
	Records   []QuotaRecord `json:"records"`
}

// Quota computes the realm quota for an account (RealmQuotaService).
func (s *Service) Quota(ctx context.Context, accountID uuid.UUID, level, perkLevel int) (QuotaResponse, error) {
	owned, err := s.Store.OwnedRealms(ctx, accountID)
	if err != nil {
		return QuotaResponse{}, err
	}
	total := tieredQuota(level, perkLevel)
	response := QuotaResponse{
		Total: total, Used: len(owned), Remaining: maxInt(total-len(owned), 0),
		Level: level, PerkLevel: perkLevel,
		Records: make([]QuotaRecord, 0, len(owned)),
	}
	for _, entity := range owned {
		response.Records = append(response.Records, QuotaRecord{ID: entity.ID, Slug: entity.Slug, Name: entity.Name})
	}
	return response, nil
}

// RecordBoost applies one paid boost: it deduplicates by order, stores the
// contribution and recomputes the realm's boost state.
func (s *Service) RecordBoost(ctx context.Context, contribution BoostContribution) error {
	exists, err := s.Store.BoostExists(ctx, contribution.OrderID)
	if err != nil {
		return err
	}
	if exists {
		s.Log.Debug("skipping duplicate realm boost order", "order", contribution.OrderID)
		return nil
	}
	realmEntity, err := s.Store.GetRealm(ctx, contribution.RealmID)
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%w: realm %s", ErrNotFound, contribution.RealmID)
	}
	if err != nil {
		return err
	}
	if err := s.Store.AddBoostContribution(ctx, &RealmBoostContributionEntity{
		ID: uuid.New(), RealmID: contribution.RealmID, AccountID: contribution.AccountID,
		Currency: contribution.Currency, Amount: contribution.Amount,
		OrderID: contribution.OrderID, TransactionID: contribution.TransactionID,
	}); err != nil {
		return err
	}
	// A paid boost is the one place the stored snapshot is refreshed.
	if _, err := s.RefreshBoost(ctx, realmEntity); err != nil {
		return err
	}
	return s.Store.SaveRealm(ctx, realmEntity)
}
