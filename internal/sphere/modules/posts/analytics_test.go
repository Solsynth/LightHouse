package posts

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

// fakeAnalyticsPort captures recorded events and answers summaries.
type fakeAnalyticsPort struct {
	mu       sync.Mutex
	events   []ports.AnalyticsEvent
	lastKind string
	lastID   string
	lastDays int
	summary  *model.AnalyticsSummary
}

func (f *fakeAnalyticsPort) Record(_ context.Context, event ports.AnalyticsEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
	return nil
}

func (f *fakeAnalyticsPort) Summary(_ context.Context, kind, resourceID string, days int) (*model.AnalyticsSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastKind = kind
	f.lastID = resourceID
	f.lastDays = days
	if f.summary != nil {
		return f.summary, nil
	}
	return &model.AnalyticsSummary{Kind: kind, ResourceID: resourceID, WindowDays: days}, nil
}

func analyticsTestHandler(t *testing.T) (*handler, *fakeAnalyticsPort) {
	t.Helper()
	db := testNotificationDB(t)
	reg := module.NewPortRegistry()
	fake := &fakeAnalyticsPort{}
	module.Provide[ports.Analytics](reg, ports.PortAnalytics, "test", module.KindFeature, fake)
	return &handler{db: db, ports: reg, log: slog.Default()}, fake
}

// A viewed post records one analytics event with the request identity.
func TestEnqueueViewRecordsAnalyticsEvent(t *testing.T) {
	h, fake := analyticsTestHandler(t)
	postID := uuid.New()
	visitor := ports.AnalyticsVisitor{VisitorID: uuid.NewString(), UserAgent: "curl/8.5.0"}
	h.enqueueView(context.Background(), postID, &viewer{visitor: visitor})

	if len(fake.events) != 1 {
		t.Fatalf("events = %d, want 1", len(fake.events))
	}
	event := fake.events[0]
	if event.Kind != "post" || event.ResourceID != postID.String() || event.Event != "view" {
		t.Fatalf("event = %+v", event)
	}
	if event.Visitor.VisitorID != visitor.VisitorID {
		t.Fatalf("event visitor = %+v, want %+v", event.Visitor, visitor)
	}
}

// The owner of the publisher may read the post's analytics summary.
func TestGetPostAnalyticsAllowsPublisherOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, fake := analyticsTestHandler(t)
	accountID := uuid.New()
	publisherID := uuid.New()
	postID := uuid.New()
	if err := h.db.Create(&store.PublisherEntity{
		ID:        publisherID,
		Type:      int(model.PublisherIndividual),
		Name:      "sheep",
		Nick:      "Sheep",
		AccountID: &accountID,
	}).Error; err != nil {
		t.Fatalf("create publisher: %v", err)
	}
	content := "hello"
	now := time.Now().UTC()
	if err := h.db.Create(&store.PostEntity{
		ID:          postID,
		PublisherID: publisherID,
		Content:     &content,
		Visibility:  int(model.VisibilityPublic),
		PublishedAt: &now,
	}).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	fake.summary = &model.AnalyticsSummary{
		Kind: "post", ResourceID: postID.String(), WindowDays: 7,
		Events: []model.AnalyticsEventSummary{{Event: "view", Total: 4, Humans: 3, Bots: 1, UniqueVisitors: 2}},
		Daily:  []model.AnalyticsDailyPoint{},
	}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/posts/"+postID.String()+"/analytics?days=7", nil)
	c.Params = gin.Params{{Key: "id", Value: postID.String()}}
	c.Set(authn.ContextKeyUser, &gen.DyAccount{Id: accountID.String()})

	h.getPostAnalytics(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if fake.lastKind != "post" || fake.lastID != postID.String() || fake.lastDays != 7 {
		t.Fatalf("summary call = %s/%s/%d", fake.lastKind, fake.lastID, fake.lastDays)
	}
	var body model.AnalyticsSummary
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Events) != 1 || body.Events[0].Total != 4 {
		t.Fatalf("body = %+v", body)
	}
	// The response sets the anonymous visitor cookie for consistency across
	// routes; the analytics call itself is covered above.
	if len(recorder.Result().Cookies()) == 0 {
		t.Fatal("no visitor cookie on the analytics response")
	}
}

// Accounts that do not own the publisher are rejected before the summary runs.
func TestGetPostAnalyticsRejectsOutsiders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, fake := analyticsTestHandler(t)
	publisherID := uuid.New()
	postID := uuid.New()
	if err := h.db.Create(&store.PublisherEntity{
		ID:   publisherID,
		Type: int(model.PublisherIndividual),
		Name: "sheep",
		Nick: "Sheep",
	}).Error; err != nil {
		t.Fatalf("create publisher: %v", err)
	}
	now := time.Now().UTC()
	if err := h.db.Create(&store.PostEntity{
		ID:          postID,
		PublisherID: publisherID,
		Visibility:  int(model.VisibilityPublic),
		PublishedAt: &now,
	}).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/posts/"+postID.String()+"/analytics", nil)
	c.Params = gin.Params{{Key: "id", Value: postID.String()}}
	c.Set(authn.ContextKeyUser, &gen.DyAccount{Id: uuid.NewString()})

	h.getPostAnalytics(c)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	if fake.lastID != "" {
		t.Fatalf("summary ran for an outsider: %s", fake.lastID)
	}
}

// Anonymous callers get 401, not a summary.
func TestGetPostAnalyticsRequiresAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _ := analyticsTestHandler(t)
	postID := uuid.New()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/posts/"+postID.String()+"/analytics", nil)
	c.Params = gin.Params{{Key: "id", Value: postID.String()}}

	h.getPostAnalytics(c)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}
