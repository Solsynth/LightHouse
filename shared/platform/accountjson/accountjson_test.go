package accountjson_test

import (
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/types/known/timestamppb"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/accountjson"
)

// wireMap embeds a proto the way the HTTP handlers do and decodes the result
// back into a generic map.
func wireMap(t *testing.T, value any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal embedded payload: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode embedded payload: %v", err)
	}
	return decoded
}

func fileOf(t *testing.T, object map[string]any, path ...string) map[string]any {
	t.Helper()
	current := object
	for _, key := range path {
		nested, ok := current[key].(map[string]any)
		if !ok {
			t.Fatalf("%v = %#v, want an object", path, current[key])
		}
		current = nested
	}
	return current
}

// A client reading the embedded account parses file_meta/user_meta as objects.
// protojson emits them as base64 strings, which is what made the Dart client
// throw "type 'String' is not a subtype of type 'Map<String, dynamic>?'".
func TestFromProtoDecodesFileReferenceJSONBlobs(t *testing.T) {
	account := &gen.DyAccount{
		Id:   "acc-1",
		Name: "sheep",
		Profile: &gen.DyAccountProfile{
			Picture: &gen.DyCloudFile{
				Id:             "file-1",
				Name:           "a.png",
				MimeType:       "image/png",
				Size:           4242,
				FileMeta:       []byte(`{"width":1024,"height":768}`),
				UserMeta:       []byte(`{"caption":"hi"}`),
				SensitiveMarks: []byte(`[3,5]`),
				CreatedAt:      timestamppb.Now(),
			},
		},
	}

	picture := fileOf(t, wireMap(t, accountjson.FromProto(account)), "profile", "picture")

	fileMeta, ok := picture["file_meta"].(map[string]any)
	if !ok {
		t.Fatalf("file_meta = %#v (%T), want a parsed object", picture["file_meta"], picture["file_meta"])
	}
	if fileMeta["width"] != float64(1024) || fileMeta["height"] != float64(768) {
		t.Errorf("file_meta = %#v, want the parsed dimensions", fileMeta)
	}
	userMeta, ok := picture["user_meta"].(map[string]any)
	if !ok || userMeta["caption"] != "hi" {
		t.Errorf("user_meta = %#v, want the parsed object", picture["user_meta"])
	}
	marks, ok := picture["sensitive_marks"].([]any)
	if !ok || len(marks) != 2 || marks[0] != float64(3) || marks[1] != float64(5) {
		t.Errorf("sensitive_marks = %#v, want [3 5]", picture["sensitive_marks"])
	}
	// System.Text.Json wrote the size as a bare number; protojson quotes
	// 64-bit integers as strings.
	if picture["size"] != float64(4242) {
		t.Errorf("size = %#v (%T), want the unquoted number", picture["size"], picture["size"])
	}
}

// An unset jsonb column must still arrive as an object: the SDK casts
// file_meta/user_meta to Map<String, dynamic>, so "" crashes the same way a
// base64 string does.
func TestFromProtoEmitsEmptyFileMetaAsObject(t *testing.T) {
	account := &gen.DyAccount{
		Id:      "acc-1",
		Profile: &gen.DyAccountProfile{Picture: &gen.DyCloudFile{Id: "file-1"}},
	}

	picture := fileOf(t, wireMap(t, accountjson.FromProto(account)), "profile", "picture")
	for _, key := range []string{"file_meta", "user_meta"} {
		if _, ok := picture[key].(map[string]any); !ok {
			t.Errorf("%s = %#v (%T), want {}", key, picture[key], picture[key])
		}
	}
}

// A local cloud file has no off-site URL. C# stored Url as null; the proto's
// url is a plain string, so EmitUnpopulated renders "" instead. Clients test
// `storageUrl != null` to fall back to /drive/files/{id}, and an empty string
// passes that test — the image then fails with "No host specified in URI".
// Blank must travel as null.
func TestFromProtoEmitsNullURLForLocalFiles(t *testing.T) {
	account := &gen.DyAccount{
		Id:      "acc-1",
		Profile: &gen.DyAccountProfile{Picture: &gen.DyCloudFile{Id: "file-1"}},
	}

	picture := fileOf(t, wireMap(t, accountjson.FromProto(account)), "profile", "picture")
	url, present := picture["url"]
	if !present {
		t.Fatal("url key is missing; C# always emitted it")
	}
	if url != nil {
		t.Errorf("url = %#v (%T), want null", url, url)
	}
}

// An external (fediverse) reference carries a real URL, which must survive.
func TestFromProtoKeepsExternalURL(t *testing.T) {
	account := &gen.DyAccount{
		Id: "acc-1",
		Profile: &gen.DyAccountProfile{
			Picture: &gen.DyCloudFile{Url: "https://files.mastodon.social/a.png"},
		},
	}

	picture := fileOf(t, wireMap(t, accountjson.FromProto(account)), "profile", "picture")
	if picture["url"] != "https://files.mastodon.social/a.png" {
		t.Errorf("url = %#v, want the remote address", picture["url"])
	}
}

// Dart's SnAccountBadge casts created_at/updated_at as non-null strings. C#
// SnAccountBadgeRef embeds ModelBase and always serialized both (zero Instant
// when the proto could not carry them), so the ported account JSON must too.
func TestFromProtoRestoresBadgeRefTimestamps(t *testing.T) {
	account := &gen.DyAccount{
		Id: "acc-1",
		Profile: &gen.DyAccountProfile{
			ActiveBadge: &gen.DyBadgeReferenceObject{
				Id:        "badge-1",
				Type:      "pioneer",
				AccountId: "acc-1",
			},
		},
	}

	badge := fileOf(t, wireMap(t, accountjson.FromProto(account)), "profile", "active_badge")
	for _, key := range []string{"created_at", "updated_at"} {
		if badge[key] != "0001-01-01T00:00:00Z" {
			t.Errorf("active_badge.%s = %#v, want the zero Instant", key, badge[key])
		}
	}
	if _, ok := badge["deleted_at"]; !ok {
		t.Error("active_badge.deleted_at is missing")
	}
}

// InfraObjectCoder encodes a nil dictionary as the literal "null". That is
// still JSON, so the adapter must expose it as JSON null rather than leaving
// the base64 blob (bnVsbA==) in the wire payload.
func TestMessageDecodesJSONNullBlob(t *testing.T) {
	status := &gen.DyAccountStatus{AccountId: "acc-1", Meta: []byte("null")}

	embedded := wireMap(t, accountjson.Message(status))
	value, ok := embedded["meta"]
	if !ok {
		t.Fatal("meta is missing from the status payload")
	}
	if value != nil {
		t.Errorf("meta = %#v (%T), want JSON null", value, value)
	}
}

// DyAccountStatus embeds ModelBase. A producer that omits the timestamps must
// still produce the C# wire shape: the zero Instant for created_at/updated_at
// and null for deleted_at.
func TestMessageFillsStatusModelTimestamps(t *testing.T) {
	status := &gen.DyAccountStatus{AccountId: "acc-1"}

	embedded := wireMap(t, accountjson.Message(status))
	for _, key := range []string{"created_at", "updated_at"} {
		if embedded[key] != "0001-01-01T00:00:00Z" {
			t.Errorf("status.%s = %#v, want the zero Instant", key, embedded[key])
		}
	}
	if value, ok := embedded["deleted_at"]; !ok || value != nil {
		t.Errorf("status.deleted_at = %#v (present %v), want null", value, ok)
	}
}

// Account snapshots embed ModelBase too. A proto without timestamps must still
// carry the non-nullable created_at/updated_at and the nullable deleted_at the
// Dart SnAccount parser expects.
func TestMessageFillsAccountModelTimestamps(t *testing.T) {
	embedded := wireMap(t, accountjson.FromProto(&gen.DyAccount{Id: "acc-1"}))
	for _, key := range []string{"created_at", "updated_at"} {
		if embedded[key] != "0001-01-01T00:00:00Z" {
			t.Errorf("account.%s = %#v, want the zero Instant", key, embedded[key])
		}
	}
	if value, ok := embedded["deleted_at"]; !ok || value != nil {
		t.Errorf("account.deleted_at = %#v (present %v), want null", value, ok)
	}
}

// Realm snapshots are ModelBase-shaped in C# but the fleet proto has no
// timestamp fields. Embedding one in a chat room must still yield the keys the
// Dart SnRealm parser requires.
func TestMessageFillsRealmModelTimestamps(t *testing.T) {
	realm := &gen.DyRealm{Id: "realm-1", Slug: "test", Name: "Test"}

	embedded := wireMap(t, accountjson.Message(realm))
	for _, key := range []string{"created_at", "updated_at"} {
		if embedded[key] != "0001-01-01T00:00:00Z" {
			t.Errorf("realm.%s = %#v, want the zero Instant", key, embedded[key])
		}
	}
	if value, ok := embedded["deleted_at"]; !ok || value != nil {
		t.Errorf("realm.deleted_at = %#v (present %v), want null", value, ok)
	}

	member := wireMap(t, accountjson.Message(&gen.DyRealmMember{RealmId: "realm-1", AccountId: "acc-1"}))
	for _, key := range []string{"created_at", "updated_at"} {
		if member[key] != "0001-01-01T00:00:00Z" {
			t.Errorf("realm member %s = %#v, want the zero Instant", key, member[key])
		}
	}
}

// StatusAttitude is the enum whose proto numbering shifts because of the
// UNSPECIFIED zero. The C# SnAccountStatus.FromProtoValue mapped it back to
// the model values, so chat/status payloads must not expose 3 for Neutral.
func TestMessageMapsStatusAttitudeToModelValues(t *testing.T) {
	for _, tc := range []struct {
		proto gen.DyStatusAttitude
		want  float64
	}{
		{gen.DyStatusAttitude_DY_POSITIVE, 0},
		{gen.DyStatusAttitude_DY_NEGATIVE, 1},
		{gen.DyStatusAttitude_DY_NEUTRAL, 2},
		{gen.DyStatusAttitude_DY_STATUS_ATTITUDE_UNSPECIFIED, 2},
	} {
		status := &gen.DyAccountStatus{AccountId: "acc-1", Attitude: tc.proto}
		embedded := wireMap(t, accountjson.Message(status))
		if embedded["attitude"] != tc.want {
			t.Errorf("attitude(%d) = %#v, want %v", tc.proto, embedded["attitude"], tc.want)
		}
	}
}

// Enums travel as the integers the C# fleet serialized (declaration order),
// not as protojson's name strings.
func TestFromProtoEmitsEnumNumbers(t *testing.T) {
	account := &gen.DyAccount{
		Id: "acc-1",
		Profile: &gen.DyAccountProfile{
			Verification: &gen.DyVerificationMark{Type: gen.DyVerificationMarkType(2)},
		},
	}

	verification := fileOf(t, wireMap(t, accountjson.FromProto(account)), "profile", "verification")
	if verification["type"] != float64(2) {
		t.Errorf("verification.type = %#v (%T), want the number 2", verification["type"], verification["type"])
	}
}

// Bytes that are not JSON (ciphertext, key packages) keep their base64 form;
// decoding them would corrupt binary payloads their consumers decode.
func TestMessageKeepsNonJSONBytes(t *testing.T) {
	packet := &gen.DyWebSocketPacket{
		Type: "messages.new",
		Data: []byte(`{"content":"hi"}`),
	}

	embedded := wireMap(t, accountjson.Message(packet))
	if embedded["data"] == nil {
		t.Fatal("data must be preserved")
	}
}

// DyRealm.boost_points is a proto3 string because C# stores a decimal, but
// System.Text.Json emitted a bare number. Chat rooms embed the realm, and a
// client that reads boost_points as num rejects the quoted form.
func TestMessageEmitsBoostPointsAsNumber(t *testing.T) {
	for _, tc := range []struct {
		points string
		want   float64
	}{
		{"1234", 1234},
		{"12.5", 12.5},
		{"", 0},
	} {
		realm := &gen.DyRealm{Id: "realm-1", BoostPoints: tc.points}
		embedded := wireMap(t, accountjson.Message(realm))
		if embedded["boost_points"] != tc.want {
			t.Errorf("boost_points(%q) = %#v (%T), want the number %v",
				tc.points, embedded["boost_points"], embedded["boost_points"], tc.want)
		}
	}
}

// Dart's SnWalletSubscriptionRef casts created_at/updated_at as non-null
// strings. The perk reference rides inside every embedded account (chat
// members, publishers, surveys); when the producer leaves the Timestamps
// unset, protojson emits null and the client throws "type 'Null' is not a
// subtype of type 'String' in type cast". C# always serialized both.
func TestMessageFillsSubscriptionReferenceTimestamps(t *testing.T) {
	account := &gen.DyAccount{
		Id: "acc-1",
		PerkSubscription: &gen.DySubscriptionReferenceObject{
			Id:         "sub-1",
			Identifier: "dy_plus",
		},
	}

	subscription := fileOf(t, wireMap(t, accountjson.FromProto(account)), "perk_subscription")
	for _, key := range []string{"created_at", "updated_at"} {
		if subscription[key] != "0001-01-01T00:00:00Z" {
			t.Errorf("perk_subscription.%s = %#v, want the zero Instant", key, subscription[key])
		}
	}
}
