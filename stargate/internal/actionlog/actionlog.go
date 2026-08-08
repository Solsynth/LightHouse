// Package actionlog writes account action logs (action_logs table),
// mirroring Padlock's ActionLogService.
package actionlog

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

type Service struct {
	DB *gorm.DB
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
	return s.DB.WithContext(ctx).Create(&store.ActionLogEntity{
		ID: uuid.New(), AccountID: account, Action: string(action),
		Meta: metaValue, Location: locJSON, UserAgent: nullableString(userAgent),
		IPAddress: nullableString(ipAddress), SessionID: session,
	}).Error
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
