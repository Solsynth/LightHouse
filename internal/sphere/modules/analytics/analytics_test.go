package analytics

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

func newTestHandler(t *testing.T) (*handler, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("sqlite open: %v", err)
	}
	if err := db.AutoMigrate(&store.AnalyticsEventEntity{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	h := &handler{
		db:            db,
		log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		events:        make(chan ports.AnalyticsEvent, 32),
		batchSize:     8,
		retention:     24 * time.Hour,
		ipPepper:      "test-pepper",
		cookieName:    "sol_visitor",
		trustedProxy:  1,
		flushInterval: time.Second,
	}
	return h, db
}

// Enqueue -> flush must persist one row per event with the visitor classified,
// the address hashed and the account resolved.
func TestFlushPersistsClassifiedEvents(t *testing.T) {
	h, db := newTestHandler(t)
	accountID := uuid.New()
	visitorID := uuid.NewString()
	events := []ports.AnalyticsEvent{
		{
			Kind: "post", ResourceID: "post-1", Event: "view",
			Visitor: ports.AnalyticsVisitor{
				AccountID: accountID.String(),
				VisitorID: visitorID,
				IP:        "203.0.113.7",
				UserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
				Referrer:  "https://example.com/feed",
			},
		},
		{
			Kind: "post", ResourceID: "post-1", Event: "view",
			Visitor: ports.AnalyticsVisitor{
				VisitorID: uuid.NewString(),
				IP:        "203.0.113.7",
				UserAgent: "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
			},
		},
		{
			Kind: "post", ResourceID: "post-1", Event: "reaction.add",
			Visitor: ports.AnalyticsVisitor{AccountID: accountID.String(), VisitorID: visitorID, IP: "203.0.113.7"},
		},
	}
	for _, event := range events {
		if err := h.enqueue(event); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	if err := h.flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	var rows []store.AnalyticsEventEntity
	if err := db.Order("created_at").Find(&rows).Error; err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != len(events) {
		t.Fatalf("rows = %d, want %d", len(rows), len(events))
	}
	if rows[0].ClientClass != model.AnalyticsClientHuman || rows[0].ClientName != "Chrome" {
		t.Fatalf("human row class/name = %d/%q", rows[0].ClientClass, rows[0].ClientName)
	}
	if rows[1].ClientClass != model.AnalyticsClientBot || rows[1].ClientName != "Googlebot" {
		t.Fatalf("bot row class/name = %d/%q", rows[1].ClientClass, rows[1].ClientName)
	}
	if rows[0].AccountID == nil || *rows[0].AccountID != accountID {
		t.Fatalf("account id = %v, want %s", rows[0].AccountID, accountID)
	}
	if rows[1].AccountID != nil {
		t.Fatalf("anonymous row has account id %v", rows[1].AccountID)
	}
	if rows[0].IPHash == "" || rows[0].IPHash != rows[1].IPHash {
		t.Fatalf("ip hashes differ for the same address: %q vs %q", rows[0].IPHash, rows[1].IPHash)
	}
	if strings.Contains(rows[0].IPHash, "203.0.113.7") {
		t.Fatal("ip hash leaks the raw address")
	}
}

func TestSummaryAggregatesAndSplitsBots(t *testing.T) {
	h, db := newTestHandler(t)
	now := time.Now().UTC()
	rows := []store.AnalyticsEventEntity{
		{ID: uuid.New(), Kind: "post", ResourceID: "post-1", Event: "view", AccountID: new(uuid.New()), ClientClass: model.AnalyticsClientHuman, CreatedAt: now},
		{ID: uuid.New(), Kind: "post", ResourceID: "post-1", Event: "view", AccountID: new(uuid.New()), ClientClass: model.AnalyticsClientHuman, CreatedAt: now.Add(-time.Hour)},
		{ID: uuid.New(), Kind: "post", ResourceID: "post-1", Event: "view", VisitorID: uuid.NewString(), ClientClass: model.AnalyticsClientBot, CreatedAt: now},
		{ID: uuid.New(), Kind: "post", ResourceID: "post-1", Event: "reaction.add", AccountID: new(uuid.New()), ClientClass: model.AnalyticsClientHuman, CreatedAt: now},
		{ID: uuid.New(), Kind: "post", ResourceID: "post-2", Event: "view", ClientClass: model.AnalyticsClientHuman, CreatedAt: now},
		{ID: uuid.New(), Kind: "post", ResourceID: "post-1", Event: "view", ClientClass: model.AnalyticsClientHuman, CreatedAt: now.AddDate(0, 0, -90)},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	summary, err := h.summary(context.Background(), "post", "post-1", 30)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if len(summary.Events) != 2 {
		t.Fatalf("event groups = %d, want 2: %+v", len(summary.Events), summary.Events)
	}
	views := summary.Events[0]
	if views.Event != "view" || views.Total != 3 || views.Humans != 2 || views.Bots != 1 {
		t.Fatalf("view summary = %+v", views)
	}
	if views.UniqueVisitors != 3 {
		t.Fatalf("unique visitors = %d, want 3", views.UniqueVisitors)
	}
	if len(summary.Daily) == 0 {
		t.Fatal("daily series is empty")
	}
	// The 90-day-old row is outside the window.
	var total int64
	for _, point := range summary.Daily {
		total += point.Total
	}
	if total != 4 {
		t.Fatalf("daily total = %d, want 4", total)
	}
}

// The ingest endpoint issues the visitor cookie and records the request
// identity for anonymous clients.
func TestIngestEventIssuesVisitorCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, db := newTestHandler(t)
	router := gin.New()
	router.POST("/analytics/events", h.ingestEvent)

	body := `{"kind":"post","resource_id":"post-9","event":"view"}`
	request := httptest.NewRequest(http.MethodPost, "/analytics/events", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "curl/8.5.0")
	request.Header.Set("X-Forwarded-For", "198.51.100.4")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("no visitor cookie issued")
	}
	if err := h.flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	var row store.AnalyticsEventEntity
	if err := db.Where("resource_id = ?", "post-9").First(&row).Error; err != nil {
		t.Fatalf("row: %v", err)
	}
	if row.VisitorID != cookies[0].Value {
		t.Fatalf("stored visitor id = %q, cookie = %q", row.VisitorID, cookies[0].Value)
	}
	if row.ClientClass != model.AnalyticsClientBot || row.ClientName != "curl" {
		t.Fatalf("client = %d/%q, want bot/curl", row.ClientClass, row.ClientName)
	}
}

// An event without its identity fields is rejected before it is buffered.
func TestEnqueueRejectsIncompleteEvents(t *testing.T) {
	h, db := newTestHandler(t)
	if err := h.enqueue(ports.AnalyticsEvent{Kind: "post", Event: "view"}); err == nil {
		t.Fatal("expected an error for a missing resource id")
	}
	if err := h.enqueue(ports.AnalyticsEvent{Kind: "post", ResourceID: "post-1"}); err == nil {
		t.Fatal("expected an error for a missing event name")
	}
	if err := h.flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	var count int64
	db.Model(&store.AnalyticsEventEntity{}).Count(&count)
	if count != 0 {
		t.Fatalf("rows = %d, want 0", count)
	}
}

// Retention prunes expired rows and keeps fresh ones.
func TestRetentionSweepDeletesExpiredEvents(t *testing.T) {
	h, db := newTestHandler(t)
	now := time.Now().UTC()
	rows := []store.AnalyticsEventEntity{
		{ID: uuid.New(), Kind: "post", ResourceID: "old", Event: "view", CreatedAt: now.Add(-48 * time.Hour)},
		{ID: uuid.New(), Kind: "post", ResourceID: "fresh", Event: "view", CreatedAt: now},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := h.retentionSweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	var remaining []store.AnalyticsEventEntity
	if err := db.Find(&remaining).Error; err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ResourceID != "fresh" {
		t.Fatalf("remaining = %+v", remaining)
	}
}

func TestSummaryReturnsEmptySlices(t *testing.T) {
	h, _ := newTestHandler(t)
	summary, err := h.summary(context.Background(), "post", "missing", 7)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"events":[]`) || !strings.Contains(string(encoded), `"daily":[]`) {
		t.Fatalf("empty summary does not serialize empty arrays: %s", encoded)
	}
}
