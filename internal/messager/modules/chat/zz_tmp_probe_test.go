package chat

import (
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/store"
)

func TestTmpMetaReload(t *testing.T) {
	db, err := gorm.Open(postgres.Open("host=localhost port=5432 user=postgres password=postgres dbname=dyson_messager sslmode=disable"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var e store.ChatMessageEntity
	if err := db.Where("id = ?", "66666666-6666-6666-6666-666666666666").First(&e).Error; err != nil {
		t.Fatal(err)
	}
	t.Logf("before meta=%s content=%v", string(e.Meta), e.Content)
	err = db.Model(&store.ChatMessageEntity{}).Where("id = ?", e.ID).Updates(map[string]any{
		"type": "text", "content": "probe", "meta": nil,
	}).Error
	t.Logf("update err=%v", err)
	if err := db.Where("id = ?", e.ID).First(&e).Error; err != nil {
		t.Fatal(err)
	}
	t.Logf("after meta=%s content=%v type=%s", string(e.Meta), *e.Content, e.Type)
	_ = db.Model(&store.ChatMessageEntity{}).Where("id = ?", e.ID).Update("meta", store.ToJSON(map[string]any{"placeholder_kind": "x"}))
	_ = db.Where("id = ?", e.ID).First(&e).Error
	t.Logf("after2 meta=%s at %s", string(e.Meta), time.Now())
}
