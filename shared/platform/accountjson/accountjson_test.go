package accountjson_test

import (
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/types/known/timestamppb"

	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/accountjson"
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
