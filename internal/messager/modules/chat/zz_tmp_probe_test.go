package chat

import (
	"context"
	"log/slog"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/store"
)

func TestTmpFinalize(t *testing.T) {
	db, err := gorm.Open(postgres.Open("host=localhost port=5432 user=postgres password=postgres dbname=dyson_messager sslmode=disable"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	meta := store.ToJSON(map[string]any{
		"placeholder_kind":       "streaming",
		"placeholder_content":    "Hel",
		"placeholder_expires_at": 1793189865283,
	})
	if err := db.Model(&store.ChatMessageEntity{}).Where("id = ?", "66666666-6666-6666-6666-666666666666").
		Updates(map[string]any{"type": "placeholder", "content": nil, "meta": meta}).Error; err != nil {
		t.Fatal(err)
	}
	i := &instance{db: db, log: slog.Default(), st: store.New(db)}
	ctx := context.Background()
	var entity store.ChatMessageEntity
	if err := db.Where("id = ?", "66666666-6666-6666-6666-666666666666").First(&entity).Error; err != nil {
		t.Fatal(err)
	}
	room, err := i.st.GetRoom(ctx, entity.ChatRoomID)
	if err != nil {
		t.Fatal(err)
	}
	var member store.ChatMemberEntity
	if err := db.Where("id = ?", entity.SenderID).First(&member).Error; err != nil {
		t.Fatal(err)
	}
	i.finalizePlaceholderWith(ctx, room, &member, &entity, "Hello finalized", nil)
	t.Logf("entity meta after=%q type=%s", string(entity.Meta), entity.Type)
	var sync store.ChatMessageEntity
	if err := db.Where("type = ?", "messages.sync.finalize").Order("created_at desc").First(&sync).Error; err != nil {
		t.Fatal(err)
	}
	t.Logf("sync meta=%s content=%v", string(sync.Meta), *sync.Content)
}
