package realm

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// legacyArtwork is the C# System.Text.Json shape the old fleet stored in
// realms.picture/background, and legacyVerification the same for
// realms.verification. Clients strict-cast these objects, so every key must
// survive the read path with its snake_case name.
const legacyArtwork = `{
	"Id": "mcG-Vtz0T48GMP3FjaXjt",
	"Name": "upload.bin",
	"FileMeta": {"width": 512, "keep": true},
	"UserMeta": {"caption": "carved"},
	"SensitiveMarks": [],
	"MimeType": "application/octet-stream",
	"Hash": "2c84a88b55b1766d28f854cf721d58eb",
	"Size": 535381,
	"HasCompression": false,
	"Url": "https://drive.example/files/upload.bin",
	"Width": 512,
	"Height": 512
}`

const legacyVerification = `{
	"Type": 1,
	"Title": "Official",
	"Description": "Verified by staff",
	"VerifiedBy": "staff"
}`

// TestRealmFromEntityDecodesLegacyArtwork reproduces the wire regression where
// a PascalCase blob kept only the keys whose names matched case-insensitively
// (Id, Name, Hash, Size) and emitted file_meta/user_meta as {}.
func TestRealmFromEntityDecodesLegacyArtwork(t *testing.T) {
	raw := datatypes.JSON(legacyArtwork)
	verification := datatypes.JSON(legacyVerification)
	entity := &RealmEntity{
		ID:           uuid.New(),
		Picture:      &raw,
		Background:   &raw,
		Verification: &verification,
	}

	wire, err := json.Marshal(RealmFromEntity(entity))
	if err != nil {
		t.Fatalf("marshal realm: %v", err)
	}
	var decoded struct {
		Picture      map[string]any `json:"picture"`
		Background   map[string]any `json:"background"`
		Verification map[string]any `json:"verification"`
	}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal realm: %v", err)
	}

	for name, reference := range map[string]map[string]any{
		"picture":    decoded.Picture,
		"background": decoded.Background,
	} {
		if reference["id"] != "mcG-Vtz0T48GMP3FjaXjt" {
			t.Errorf("%s.id = %v", name, reference["id"])
		}
		if reference["mime_type"] != "application/octet-stream" {
			t.Errorf("%s.mime_type = %v, want application/octet-stream", name, reference["mime_type"])
		}
		if _, legacy := reference["Id"]; legacy {
			t.Errorf("%s still carries the legacy Id key", name)
		}
		fileMeta, _ := reference["file_meta"].(map[string]any)
		if fileMeta["width"] != float64(512) {
			t.Errorf("%s.file_meta.width = %v, want 512", name, fileMeta["width"])
		}
		userMeta, _ := reference["user_meta"].(map[string]any)
		if userMeta["caption"] != "carved" {
			t.Errorf("%s.user_meta.caption = %v, want carved", name, userMeta["caption"])
		}
	}

	if decoded.Verification["type"] != float64(1) {
		t.Errorf("verification.type = %v, want 1", decoded.Verification["type"])
	}
	if decoded.Verification["verified_by"] != "staff" {
		t.Errorf("verification.verified_by = %v, want staff", decoded.Verification["verified_by"])
	}
}

// TestRealmToProtoCarriesFileMeta covers the gRPC path Sphere and Messager use
// for realm snapshots: the DyCloudFile bytes must carry the metadata, because a
// dropped FileMeta re-emits as {} downstream.
func TestRealmToProtoCarriesFileMeta(t *testing.T) {
	raw := datatypes.JSON(legacyArtwork)
	entity := &RealmEntity{ID: uuid.New(), Picture: &raw, Background: &raw}

	proto := realmToProto(entity)
	if proto.GetPicture() == nil || proto.GetBackground() == nil {
		t.Fatal("picture/background missing from proto")
	}
	var fileMeta, userMeta map[string]any
	if err := json.Unmarshal(proto.GetBackground().GetFileMeta(), &fileMeta); err != nil {
		t.Fatalf("decode file_meta: %v", err)
	}
	if fileMeta["width"] != float64(512) {
		t.Errorf("file_meta.width = %v, want 512", fileMeta["width"])
	}
	if err := json.Unmarshal(proto.GetBackground().GetUserMeta(), &userMeta); err != nil {
		t.Fatalf("decode user_meta: %v", err)
	}
	if userMeta["caption"] != "carved" {
		t.Errorf("user_meta.caption = %v, want carved", userMeta["caption"])
	}
	if proto.GetBackground().GetMimeType() != "application/octet-stream" {
		t.Errorf("mime_type = %q, want application/octet-stream", proto.GetBackground().GetMimeType())
	}
}
