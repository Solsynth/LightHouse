package model

import (
	"encoding/json"
	"testing"
	"time"
)

// TestRealmRefCarriesSnRealmTimestamps guards the Solian client contract:
// SnRealm.fromJson requires created_at/updated_at, and discovery realm cards
// parse the embedded snapshot as SnRealm.
func TestRealmRefCarriesSnRealmTimestamps(t *testing.T) {
	raw, err := json.Marshal(RealmRef{ID: "realm-1", Slug: "realm", Name: "Realm", AccountID: "account-1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"created_at", "updated_at", "deleted_at"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("realm payload missing %q: %s", key, raw)
		}
	}
	if decoded["created_at"] == nil || decoded["updated_at"] == nil {
		t.Errorf("realm timestamps must not be null: %s", raw)
	}
}

// TestDiscoveryProfileSuggestionShape guards the fields SnSuggestedData and
// SnDiscoveryInterest require from the discovery profile endpoint.
func TestDiscoveryProfileSuggestionShape(t *testing.T) {
	profile := DiscoveryProfile{
		GeneratedAt: NewTime(time.Now()),
		Interests: []InterestEntry{{
			Kind:             "tag",
			ReferenceID:      "tag-1",
			Label:            "go",
			Score:            1,
			LastInteractedAt: NewTime(time.Now()),
		}},
		SuggestedRealms: []DiscoverySuggestion{{
			Kind:        1,
			ReferenceID: "realm-1",
			Label:       "Realm",
			Score:       1,
			Reasons:     []string{"go"},
			Data:        &RealmRef{ID: "realm-1", Slug: "realm", Name: "Realm", AccountID: "account-1"},
		}},
	}
	raw, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Interests []map[string]any `json:"interests"`
		Realms    []map[string]any `json:"suggested_realms"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"kind", "reference_id", "label", "score", "interaction_count", "last_interacted_at", "last_signal_type"} {
		if _, ok := decoded.Interests[0][key]; !ok {
			t.Errorf("interest missing %q: %s", key, raw)
		}
	}
	if _, ok := decoded.Interests[0]["kind"].(string); !ok {
		t.Errorf("interest kind must be a string: %s", raw)
	}
	for _, key := range []string{"kind", "reference_id", "label", "score", "reasons", "data"} {
		if _, ok := decoded.Realms[0][key]; !ok {
			t.Errorf("realm suggestion missing %q: %s", key, raw)
		}
	}
}
