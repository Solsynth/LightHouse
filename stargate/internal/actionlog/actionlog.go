// Package actionlog writes account action logs (action_logs table),
// mirroring Padlock's ActionLogService.
package actionlog

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// Service inserts action logs.
type Service struct {
	DB *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Service { return &Service{DB: db} }

// Create records an action log row. Meta is stored as jsonb; location is
// optional.
func (s *Service) Create(ctx context.Context, accountID string, action model.ActionLogType, meta map[string]any, userAgent, ipAddress string, location *string, sessionID *string) error {
	if meta == nil {
		meta = map[string]any{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	var locJSON []byte
	if location != nil && *location != "" {
		locJSON = []byte(*location)
	}
	now := time.Now().UTC()
	_, err = s.DB.Exec(ctx, `INSERT INTO action_logs
		(id, action, meta, user_agent, ip_address, location, account_id, session_id, created_at, updated_at)
		VALUES (gen_random_uuid(),$1,$2,$3,$4,$5,$6,$7,$8,$8)`,
		string(action), metaJSON, nullStrOrNil(userAgent), nullStrOrNil(ipAddress), locJSON, accountID, sessionID, now)
	return err
}

func nullStrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
