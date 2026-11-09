package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/config"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/store"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/app"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

func setupTestRouter(t *testing.T, testAccountID uuid.UUID) (*gin.Engine, *core.Services) {
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite test db: %v", err)
	}

	_ = db.AutoMigrate(
		&model.CatalogItem{},
		&model.ActiveSession{},
		&model.Scrobble{},
		&model.Collection{},
		&model.CollectionItem{},
	)

	cfg := &config.Config{}
	cfg.HTTP.Port = "5073"
	cfg.GRPC.Port = "7101"
	cfg.Security.MaxTake = 50

	st := store.New(db)
	services := &core.Services{
		Env: &app.Env{
			DB: db,
		},
		Config: cfg,
		Store:  st,
	}

	authMiddleware := func(c *gin.Context) {
		c.Set(authn.ContextKeyUser, &gen.DyAccount{Id: testAccountID.String()})
		c.Next()
	}

	srv := httpx.New(httpx.Options{
		Auth: authMiddleware,
	})

	host := &module.Host[core.Services]{
		Core:    services,
		Engine:  srv.Engine,
		API:     srv.API(),
		Enabled: map[string]bool{"app.solian.pulsar.sessions": true},
	}

	factory := Factory()
	inst := factory.New()
	if err := inst.Configure(context.Background(), host); err != nil {
		t.Fatalf("failed to configure sessions module: %v", err)
	}

	return srv.Engine, services
}

func TestNowPlayingHttpEndpoints(t *testing.T) {
	testAcc := uuid.New()
	router, _ := setupTestRouter(t, testAcc)

	// 1. Report now playing
	sub := "Madeon"
	reqBody := nowPlayingRequest{
		ManualID:        "test:client",
		Domain:          model.DomainMusic,
		Title:           "All My Friends",
		Subtitle:        &sub,
		ProgressSeconds: 15,
		DurationSeconds: 200,
	}
	bodyBytes, _ := json.Marshal(reqBody)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/sessions/now-playing", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var sessionResp model.ActiveSession
	if err := json.Unmarshal(w.Body.Bytes(), &sessionResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if sessionResp.Title != "All My Friends" || sessionResp.AccountID != testAcc {
		t.Fatalf("unexpected session returned: %+v", sessionResp)
	}

	// 2. Get active sessions
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/sessions/active", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from GET /api/sessions/active, got %d", w.Code)
	}
	var listResp []model.ActiveSession
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil || len(listResp) != 1 {
		t.Fatalf("expected 1 active session in list, got %d: %v", len(listResp), err)
	}

	// 3. Pause
	pauseBytes, _ := json.Marshal(pauseRequest{
		ManualID: "test:client",
		IsPaused: true,
	})
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodPost, "/api/sessions/pause", bytes.NewReader(pauseBytes))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from pause, got %d", w.Code)
	}

	// 4. End session
	endBytes, _ := json.Marshal(endSessionRequest{
		ManualID: "test:client",
	})
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodPost, "/api/sessions/end", bytes.NewReader(endBytes))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from end, got %d", w.Code)
	}
}
