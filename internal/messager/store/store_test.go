package store

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// legacyRoomArt is the picture/background column of a chat_rooms row written by
// the C# fleet: System.Text.Json PascalCase keys. The Island SDK parses these
// objects as SnCloudFileReference and casts `id` to a non-null String.
const legacyRoomArt = `{
	"Id": "file-room-art",
	"Name": "room.png",
	"FileMeta": {"ratio": 1.5},
	"UserMeta": {},
	"SensitiveMarks": [],
	"MimeType": "image/png",
	"Hash": "sha256:abc",
	"Size": 30,
	"HasCompression": false,
	"HasThumbnail": true,
	"Url": "https://drive.example/files/room.png",
	"Width": 3,
	"Height": 2
}`

func TestRoomToModelNormalizesLegacyArt(t *testing.T) {
	room := RoomToModel(&ChatRoomEntity{
		ID:         uuid.New(),
		Picture:    datatypes.JSON(legacyRoomArt),
		Background: datatypes.JSON(legacyRoomArt),
	})

	wire, err := json.Marshal(room)
	if err != nil {
		t.Fatalf("marshal room: %v", err)
	}
	var decoded struct {
		Picture    map[string]any `json:"picture"`
		Background map[string]any `json:"background"`
	}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal room: %v", err)
	}

	for name, reference := range map[string]map[string]any{
		"picture":    decoded.Picture,
		"background": decoded.Background,
	} {
		if reference["id"] != "file-room-art" {
			t.Errorf("%s.id = %v, want file-room-art", name, reference["id"])
		}
		if _, legacy := reference["Id"]; legacy {
			t.Errorf("%s still carries the legacy Id key", name)
		}
		if _, ok := reference["file_meta"]; !ok {
			t.Errorf("%s.file_meta is missing", name)
		}
	}
}
