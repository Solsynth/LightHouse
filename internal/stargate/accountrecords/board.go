package accountrecords

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// BoardWidgetValidator validates custom-app board widgets against Develop.
// When a module wires one, replace requests check the widget exists/enabled
// and admin payload updates use the app's widget schema. When nil the store
// still validates the structural contract, matching the "structural only when
// Develop is not configured" fallback.
type BoardWidgetValidator interface {
	ValidateWidget(ctx context.Context, appID, widgetKey string) error
	ValidatePayload(ctx context.Context, appID, widgetKey string, payload map[string]any) (map[string]any, error)
}

// ListBoard returns an account's board ordered by placement.
func (s *Store) ListBoard(ctx context.Context, accountID uuid.UUID) ([]BoardItem, error) {
	var entities []BoardItemEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order(`"order" ASC`).
		Find(&entities).Error; err != nil {
		return nil, err
	}
	items := make([]BoardItem, 0, len(entities))
	for i := range entities {
		items = append(items, boardItemFromEntity(&entities[i]))
	}
	return items, nil
}

// ReplaceBoard validates a submitted layout, preserves custom-app payloads
// across the replace, then hard-deletes and re-inserts the account's rows in
// one transaction. Validation failures are returned as *ValidationError and
// become PASSPORT_BOARD_REPLACE_FAILED responses.
func (s *Store) ReplaceBoard(ctx context.Context, accountID uuid.UUID, items []BoardItemInput) ([]BoardItem, error) {
	materialized := make([]BoardItemInput, len(items))
	copy(materialized, items)

	existing, err := s.customAppItems(ctx, accountID)
	if err != nil {
		return nil, err
	}
	existingByID := make(map[uuid.UUID]BoardItemEntity, len(existing))
	remainingByWidget := make(map[string][]uuid.UUID, len(existing))
	existingPayload := make(map[uuid.UUID]map[string]any, len(existing))
	for _, item := range existing {
		existingByID[item.ID] = item
		payload := payloadFromJSON(item.Payload)
		existingPayload[item.ID] = payload
		key := widgetInstanceKey(item.CustomAppID, item.CustomAppWidgetKey)
		remainingByWidget[key] = append(remainingByWidget[key], item.ID)
	}

	if err := s.validateBoard(ctx, materialized); err != nil {
		return nil, err
	}

	for i := range materialized {
		item := &materialized[i]
		if item.Kind != BoardKindCustomApp {
			if item.Payload == nil {
				item.Payload = map[string]any{}
			}
			continue
		}
		key := widgetInstanceKey(item.CustomAppId, item.CustomAppWidgetKey)
		if item.Id != nil && *item.Id != uuid.Nil {
			if saved, ok := existingByID[*item.Id]; ok &&
				sameUUID(saved.CustomAppID, item.CustomAppId) &&
				sameFold(saved.CustomAppWidgetKey, item.CustomAppWidgetKey) {
				item.Payload = existingPayload[saved.ID]
				remainingByWidget[key] = removeUUID(remainingByWidget[key], saved.ID)
				continue
			}
		}
		if remaining := remainingByWidget[key]; len(remaining) > 0 {
			id := remaining[0]
			item.Payload = existingPayload[id]
			remainingByWidget[key] = remaining[1:]
		} else {
			item.Payload = map[string]any{}
		}
	}

	now := time.Now().UTC()
	entities := make([]BoardItemEntity, 0, len(materialized))
	for i := range materialized {
		item := &materialized[i]
		id := uuid.New()
		if item.Id != nil && *item.Id != uuid.Nil {
			id = *item.Id
		}
		rawPayload, err := jsonFromPayload(item.Payload)
		if err != nil {
			return nil, err
		}
		entities = append(entities, BoardItemEntity{
			ID:                 id,
			AccountID:          accountID,
			Order:              item.Order,
			Kind:               item.Kind,
			WidgetKey:          item.WidgetKey,
			CustomAppID:        item.CustomAppId,
			CustomAppWidgetKey: item.CustomAppWidgetKey,
			IsEnabled:          item.IsEnabled,
			Payload:            datatypes.JSON(rawPayload),
			EntityBase:         EntityBase{CreatedAt: now, UpdatedAt: now},
		})
	}

	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The legacy table has a unique (account_id, order) index and the C#
		// replace path executes a hard delete. Unscoped delete keeps the
		// re-insert from colliding with a soft-deleted row.
		if err := tx.Unscoped().Where("account_id = ?", accountID).Delete(&BoardItemEntity{}).Error; err != nil {
			return err
		}
		if len(entities) == 0 {
			return nil
		}
		return tx.Create(&entities).Error
	})
	if err != nil {
		return nil, err
	}

	result := make([]BoardItem, 0, len(entities))
	for i := range entities {
		result = append(result, boardItemFromEntity(&entities[i]))
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Order < result[j].Order })
	return result, nil
}

// AdminUpdateBoardItemPayload validates and replaces one item's payload. The
// caller is an administrator acting for the account, so custom-app ownership
// checks are intentionally skipped (matching Passport).
func (s *Store) AdminUpdateBoardItemPayload(ctx context.Context, accountID, itemID uuid.UUID, payload map[string]any) (*BoardItem, error) {
	var entity BoardItemEntity
	err := s.DB.WithContext(ctx).
		Where("account_id = ? AND id = ?", accountID, itemID).
		First(&entity).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBoardItemNotFound
		}
		return nil, err
	}

	var normalized map[string]any
	switch entity.Kind {
	case BoardKindPrebuilt:
		normalized, err = ValidateBoardPayload(payload)
		if err != nil {
			return nil, &ValidationError{Message: err.Error()}
		}
	case BoardKindCustomApp:
		if entity.CustomAppID == nil {
			return nil, &ValidationError{Message: "Custom app board item is missing custom_app_id."}
		}
		if s.Widgets != nil {
			normalized, err = s.Widgets.ValidatePayload(ctx, entity.CustomAppID.String(), stringValue(entity.CustomAppWidgetKey), payload)
			if err != nil {
				return nil, &ValidationError{Message: err.Error()}
			}
		} else {
			normalized, err = ValidateBoardPayload(payload)
			if err != nil {
				return nil, &ValidationError{Message: err.Error()}
			}
		}
	default:
		return nil, &ValidationError{Message: fmt.Sprintf("Unsupported board item kind '%d'.", entity.Kind)}
	}

	rawPayload, err := jsonFromPayload(normalized)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if err := s.DB.WithContext(ctx).Model(&BoardItemEntity{}).Where("id = ?", entity.ID).Updates(map[string]any{
		"payload":    datatypes.JSON(rawPayload),
		"updated_at": now,
	}).Error; err != nil {
		return nil, err
	}
	entity.Payload = datatypes.JSON(rawPayload)
	entity.UpdatedAt = now
	item := boardItemFromEntity(&entity)
	return &item, nil
}

// DeleteBoardItem removes one board item from an account.
func (s *Store) DeleteBoardItem(ctx context.Context, accountID, itemID uuid.UUID) error {
	var entity BoardItemEntity
	err := s.DB.WithContext(ctx).Where("account_id = ? AND id = ?", accountID, itemID).First(&entity).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrBoardItemNotFound
		}
		return err
	}
	return s.DB.WithContext(ctx).Delete(&entity).Error
}

// Widgets is the optional Develop validator. It lives on the store so both
// replace and admin payload updates consult the same configuration.
func (s *Store) SetWidgets(validator BoardWidgetValidator) { s.Widgets = validator }

func (s *Store) customAppItems(ctx context.Context, accountID uuid.UUID) ([]BoardItemEntity, error) {
	var entities []BoardItemEntity
	err := s.DB.WithContext(ctx).
		Where("account_id = ? AND kind = ?", accountID, BoardKindCustomApp).
		Order("created_at ASC").
		Find(&entities).Error
	return entities, err
}

func (s *Store) validateBoard(ctx context.Context, items []BoardItemInput) error {
	orders := make(map[int]bool, len(items))
	singletons := make(map[string]bool, len(items))
	for i := range items {
		item := &items[i]
		if orders[item.Order] {
			return &ValidationError{Message: fmt.Sprintf("Duplicate board order '%d' is not allowed.", item.Order)}
		}
		orders[item.Order] = true

		switch item.Kind {
		case BoardKindPrebuilt:
			// Prebuilt payload validation is controlled by the client; just
			// clear custom-app fields so the row stays clean.
			item.CustomAppId = nil
			item.CustomAppWidgetKey = nil
		case BoardKindCustomApp:
			if item.CustomAppId == nil {
				return &ValidationError{Message: "Custom app board widgets require custom_app_id."}
			}
			if strings.TrimSpace(stringValue(item.CustomAppWidgetKey)) == "" {
				return &ValidationError{Message: "Custom app board widgets require custom_app_widget_key."}
			}
			key := widgetInstanceKey(item.CustomAppId, item.CustomAppWidgetKey)
			// Develop is authoritative when configured; its allow_multiple
			// flag is not exposed to this module, so a duplicate instance of
			// the same (app, widget) pair is always rejected.
			if singletons[key] {
				return &ValidationError{Message: fmt.Sprintf(
					"Custom app widget '%s:%s' can only appear once.",
					item.CustomAppId.String(), stringValue(item.CustomAppWidgetKey))}
			}
			singletons[key] = true
			if s.Widgets != nil {
				if err := s.Widgets.ValidateWidget(ctx, item.CustomAppId.String(), stringValue(item.CustomAppWidgetKey)); err != nil {
					return &ValidationError{Message: err.Error()}
				}
			}
		default:
			return &ValidationError{Message: fmt.Sprintf("Unsupported board item kind '%d'.", item.Kind)}
		}
	}
	return nil
}

func boardItemFromEntity(entity *BoardItemEntity) BoardItem {
	return BoardItem{
		Id:                 entity.ID,
		AccountId:          entity.AccountID,
		Order:              entity.Order,
		Kind:               entity.Kind,
		WidgetKey:          entity.WidgetKey,
		CustomAppId:        entity.CustomAppID,
		CustomAppWidgetKey: entity.CustomAppWidgetKey,
		IsEnabled:          entity.IsEnabled,
		Payload:            payloadFromJSON(entity.Payload),
		CreatedAt:          timePtr(entity.CreatedAt),
		UpdatedAt:          timePtr(entity.UpdatedAt),
		DeletedAt:          deletedTime(entity.DeletedAt),
	}
}

func widgetInstanceKey(appID *uuid.UUID, widgetKey *string) string {
	app := ""
	if appID != nil {
		app = appID.String()
	}
	return strings.ToLower(app + ":" + stringValue(widgetKey))
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func sameUUID(left, right *uuid.UUID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameFold(left, right *string) bool {
	return strings.EqualFold(stringValue(left), stringValue(right))
}

func removeUUID(ids []uuid.UUID, target uuid.UUID) []uuid.UUID {
	out := ids[:0]
	for _, id := range ids {
		if id != target {
			out = append(out, id)
		}
	}
	return out
}
