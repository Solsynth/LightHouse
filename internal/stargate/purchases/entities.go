package purchases

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/model"
)

// EntityBase mirrors the C# ModelBase (timestamps plus soft delete).
type EntityBase struct {
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at"`
}

// AffiliationSpellEntity is the legacy `affiliation_spells` table. The core
// spell service owns the registration-consumption path on these same rows, so
// the module maps the table rather than creating a parallel one.
type AffiliationSpellEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	Spell      string         `gorm:"column:spell;size:1024;not null"`
	Type       int            `gorm:"column:type;type:integer"`
	ExpiresAt  *time.Time     `gorm:"column:expires_at"`
	AffectedAt *time.Time     `gorm:"column:affected_at"`
	Meta       datatypes.JSON `gorm:"column:meta;type:jsonb;default:{}"`
	AccountID  *uuid.UUID     `gorm:"column:account_id;type:uuid"`
}

func (AffiliationSpellEntity) TableName() string { return "affiliation_spells" }

// AffiliationResultEntity is `affiliation_results`: one recorded use of a
// spell. The C# controller filters the list by the owning spell's account.
type AffiliationResultEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	ResourceIdentifier string    `gorm:"column:resource_identifier;size:8192;not null"`
	SpellID            uuid.UUID `gorm:"column:spell_id;type:uuid"`
}

func (AffiliationResultEntity) TableName() string { return "affiliation_results" }

// AffiliationSpellPurchaseEntity is `affiliation_spell_purchases` (new in
// Passport, owned by this module).
type AffiliationSpellPurchaseEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	AccountID   uuid.UUID  `gorm:"column:account_id;type:uuid;not null"`
	OrderID     uuid.UUID  `gorm:"column:order_id;type:uuid;not null"`
	Amount      Decimal    `gorm:"column:amount;type:numeric;not null"`
	SpellID     *uuid.UUID `gorm:"column:spell_id;type:uuid"`
	FulfilledAt *time.Time `gorm:"column:fulfilled_at"`
}

func (AffiliationSpellPurchaseEntity) TableName() string { return "affiliation_spell_purchases" }

// NameChangeCardPurchaseEntity is `name_change_card_purchases`, owned by this
// module. One fulfilled, unconsumed row means one available card.
type NameChangeCardPurchaseEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	AccountID   uuid.UUID  `gorm:"column:account_id;type:uuid;not null"`
	OrderID     uuid.UUID  `gorm:"column:order_id;type:uuid;not null"`
	Amount      Decimal    `gorm:"column:amount;type:numeric;not null"`
	FulfilledAt *time.Time `gorm:"column:fulfilled_at"`
	ConsumedAt  *time.Time `gorm:"column:consumed_at"`
	TargetType  *string    `gorm:"column:target_type;size:32"`
	TargetID    *uuid.UUID `gorm:"column:target_id;type:uuid"`
	OldName     *string    `gorm:"column:old_name;size:256"`
	NewName     *string    `gorm:"column:new_name;size:256"`
}

func (NameChangeCardPurchaseEntity) TableName() string { return "name_change_card_purchases" }

// realmRenameEntity maps only the columns the name-change-card realm target
// needs. It is deliberately not registered with AutoMigrate: the realm table
// belongs to the realm module/core schema.
type realmRenameEntity struct {
	ID        uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	AccountID uuid.UUID `gorm:"column:account_id;type:uuid"`
	Slug      string    `gorm:"column:slug"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (realmRenameEntity) TableName() string { return "realms" }

// ── conversions ───────────────────────────────────────────────────────────

func decodeMeta(raw datatypes.JSON) map[string]any {
	meta := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &meta)
	}
	if meta == nil {
		meta = map[string]any{}
	}
	return meta
}

func encodeMeta(meta map[string]any) (datatypes.JSON, error) {
	if meta == nil {
		meta = map[string]any{}
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	return datatypes.JSON(raw), nil
}

func (e *AffiliationSpellEntity) toWire() *AffiliationSpell {
	spell := &AffiliationSpell{
		Id:         e.ID.String(),
		Spell:      e.Spell,
		Type:       e.Type,
		ExpiresAt:  wireTime(e.ExpiresAt),
		AffectedAt: wireTime(e.AffectedAt),
		Meta:       decodeMeta(e.Meta),
		CreatedAt:  model.NewTime(e.CreatedAt),
		UpdatedAt:  model.NewTime(e.UpdatedAt),
	}
	if e.AccountID != nil {
		value := e.AccountID.String()
		spell.AccountId = &value
	}
	if e.DeletedAt.Valid {
		spell.DeletedAt = model.NewTime(e.DeletedAt.Time)
	}
	return spell
}

func (e *AffiliationResultEntity) toWire() *AffiliationResult {
	result := &AffiliationResult{
		Id:                 e.ID.String(),
		ResourceIdentifier: e.ResourceIdentifier,
		SpellId:            e.SpellID.String(),
		CreatedAt:          model.NewTime(e.CreatedAt),
		UpdatedAt:          model.NewTime(e.UpdatedAt),
	}
	if e.DeletedAt.Valid {
		result.DeletedAt = model.NewTime(e.DeletedAt.Time)
	}

	return result
}

func (e *AffiliationSpellPurchaseEntity) toWire() *AffiliationSpellPurchase {
	purchase := &AffiliationSpellPurchase{
		Id:          e.ID.String(),
		AccountId:   e.AccountID.String(),
		OrderId:     e.OrderID.String(),
		Amount:      e.Amount,
		FulfilledAt: wireTime(e.FulfilledAt),
		CreatedAt:   model.NewTime(e.CreatedAt),
		UpdatedAt:   model.NewTime(e.UpdatedAt),
	}
	if e.SpellID != nil {
		value := e.SpellID.String()
		purchase.SpellId = &value
	}
	if e.DeletedAt.Valid {
		purchase.DeletedAt = model.NewTime(e.DeletedAt.Time)
	}
	return purchase
}

func (e *NameChangeCardPurchaseEntity) toWire() *NameChangeCardPurchase {
	purchase := &NameChangeCardPurchase{
		Id:          e.ID.String(),
		AccountId:   e.AccountID.String(),
		OrderId:     e.OrderID.String(),
		Amount:      e.Amount,
		FulfilledAt: wireTime(e.FulfilledAt),
		ConsumedAt:  wireTime(e.ConsumedAt),
		TargetType:  e.TargetType,
		OldName:     e.OldName,
		NewName:     e.NewName,
		CreatedAt:   model.NewTime(e.CreatedAt),
		UpdatedAt:   model.NewTime(e.UpdatedAt),
	}
	if e.TargetID != nil {
		value := e.TargetID.String()
		purchase.TargetId = &value
	}
	if e.DeletedAt.Valid {
		purchase.DeletedAt = model.NewTime(e.DeletedAt.Time)
	}
	return purchase
}

// wireTime converts a nullable entity timestamp for the JSON payload.
func wireTime(value *time.Time) *model.Time {
	if value == nil {
		return nil
	}
	return model.NewTime(*value)
}
