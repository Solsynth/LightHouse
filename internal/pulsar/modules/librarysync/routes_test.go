package librarysync

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/config"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/store"
	platformapp "src.solsynth.dev/sosys/lighthouse/shared/platform/app"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
)

// testEngine mounts the three endpoints behind the dev account header, which is
// the only guard chain a test can satisfy without a Stargate.
func testEngine(t *testing.T) (*gin.Engine, uuid.UUID) {
	t.Helper()
	db := testDB(t)
	cfg := &config.Config{}
	cfg.LibrarySync.MaxBatch = 500
	cfg.LibrarySync.RetentionDays = 90
	cfg.LibrarySync.DevAccountHeader = "X-Dev-Account"
	services := &core.Services{
		Env:    &platformapp.Env{DB: db},
		Config: cfg,
	}
	h := &handler{core: services, store: store.New(db), log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	engine := httpx.New(httpx.Options{}).Engine
	guard := devAccountHeader("X-Dev-Account")
	group := engine.Group("/api/library")
	group.Handle(http.MethodPost, "/push", guard, h.push)
	group.Handle(http.MethodGet, "/pull", guard, h.pull)
	group.Handle(http.MethodGet, "/status", guard, h.status)
	return engine, uuid.New()
}

func do(t *testing.T, engine *gin.Engine, account uuid.UUID, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Dev-Account", account.String())
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

// A push then a pull round-trips one track, and the follow-up pull is empty.
func TestPushThenPull(t *testing.T) {
	engine, account := testEngine(t)

	recorder := do(t, engine, account, http.MethodPost, "/api/library/push", PushRequest{
		DeviceID: "dev-a",
		Changes:  []Change{trackChange("coldplay|yellow", "Yellow", "2026-01-01T00:00:00Z")},
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("push status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var pushed PushResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &pushed); err != nil {
		t.Fatalf("decode push: %v", err)
	}
	if pushed.Rev != 1 || len(pushed.Rejected) != 0 {
		t.Fatalf("push=%+v, want rev 1 and no rejections", pushed)
	}

	recorder = do(t, engine, account, http.MethodGet, "/api/library/pull?since=0", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("pull status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var page PullResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode pull: %v", err)
	}
	if page.Rev != 1 || page.HasMore {
		t.Fatalf("pull=%+v, want rev 1 without more", page)
	}
	if len(page.Tracks) != 1 || page.Tracks[0].Fingerprint != "coldplay|yellow" {
		t.Fatalf("tracks=%+v, want the pushed track", page.Tracks)
	}

	recorder = do(t, engine, account, http.MethodGet, "/api/library/pull?since=1", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("follow-up pull status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode follow-up pull: %v", err)
	}
	if page.Rev != 1 || page.HasMore || len(page.Tracks) != 0 || len(page.Links) != 0 || len(page.Lyrics) != 0 || len(page.Plays) != 0 {
		t.Fatalf("follow-up pull=%+v, want an empty page at rev 1", page)
	}
}

// A tombstone travels through the pull's removed map, not the track array.
func TestPullReportsTombstone(t *testing.T) {
	engine, account := testEngine(t)

	do(t, engine, account, http.MethodPost, "/api/library/push", PushRequest{
		DeviceID: "dev-a",
		Changes:  []Change{trackChange("coldplay|yellow", "Yellow", "2026-01-02T00:00:00Z")},
	})
	do(t, engine, account, http.MethodPost, "/api/library/push", PushRequest{
		DeviceID: "dev-a",
		Changes: []Change{{
			Kind:      ChangeDelete,
			Entity:    EntityTrack,
			DeletedAt: new("2026-01-03T00:00:00Z"),
			Track:     &TrackRecord{Fingerprint: "coldplay|yellow"},
		}},
	})

	recorder := do(t, engine, account, http.MethodGet, "/api/library/pull?since=1", nil)
	var page PullResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode pull: %v", err)
	}
	if len(page.Tracks) != 0 {
		t.Fatalf("tracks=%+v, want none for a tombstoned row", page.Tracks)
	}
	removed := page.Removed[EntityTrack]
	if len(removed) != 1 {
		t.Fatalf("removed=%v, want one track removal", page.Removed)
	}
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(removed[0]), &decoded); err != nil {
		t.Fatalf("decode removal: %v", err)
	}
	if decoded["fingerprint"] != "coldplay|yellow" || decoded["deleted_at"] != "2026-01-03T00:00:00Z" {
		t.Fatalf("removal=%+v, want the natural key and its clock", decoded)
	}
}

// An unknown entity fails the whole batch before any row is written.
func TestPushRejectsUnknownEntity(t *testing.T) {
	engine, account := testEngine(t)

	recorder := do(t, engine, account, http.MethodPost, "/api/library/push", PushRequest{
		DeviceID: "dev-a",
		Changes:  []Change{{Kind: ChangeUpsert, Entity: "album"}},
	})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", recorder.Code, recorder.Body.String())
	}
	var failure map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &failure); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if failure["code"] != "VALIDATION_ERROR" {
		t.Fatalf("error=%+v, want VALIDATION_ERROR", failure)
	}
}

// The status endpoint counts devices from the rows the pushes stamped.
func TestStatusReportsDevices(t *testing.T) {
	engine, account := testEngine(t)

	for _, device := range []string{"dev-a", "dev-b"} {
		do(t, engine, account, http.MethodPost, "/api/library/push", PushRequest{
			DeviceID: device,
			Changes:  []Change{trackChange("coldplay|yellow|"+device, "Yellow", "2026-01-01T00:00:00Z")},
		})
	}

	recorder := do(t, engine, account, http.MethodGet, "/api/library/status", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var status StatusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.Tracks != 2 || status.DeviceCount != 2 || status.Rev != 2 {
		t.Fatalf("status=%+v, want 2 tracks, 2 devices, rev 2", status)
	}
	if status.LastDevice != "dev-b" {
		t.Fatalf("last_device=%q, want dev-b", status.LastDevice)
	}
}
