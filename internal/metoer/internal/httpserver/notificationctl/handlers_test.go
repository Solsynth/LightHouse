package notificationctl

import (
	"encoding/json"
	"strings"
	"testing"

	"src.solsynth.dev/sosys/metoer/internal/model"
)

func TestNotificationsResponsePreservesUnreadStateSnapshot(t *testing.T) {
	notification := &model.Notification{}
	response := notificationsResponse([]*model.Notification{notification})

	if response[0].ViewedAt != nil {
		t.Fatal("unread notification response should have a nil viewed_at")
	}
	viewedAt := model.NowTime()
	notification.ViewedAt = &viewedAt
	// Marking the selected model after building the response must not rewrite the
	// unread state that the list endpoint selected for its payload.
	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if !strings.Contains(string(payload), `"viewed_at":null`) {
		t.Fatalf("response payload lost unread viewed_at: %s", payload)
	}
}
