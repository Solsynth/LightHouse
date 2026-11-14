package posts

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/config"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/modules/analytics"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
	platformapp "src.solsynth.dev/sosys/lighthouse/shared/platform/app"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/jobs"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

// The analytics module mounts next to the posts routes without a gin route
// conflict, and an ingested event travels through the scheduled flush into the
// summary the port returns.
func TestAnalyticsModuleMountsAndFlushes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "wiring.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("sqlite open: %v", err)
	}
	if err := db.AutoMigrate(
		&store.AnalyticsEventEntity{},
		&store.PostEntity{},
		&store.PublisherEntity{},
		&store.PostReactionEntity{},
		&store.PostBookmarkEntity{},
		&store.PostWatchPreferenceEntity{},
		&store.PostCollectionEntity{},
		&store.PostCollectionItemEntity{},
		&store.PostFeaturedRecordEntity{},
		&store.PostTagEntity{},
		&store.PostCategoryEntity{},
		&store.PostTagLink{},
		&store.PostCategoryLink{},
		&store.PostAwardEntity{},
		&store.BoostEntity{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}

	cfg := &config.Config{}
	cfg.Analytics.FlushInterval = 1
	cfg.Analytics.BatchSize = 50
	cfg.Analytics.RetentionDays = 180
	cfg.Analytics.VisitorCookie = "sol_visitor"
	log := slog.New(slog.NewTextHandler(testWriter{t: t}, &slog.HandlerOptions{Level: slog.LevelError}))
	svc := &core.Services{
		Env:    &platformapp.Env{DB: db, Jobs: jobs.New(ctx, log), Log: log},
		Config: cfg,
	}

	engine := gin.New()
	host := &module.Host[core.Services]{
		Core:   svc,
		Engine: engine,
		API:    engine.Group("/api"),
		Ports:  module.NewPortRegistry(),
		Log:    log,
	}

	install := func(instance module.Instance[core.Services]) {
		if provider, ok := instance.(module.PortProvider[core.Services]); ok {
			if err := provider.ProvidePorts(ctx, host); err != nil {
				t.Fatalf("provide ports: %v", err)
			}
		}
		if err := instance.Configure(ctx, host); err != nil {
			t.Fatalf("configure: %v", err)
		}
	}
	install(analytics.Factory().New())
	install(Factory().New())

	routes := map[string]bool{}
	for _, route := range engine.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, want := range []string{
		"POST /api/analytics/events",
		"GET /api/posts/:id/analytics",
		"GET /api/posts/:id",
		"GET /api/posts/:id/:slug",
	} {
		if !routes[want] {
			t.Fatalf("route %q is not mounted", want)
		}
	}

	body := `{"kind":"post","resource_id":"post-1","event":"view"}`
	request := httptest.NewRequest(http.MethodPost, "/api/analytics/events", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("ingest status = %d: %s", recorder.Code, recorder.Body.String())
	}

	analyticsPort, ok := module.Get[ports.Analytics](host.Ports, ports.PortAnalytics)
	if !ok {
		t.Fatal("analytics port is not resolvable")
	}
	deadline := time.Now().Add(5 * time.Second)
	var summary *model.AnalyticsSummary
	for time.Now().Before(deadline) {
		summary, err = analyticsPort.Summary(ctx, "post", "post-1", 30)
		if err != nil {
			t.Fatalf("summary: %v", err)
		}
		if len(summary.Events) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(summary.Events) != 1 {
		t.Fatalf("flushed summary = %+v", summary)
	}
	if summary.Events[0].Event != "view" || summary.Events[0].Total != 1 || summary.Events[0].Bots != 1 {
		t.Fatalf("view summary = %+v", summary.Events[0])
	}
	encoded, err := json.Marshal(summary.Daily)
	if err != nil || len(encoded) == 0 {
		t.Fatalf("daily series = %s (%v)", encoded, err)
	}
}

// testWriter forwards scheduled-job error logs into the test log.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}
