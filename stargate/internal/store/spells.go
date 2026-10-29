package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// Spell storage for the magic-spell + affiliation-spell surface migrated from
// Passport's MagicSpellService / AffiliationSpellService.

func magicSpellFromEntity(entity *MagicSpellEntity) *model.MagicSpell {
	spell := &model.MagicSpell{
		Id:         entity.ID.String(),
		Spell:      entity.Spell,
		Type:       model.MagicSpellType(entity.Type),
		ExpiresAt:  timePtr(entity.ExpiresAt),
		AffectedAt: timePtr(entity.AffectedAt),
		Meta:       map[string]any{},
		CreatedAt:  timePtr(&entity.CreatedAt),
		UpdatedAt:  timePtr(&entity.UpdatedAt),
		DeletedAt:  deletedTime(entity.DeletedAt),
	}
	if entity.AccountID != nil {
		spell.AccountId = entity.AccountID.String()
	}
	_ = decodeJSONValue(entity.Meta, &spell.Meta)
	return spell
}

// CreateMagicSpell inserts a spell (ids and timestamps must be set).
func (s *Store) CreateMagicSpell(ctx context.Context, spell *model.MagicSpell) error {
	metaValue := spell.Meta
	if metaValue == nil {
		metaValue = map[string]any{}
	}
	meta, err := encodeJSON(metaValue)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(spell.Id)
	if err != nil {
		return err
	}
	var accountID *uuid.UUID
	if spell.AccountId != "" {
		parsed, err := uuid.Parse(spell.AccountId)
		if err != nil {
			return err
		}
		accountID = &parsed
	}
	entity := &MagicSpellEntity{
		ID:         id,
		Spell:      spell.Spell,
		Type:       int(spell.Type),
		ExpiresAt:  timeValue(spell.ExpiresAt),
		AffectedAt: timeValue(spell.AffectedAt),
		Meta:       meta,
		AccountID:  accountID,
	}
	if spell.CreatedAt != nil {
		entity.CreatedAt = time.Time(*spell.CreatedAt)
	}
	if spell.UpdatedAt != nil {
		entity.UpdatedAt = time.Time(*spell.UpdatedAt)
	}
	return s.DB.WithContext(ctx).Create(entity).Error
}

// FindLiveMagicSpell returns a non-expired, non-deleted spell of the given
// type for an account (the C# preventRepeat lookup).
func (s *Store) FindLiveMagicSpell(ctx context.Context, accountID string, typ model.MagicSpellType) (*model.MagicSpell, error) {
	var entity MagicSpellEntity
	err := s.DB.WithContext(ctx).
		Where("account_id = ? AND type = ? AND (expires_at IS NULL OR expires_at > ?)", accountID, int(typ), time.Now().UTC()).
		Order("created_at DESC").
		First(&entity).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	return magicSpellFromEntity(&entity), nil
}

// GetMagicSpellByWord loads a spell by its secret word (not deleted).
func (s *Store) GetMagicSpellByWord(ctx context.Context, word string) (*model.MagicSpell, error) {
	var entity MagicSpellEntity
	if err := s.DB.WithContext(ctx).Where("spell = ?", word).First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return magicSpellFromEntity(&entity), nil
}

// ListMagicSpellsByAccount returns an account's spells, newest first (the
// C# admin list: no deleted_at filter, OrderByDescending(CreatedAt)).
func (s *Store) ListMagicSpellsByAccount(ctx context.Context, accountID string) ([]*model.MagicSpell, error) {
	var entities []MagicSpellEntity
	if err := s.DB.WithContext(ctx).Unscoped().
		Where("account_id = ?", accountID).
		Order("created_at DESC").
		Find(&entities).Error; err != nil {
		return nil, err
	}
	spells := make([]*model.MagicSpell, 0, len(entities))
	for i := range entities {
		spells = append(spells, magicSpellFromEntity(&entities[i]))
	}
	return spells, nil
}

// GetMagicSpellByID loads a spell by id (the C# resend lookup has no
// deleted_at filter).
func (s *Store) GetMagicSpellByID(ctx context.Context, id uuid.UUID) (*model.MagicSpell, error) {
	var entity MagicSpellEntity
	if err := s.DB.WithContext(ctx).Unscoped().Where("id = ?", id).First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return magicSpellFromEntity(&entity), nil
}

// GetContactVerificationSpell returns an account's contact-verification
// spell (the C# resend lookup: no deleted_at/expiry filters).
func (s *Store) GetContactVerificationSpell(ctx context.Context, accountID string) (*model.MagicSpell, error) {
	var entity MagicSpellEntity
	err := s.DB.WithContext(ctx).Unscoped().
		Where("type = ? AND account_id = ?", int(model.MagicSpellTypeContactVerification), accountID).
		Order("created_at DESC").
		First(&entity).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	return magicSpellFromEntity(&entity), nil
}

// DeleteMagicSpell hard-deletes a spell (C# db.Remove semantics).
func (s *Store) DeleteMagicSpell(ctx context.Context, id string) error {
	return s.DB.WithContext(ctx).Unscoped().Delete(&MagicSpellEntity{}, "id = ?", id).Error
}

// GetEmailContactForNotify returns an account's email contact for spell
// delivery: primary first, optionally verified only (NotifyMagicSpell's
// remote contact lookup with verifiedOnly: !bypassVerify).
func (s *Store) GetEmailContactForNotify(ctx context.Context, accountID string, verifiedOnly bool) (*model.Contact, error) {
	query := s.DB.WithContext(ctx).
		Where("account_id = ? AND type = ?", accountID, int(model.ContactTypeEmail))
	if verifiedOnly {
		query = query.Where("verified_at IS NOT NULL")
	}
	var entity ContactEntity
	if err := query.Order("is_primary DESC").Order("created_at").First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	contact := contactFromEntity(&entity)
	return &contact, nil
}

// MarkContactVerified sets verified_at on a contact, mirroring
// AccountService.MarkContactMethodVerified (only moves the stamp forward).
func (s *Store) MarkContactVerified(ctx context.Context, accountID, contactID string, verifiedAt time.Time) (bool, error) {
	res := s.DB.WithContext(ctx).Model(&ContactEntity{}).
		Where("account_id = ? AND id = ? AND (verified_at IS NULL OR verified_at < ?)", accountID, contactID, verifiedAt).
		Updates(map[string]any{"verified_at": verifiedAt, "updated_at": verifiedAt})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// ActivateAccountAndGrantVerified sets activated_at and upserts the
// `verified` group membership, mirroring the spell-path combination of
// TestService.TryActivateAfterContactVerification (skip when already
// activated) and Padlock's ActivateAccountAndGrantDefaultPermissions (set
// stamp + grant group + clear permission cache). It reports whether the
// account was newly activated (false when already activated or missing).
func (s *Store) ActivateAccountAndGrantVerified(ctx context.Context, accountID uuid.UUID, activatedAt time.Time) (bool, error) {
	now := time.Now().UTC()
	res := s.DB.WithContext(ctx).Model(&AccountEntity{}).
		Where("id = ? AND activated_at IS NULL", accountID).
		Updates(map[string]any{"activated_at": activatedAt, "updated_at": now})
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		return false, nil
	}
	var group PermissionGroupEntity
	if err := s.DB.WithContext(ctx).Where(`"key" = ?`, "verified").First(&group).Error; err != nil {
		return false, mapNotFound(err)
	}
	err := s.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "group_id"}, {Name: "actor"}},
		DoUpdates: clause.Assignments(map[string]any{"affected_at": nil, "expired_at": nil, "updated_at": now}),
	}).Create(&PermissionGroupMemberEntity{
		GroupID: group.ID,
		Actor:   accountID.String(),
		EntityBase: EntityBase{
			CreatedAt: now,
			UpdatedAt: now,
		},
	}).Error
	if err != nil {
		return false, err
	}
	return true, nil
}

// ResetPasswordFactor replaces an account's password auth factor secret,
// mirroring the C# ResetPasswordFactorAsync on the account service.
func (s *Store) ResetPasswordFactor(ctx context.Context, accountID, passwordHash string) error {
	now := time.Now().UTC()
	return s.DB.WithContext(ctx).Model(&AuthFactorEntity{}).
		Where("account_id = ? AND type = ?", accountID, int(model.AuthFactorTypePassword)).
		Updates(map[string]any{"secret": passwordHash, "updated_at": now}).Error
}

// ─────────────────────────── Affiliation spells ───────────────────────────

func affiliationSpellFromEntity(entity *AffiliationSpellEntity) *model.AffiliationSpell {
	spell := &model.AffiliationSpell{
		Id:         entity.ID.String(),
		Spell:      entity.Spell,
		Type:       model.AffiliationSpellType(entity.Type),
		ExpiresAt:  timePtr(entity.ExpiresAt),
		AffectedAt: timePtr(entity.AffectedAt),
		Meta:       map[string]any{},
		CreatedAt:  timePtr(&entity.CreatedAt),
		UpdatedAt:  timePtr(&entity.UpdatedAt),
		DeletedAt:  deletedTime(entity.DeletedAt),
	}
	if entity.AccountID != nil {
		spell.AccountId = entity.AccountID.String()
	}
	_ = decodeJSONValue(entity.Meta, &spell.Meta)
	return spell
}

// GetAffiliationSpellByWord loads an affiliation spell by its word and type
// (the C# ConsumeRegistrationInvite lookup has no deleted_at filter).
func (s *Store) GetAffiliationSpellByWord(ctx context.Context, word string, typ model.AffiliationSpellType) (*model.AffiliationSpell, error) {
	var entity AffiliationSpellEntity
	if err := s.DB.WithContext(ctx).Unscoped().
		Where("spell = ? AND type = ?", word, int(typ)).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return affiliationSpellFromEntity(&entity), nil
}

// CountAffiliationResults counts the recorded uses of an affiliation spell.
func (s *Store) CountAffiliationResults(ctx context.Context, spellID string) (int, error) {
	var count int64
	if err := s.DB.WithContext(ctx).Model(&AffiliationResultEntity{}).
		Where("spell_id = ?", spellID).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}

// CreateAffiliationResult records one use of an affiliation spell.
func (s *Store) CreateAffiliationResult(ctx context.Context, spellID, resourceIdentifier string) error {
	spell, err := uuid.Parse(spellID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return s.DB.WithContext(ctx).Create(&AffiliationResultEntity{
		ID:                 uuid.New(),
		ResourceIdentifier: resourceIdentifier,
		SpellID:            spell,
		EntityBase: EntityBase{
			CreatedAt: now,
			UpdatedAt: now,
		},
	}).Error
}

// SetAffiliationSpellAffected stamps affected_at (max_usages == 1 case).
func (s *Store) SetAffiliationSpellAffected(ctx context.Context, spellID string, at time.Time) error {
	return s.DB.WithContext(ctx).Unscoped().Model(&AffiliationSpellEntity{}).
		Where("id = ?", spellID).
		Updates(map[string]any{"affected_at": at, "updated_at": at}).Error
}
