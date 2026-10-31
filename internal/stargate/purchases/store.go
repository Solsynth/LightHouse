package purchases

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/stargate/model"
)

// ErrNotFound reports a missing spell, purchase or owned realm.
var ErrNotFound = errors.New("purchases: not found")

// ErrRealmNotFound and ErrSlugTaken are the name-change-card realm failures.
var (
	ErrRealmNotFound = errors.New("realm not found or not owned by you")
	ErrSlugTaken     = errors.New("a realm with this slug already exists")
)

// Store is the persistence layer for the affiliation and name-change-card
// domains. It maps the legacy Passport tables.
type Store struct {
	DB *gorm.DB
}

// NewStore builds the persistence layer over the module database handle.
func NewStore(db *gorm.DB) *Store { return &Store{DB: db} }

func mapNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

// ── affiliation spells ────────────────────────────────────────────────────

// GetAffiliationSpellByWord loads a spell by its word. typ nil means any
// type (the C# RecordAffiliationEvent lookup). Deleted rows are included,
// matching the C# service, which has no soft-delete query filter.
func (s *Store) GetAffiliationSpellByWord(ctx context.Context, word string, typ *int) (*AffiliationSpellEntity, error) {
	query := s.DB.WithContext(ctx).Unscoped().Where("spell = ?", word)
	if typ != nil {
		query = query.Where("type = ?", *typ)
	}
	var entity AffiliationSpellEntity
	if err := query.First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &entity, nil
}

// AffiliationSpellTaken mirrors CheckAffiliationSpellHasTaken: it includes
// soft-deleted rows because the C# query does.
func (s *Store) AffiliationSpellTaken(ctx context.Context, word string) (bool, error) {
	var count int64
	if err := s.DB.WithContext(ctx).Unscoped().Model(&AffiliationSpellEntity{}).
		Where("spell = ?", word).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// CreateAffiliationSpell inserts one spell and returns it.
func (s *Store) CreateAffiliationSpell(ctx context.Context, accountID uuid.UUID, word string, typ int, meta map[string]any, expiresAt *time.Time) (*AffiliationSpellEntity, error) {
	rawMeta, err := encodeMeta(meta)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	entity := &AffiliationSpellEntity{
		ID: uuid.New(),
		EntityBase: EntityBase{
			CreatedAt: now,
			UpdatedAt: now,
		},
		Spell:     word,
		Type:      typ,
		ExpiresAt: expiresAt,
		Meta:      rawMeta,
		AccountID: &accountID,
	}
	if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
		return nil, err
	}
	return entity, nil
}

// CountAffiliationResults counts recorded uses of a spell.
func (s *Store) CountAffiliationResults(ctx context.Context, spellID uuid.UUID) (int, error) {
	var count int64
	if err := s.DB.WithContext(ctx).Unscoped().Model(&AffiliationResultEntity{}).
		Where("spell_id = ?", spellID).Count(&count).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}

// CreateAffiliationResult records one use of a spell.
func (s *Store) CreateAffiliationResult(ctx context.Context, spellID uuid.UUID, resource string) (*AffiliationResultEntity, error) {
	now := time.Now().UTC()
	entity := &AffiliationResultEntity{
		ID: uuid.New(),
		EntityBase: EntityBase{
			CreatedAt: now,
			UpdatedAt: now,
		},
		ResourceIdentifier: resource,
		SpellID:            spellID,
	}
	if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
		return nil, err
	}
	return entity, nil
}

// SetAffiliationSpellAffected stamps affected_at (max_usages == 1).
func (s *Store) SetAffiliationSpellAffected(ctx context.Context, spellID uuid.UUID, at time.Time) error {
	return s.DB.WithContext(ctx).Unscoped().Model(&AffiliationSpellEntity{}).
		Where("id = ?", spellID).
		Updates(map[string]any{"affected_at": at, "updated_at": at}).Error
}

// ListAffiliationSpells lists one account's spells with the X-Total count.
// orderBy "usage" orders by the number of results; anything else orders by
// creation time.
func (s *Store) ListAffiliationSpells(ctx context.Context, accountID uuid.UUID, orderBy string, desc bool, take, offset int) ([]AffiliationSpellEntity, int64, error) {
	query := s.DB.WithContext(ctx).Unscoped().Model(&AffiliationSpellEntity{}).
		Where("account_id = ?", accountID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if orderBy == "usage" {
		order := "(SELECT COUNT(*) FROM affiliation_results WHERE affiliation_results.spell_id = affiliation_spells.id)"
		if desc {
			query = query.Order(order + " DESC")
		} else {
			query = query.Order(order + " ASC")
		}
	} else if desc {
		query = query.Order("created_at DESC")
	} else {
		query = query.Order("created_at ASC")
	}
	if take >= 0 {
		query = query.Limit(take)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	var entities []AffiliationSpellEntity
	if err := query.Find(&entities).Error; err != nil {
		return nil, 0, err
	}
	return entities, total, nil
}

// GetAffiliationSpellForAccount resolves a spell owned by the account.
func (s *Store) GetAffiliationSpellForAccount(ctx context.Context, accountID, spellID uuid.UUID) (*AffiliationSpellEntity, error) {
	var entity AffiliationSpellEntity
	if err := s.DB.WithContext(ctx).Unscoped().
		Where("id = ? AND account_id = ?", spellID, accountID).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &entity, nil
}

// DeleteAffiliationSpell removes a spell. The C# controller calls
// DbSet.Remove, which issues a hard delete.
func (s *Store) DeleteAffiliationSpell(ctx context.Context, accountID, spellID uuid.UUID) (bool, error) {
	result := s.DB.WithContext(ctx).Unscoped().
		Where("id = ? AND account_id = ?", spellID, accountID).
		Delete(&AffiliationSpellEntity{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// ListAffiliationResults lists one spell's results with the X-Total count. The
// owner check is done here so another account's spell reads as empty, exactly
// like the C# account-filtered query.
func (s *Store) ListAffiliationResults(ctx context.Context, accountID, spellID uuid.UUID, desc bool, take, offset int) ([]*AffiliationResult, int64, error) {
	if _, err := s.GetAffiliationSpellForAccount(ctx, accountID, spellID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return []*AffiliationResult{}, 0, nil
		}
		return nil, 0, err
	}
	query := s.DB.WithContext(ctx).Unscoped().Model(&AffiliationResultEntity{}).
		Where("spell_id = ?", spellID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if desc {
		query = query.Order("created_at DESC")
	} else {
		query = query.Order("created_at ASC")
	}
	if take >= 0 {
		query = query.Limit(take)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	var entities []AffiliationResultEntity
	if err := query.Find(&entities).Error; err != nil {
		return nil, 0, err
	}
	out := make([]*AffiliationResult, 0, len(entities))
	for i := range entities {
		out = append(out, entities[i].toWire())
	}
	return out, total, nil
}

// ── affiliation purchases ─────────────────────────────────────────────────

// CountAffiliationPurchasesSince counts the account's purchases in the
// purchase window. Deleted purchases do not count (they are never deleted).
func (s *Store) CountAffiliationPurchasesSince(ctx context.Context, accountID uuid.UUID, since time.Time) (int64, error) {
	var count int64
	if err := s.DB.WithContext(ctx).Model(&AffiliationSpellPurchaseEntity{}).
		Where("account_id = ? AND created_at >= ?", accountID, since).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// CreateAffiliationPurchase inserts one purchase row.
func (s *Store) CreateAffiliationPurchase(ctx context.Context, purchase *AffiliationSpellPurchaseEntity) error {
	return s.DB.WithContext(ctx).Create(purchase).Error
}

// FulfillAffiliationPurchase atomically marks a purchase fulfilled and creates
// its registration-invite spell. It returns (purchase, spell, applied, err);
// an already-fulfilled purchase returns applied=false and no spell, which
// makes duplicate order deliveries harmless.
func (s *Store) FulfillAffiliationPurchase(ctx context.Context, purchaseID, orderID uuid.UUID, expectedAccount *uuid.UUID, word string) (*AffiliationSpellPurchaseEntity, *AffiliationSpellEntity, bool, error) {
	var (
		purchase *AffiliationSpellPurchaseEntity
		spell    *AffiliationSpellEntity
	)
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var entity AffiliationSpellPurchaseEntity
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND order_id = ?", purchaseID, orderID).
			First(&entity).Error; err != nil {
			return mapNotFound(err)
		}
		if expectedAccount != nil && entity.AccountID != *expectedAccount {
			return ErrNotFound
		}
		purchase = &entity
		if entity.FulfilledAt != nil {
			return nil
		}
		rawMeta, err := encodeMeta(map[string]any{"max_usages": 1, "skip_tests": true})
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		created := &AffiliationSpellEntity{
			ID: uuid.New(),
			EntityBase: EntityBase{
				CreatedAt: now,
				UpdatedAt: now,
			},
			Spell:     word,
			Type:      AffiliationSpellTypeRegistrationInvite,
			Meta:      rawMeta,
			AccountID: &entity.AccountID,
		}
		if err := tx.Create(created).Error; err != nil {
			return err
		}
		if err := tx.Model(&AffiliationSpellPurchaseEntity{}).
			Where("id = ? AND order_id = ? AND fulfilled_at IS NULL", entity.ID, orderID).
			Updates(map[string]any{
				"spell_id":     created.ID,
				"fulfilled_at": now,
				"updated_at":   now,
			}).Error; err != nil {
			return err
		}
		entity.FulfilledAt = &now
		entity.SpellID = &created.ID
		entity.UpdatedAt = now
		purchase = &entity
		spell = created
		return nil
	})
	if err != nil {
		return nil, nil, false, err
	}
	return purchase, spell, spell != nil, nil
}

// ── name change card purchases ────────────────────────────────────────────

// CountNameChangeCardPurchasesSince counts purchases in the window.
func (s *Store) CountNameChangeCardPurchasesSince(ctx context.Context, accountID uuid.UUID, since time.Time) (int64, error) {
	var count int64
	if err := s.DB.WithContext(ctx).Model(&NameChangeCardPurchaseEntity{}).
		Where("account_id = ? AND created_at >= ?", accountID, since).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// CreateNameChangeCardPurchase inserts one purchase row.
func (s *Store) CreateNameChangeCardPurchase(ctx context.Context, purchase *NameChangeCardPurchaseEntity) error {
	return s.DB.WithContext(ctx).Create(purchase).Error
}

// FulfillNameChangeCardPurchase stamps fulfilled_at. Duplicate deliveries are
// no-ops.
func (s *Store) FulfillNameChangeCardPurchase(ctx context.Context, purchaseID, orderID uuid.UUID, expectedAccount *uuid.UUID) (*NameChangeCardPurchaseEntity, error) {
	var entity NameChangeCardPurchaseEntity
	if err := s.DB.WithContext(ctx).Where("id = ? AND order_id = ?", purchaseID, orderID).First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	if expectedAccount != nil && entity.AccountID != *expectedAccount {
		return nil, ErrNotFound
	}
	if entity.FulfilledAt != nil {
		return &entity, nil
	}
	now := time.Now().UTC()
	if err := s.DB.WithContext(ctx).Model(&NameChangeCardPurchaseEntity{}).
		Where("id = ? AND fulfilled_at IS NULL", entity.ID).
		Updates(map[string]any{"fulfilled_at": now, "updated_at": now}).Error; err != nil {
		return nil, err
	}
	entity.FulfilledAt = &now
	entity.UpdatedAt = now
	return &entity, nil
}

// ListNameChangeCardPurchases lists an account's purchases newest first.
func (s *Store) ListNameChangeCardPurchases(ctx context.Context, accountID uuid.UUID) ([]NameChangeCardPurchaseEntity, error) {
	var entities []NameChangeCardPurchaseEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("created_at DESC").
		Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// LockNameChangeCardPurchase finds the oldest fulfilled, unconsumed purchase
// inside tx and locks it. The caller applies the rename and updates the row;
// returning an error rolls everything back.
func (s *Store) LockNameChangeCardPurchase(ctx context.Context, tx *gorm.DB, accountID uuid.UUID) (*NameChangeCardPurchaseEntity, error) {
	var entity NameChangeCardPurchaseEntity
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("account_id = ? AND fulfilled_at IS NOT NULL AND consumed_at IS NULL", accountID).
		Order("created_at ASC").
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &entity, nil
}

// ConsumeNameChangeCardPurchase stamps the consumption fields. It only matches
// a still-unconsumed fulfilled card, so two requests cannot both consume it.
func (s *Store) ConsumeNameChangeCardPurchase(ctx context.Context, tx *gorm.DB, purchase *NameChangeCardPurchaseEntity, targetType string, targetID *uuid.UUID, oldName, newName string) error {
	now := time.Now().UTC()
	result := tx.WithContext(ctx).Model(&NameChangeCardPurchaseEntity{}).
		Where("id = ? AND fulfilled_at IS NOT NULL AND consumed_at IS NULL", purchase.ID).
		Updates(map[string]any{
			"consumed_at": now,
			"target_type": targetType,
			"target_id":   targetID,
			"old_name":    oldName,
			"new_name":    newName,
			"updated_at":  now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	purchase.ConsumedAt = &now
	purchase.TargetType = &targetType
	purchase.TargetID = targetID
	purchase.OldName = &oldName
	purchase.NewName = &newName
	purchase.UpdatedAt = now
	return nil
}

// ── realm slug rename ─────────────────────────────────────────────────────

// RenameRealmSlugTx renames an owned realm slug inside the caller's
// transaction. The realm table belongs to the realm module; this is a narrow
// query rather than an import of that package.
func (s *Store) RenameRealmSlugTx(ctx context.Context, tx *gorm.DB, accountID, realmID uuid.UUID, slug string) (string, error) {
	var realm realmRenameEntity
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND account_id = ? AND deleted_at IS NULL", realmID, accountID).
		First(&realm).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrRealmNotFound
		}
		return "", err
	}
	var count int64
	if err := tx.WithContext(ctx).Model(&realmRenameEntity{}).
		Where("LOWER(slug) = LOWER(?) AND id <> ? AND deleted_at IS NULL", slug, realmID).
		Count(&count).Error; err != nil {
		return "", err
	}
	if count > 0 {
		return "", ErrSlugTaken
	}
	if err := tx.WithContext(ctx).Model(&realmRenameEntity{}).
		Where("id = ?", realmID).
		Updates(map[string]any{"slug": slug, "updated_at": time.Now().UTC()}).Error; err != nil {
		return "", err
	}
	return realm.Slug, nil
}

// AccountName is the old-name lookup the account rename records.
type AccountStore interface {
	GetAccountByID(ctx context.Context, id uuid.UUID) (*model.Account, error)
	RenameAccount(ctx context.Context, accountID uuid.UUID, newName string) (*model.Account, error)
}
