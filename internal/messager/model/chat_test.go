package model

import (
	"encoding/json"
	"testing"
	"time"
)

// The Island SDK casts realm_label as Map<String, dynamic> and requires its
// timestamp strings, so a member snapshot must never serialize the label as a
// bare name.
func TestChatMemberSerializesRealmLabelObject(t *testing.T) {
	zero := Time(time.Time{})
	member := ChatMember{
		ID:         "member-1",
		ChatRoomID: "room-1",
		AccountID:  "account-1",
		RealmLabel: &ChatRealmLabel{
			ID:                 "label-1",
			RealmID:            "realm-1",
			Name:               "Builder",
			CreatedByAccountID: "00000000-0000-0000-0000-000000000000",
			CreatedAt:          &zero,
			UpdatedAt:          &zero,
		},
	}

	raw, err := json.Marshal(member)
	if err != nil {
		t.Fatalf("marshal member: %v", err)
	}
	var decoded struct {
		Label map[string]any `json:"realm_label"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal member: %v", err)
	}
	if decoded.Label["id"] != "label-1" || decoded.Label["realm_id"] != "realm-1" {
		t.Fatalf("realm_label = %#v, want an object with id and realm_id", decoded.Label)
	}
	if decoded.Label["created_at"] != "0001-01-01T00:00:00Z" {
		t.Fatalf("realm_label.created_at = %#v, want the zero Instant", decoded.Label["created_at"])
	}
	if value, present := decoded.Label["description"]; !present || value != nil {
		t.Fatalf("realm_label.description = %#v, want null", value)
	}
}
