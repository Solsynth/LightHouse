package store

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// TestDecodeJSONArray verifies jsonb column bytes decode into a string slice,
// matching how the driver returns jsonb values (raw []byte).
func TestDecodeJSONArray(t *testing.T) {
	for name, tt := range map[string]struct {
		in   []byte
		want []string
	}{
		"empty array":  {in: []byte(`[]`), want: []string{}},
		"string array": {in: []byte(`["a","b"]`), want: []string{"a", "b"}},
		"empty bytes":  {in: nil, want: nil},
		"zero length":  {in: []byte{}, want: nil},
		"null literal": {in: []byte(`null`), want: nil},
		"invalid json": {in: []byte(`not-json`), want: nil},
		"wrong shape":  {in: []byte(`{"a":1}`), want: nil},
	} {
		t.Run(name, func(t *testing.T) {
			if got := decodeJSONArray(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("decodeJSONArray(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

// legacyReference is a profile picture/status icon blob written by the C#
// fleet: System.Text.Json PascalCase keys. Clients parse the object as
// SnCloudFileReference, so the wire keys must be snake_case.
const legacyReference = `{
	"Id": "file-legacy-ref",
	"Name": "avatar.png",
	"FileMeta": {"width": 512, "height": 512},
	"UserMeta": {},
	"SensitiveMarks": [],
	"MimeType": "image/png",
	"Hash": "sha256:abc",
	"Size": 40,
	"HasCompression": false,
	"Url": "https://drive.example/files/avatar.png",
	"Width": 512,
	"Height": 512
}`

func TestDecodeFileReferenceNormalizesLegacyKeys(t *testing.T) {
	raw := datatypes.JSON(legacyReference)
	reference := DecodeFileReference(&raw)
	if reference == nil {
		t.Fatal("reference = nil, want decoded reference")
	}
	if reference.Id != "file-legacy-ref" {
		t.Errorf("id = %q, want file-legacy-ref", reference.Id)
	}
	if reference.MimeType != "image/png" {
		t.Errorf("mime_type = %q, want image/png", reference.MimeType)
	}
	if reference.FileMeta["width"] != float64(512) {
		t.Errorf("file_meta.width = %v, want 512", reference.FileMeta["width"])
	}

	wire, err := json.Marshal(reference)
	if err != nil {
		t.Fatalf("marshal reference: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal reference: %v", err)
	}
	if decoded["id"] != "file-legacy-ref" {
		t.Errorf("wire id = %v, want file-legacy-ref", decoded["id"])
	}
	if _, legacy := decoded["Id"]; legacy {
		t.Error("wire still carries the legacy Id key")
	}
	if _, ok := decoded["file_meta"]; !ok {
		t.Error("wire file_meta is missing")
	}
}

func TestDecodeFileReferenceHandlesNull(t *testing.T) {
	if DecodeFileReference(nil) != nil {
		t.Error("nil column should decode to nil")
	}
	empty := datatypes.JSON(nil)
	if DecodeFileReference(&empty) != nil {
		t.Error("empty column should decode to nil")
	}
	literalNull := datatypes.JSON("null")
	if DecodeFileReference(&literalNull) != nil {
		t.Error("JSON null column should decode to nil")
	}
}

func TestProfileFromEntityNormalizesLegacyArt(t *testing.T) {
	raw := datatypes.JSON(legacyReference)
	profile := profileFromEntity(&ProfileEntity{
		ID:         uuid.New(),
		AccountID:  uuid.New(),
		Picture:    &raw,
		Background: &raw,
	})

	wire, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("marshal profile: %v", err)
	}
	var decoded struct {
		Picture    map[string]any `json:"picture"`
		Background map[string]any `json:"background"`
	}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal profile: %v", err)
	}
	for name, reference := range map[string]map[string]any{
		"picture":    decoded.Picture,
		"background": decoded.Background,
	} {
		if reference["id"] != "file-legacy-ref" {
			t.Errorf("%s.id = %v, want file-legacy-ref", name, reference["id"])
		}
		if reference["mime_type"] != "image/png" {
			t.Errorf("%s.mime_type = %v, want image/png", name, reference["mime_type"])
		}
		if _, legacy := reference["Id"]; legacy {
			t.Errorf("%s still carries the legacy Id key", name)
		}
	}
}

func TestStatusFromEntityNormalizesLegacyArt(t *testing.T) {
	raw := datatypes.JSON(legacyReference)
	status := statusFromEntity(&AccountStatusEntity{
		ID:         uuid.New(),
		AccountID:  uuid.New(),
		Icon:       &raw,
		Background: &raw,
	})

	wire, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	var decoded struct {
		Icon       map[string]any `json:"icon"`
		Background map[string]any `json:"background"`
	}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if decoded.Icon["id"] != "file-legacy-ref" {
		t.Errorf("icon.id = %v, want file-legacy-ref", decoded.Icon["id"])
	}
	if decoded.Background["id"] != "file-legacy-ref" {
		t.Errorf("background.id = %v, want file-legacy-ref", decoded.Background["id"])
	}
}

func TestProfileFromEntityNormalizesVerification(t *testing.T) {
	raw := datatypes.JSON(`{"Type":1,"Title":"Verified","VerifiedBy":"staff"}`)
	profile := profileFromEntity(&ProfileEntity{
		ID:           uuid.New(),
		AccountID:    uuid.New(),
		Verification: &raw,
	})
	if profile.Verification == nil {
		t.Fatal("verification = nil, want decoded mark")
	}
	if profile.Verification.VerifiedBy == nil || *profile.Verification.VerifiedBy != "staff" {
		t.Errorf("verified_by = %v, want staff", profile.Verification.VerifiedBy)
	}
}
