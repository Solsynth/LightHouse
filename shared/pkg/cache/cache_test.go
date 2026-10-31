package cache

import (
	"testing"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestNormalizeKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"auth:session:abc", "dyson:auth:session:abc"},
		{"auth:profile:42", "dyson:auth:profile:42"},
		{"simple", "dyson:simple"},
		{"", "dyson:"},
	}
	for _, tt := range tests {
		got := NormalizeKey(tt.input)
		if got != tt.want {
			t.Errorf("NormalizeKey(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSerializeDeserializeProto(t *testing.T) {
	session := &gen.DyAuthSession{
		Id:        "sess-123",
		AccountId: "acc-456",
		Scopes:    []string{"read", "write"},
		IpAddress: wrapperspb.String("127.0.0.1"),
	}

	data, err := serialize(session)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}

	if data == "" {
		t.Fatal("expected non-empty serialized data")
	}

	// Verify it contains snake_case field names
	if !contains(data, "account_id") {
		t.Errorf("expected snake_case field 'account_id' in serialized data: %s", data)
	}
	if !contains(data, "session_id") && !contains(data, "id") {
		t.Errorf("expected 'id' field in serialized data: %s", data)
	}

	var dest gen.DyAuthSession
	if err := deserialize(data, &dest); err != nil {
		t.Fatalf("deserialize: %v", err)
	}
	if dest.Id != "sess-123" {
		t.Errorf("expected id 'sess-123', got %q", dest.Id)
	}
	if dest.AccountId != "acc-456" {
		t.Errorf("expected account_id 'acc-456', got %q", dest.AccountId)
	}
}

func TestSerializeDeserializePlainJSON(t *testing.T) {
	type simple struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}

	orig := simple{Name: "test", Count: 42}
	data, err := serialize(orig)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}

	var dest simple
	if err := deserialize(data, &dest); err != nil {
		t.Fatalf("deserialize: %v", err)
	}
	if dest.Name != orig.Name || dest.Count != orig.Count {
		t.Errorf("round-trip mismatch: got %+v, want %+v", dest, orig)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
