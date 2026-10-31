package accountevents

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// TestUserEventFromEntityDecodesLegacyArtwork pins the calendar read path to
// the same legacy-key normalization as profiles and realms: a PascalCase blob
// written by the old fleet must not surface as file_meta/user_meta {}.
func TestUserEventFromEntityDecodesLegacyArtwork(t *testing.T) {
	legacy := datatypes.JSON(`{
		"Id": "file-1",
		"Name": "cover.png",
		"FileMeta": {"width": 64},
		"UserMeta": {"caption": "cover"},
		"MimeType": "image/png",
		"Hash": "h",
		"Size": 9
	}`)
	event := userEventFromEntity(&UserCalendarEventEntity{
		ID:         uuid.New(),
		AccountID:  uuid.New(),
		Icon:       legacy,
		Background: legacy,
	})

	wire, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	var decoded struct {
		Icon       map[string]any `json:"icon"`
		Background map[string]any `json:"background"`
	}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	for name, reference := range map[string]map[string]any{
		"icon":       decoded.Icon,
		"background": decoded.Background,
	} {
		if reference["id"] != "file-1" {
			t.Errorf("%s.id = %v, want file-1", name, reference["id"])
		}
		if reference["mime_type"] != "image/png" {
			t.Errorf("%s.mime_type = %v, want image/png", name, reference["mime_type"])
		}
		fileMeta, _ := reference["file_meta"].(map[string]any)
		if fileMeta["width"] != float64(64) {
			t.Errorf("%s.file_meta.width = %v, want 64", name, fileMeta["width"])
		}
		userMeta, _ := reference["user_meta"].(map[string]any)
		if userMeta["caption"] != "cover" {
			t.Errorf("%s.user_meta.caption = %v, want cover", name, userMeta["caption"])
		}
	}
}
