package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// legacyStickerImage is the image column of a stickers row written by the C#
// fleet: System.Text.Json PascalCase keys. The Solian client decodes the wire
// object with snake_case keys and casts `id` to a non-null String, so the
// mapping has to normalize the blob before it is serialized.
const legacyStickerImage = `{
	"Id": "file-image-1",
	"Name": "wave.webp",
	"FileMeta": {"ratio": 1.0},
	"UserMeta": {},
	"SensitiveMarks": [],
	"MimeType": "image/webp",
	"Hash": "sha256:abc",
	"Size": 20,
	"HasCompression": false,
	"HasThumbnail": true,
	"Url": "https://drive.example/files/wave.webp",
	"Width": 2,
	"Height": 2
}`

func TestStickerToModelNormalizesLegacyFileReferences(t *testing.T) {
	created := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	packID := uuid.New()
	sticker := StickerToModel(&StickerEntity{
		ID:         uuid.New(),
		EntityBase: EntityBase{CreatedAt: created, UpdatedAt: created},
		Slug:       "wave",
		Image:      datatypes.JSON(legacyStickerImage),
		PackID:     packID,
	})
	pack := StickerPackToModel(&StickerPackEntity{
		ID:         uuid.New(),
		EntityBase: EntityBase{CreatedAt: created, UpdatedAt: created},
		Icon:       datatypes.JSON(legacyStickerImage),
		Name:       "Probe Pack",
		Prefix:     "probe",
	})

	wire, err := json.Marshal(struct {
		Sticker any `json:"sticker"`
		Pack    any `json:"pack"`
	}{Sticker: sticker, Pack: pack})
	if err != nil {
		t.Fatalf("marshal wire: %v", err)
	}
	var decoded struct {
		Sticker struct {
			Image map[string]any `json:"image"`
		} `json:"sticker"`
		Pack struct {
			Icon map[string]any `json:"icon"`
		} `json:"pack"`
	}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}

	for name, reference := range map[string]map[string]any{
		"image": decoded.Sticker.Image,
		"icon":  decoded.Pack.Icon,
	} {
		if reference["id"] != "file-image-1" {
			t.Errorf("%s.id = %v, want file-image-1", name, reference["id"])
		}
		if _, legacy := reference["Id"]; legacy {
			t.Errorf("%s still carries the legacy Id key", name)
		}
		if _, ok := reference["file_meta"]; !ok {
			t.Errorf("%s.file_meta is missing", name)
		}
	}
}

func TestPostToModelMapsAccountID(t *testing.T) {
	postID := uuid.New()
	pubID := uuid.New()
	accID := uuid.New()
	title := "Post with creator audit"
	entity := &PostEntity{
		ID:          postID,
		PublisherID: pubID,
		AccountID:   &accID,
		Title:       &title,
	}
	model := PostToModel(entity)
	if model.AccountID == nil || *model.AccountID != accID.String() {
		t.Errorf("model.AccountID = %v, want %s", model.AccountID, accID.String())
	}
}
