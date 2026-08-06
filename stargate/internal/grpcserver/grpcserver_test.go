package grpcserver

import (
	"context"
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// --- DyCapabilitiesService ---

func TestGetCapabilitiesRegistry(t *testing.T) {
	svc := &dyCapabilitiesService{}
	resp, err := svc.GetCapabilities(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("GetCapabilities: %v", err)
	}
	if len(resp.Capabilities) == 0 {
		t.Fatal("capabilities registry is empty")
	}
	// api_revision must be the highest revision (all features ship at 1).
	if resp.ApiRevision != 1 {
		t.Errorf("api_revision = %d, want 1", resp.ApiRevision)
	}
	byName := map[string]*gen.DyCapabilityState{}
	for _, c := range resp.Capabilities {
		byName[c.Name] = c
		if !c.Enabled {
			t.Errorf("capability %s is disabled", c.Name)
		}
		if c.Experimental {
			t.Errorf("capability %s is experimental (none are in the registry)", c.Name)
		}
	}
	// Every Padlock [ApiFeature] capability plus the moved Passport groups.
	for _, want := range []string{"auth", "auth.challenge", "accounts.registration", "accounts.factors",
		"accounts.sessions", "accounts.authorized-apps", "accounts.action-log", "accounts.punishments",
		"auth.qr-login", "auth.api-keys", "e2ee", "e2ee.mls", "admin.accounts", "admin.permissions",
		"relationships", "relationships.friends", "relationships.block", "friends"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("capability %q missing from registry", want)
		}
	}
}

// --- permission evaluation helpers ---

func TestWildcardMatch(t *testing.T) {
	cases := []struct {
		pattern, target string
		want            bool
	}{
		{"accounts.*.view", "accounts.profile.view", true},
		{"accounts.*.view", "accounts.profile.edit", false},
		{"*", "anything", true},
		{"admin.accounts.*", "admin.accounts", false},
		{"a*b*c", "aXbYc", true},
		{"a*b*c", "abc", true},
		{"a*b*c", "aXb", false},
		{"chat.*", "chat", false},
	}
	for _, c := range cases {
		if got := wildcardMatch(c.pattern, c.target); got != c.want {
			t.Errorf("wildcardMatch(%q, %q) = %v, want %v", c.pattern, c.target, got, c.want)
		}
	}
}

func TestIsBlockedKey(t *testing.T) {
	blocked := []string{"accounts.profile.board", "admin.*", "chat.messages.create"}
	for _, key := range []string{"accounts.profile.board", "ADMIN.ACCOUNTS.LIST", "admin.stats.geography", "chat.messages.create"} {
		if !isBlockedKey(blocked, key) {
			t.Errorf("isBlockedKey(%q) = false, want true", key)
		}
	}
	for _, key := range []string{"accounts.profile.manage", "chat.messages.read"} {
		if isBlockedKey(blocked, key) {
			t.Errorf("isBlockedKey(%q) = true, want false", key)
		}
	}
}

// --- converters ---

func TestAuthFactorToProto(t *testing.T) {
	f := &model.AuthFactor{
		Id:              "f1",
		Type:            model.AuthFactorTypePinCode,
		Trustworthy:     2,
		AccountId:       "a1",
		CreatedResponse: map[string]any{"recovery_code": "ABC-123"},
	}
	proto := authFactorToProto(f)
	if proto.Type != gen.DyAccountAuthFactorType_DY_PIN_CODE {
		t.Errorf("type = %v, want DY_PIN_CODE", proto.Type)
	}
	if proto.Trustworthy != 2 {
		t.Errorf("trustworthy = %d, want 2", proto.Trustworthy)
	}
	if got := proto.CreatedResponse["recovery_code"]; got == nil {
		t.Error("created_response missing recovery_code")
	}
}

func TestConnectionToProtoTokens(t *testing.T) {
	c := &model.Connection{
		Id:                 "c1",
		Provider:           "google",
		ProvidedIdentifier: "user@example.com",
		AccessToken:        "at",
		RefreshToken:       "rt",
		AccountId:          "a1",
	}
	proto := connectionToProto(c)
	if proto.AccessToken.GetValue() != "at" || proto.RefreshToken.GetValue() != "rt" {
		t.Errorf("tokens not mapped: %v %v", proto.AccessToken, proto.RefreshToken)
	}
}

func TestActionLogToProto(t *testing.T) {
	ua := "test-agent"
	loc := model.GeoPoint{CountryCode: "CN"}
	l := &model.ActionLog{
		Id:        "l1",
		Action:    "login",
		UserAgent: &ua,
		Location:  &loc,
		AccountId: "a1",
		Meta:      map[string]any{"int": 5, "str": "s", "nested": map[string]any{"k": true}},
	}
	proto := actionLogToProto(l)
	if proto.UserAgent.GetValue() != "test-agent" {
		t.Errorf("user_agent = %q", proto.UserAgent.GetValue())
	}
	var parsed model.GeoPoint
	if err := json.Unmarshal([]byte(proto.Location.GetValue()), &parsed); err != nil {
		t.Fatalf("location not GeoPoint JSON: %v", err)
	}
	if parsed.CountryCode != "CN" {
		t.Errorf("location country = %q, want CN", parsed.CountryCode)
	}
	if got := proto.Meta["int"].GetNumberValue(); got != 5 {
		t.Errorf("meta int = %v, want 5", got)
	}
}

func TestMetaConversionsRoundTrip(t *testing.T) {
	meta := map[string]any{"name": "bot", "count": 3, "ok": true, "nested": map[string]any{"a": "b"}}
	proto := anyMetaToProto(meta)
	back := protoMetaToAny(proto)
	if back["name"] != "bot" || back["count"] != float64(3) || back["ok"] != true {
		t.Errorf("round trip mismatch: %#v", back)
	}
	// ConvertToValueMap serializes non-primitive values to JSON strings
	// (the C# fallback), and ConvertValueToObject keeps strings as strings.
	if got, ok := back["nested"].(string); !ok || got != `{"a":"b"}` {
		t.Errorf("nested map mismatch: %#v", back["nested"])
	}
}

func TestJSONToProtoValue(t *testing.T) {
	v, err := jsonToProtoValue([]byte(`{"board": true, "limit": 5}`))
	if err != nil {
		t.Fatalf("jsonToProtoValue: %v", err)
	}
	fields := v.GetStructValue().GetFields()
	if !fields["board"].GetBoolValue() {
		t.Error("board field not parsed as bool")
	}
	if fields["limit"].GetNumberValue() != 5 {
		t.Error("limit field not parsed as number")
	}
	// Round trip: Value -> JSON -> Value.
	raw, err := protoValueToJSON(v)
	if err != nil {
		t.Fatalf("protoValueToJSON: %v", err)
	}
	v2, err := jsonToProtoValue(raw)
	if err != nil || !v2.GetStructValue().GetFields()["board"].GetBoolValue() {
		t.Errorf("JSON round trip failed: %v %v", v2, err)
	}
}

func TestAccountFromProto(t *testing.T) {
	automated := "00000000-0000-0000-0000-000000000001"
	p := &gen.DyAccount{
		Id:          "00000000-0000-0000-0000-000000000002",
		Name:        "bot",
		Nick:        "Bot",
		Language:    "en",
		Region:      "US",
		IsSuperuser: false,
		AutomatedId: wrapperspb.String(automated),
		Profile: &gen.DyAccountProfile{
			// The C# FromProtoValue only maps a profile that carries an id or
			// account id; a bare profile is dropped (matching EF's behavior).
			AccountId: "00000000-0000-0000-0000-000000000002",
			FirstName: wrapperspb.String("First"),
			Links:     []*gen.DyProfileLink{{Name: "web", Url: "https://example.com"}},
		},
	}
	a := accountFromProto(p)
	if a.Name != "bot" || a.AutomatedId == nil || *a.AutomatedId != automated {
		t.Errorf("account mapping wrong: %+v", a)
	}
	if a.Profile == nil || a.Profile.FirstName == nil || *a.Profile.FirstName != "First" {
		t.Error("profile mapping missing")
	}
	if len(a.Profile.Links) != 1 || a.Profile.Links[0].Name != "web" {
		t.Error("profile links not mapped")
	}
}

func TestConvertActorType(t *testing.T) {
	group := gen.DyPermissionNodeActorType_DY_GROUP
	account := gen.DyPermissionNodeActorType_DY_ACCOUNT
	if convertActorType(nil) != 0 || convertActorType(&account) != 0 {
		t.Error("nil/account type must map to Account(0)")
	}
	if convertActorType(&group) != 1 {
		t.Error("group type must map to Group(1)")
	}
}

func TestProtoValueNil(t *testing.T) {
	if _, err := protoValueToJSON(nil); err == nil {
		t.Error("nil Value must error (invalid permission value)")
	}
}
