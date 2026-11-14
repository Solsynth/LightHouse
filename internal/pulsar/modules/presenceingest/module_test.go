package presenceingest

import (
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/store"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/app"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/presencecmd"
)

func TestStartAndEndProviderSession(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:presence_ingest?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.CatalogItem{}, &model.ActiveSession{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	services := &core.Services{
		Env:   &app.Env{DB: db},
		Store: store.New(db),
	}
	i := &instance{core: services}
	accountID := uuid.New()
	title := "Counter-Strike 2"

	if err := i.start(accountID, "steam", presencecmd.SessionSpec{
		Type:         model.DomainGaming,
		Provider:     "steam",
		ReferenceID:  new("730"),
		Title:        &title,
		CatalogKey:   "730",
		CatalogName:  &title,
		LeaseMinutes: 10,
	}); err != nil {
		t.Fatalf("start: %v", err)
	}

	var session model.ActiveSession
	if err := db.Where("account_id = ? AND manual_id = ?", accountID, "steam").First(&session).Error; err != nil {
		t.Fatalf("load session: %v", err)
	}
	if session.Title != title || session.Domain != model.DomainGaming {
		t.Fatalf("session = %+v", session)
	}
	if session.CatalogID == nil {
		t.Fatal("catalog id not attached")
	}
	var catalog model.CatalogItem
	if err := db.First(&catalog, "id = ?", *session.CatalogID).Error; err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	if catalog.Provider != "steam" || catalog.ProviderKey != "730" {
		t.Fatalf("catalog = %+v", catalog)
	}

	if err := i.end(accountID, "steam"); err != nil {
		t.Fatalf("end: %v", err)
	}
	if err := db.Where("account_id = ? AND manual_id = ?", accountID, "steam").First(&session).Error; err == nil {
		t.Fatal("session still active after end")
	}
}
