package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"src.solsynth.dev/sosys/go/pkg/models"
)

// TestQueueMessageWire pins the empirically verified C# envelope byte shape
// (snake_case, nulls included, no omitempty).
func TestQueueMessageWire(t *testing.T) {
	target := "u1"
	msg := QueueMessage{
		Type:     QueueMessageTypePushNotification,
		TargetId: &target,
		Data:     `{"x":1}`,
		IsSavable: false,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":1,"target_id":"u1","data":"{\"x\":1}","excluded_web_socket_device_ids":null,"is_savable":false}`
	if string(data) != want {
		t.Fatalf("envelope mismatch:\n got %s\nwant %s", data, want)
	}
}

// TestQueueMessageEmailWire pins the email envelope (type 0, null target).
func TestQueueMessageEmailWire(t *testing.T) {
	msg := QueueMessage{
		Type: QueueMessageTypeEmail,
		Data: `{"ToName":"A","ToAddress":"a@b.c","Subject":"S","Body":"B"}`,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":0,"target_id":null,"data":"{\"ToName\":\"A\",\"ToAddress\":\"a@b.c\",\"Subject\":\"S\",\"Body\":\"B\"}","excluded_web_socket_device_ids":null,"is_savable":false}`
	if string(data) != want {
		t.Fatalf("envelope mismatch:\n got %s\nwant %s", data, want)
	}
}

// TestQueueNotificationInstants pins the C# default-STJ instant shape:
// CreatedAt/UpdatedAt emit {}, nullables emit null, PascalCase keys.
func TestQueueNotificationInstants(t *testing.T) {
	id := uuid.New()
	accountID := uuid.New()
	now := time.Now().UTC()
	n := &Notification{
		ModelBase: ModelBase{Id: id, CreatedAt: models.Time(now), UpdatedAt: models.Time(now)},
		Topic:     "t",
		Title:     new("x"),
		Meta:      map[string]any{},
		Priority:  10,
		AccountId: accountID,
	}
	data, err := json.Marshal(NewQueueNotification(n))
	if err != nil {
		t.Fatal(err)
	}
	raw := string(data)
	for _, frag := range []string{
		`"Id":"` + id.String() + `"`,
		`"Topic":"t"`,
		`"Title":"x"`,
		`"Meta":{}`,
		`"Priority":10`,
		`"ViewedAt":null`,
		`"AppId":null`,
		`"PushType":null`,
		`"AccountId":"` + accountID.String() + `"`,
		`"CreatedAt":{}`,
		`"UpdatedAt":{}`,
		`"DeletedAt":null`,
	} {
		if !strings.Contains(raw, frag) {
			t.Fatalf("queue notification DTO missing %s in %s", frag, raw)
		}
	}
}

// TestQueueNotificationRoundTrip parses a hand-written C#-style sample
// (PascalCase keys, {} instants) and asserts the epoch-zeroing behavior.
func TestQueueNotificationRoundTrip(t *testing.T) {
	id := uuid.New()
	accountID := uuid.New()
	sample := `{"Id":"` + id.String() + `","Topic":"t","Title":"x","Subtitle":null,"Content":null,` +
		`"Meta":{},"Priority":10,"ViewedAt":null,"AppId":null,"PushType":null,"AccountId":"` + accountID.String() + `",` +
		`"CreatedAt":{},"UpdatedAt":{},"DeletedAt":null}`
	var qn QueueNotification
	if err := json.Unmarshal([]byte(sample), &qn); err != nil {
		t.Fatalf("parse C#-style sample: %v", err)
	}
	n, err := qn.ToNotification()
	if err != nil {
		t.Fatal(err)
	}
	if n.Id != id || n.AccountId != accountID || n.Topic != "t" || n.Title == nil || *n.Title != "x" {
		t.Fatalf("round-trip mismatch: %+v", n)
	}
	// The C# round-trip zeroes instants: CreatedAt/UpdatedAt must be epoch.
	if !n.CreatedAt.Time().IsZero() {
		t.Fatalf("created_at should be zero after {} parse, got %v", n.CreatedAt.Time())
	}
	if n.Meta == nil {
		t.Fatal("meta must normalize to an empty map")
	}
}

// TestSnakeMapKeys pins the STJ SnakeCaseLower algorithm on dictionary keys
// (DictionaryKeyPolicy), including the consecutive-capitals look-ahead rule.
func TestSnakeMapKeys(t *testing.T) {
	input := map[string]any{
		"actionUri":   "x",
		"HTTPRequest": []any{"a"},
		"already_snake": map[string]any{"SomeKey": 1},
	}
	out := SnakeMapKeys(input).(map[string]any)
	if _, ok := out["action_uri"]; !ok {
		t.Fatalf("actionUri not snake-cased: %v", out)
	}
	if _, ok := out["http_request"]; !ok {
		t.Fatalf("HTTPRequest not snake-cased (look-ahead rule): %v", out)
	}
	nested := out["already_snake"].(map[string]any)
	if _, ok := nested["some_key"]; !ok {
		t.Fatalf("nested map key not snake-cased: %v", nested)
	}
}
