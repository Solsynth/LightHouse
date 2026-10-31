package wallet

import (
	"encoding/json"
	"testing"

	"gorm.io/datatypes"
)

func TestCatalogItemProviderMappingsUsePaymentMethodKeys(t *testing.T) {
	entity := &WalletSubscriptionDefinitionEntity{
		Identifier: "solian.stellar.primary",
		ProviderMappings: datatypes.JSON([]byte(`{
			"AppleStore": ["STELLAR01"],
			"Afdian": ["afdian-plan"],
			"Paddle": ["pri_monthly", "pri_yearly"]
		}`)),
	}
	item := catalogItemFromDefinition(entity)

	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	mappings, ok := wire["provider_mappings"].(map[string]any)
	if !ok {
		t.Fatalf("provider_mappings is %T", wire["provider_mappings"])
	}
	for _, key := range []string{"afdian", "paddle", "apple_store"} {
		if _, ok := mappings[key].([]any); !ok {
			t.Fatalf("%q missing or not a list: %#v", key, mappings[key])
		}
	}
	if _, ok := mappings["AppleStore"]; ok {
		t.Fatalf("legacy key leaked into response: %#v", mappings)
	}
	if got := mappings["apple_store"].([]any); len(got) != 1 || got[0] != "STELLAR01" {
		t.Fatalf("apple_store = %#v", got)
	}
	if got := mappings["paddle"].([]any); len(got) != 2 {
		t.Fatalf("paddle = %#v", got)
	}

	empty := catalogItemFromDefinition(&WalletSubscriptionDefinitionEntity{Identifier: "empty"})
	if len(empty.ProviderMappings) != 3 {
		t.Fatalf("empty mappings = %#v", empty.ProviderMappings)
	}
	for _, key := range []string{"afdian", "paddle", "apple_store"} {
		if list, ok := empty.ProviderMappings[key]; !ok || list == nil || len(list) != 0 {
			t.Fatalf("%q = %#v", key, empty.ProviderMappings[key])
		}
	}
}
