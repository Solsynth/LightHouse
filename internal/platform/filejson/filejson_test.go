package filejson

import (
	"encoding/json"
	"strings"
	"testing"
)

// legacyAttachments is the attachments column of a chat_messages row written
// by the C# fleet: System.Text.Json PascalCase keys plus the EXIF dictionary
// Drive stored under FileMeta.
const legacyAttachments = `[{
	"Id": "01M66KRQD8KYE5YNRK50WH2PNF",
	"Name": "IMG_20261030_223219.jpg",
	"FileMeta": {
		"exif": {"DateTime": "2026:10:30 22:32:19", "Software": "\"Android PLE110_16.0.10.600(CN01)\""},
		"width": 1080,
		"height": 2362
	},
	"SensitiveMarks": [],
	"MimeType": "image/jpeg",
	"Hash": "sha256:41d8cd98f00b204e9800998ecf8427e",
	"Size": 9007199254740993,
	"HasCompression": false,
	"Url": "https://drive.example/files/IMG_20261030_223219.jpg",
	"Width": 1080,
	"Height": 2362,
	"Blurhash": "LEHV6nWB2yk8pyo0adR*.7kCMdnj",
	"Usage": "attachment",
	"ApplicationType": "image",
	"CreatedAt": "2026-10-30T14:32:19Z",
	"UpdatedAt": "2026-10-30T14:32:19.1234567Z",
	"UserMeta": {}
}]`

func TestReferencesDecodesLegacyPascalCase(t *testing.T) {
	references := References([]byte(legacyAttachments))
	if len(references) != 1 {
		t.Fatalf("references = %d, want 1", len(references))
	}
	reference := references[0]
	if reference.Id != "01M66KRQD8KYE5YNRK50WH2PNF" {
		t.Errorf("id = %q", reference.Id)
	}
	if reference.Name != "IMG_20261030_223219.jpg" {
		t.Errorf("name = %q", reference.Name)
	}
	if reference.MimeType != "image/jpeg" {
		t.Errorf("mime_type = %q", reference.MimeType)
	}
	if reference.Hash == "" || reference.Size != 9007199254740993 {
		t.Errorf("hash/size = %q/%d", reference.Hash, reference.Size)
	}
	if reference.Url != "https://drive.example/files/IMG_20261030_223219.jpg" {
		t.Errorf("url = %q", reference.Url)
	}
	if reference.Width == nil || *reference.Width != 1080 {
		t.Errorf("width = %v", reference.Width)
	}
	if reference.Height == nil || *reference.Height != 2362 {
		t.Errorf("height = %v", reference.Height)
	}
	if reference.Blurhash == "" || reference.Usage != "attachment" || reference.ApplicationType != "image" {
		t.Errorf("blurhash/usage/application_type = %q/%q/%q",
			reference.Blurhash, reference.Usage, reference.ApplicationType)
	}
	if reference.CreatedAt == nil || reference.UpdatedAt == nil {
		t.Fatalf("timestamps = %v/%v", reference.CreatedAt, reference.UpdatedAt)
	}
	if got := reference.CreatedAt.Time().Format("2006-01-02T15:04:05Z"); got != "2026-10-30T14:32:19Z" {
		t.Errorf("created_at = %s", got)
	}

	// The regression: FileMeta must land on file_meta, contents untouched.
	if len(reference.FileMeta) == 0 {
		t.Fatal("file_meta is empty, the FileMeta table was dropped")
	}
	exif, ok := reference.FileMeta["exif"].(map[string]any)
	if !ok {
		t.Fatalf("file_meta.exif = %T", reference.FileMeta["exif"])
	}
	if exif["DateTime"] != "2026:10:30 22:32:19" {
		t.Errorf("file_meta.exif.DateTime = %v", exif["DateTime"])
	}
	if exif["Software"] != `"Android PLE110_16.0.10.600(CN01)"` {
		t.Errorf("file_meta.exif.Software = %v", exif["Software"])
	}
	if reference.FileMeta["width"] != float64(1080) || reference.FileMeta["height"] != float64(2362) {
		t.Errorf("file_meta dimensions = %v/%v", reference.FileMeta["width"], reference.FileMeta["height"])
	}
	if reference.UserMeta == nil {
		t.Error("user_meta is nil, it must marshal as {}")
	}

	encoded, err := json.Marshal(reference)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	wire := string(encoded)
	if strings.Contains(wire, `"file_meta":{}`) || !strings.Contains(wire, `"file_meta":{"exif"`) {
		t.Errorf("wire file_meta = %s", wire)
	}
	if !strings.Contains(wire, `"user_meta":{}`) {
		t.Errorf("wire user_meta = %s", wire)
	}
}

func TestReferencesDecodesSnakeCase(t *testing.T) {
	raw := `[{"id":"file-1","name":"a.png","file_meta":{"width":1},"user_meta":{"caption":"hi"},` +
		`"mime_type":"image/png","url":"https://cdn.example/a.png"}]`
	references := References([]byte(raw))
	if len(references) != 1 {
		t.Fatalf("references = %d, want 1", len(references))
	}
	reference := references[0]
	if reference.Id != "file-1" || reference.MimeType != "image/png" || reference.Url != "https://cdn.example/a.png" {
		t.Errorf("id/mime_type/url = %q/%q/%q", reference.Id, reference.MimeType, reference.Url)
	}
	if reference.FileMeta["width"] != float64(1) {
		t.Errorf("file_meta = %v", reference.FileMeta)
	}
	if reference.UserMeta["caption"] != "hi" {
		t.Errorf("user_meta = %v", reference.UserMeta)
	}
}

func TestReferencesEmptyColumnIsEmptySlice(t *testing.T) {
	for name, raw := range map[string][]byte{"nil": nil, "empty": {}, "null": []byte("null")} {
		references := References(raw)
		if references == nil {
			t.Fatalf("%s: references is nil, the wire would emit null", name)
		}
		encoded, err := json.Marshal(references)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		if string(encoded) != "[]" {
			t.Errorf("%s: wire = %s, want []", name, encoded)
		}
	}
}

func TestReferenceEmptyOrNull(t *testing.T) {
	if reference := Reference(nil); reference != nil {
		t.Errorf("nil column = %+v, want nil", reference)
	}
	if reference := Reference([]byte("null")); reference != nil {
		t.Errorf("null column = %+v, want nil", reference)
	}
	reference := Reference([]byte(`{"Id":"file-1","FileMeta":{"exif":{}}}`))
	if reference == nil || reference.Id != "file-1" || len(reference.FileMeta) == 0 {
		t.Fatalf("legacy reference = %+v", reference)
	}
}

func TestValueNormalizesTopLevelKeysOnly(t *testing.T) {
	raw := `{"Id":"file-1","MimeType":"image/jpeg","FileMeta":{"exif":{"DateTime":"2026:10:30 22:32:19"}}}`

	legacy, ok := Value([]byte(raw)).(map[string]any)
	if !ok {
		t.Fatalf("Value = %T, want map[string]any", Value([]byte(raw)))
	}
	if legacy["id"] != "file-1" || legacy["mime_type"] != "image/jpeg" {
		t.Errorf("top-level keys = %v", legacy)
	}
	fileMeta, ok := legacy["file_meta"].(map[string]any)
	if !ok {
		t.Fatalf("file_meta = %T", legacy["file_meta"])
	}
	exif, ok := fileMeta["exif"].(map[string]any)
	if !ok {
		t.Fatalf("file_meta.exif = %T", fileMeta["exif"])
	}
	if exif["DateTime"] != "2026:10:30 22:32:19" {
		t.Errorf("nested key rewritten: %v", exif)
	}

	snake, ok := Value([]byte(`{"id":"file-1","file_meta":{"width":1}}`)).(map[string]any)
	if !ok || snake["id"] != "file-1" {
		t.Fatalf("snake_case value = %v", snake)
	}
}

func TestValueEdgeColumns(t *testing.T) {
	if value := Value(nil); value != nil {
		t.Errorf("nil column = %v, want nil", value)
	}
	if value := Value([]byte("null")); value != nil {
		t.Errorf("null column = %v, want nil", value)
	}
	// A non-object blob is not translatable; it passes through unchanged.
	if value := Value([]byte(`[1,2]`)); value == nil {
		t.Error("array column = nil, want the decoded array")
	}
}

func TestReferencesPreferExactSnakeCaseKey(t *testing.T) {
	raw := `[{"FileMeta":{"legacy":true},"file_meta":{"exact":true}}]`
	references := References([]byte(raw))
	if len(references) != 1 {
		t.Fatalf("references = %d, want 1", len(references))
	}
	if _, legacy := references[0].FileMeta["legacy"]; legacy {
		t.Errorf("legacy alias overrode the exact key: %v", references[0].FileMeta)
	}
	if references[0].FileMeta["exact"] != true {
		t.Errorf("file_meta = %v", references[0].FileMeta)
	}
}

func TestToSnakeCase(t *testing.T) {
	cases := map[string]string{
		"Id":              "id",
		"ID":              "id",
		"Url":             "url",
		"URL":             "url",
		"MimeType":        "mime_type",
		"FileMeta":        "file_meta",
		"fileMeta":        "file_meta",
		"UserMeta":        "user_meta",
		"SensitiveMarks":  "sensitive_marks",
		"HasCompression":  "has_compression",
		"ApplicationType": "application_type",
		"CreatedAt":       "created_at",
		"width":           "width",
		"":                "",
	}
	for input, want := range cases {
		if got := toSnakeCase(input); got != want {
			t.Errorf("toSnakeCase(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestUnmarshalNormalizesTypedObject(t *testing.T) {
	type verification struct {
		Type       int     `json:"type"`
		Title      *string `json:"title"`
		VerifiedBy *string `json:"verified_by"`
	}
	var decoded verification
	if err := Unmarshal([]byte(`{"Type":1,"Title":"Verified","VerifiedBy":"staff"}`), &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Type != 1 {
		t.Errorf("type = %d, want 1", decoded.Type)
	}
	if decoded.VerifiedBy == nil || *decoded.VerifiedBy != "staff" {
		t.Errorf("verified_by = %v, want staff", decoded.VerifiedBy)
	}
}
