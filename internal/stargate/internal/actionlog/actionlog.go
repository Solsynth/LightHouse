// Package actionlog writes account action logs (action_logs table),
// mirroring Padlock's ActionLogService.
package actionlog

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// ActionLogPublisher emits a freshly stored action log as an
// ActionLogTriggeredEvent. It mirrors the Padlock
// ActionLogService.CreateActionLogAsync publish step: Passport's
// ProgressionService consumes these to advance achievements/quests.
type ActionLogPublisher func(ctx context.Context, actionLogID uuid.UUID, accountID, action string, meta map[string]any, sessionID *string, occurredAt time.Time)

type Service struct {
	DB *gorm.DB
	// Publish, when non-nil, broadcasts a stored action log to the fleet
	// progression consumer after the row is persisted. Set once in main; nil
	// disables publishing (events unavailable/disabled).
	Publish ActionLogPublisher
}

func New(database *gorm.DB) *Service { return &Service{DB: database} }

func (s *Service) Create(ctx context.Context, accountID string, action model.ActionLogType, meta map[string]any, userAgent, ipAddress string, location *string, sessionID *string) error {
	if meta == nil {
		meta = map[string]any{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	var locJSON *datatypes.JSON
	if location != nil && *location != "" {
		encoded, err := json.Marshal(*location)
		if err != nil {
			return err
		}
		value := datatypes.JSON(encoded)
		locJSON = &value
	}
	account, err := uuid.Parse(accountID)
	if err != nil {
		return err
	}
	var session *uuid.UUID
	if sessionID != nil {
		value, err := uuid.Parse(*sessionID)
		if err != nil {
			return err
		}
		session = &value
	}
	metaValue := datatypes.JSON(metaJSON)
	entity := store.ActionLogEntity{
		ID: uuid.New(), AccountID: account, Action: string(action),
		Meta: metaValue, Location: locJSON, UserAgent: nullableString(userAgent),
		IPAddress: nullableString(ipAddress), SessionID: session,
	}
	if err := s.DB.WithContext(ctx).Create(&entity).Error; err != nil {
		return err
	}

	if s.Publish != nil {
		occurredAt := entity.CreatedAt
		if occurredAt.IsZero() {
			occurredAt = time.Now().UTC()
		}
		s.Publish(ctx, entity.ID, accountID, string(action), meta, sessionID, occurredAt.UTC())
	}

	return nil
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
