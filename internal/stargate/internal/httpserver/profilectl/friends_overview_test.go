package profilectl

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"testing"
	"time"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// TestFriendOverviewStatusContract pins the wire contract the Dart SDK
// (solar_network_sdk _SnAccountStatusFromJson) enforces: id, account_id,
// created_at and updated_at are non-nullable and DateTime.parse'd, so a null
// in any of them throws and the client silently drops the whole card
// (FriendsOverviewWidget hides itself on error). The status is synthesized
// server-side, so every field must be populated even when the friend has no
// profile row or has never been seen.
func TestFriendOverviewStatusContract(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	seen := now.Add(-2 * time.Hour)
	accountID := "22222222-2222-2222-2222-222222222222"
	profile := &model.Profile{
		Id:               "11111111-1111-1111-1111-111111111111",
		AccountId:        accountID,
		Experience:       10,
		Level:            1,
		LevelingProgress: 0.1,
		CreatedAt:        model.NewTime(now.Add(-48 * time.Hour)),
		UpdatedAt:        model.NewTime(now.Add(-48 * time.Hour)),
		LastSeenAt:       model.NewTime(seen),
	}

	for _, tc := range []struct {
		name   string
		status any
	}{
		{"hydrated profile", buildOverviewStatus(accountID, profile, now)},
		{"nil profile", buildOverviewStatus(accountID, nil, now)},
		{"profile never seen", buildOverviewStatus(accountID, &model.Profile{
			Id:        profile.Id,
			AccountId: accountID,
			CreatedAt: model.NewTime(now.Add(-72 * time.Hour)),
			UpdatedAt: model.NewTime(now.Add(-72 * time.Hour)),
		}, now)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.status)
			if err != nil {
				t.Fatalf("marshal status: %v", err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("unmarshal status: %v", err)
			}
			for _, key := range []string{"id", "account_id", "created_at", "updated_at", "label"} {
				value, ok := decoded[key]
				if !ok {
					t.Errorf("%s missing from status", key)
					continue
				}
				if value == nil {
					t.Errorf("%s is null; the SDK casts it to a non-nullable type", key)
				}
			}
			if got := decoded["account_id"]; got != accountID {
				t.Errorf("account_id = %v, want %s", got, accountID)
			}
		})
	}

	if got := buildOverviewStatus(accountID, profile, now).(gin.H)["is_online"]; got != false {
		t.Errorf("is_online = %v, want false for a friend last seen 2h ago", got)
	}
	onlineProfile := &model.Profile{
		Id: profile.Id, AccountId: accountID,
		CreatedAt: profile.CreatedAt, UpdatedAt: profile.UpdatedAt,
		LastSeenAt: model.NewTime(now.Add(-time.Minute)),
	}
	if got := buildOverviewStatus(accountID, onlineProfile, now).(gin.H)["is_online"]; got != true {
		t.Errorf("is_online = %v, want true for a friend seen 1m ago", got)
	}
}
