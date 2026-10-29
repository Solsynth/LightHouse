// Package rewind implements Messager's half of the yearly Rewind aggregation.
// Passport asks every service for one year's highlight document over gRPC and
// stitches the answers together, so the payload keys below are the contract.
package rewind

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/core"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
)

// ServiceID is the key Passport files this document under.
const ServiceID = "messager"

// Factory returns the rewind module factory.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             "app.solian.messager.rewind",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.messager.chat"},
		Capabilities:   []string{"rewind"},
		New:            func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct{ core *core.Services }

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	i.core = host.Core
	gen.RegisterDyRewindServiceServer(host.GRPC, &server{core: host.Core})
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

type server struct {
	gen.UnimplementedDyRewindServiceServer
	core *core.Services
}

// yearWindow mirrors the C# window: [year-1-12-26, year-12-26).
func yearWindow(year int) (time.Time, time.Time) {
	start := time.Date(year-1, time.December, 26, 0, 0, 0, 0, time.UTC)
	end := time.Date(year, time.December, 26, 0, 0, 0, 0, time.UTC)
	return start, end
}

// GetRewindEvent builds the account's Messager highlights for one year.
func (s *server) GetRewindEvent(ctx context.Context, req *gen.DyRequestRewindEvent) (*gen.DyRewindEvent, error) {
	accountID := strings.TrimSpace(req.GetAccountId())
	if accountID == "" {
		return &gen.DyRewindEvent{ServiceId: ServiceID}, nil
	}
	start, end := yearWindow(int(req.GetYear()))

	data := map[string]any{
		"most_messaged_chat":           nil,
		"most_messaged_direct_chat":    nil,
		"most_called_chat":             nil,
		"most_called_chat_top_members": []any{},
		"most_called_accounts":         []any{},
	}
	if s.core != nil && s.core.DB != nil {
		if err := s.collect(ctx, accountID, start, end, data); err != nil {
			return nil, err
		}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return &gen.DyRewindEvent{AccountId: accountID, Data: raw, ServiceId: ServiceID}, nil
}

type roomCount struct {
	RoomID string  `gorm:"column:room_id"`
	Name   *string `gorm:"column:name"`
	Count  int64   `gorm:"column:count"`
}

func (s *server) collect(ctx context.Context, accountID string, start, end time.Time, data map[string]any) error {
	db := s.core.DB.WithContext(ctx)

	// Rooms the account is (or was) a member of.
	member := `SELECT chat_room_id FROM chat_members WHERE account_id = ? AND deleted_at IS NULL`

	mostMessaged := roomCount{}
	if err := db.Raw(
		`SELECT m.chat_room_id::text AS room_id, r.name, count(*) AS count
		 FROM chat_messages m JOIN chat_rooms r ON r.id = m.chat_room_id
		 WHERE m.chat_room_id IN (`+member+`)
		   AND m.created_at >= ? AND m.created_at < ? AND m.deleted_at IS NULL
		 GROUP BY m.chat_room_id, r.name ORDER BY count DESC LIMIT 1`,
		accountID, start, end).Scan(&mostMessaged).Error; err != nil {
		return err
	}
	if mostMessaged.RoomID != "" {
		data["most_messaged_chat"] = map[string]any{
			"chat":           map[string]any{"id": mostMessaged.RoomID, "name": mostMessaged.Name},
			"message_counts": mostMessaged.Count,
		}
	}

	directMost := roomCount{}
	if err := db.Raw(
		`SELECT m.chat_room_id::text AS room_id, r.name, count(*) AS count
		 FROM chat_messages m JOIN chat_rooms r ON r.id = m.chat_room_id
		 WHERE m.chat_room_id IN (`+member+`) AND r.type = 1
		   AND m.created_at >= ? AND m.created_at < ? AND m.deleted_at IS NULL
		 GROUP BY m.chat_room_id, r.name ORDER BY count DESC LIMIT 1`,
		accountID, start, end).Scan(&directMost).Error; err != nil {
		return err
	}
	if directMost.RoomID != "" {
		data["most_messaged_direct_chat"] = map[string]any{
			"chat":           map[string]any{"id": directMost.RoomID, "name": directMost.Name},
			"message_counts": directMost.Count,
		}
	}

	// Calls: total duration per room, in seconds.
	type callCount struct {
		RoomID   string  `gorm:"column:room_id"`
		Name     *string `gorm:"column:name"`
		Duration int64   `gorm:"column:duration"`
	}
	mostCalled := callCount{}
	if err := db.Raw(
		`SELECT c.room_id::text AS room_id, r.name,
		        coalesce(sum(extract(epoch FROM (c.ended_at - c.created_at))), 0)::bigint AS duration
		 FROM chat_realtime_call c JOIN chat_rooms r ON r.id = c.room_id
		 WHERE c.room_id IN (`+member+`)
		   AND c.created_at >= ? AND c.created_at < ? AND c.deleted_at IS NULL
		 GROUP BY c.room_id, r.name ORDER BY duration DESC LIMIT 1`,
		accountID, start, end).Scan(&mostCalled).Error; err != nil {
		return err
	}
	if mostCalled.RoomID != "" {
		data["most_called_chat"] = map[string]any{
			"chat":     map[string]any{"id": mostCalled.RoomID, "name": mostCalled.Name},
			"duration": mostCalled.Duration,
		}

		type memberDuration struct {
			AccountID string  `gorm:"column:account_id"`
			Username  *string `gorm:"column:username"`
			Nick      *string `gorm:"column:nick"`
			Duration  int64   `gorm:"column:duration"`
		}
		var topMembers []memberDuration
		if err := db.Raw(
			`SELECT m.account_id::text AS account_id, m.username, m.nick,
			        coalesce(sum(extract(epoch FROM (c.ended_at - c.created_at))), 0)::bigint AS duration
			 FROM chat_realtime_call c JOIN chat_members m ON m.chat_room_id = c.room_id
			 WHERE c.room_id = ? AND c.created_at >= ? AND c.created_at < ? AND c.deleted_at IS NULL
			 GROUP BY m.account_id, m.username, m.nick ORDER BY duration DESC LIMIT 5`,
			mostCalled.RoomID, start, end).Scan(&topMembers).Error; err != nil {
			return err
		}
		members := make([]map[string]any, 0, len(topMembers))
		for _, member := range topMembers {
			members = append(members, map[string]any{
				"account_id": member.AccountID,
				"username":   member.Username,
				"nick":       member.Nick,
				"duration":   member.Duration,
			})
		}
		data["most_called_chat_top_members"] = members
	}

	// Top direct-message call partners.
	type partner struct {
		AccountID string  `gorm:"column:account_id"`
		Username  *string `gorm:"column:username"`
		Nick      *string `gorm:"column:nick"`
		Duration  int64   `gorm:"column:duration"`
	}
	var partners []partner
	if err := db.Raw(
		`SELECT other.account_id::text AS account_id, other.username, other.nick,
		        coalesce(sum(extract(epoch FROM (c.ended_at - c.created_at))), 0)::bigint AS duration
		 FROM chat_realtime_call c
		 JOIN chat_rooms r ON r.id = c.room_id AND r.type = 1
		 JOIN chat_members other ON other.chat_room_id = c.room_id AND other.account_id <> ?
		 WHERE c.room_id IN (`+member+`)
		   AND c.created_at >= ? AND c.created_at < ? AND c.deleted_at IS NULL AND other.deleted_at IS NULL
		 GROUP BY other.account_id, other.username, other.nick
		 ORDER BY duration DESC LIMIT 3`,
		accountID, accountID, start, end).Scan(&partners).Error; err != nil {
		return err
	}
	accounts := make([]map[string]any, 0, len(partners))
	for _, partner := range partners {
		accounts = append(accounts, map[string]any{
			"account_id": partner.AccountID,
			"username":   partner.Username,
			"nick":       partner.Nick,
			"duration":   partner.Duration,
		})
	}
	data["most_called_accounts"] = accounts
	return nil
}
