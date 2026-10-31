package accountrecords

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/ports"
	"src.solsynth.dev/sosys/stargate/store"
)

// Store persists the account-record tables. It is deliberately usable on its
// own: sibling modules can construct one with NewStore and call AddExperience,
// AddSocialCredit, EffectiveSocialCredit, GrantBadge or ListBadges directly.
type Store struct {
	DB *gorm.DB

	// Profiles applies the denormalized account_profiles fields (experience
	// and active_badge) exactly like the Passport profile_updated events.
	Profiles *store.Store

	// Perks resolves the subscription perk level for the XP multiplier. Nil
	// means no multiplier (AddExperienceWithMultiplier lets callers pass one
	// explicitly instead).
	Perks ports.Perks

	// Widgets optionally validates custom-app board widgets against Develop.
	// Nil keeps structural validation only.
	Widgets BoardWidgetValidator
}

// NewStore wraps a GORM handle. Profile patches share the same connection.
func NewStore(db *gorm.DB) *Store {
	records := &Store{DB: db}
	if db != nil {
		records.Profiles = store.New(db)
	}
	return records
}

// InvalidateCreditCache is the admin cache-invalidation hook. This module
// computes effective credit live from the records and keeps no cache, so it
// is intentionally a no-op; it exists for callers that route the Passport
// admin endpoint through the Store.
func (s *Store) InvalidateCreditCache(context.Context) error { return nil }

// ───────────────────────────── experience ─────────────────────────────

// AddExperience inserts an experience record and patches the profile. The perk
// multiplier comes from the configured ports.Perks provider (1 when absent or
// unavailable). Only positive deltas are multiplied, mirroring
// ExperienceService.AddRecord.
func (s *Store) AddExperience(ctx context.Context, accountID uuid.UUID, reasonType, reason string, delta int64) (*ExperienceRecord, error) {
	multiplier := 1.0
	if s.Perks != nil && s.Perks.Available() {
		subscription, err := s.Perks.Subscription(ctx, accountID.String())
		if err == nil && subscription != nil {
			switch subscription.PerkLevel {
			case 1:
				multiplier = 1.5
			case 2:
				multiplier = 2
			case 3:
				multiplier = 2.5
			}
		}
	}
	return s.AddExperienceWithMultiplier(ctx, accountID, reasonType, reason, delta, multiplier)
}

// AddExperienceWithMultiplier inserts an experience record and applies the
// (already resolved) perk multiplier. The stored Delta is the multiplied value
// and BonusMultiplier keeps the multiplier, exactly like Passport.
func (s *Store) AddExperienceWithMultiplier(ctx context.Context, accountID uuid.UUID, reasonType, reason string, delta int64, multiplier float64) (*ExperienceRecord, error) {
	if multiplier <= 0 {
		multiplier = 1
	}
	now := time.Now().UTC()
	entity := &ExperienceRecordEntity{
		ID:              uuid.New(),
		ReasonType:      reasonType,
		Reason:          reason,
		Delta:           delta,
		BonusMultiplier: 0,
		AccountID:       accountID,
		EntityBase:      EntityBase{CreatedAt: now, UpdatedAt: now},
	}
	if multiplier != 1 {
		entity.BonusMultiplier = multiplier
		if delta > 0 {
			entity.Delta = int64(math.Floor(float64(delta) * multiplier))
		}
	}
	if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
		return nil, err
	}
	if s.Profiles != nil {
		amount := int(entity.Delta)
		if err := s.Profiles.ApplyProfileFieldPatch(ctx, accountID, &store.ProfileFieldPatch{ExperienceDelta: &amount}); err != nil {
			return nil, err
		}
	}
	record := experienceFromEntity(entity)
	return &record, nil
}

// ListExperience pages one account's experience records, newest first.
func (s *Store) ListExperience(ctx context.Context, accountID uuid.UUID, take, offset int) ([]ExperienceRecord, int, error) {
	query := s.DB.WithContext(ctx).Model(&ExperienceRecordEntity{}).Where("account_id = ?", accountID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var entities []ExperienceRecordEntity
	if err := query.Order("created_at DESC").Limit(take).Offset(offset).Find(&entities).Error; err != nil {
		return nil, 0, err
	}
	records := make([]ExperienceRecord, 0, len(entities))
	for i := range entities {
		records = append(records, experienceFromEntity(&entities[i]))
	}
	return records, int(total), nil
}

func experienceFromEntity(entity *ExperienceRecordEntity) ExperienceRecord {
	return ExperienceRecord{
		Id:              entity.ID,
		ReasonType:      entity.ReasonType,
		Reason:          entity.Reason,
		Delta:           entity.Delta,
		BonusMultiplier: entity.BonusMultiplier,
		AccountId:       entity.AccountID,
		CreatedAt:       timePtr(entity.CreatedAt),
		UpdatedAt:       timePtr(entity.UpdatedAt),
		DeletedAt:       deletedTime(entity.DeletedAt),
	}
}

// ───────────────────────────── social credit ─────────────────────────────

// AddSocialCredit inserts a credit record. expiredAt is the optional instant
// the delta starts decaying from; nil means permanent.
func (s *Store) AddSocialCredit(ctx context.Context, accountID uuid.UUID, reasonType, reason string, delta float64, expiredAt *time.Time) (*SocialCreditRecord, error) {
	now := time.Now().UTC()
	entity := &SocialCreditRecordEntity{
		ID:         uuid.New(),
		ReasonType: reasonType,
		Reason:     reason,
		Delta:      delta,
		Status:     SocialCreditActive,
		ExpiredAt:  expiredAt,
		AccountID:  accountID,
		EntityBase: EntityBase{CreatedAt: now, UpdatedAt: now},
	}
	if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
		return nil, err
	}
	record := socialCreditFromEntity(entity)
	return &record, nil
}

// EffectiveSocialCredit computes the live credit: base 100 plus the sum of
// every non-deleted record's prorated delta. An expired record contributes 0;
// a record with an expiry contributes delta * max(0, 1 - elapsed/total).
// This mirrors SnSocialCreditRecord.GetEffectiveDelta.
func (s *Store) EffectiveSocialCredit(ctx context.Context, accountID uuid.UUID) (float64, error) {
	var entities []SocialCreditRecordEntity
	if err := s.DB.WithContext(ctx).Where("account_id = ?", accountID).Find(&entities).Error; err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	total := 100.0
	for i := range entities {
		total += effectiveDelta(&entities[i], now)
	}
	return total, nil
}

func effectiveDelta(entity *SocialCreditRecordEntity, now time.Time) float64 {
	if entity.ExpiredAt != nil && !entity.ExpiredAt.After(now) {
		return 0
	}
	if entity.ExpiredAt == nil {
		return entity.Delta
	}
	totalDuration := entity.ExpiredAt.Sub(entity.CreatedAt)
	if totalDuration == 0 {
		return entity.Delta
	}
	elapsed := now.Sub(entity.CreatedAt)
	remainingRatio := 1 - elapsed.Seconds()/totalDuration.Seconds()
	if remainingRatio < 0 {
		remainingRatio = 0
	}
	return entity.Delta * remainingRatio
}

// ListSocialCreditHistory pages one account's credit records, newest first.
func (s *Store) ListSocialCreditHistory(ctx context.Context, accountID uuid.UUID, take, offset int) ([]SocialCreditRecord, int, error) {
	query := s.DB.WithContext(ctx).Model(&SocialCreditRecordEntity{}).Where("account_id = ?", accountID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var entities []SocialCreditRecordEntity
	if err := query.Order("created_at DESC").Limit(take).Offset(offset).Find(&entities).Error; err != nil {
		return nil, 0, err
	}
	records := make([]SocialCreditRecord, 0, len(entities))
	for i := range entities {
		records = append(records, socialCreditFromEntity(&entities[i]))
	}
	return records, int(total), nil
}

func socialCreditFromEntity(entity *SocialCreditRecordEntity) SocialCreditRecord {
	return SocialCreditRecord{
		Id:         entity.ID,
		ReasonType: entity.ReasonType,
		Reason:     entity.Reason,
		Delta:      entity.Delta,
		Status:     entity.Status,
		ExpiredAt:  optionalTimePtr(entity.ExpiredAt),
		AccountId:  entity.AccountID,
		CreatedAt:  timePtr(entity.CreatedAt),
		UpdatedAt:  timePtr(entity.UpdatedAt),
		DeletedAt:  deletedTime(entity.DeletedAt),
	}
}

// ───────────────────────────── badges ─────────────────────────────

// GrantBadge inserts a badge with the Passport defaults: no activation/expiry
// timestamps and an empty meta object.
func (s *Store) GrantBadge(ctx context.Context, accountID uuid.UUID, badgeType string, label, caption *string, meta map[string]any) (*Badge, error) {
	return s.GrantBadgeWithDetails(ctx, accountID, badgeType, label, caption, meta, nil, nil)
}

// GrantBadgeWithDetails inserts a badge with explicit activation/expiry
// timestamps (the admin grant request).
func (s *Store) GrantBadgeWithDetails(ctx context.Context, accountID uuid.UUID, badgeType string, label, caption *string, meta map[string]any, activatedAt, expiredAt *time.Time) (*Badge, error) {
	rawMeta, err := jsonFromMeta(meta)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	entity := &BadgeEntity{
		ID:          uuid.New(),
		Type:        badgeType,
		Label:       label,
		Caption:     caption,
		Meta:        datatypes.JSON(rawMeta),
		ActivatedAt: activatedAt,
		ExpiredAt:   expiredAt,
		AccountID:   accountID,
		EntityBase:  EntityBase{CreatedAt: now, UpdatedAt: now},
	}
	if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
		return nil, err
	}
	badge := badgeFromEntity(entity)
	return &badge, nil
}

// ListBadges returns an account's badges newest first.
func (s *Store) ListBadges(ctx context.Context, accountID uuid.UUID) ([]Badge, error) {
	var entities []BadgeEntity
	if err := s.DB.WithContext(ctx).Where("account_id = ?", accountID).Order("created_at DESC").Find(&entities).Error; err != nil {
		return nil, err
	}
	badges := make([]Badge, 0, len(entities))
	for i := range entities {
		badges = append(badges, badgeFromEntity(&entities[i]))
	}
	return badges, nil
}

// RevokeBadge soft-deletes a badge and clears the profile's active badge when
// it referenced the revoked row.
func (s *Store) RevokeBadge(ctx context.Context, accountID, badgeID uuid.UUID) error {
	var entity BadgeEntity
	err := s.DB.WithContext(ctx).
		Where("account_id = ? AND id = ?", accountID, badgeID).
		Order("created_at DESC").First(&entity).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrBadgeNotFound
		}
		return err
	}
	if err := s.DB.WithContext(ctx).Delete(&entity).Error; err != nil {
		return err
	}
	if s.Profiles == nil {
		return nil
	}
	profile, err := s.Profiles.GetProfileByAccount(ctx, accountID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if profile.ActiveBadge != nil && activeBadgeID(*profile.ActiveBadge) == entity.ID.String() {
		return s.Profiles.ApplyProfileFieldPatch(ctx, accountID, &store.ProfileFieldPatch{
			HasActiveBadge: true,
			ActiveBadge:    nil,
		})
	}
	return nil
}

// ActivateBadge clears ActivatedAt on the account's other badges, marks the
// target active, and mirrors the reference into the profile.
func (s *Store) ActivateBadge(ctx context.Context, accountID, badgeID uuid.UUID) error {
	now := time.Now().UTC()
	var badge BadgeEntity
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("account_id = ? AND id = ?", accountID, badgeID).
			Order("created_at DESC").First(&badge).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBadgeNotFound
			}
			return err
		}
		if err := tx.Model(&BadgeEntity{}).
			Where("account_id = ? AND id <> ?", accountID, badgeID).
			Update("activated_at", nil).Error; err != nil {
			return err
		}
		badge.ActivatedAt = &now
		return tx.Model(&BadgeEntity{}).Where("id = ?", badge.ID).Updates(map[string]any{
			"activated_at": now,
			"updated_at":   now,
		}).Error
	})
	if err != nil {
		return err
	}
	if s.Profiles != nil {
		return s.Profiles.ApplyProfileFieldPatch(ctx, accountID, &store.ProfileFieldPatch{
			HasActiveBadge: true,
			ActiveBadge:    activeBadgeReference(&badge),
		})
	}
	return nil
}

// activeBadgeReference builds the snake_case SnAccountBadgeRef jsonb blob the
// Island SDK expects (meta is always an object).
func activeBadgeReference(entity *BadgeEntity) map[string]any {
	return map[string]any{
		"id":           entity.ID.String(),
		"type":         entity.Type,
		"label":        entity.Label,
		"caption":      entity.Caption,
		"meta":         metaFromJSON(entity.Meta),
		"activated_at": optionalTimePtr(entity.ActivatedAt),
		"expired_at":   optionalTimePtr(entity.ExpiredAt),
		"account_id":   entity.AccountID.String(),
		"created_at":   timePtr(entity.CreatedAt),
		"updated_at":   timePtr(entity.UpdatedAt),
	}
}

// activeBadgeID reads the id out of the decoded profile.active_badge value.
func activeBadgeID(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		if id, ok := typed["id"].(string); ok {
			return id
		}
		if id, ok := typed["Id"].(string); ok {
			return id
		}
	}
	return ""
}

func badgeFromEntity(entity *BadgeEntity) Badge {
	return Badge{
		Id:          entity.ID,
		Type:        entity.Type,
		Label:       entity.Label,
		Caption:     entity.Caption,
		Meta:        metaFromJSON(entity.Meta),
		ActivatedAt: optionalTimePtr(entity.ActivatedAt),
		ExpiredAt:   optionalTimePtr(entity.ExpiredAt),
		AccountId:   entity.AccountID,
		CreatedAt:   timePtr(entity.CreatedAt),
		UpdatedAt:   timePtr(entity.UpdatedAt),
		DeletedAt:   deletedTime(entity.DeletedAt),
	}
}

// BadgePorts exposes the badge list in the shape ports.Badges expects.
func (s *Store) BadgePorts(ctx context.Context, accountID string) ([]any, error) {
	id, err := uuid.Parse(accountID)
	if err != nil {
		return nil, err
	}
	badges, err := s.ListBadges(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(badges))
	for _, badge := range badges {
		out = append(out, badge)
	}
	return out, nil
}

func deletedTime(value gorm.DeletedAt) *model.Time {
	if !value.Valid {
		return nil
	}
	return model.NewTime(value.Time)
}
