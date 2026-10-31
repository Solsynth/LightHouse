package model

import (
	"encoding/json"
	"testing"
	"time"
)

// TestAccountEventEnumValues pins the numeric values the Dart SDK
// (solar_network_sdk) decodes: they must stay identical to the C# enums in
// DysonNetwork.Shared.Models.AccountEvent.cs.
func TestAccountEventEnumValues(t *testing.T) {
	if StatusAttitudePositive != 0 || StatusAttitudeNegative != 1 || StatusAttitudeNeutral != 2 {
		t.Fatalf("StatusAttitude values drifted: %d %d %d",
			StatusAttitudePositive, StatusAttitudeNegative, StatusAttitudeNeutral)
	}
	if StatusTypeDefault != 0 || StatusTypeBusy != 1 || StatusTypeDoNotDisturb != 2 || StatusTypeInvisible != 3 {
		t.Fatalf("StatusType values drifted")
	}
	if PresenceVisibilityUnknown != 0 || PresenceVisibilityPublic != 1 ||
		PresenceVisibilityFriends != 2 || PresenceVisibilityPrivate != 3 {
		t.Fatalf("PresenceVisibility values drifted")
	}
}

// TestAccountStatusWireShape pins the JSON contract: snake_case keys, integers
// for enums, RFC3339 UTC "Z" instants, nulls omitted for optional fields —
// except the presence flags C# always emits and the created_at/updated_at/
// deleted_at trio the Dart SDK requires to be present.
func TestAccountStatusWireShape(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	label := "Online"
	status := AccountStatus{
		Id:            "11111111-1111-1111-1111-111111111111",
		Attitude:      StatusAttitudeNeutral,
		Type:          StatusTypeInvisible,
		Label:         &label,
		AccountId:     "22222222-2222-2222-2222-222222222222",
		IsOnline:      true,
		IsCustomized:  false,
		IsInvisible:   true,
		IsNotDisturb:  false,
		OnlineDevices: []OnlineDevice{},
		CreatedAt:     NewTime(now),
		UpdatedAt:     NewTime(now),
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, key := range []string{
		"id", "account_id", "attitude", "type", "label", "is_online",
		"is_automated", "is_idle", "is_customized", "is_invisible",
		"is_not_disturb", "created_at", "updated_at", "deleted_at",
	} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("%s missing from status JSON", key)
		}
	}
	if decoded["deleted_at"] != nil {
		t.Errorf("deleted_at = %v, want null", decoded["deleted_at"])
	}
	for _, key := range []string{"cleared_at", "symbol", "icon", "background", "meta", "app_identifier", "idle_since"} {
		if _, ok := decoded[key]; ok {
			t.Errorf("%s must be omitted when unset", key)
		}
	}
	if got, _ := decoded["attitude"].(float64); got != 2 {
		t.Errorf("attitude = %v, want 2 (integers, not names)", decoded["attitude"])
	}
	if got, _ := decoded["type"].(float64); got != 3 {
		t.Errorf("type = %v, want 3", decoded["type"])
	}
	if got, _ := decoded["created_at"].(string); got != "2026-10-26T12:00:00Z" {
		t.Errorf("created_at = %q, want RFC3339 UTC with Z", got)
	}
	if _, ok := decoded["online_devices"]; ok {
		t.Errorf("online_devices = %v, want omitted for an empty list", decoded["online_devices"])
	}

	// nil OnlineDevices must be omitted entirely (the owner-only rule).
	status.OnlineDevices = nil
	raw, err = json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	decoded = nil
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := decoded["online_devices"]; ok {
		t.Error("online_devices must be omitted when nil")
	}
}

// TestDefaultAccountStatusWireShape pins the synthesized no-row status: the
// Dart SDK parses id, created_at and updated_at as non-nullable, so all three
// must be present even though there is no database row.
func TestDefaultAccountStatusWireShape(t *testing.T) {
	status := DefaultAccountStatus("22222222-2222-2222-2222-222222222222", true)
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if id, _ := decoded["id"].(string); id == "" {
		t.Error("id is empty; the SDK expects a non-nullable string")
	}
	for _, key := range []string{"created_at", "updated_at"} {
		value, ok := decoded[key].(string)
		if !ok || value == "" {
			t.Errorf("%s = %v, want a non-null RFC3339 string", key, decoded[key])
			continue
		}
		if _, err := time.Parse(time.RFC3339, value); err != nil {
			t.Errorf("%s = %q, want RFC3339: %v", key, value, err)
		}
	}
	if decoded["label"] != "Online" || decoded["is_customized"] != false {
		t.Errorf("default status = %v, want the Online/uncustomized projection", decoded)
	}
}

// TestPresenceActivityWireShape pins the activity/catalog JSON contract.
func TestPresenceActivityWireShape(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	title := "Listening"
	name := "Music"
	activity := PresenceActivity{
		Id:             "33333333-3333-3333-3333-333333333333",
		Title:          &title,
		Tags:           []PresenceTag{{Slug: "music", Name: &name}},
		Visibility:     PresenceVisibilityFriends,
		QueryableTerms: []string{"music"},
		LeaseMinutes:   5,
		LeaseExpiresAt: NewTime(now),
		AccountId:      "22222222-2222-2222-2222-222222222222",
	}
	raw, err := json.Marshal(activity)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"id", "account_id", "title", "tags", "visibility", "queryable_terms", "lease_minutes", "lease_expires_at"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("%s missing from activity JSON", key)
		}
	}
	for _, key := range []string{"type", "provider", "reference_id", "manual_id", "subtitle", "caption", "meta", "started_at", "ended_at", "catalog_id", "catalog", "large_image", "small_image", "title_url", "subtitle_url"} {
		if _, ok := decoded[key]; ok {
			t.Errorf("%s must be omitted when unset", key)
		}
	}
	for _, key := range []string{"created_at", "updated_at", "deleted_at"} {
		value, ok := decoded[key]
		if !ok {
			t.Errorf("%s missing from activity JSON", key)
			continue
		}
		if value != nil {
			t.Errorf("%s = %v, want null", key, value)
		}
	}
	if got, _ := decoded["visibility"].(float64); got != 2 {
		t.Errorf("visibility = %v, want 2", decoded["visibility"])
	}
	tags, ok := decoded["tags"].([]any)
	if !ok || len(tags) != 1 {
		t.Fatalf("tags = %v, want one entry", decoded["tags"])
	}
	tag, _ := tags[0].(map[string]any)
	if tag["slug"] != "music" || tag["name"] != "Music" {
		t.Errorf("tag = %v", tag)
	}

	catalog := PresenceCatalogItem{
		Id:             "44444444-4444-4444-4444-444444444444",
		AccountId:      "22222222-2222-2222-2222-222222222222",
		CatalogKey:     &title,
		Visibility:     PresenceVisibilityPublic,
		Tags:           []PresenceTag{},
		QueryableTerms: []string{},
		Meta:           map[string]any{},
		TotalSeconds:   90,
		SessionCount:   2,
	}
	raw, err = json.Marshal(catalog)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	decoded = nil
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"id", "account_id", "catalog_key", "visibility", "tags", "queryable_terms", "meta", "total_seconds", "session_count"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("%s missing from catalog JSON", key)
		}
	}
	if meta, ok := decoded["meta"].(map[string]any); !ok || len(meta) != 0 {
		t.Errorf("meta = %v, want {} (C# serializes an empty dictionary)", decoded["meta"])
	}
	if _, ok := decoded["provider"]; ok {
		t.Error("provider must be omitted when unset")
	}
}
