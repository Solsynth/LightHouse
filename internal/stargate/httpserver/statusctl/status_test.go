package statusctl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/stargate/grpcclient"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/presence"
	"src.solsynth.dev/sosys/stargate/store"
)

// ─────────────────────────  in-memory fakes  ─────────────────────────

// fakeStore implements DataStore with a status table, an activity table and a
// catalog table, enough to exercise the controller's branches.
type fakeStore struct {
	mu         sync.Mutex
	accountID  uuid.UUID
	statuses   []*model.AccountStatus
	activities []*model.PresenceActivity
	catalog    []*model.PresenceCatalogItem
	isActive   bool
	inserted   int
	updated    int
	cleared    int
}

func newFakeStore(accountID uuid.UUID) *fakeStore {
	return &fakeStore{accountID: accountID}
}

func (f *fakeStore) activeStatusLocked(now time.Time) *model.AccountStatus {
	for _, status := range f.statuses {
		if status.ClearedAt == nil || status.ClearedAt.Time().After(now) {
			return status
		}
	}
	return nil
}

func (f *fakeStore) GetCurrentStatus(_ context.Context, accountID uuid.UUID) (*model.AccountStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	status := f.activeStatusLocked(time.Now().UTC())
	if status == nil || status.AccountId != accountID.String() {
		return nil, store.ErrNotFound
	}
	return cloneStatus(status), nil
}

func (f *fakeStore) GetCurrentStatusByApp(_ context.Context, accountID uuid.UUID, app string) (*model.AccountStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	status := f.activeStatusLocked(time.Now().UTC())
	if status == nil || status.AccountId != accountID.String() || !status.IsAutomated ||
		status.AppIdentifier == nil || *status.AppIdentifier != app {
		return nil, store.ErrNotFound
	}
	return cloneStatus(status), nil
}

func (f *fakeStore) GetCurrentStatuses(ctx context.Context, accountIDs []uuid.UUID) (map[uuid.UUID]*model.AccountStatus, error) {
	out := make(map[uuid.UUID]*model.AccountStatus, len(accountIDs))
	for _, id := range accountIDs {
		status, err := f.GetCurrentStatus(ctx, id)
		if err == nil {
			out[id] = status
		}
	}
	return out, nil
}

func (f *fakeStore) CreateStatus(_ context.Context, status *model.AccountStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now().UTC()
	for _, existing := range f.statuses {
		if existing.ClearedAt == nil || existing.ClearedAt.Time().After(now) {
			cleared := model.NewTime(now)
			existing.ClearedAt = cleared
		}
	}
	if status.Id == "" {
		status.Id = uuid.NewString()
	}
	f.statuses = append(f.statuses, cloneStatus(status))
	return nil
}

func (f *fakeStore) ClearStatus(_ context.Context, accountID, statusID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, status := range f.statuses {
		if status.Id != statusID.String() || status.AccountId != accountID.String() {
			continue
		}
		f.cleared++
		status.ClearedAt = model.NewTime(time.Now().UTC())
		status.IsCustomized = false
		return nil
	}
	return store.ErrNotFound
}

func (f *fakeStore) UpdateStatus(_ context.Context, status *model.AccountStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, existing := range f.statuses {
		if existing.Id != status.Id {
			continue
		}
		f.updated++
		f.statuses[i] = cloneStatus(status)
		return nil
	}
	return store.ErrNotFound
}

func (f *fakeStore) GetActiveActivities(_ context.Context, accountID uuid.UUID) ([]model.PresenceActivity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []model.PresenceActivity{}
	for _, activity := range f.activities {
		if activity.AccountId == accountID.String() && activity.DeletedAt == nil && f.isActive {
			out = append(out, *activity)
		}
	}
	return out, nil
}

func (f *fakeStore) GetActiveActivitiesBatch(ctx context.Context, accountIDs []uuid.UUID) (map[uuid.UUID][]model.PresenceActivity, error) {
	out := make(map[uuid.UUID][]model.PresenceActivity, len(accountIDs))
	for _, id := range accountIDs {
		list, err := f.GetActiveActivities(ctx, id)
		if err != nil {
			return nil, err
		}
		out[id] = list
	}
	return out, nil
}

func (f *fakeStore) ListActivities(_ context.Context, accountID uuid.UUID, filter store.ActivityFilter) ([]model.PresenceActivity, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []model.PresenceActivity{}
	for _, activity := range f.activities {
		if activity.AccountId == accountID.String() && activity.DeletedAt == nil {
			out = append(out, *activity)
		}
	}
	return out, len(out), nil
}

func (f *fakeStore) InsertActivity(_ context.Context, activity *model.PresenceActivity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if activity.Id == "" {
		activity.Id = uuid.NewString()
	}
	f.inserted++
	f.activities = append(f.activities, cloneActivity(activity))
	return nil
}

func (f *fakeStore) UpdateActivity(_ context.Context, activity *model.PresenceActivity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, existing := range f.activities {
		if existing.Id != activity.Id {
			continue
		}
		f.updated++
		f.activities[i] = cloneActivity(activity)
		return nil
	}
	return store.ErrNotFound
}

func (f *fakeStore) GetActivityByID(_ context.Context, accountID, activityID uuid.UUID) (*model.PresenceActivity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, activity := range f.activities {
		if activity.Id == activityID.String() && activity.AccountId == accountID.String() {
			return cloneActivity(activity), nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) GetActivityByManualID(_ context.Context, accountID uuid.UUID, manualID string) (*model.PresenceActivity, error) {
	return f.GetActiveSessionByManualID(context.Background(), accountID, manualID)
}

func (f *fakeStore) GetActiveSessionByManualID(_ context.Context, accountID uuid.UUID, manualID string) (*model.PresenceActivity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, activity := range f.activities {
		if activity.AccountId == accountID.String() && activity.DeletedAt == nil &&
			manualIDOf(activity) == manualID && activity.EndedAt == nil {
			return cloneActivity(activity), nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) DeleteActivity(_ context.Context, accountID, activityID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, activity := range f.activities {
		if activity.Id == activityID.String() && activity.AccountId == accountID.String() {
			activity.DeletedAt = model.NewTime(time.Now().UTC())
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeStore) EndActivitySession(_ context.Context, accountID uuid.UUID, manualID string) (*model.PresenceActivity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, activity := range f.activities {
		if activity.AccountId == accountID.String() && activity.ManualId != nil &&
			*activity.ManualId == manualID && activity.EndedAt == nil {
			now := model.NewTime(time.Now().UTC())
			activity.EndedAt = now
			activity.LeaseExpiresAt = now
			return cloneActivity(activity), nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) GetCatalogItems(_ context.Context, accountID uuid.UUID, _ []string, _, _ string, _ *bool, _, _ int) ([]model.PresenceCatalogItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []model.PresenceCatalogItem{}
	for _, item := range f.catalog {
		if item.AccountId == accountID.String() {
			out = append(out, *item)
		}
	}
	return out, nil
}

func (f *fakeStore) GetCatalogItemByKey(_ context.Context, accountID uuid.UUID, _ *string, catalogKey string) (*model.PresenceCatalogItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, item := range f.catalog {
		if item.AccountId == accountID.String() && item.CatalogKey != nil && *item.CatalogKey == catalogKey {
			clone := *item
			return &clone, nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) GetCatalogStats(context.Context, uuid.UUID, *time.Time, *time.Time, []string) (map[string]int64, error) {
	return map[string]int64{"catalog": 90}, nil
}

func (f *fakeStore) GetTagStats(context.Context, uuid.UUID, *time.Time, *time.Time) (map[string]int64, error) {
	return map[string]int64{"gaming": 90}, nil
}

func (f *fakeStore) UpsertCatalogItem(_ context.Context, item *model.PresenceCatalogItem) (*model.PresenceCatalogItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.catalog {
		if existing.AccountId == item.AccountId && existing.CatalogKey != nil && item.CatalogKey != nil &&
			*existing.CatalogKey == *item.CatalogKey {
			return existing, nil
		}
	}
	if item.Id == "" {
		item.Id = uuid.NewString()
	}
	clone := *item
	f.catalog = append(f.catalog, &clone)
	return &clone, nil
}

func cloneActivity(activity *model.PresenceActivity) *model.PresenceActivity {
	clone := *activity
	if activity.ManualId != nil {
		manualID := *activity.ManualId
		clone.ManualId = &manualID
	}
	clone.Tags = append([]model.PresenceTag(nil), activity.Tags...)
	return &clone
}

// MediaManualID is a nil-safe manual id accessor for the fake (the model keeps
// it optional because a plain activity may have none).
func manualIDOf(activity *model.PresenceActivity) string {
	if activity.ManualId == nil {
		return ""
	}
	return *activity.ManualId
}

// fakePresence implements Presence. online/devices/idle are the fixed answers;
// friendOf returns the friend set used by CanViewDevicePresence.
type fakePresence struct {
	online  bool
	devices []presence.OnlineDevice
	idle    bool
	friends map[uuid.UUID]map[uuid.UUID]bool
}

func (f *fakePresence) ResolveOnlineDevices(context.Context, uuid.UUID) (bool, []presence.OnlineDevice, error) {
	return f.online, f.devices, nil
}

func (f *fakePresence) ResolveOnlineDevicesBatch(_ context.Context, accountIDs []uuid.UUID) (map[uuid.UUID][]presence.OnlineDevice, error) {
	out := make(map[uuid.UUID][]presence.OnlineDevice, len(accountIDs))
	for _, id := range accountIDs {
		if f.online {
			out[id] = f.devices
			continue
		}
		out[id] = []presence.OnlineDevice{}
	}
	return out, nil
}

func (f *fakePresence) GetAccountIdleState(context.Context, uuid.UUID) (bool, *time.Time, error) {
	if !f.idle {
		return false, nil, nil
	}
	since := time.Now().UTC().Add(-15 * time.Minute)
	return true, &since, nil
}

func (f *fakePresence) ApplyPresence(_ context.Context, status *model.AccountStatus, online bool, devices []presence.OnlineDevice, idle bool, idleSince *time.Time) error {
	status.IsOnline = online && status.Type != model.StatusTypeInvisible
	status.IsInvisible = status.Type == model.StatusTypeInvisible
	status.IsNotDisturb = status.Type == model.StatusTypeDoNotDisturb
	if status.IsOnline {
		status.OnlineDevices = devices
	} else {
		status.OnlineDevices = []model.OnlineDevice{}
	}
	status.IsIdle = status.IsOnline && idle
	if status.IsIdle && idleSince != nil {
		status.IdleSince = model.NewTime(*idleSince)
	} else {
		status.IdleSince = nil
	}
	return nil
}

func (f *fakePresence) CanViewDevicePresence(_ context.Context, ownerID uuid.UUID, viewerID *uuid.UUID, includeDevicePresence bool) (bool, error) {
	if includeDevicePresence || viewerID == nil {
		return includeDevicePresence, nil
	}
	if *viewerID == ownerID {
		return true, nil
	}
	return f.friends[*viewerID][ownerID], nil
}

// fakeAccounts implements AccountLookup with a single named account.
type fakeAccounts struct {
	account *model.Account
}

func (f *fakeAccounts) GetAccountWithProfile(_ context.Context, id uuid.UUID) (*model.Account, error) {
	if f.account == nil || f.account.Id != id.String() {
		return nil, store.ErrNotFound
	}
	return f.account, nil
}

func (f *fakeAccounts) GetAccountWithProfileByNameFold(_ context.Context, name string) (*model.Account, error) {
	if f.account == nil || !strings.EqualFold(f.account.Name, name) {
		return nil, store.ErrNotFound
	}
	return f.account, nil
}

func (f *fakeAccounts) GetAccountNameHistoryOwner(context.Context, string) (*model.Account, error) {
	return nil, store.ErrNotFound
}

// fakeBroadcast records the notifications the controllers emit.
type fakeBroadcast struct {
	statuses   []*model.AccountStatus
	activities int
}

func (f *fakeBroadcast) StatusChanged(_ context.Context, _ uuid.UUID, _ string, previous *model.AccountStatus, _ time.Time) error {
	f.statuses = append(f.statuses, previous)
	return nil
}

func (f *fakeBroadcast) ActivitiesChanged(context.Context, uuid.UUID) error {
	f.activities++
	return nil
}

// fakePerm grants everything.
type fakePerm struct{ deny bool }

func (f fakePerm) HasPermission(context.Context, uuid.UUID, string) (bool, error) {
	return !f.deny, nil
}

// ─────────────────────────  harness  ─────────────────────────

var testAccountID = uuid.MustParse("11111111-1111-1111-1111-111111111111")

type harness struct {
	engine    *gin.Engine
	store     *fakeStore
	presence  *fakePresence
	accounts  *fakeAccounts
	broadcast *fakeBroadcast
	user      *model.Account
}

// newHarness mounts the controllers with a viewer attached for every request.
func newHarness(t *testing.T) *harness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	user := &model.Account{Id: testAccountID.String(), Name: "owner"}
	h := &harness{
		store:     newFakeStore(testAccountID),
		presence:  &fakePresence{},
		accounts:  &fakeAccounts{account: user},
		broadcast: &fakeBroadcast{},
		user:      user,
	}
	deps := Deps{
		Store: h.store, Accounts: h.accounts, Perm: fakePerm{},
		Presence: h.presence, Broadcast: h.broadcast,
	}
	h.engine = gin.New()
	api := h.engine.Group("/api")
	api.Use(func(c *gin.Context) {
		if h.user != nil {
			c.Request = c.Request.WithContext(middleware.WithCurrentUser(c.Request.Context(), h.user))
		}
		c.Next()
	})
	Register(api, deps)
	return h
}

func (h *harness) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.engine.ServeHTTP(rec, req)
	return rec
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(rec.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return value
}

// ─────────────────────────  tests  ─────────────────────────

// TestDefaultStatusWireContract pins the synthesized no-row status returned by
// GET /api/accounts/me/statuses: the Dart SDK parses id, created_at and
// updated_at as non-nullable, so the default status must carry all three.
func TestDefaultStatusWireContract(t *testing.T) {
	h := newHarness(t)
	h.presence.online = true

	rec := h.do(t, http.MethodGet, "/api/accounts/me/statuses", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var status map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if id, _ := status["id"].(string); id == "" {
		t.Errorf("id = %v, want a non-empty string", status["id"])
	}
	for _, key := range []string{"created_at", "updated_at"} {
		value, ok := status[key].(string)
		if !ok || value == "" {
			t.Errorf("%s = %v, want a non-null RFC3339 string", key, status[key])
			continue
		}
		if _, err := time.Parse(time.RFC3339, value); err != nil {
			t.Errorf("%s = %q, want RFC3339: %v", key, value, err)
		}
	}
	if _, ok := status["deleted_at"]; !ok {
		t.Error("deleted_at missing; the key must be present even when null")
	}
	if status["label"] != "Online" || status["is_customized"] != false {
		t.Errorf("status = %v, want the Online/uncustomized projection", status)
	}
}

// TestPatchAutomatedRejections pins the two automated-status rejections of
// PATCH /statuses and their order (both answer 400
// PASSPORT_STATUS_AUTOMATED_NO_UPDATE, the missing-row 404 after them).
func TestPatchAutomatedRejections(t *testing.T) {
	h := newHarness(t)

	// (1) automated request carrying an app identifier is always rejected,
	// even when no status exists.
	rec := h.do(t, http.MethodPatch, "/api/accounts/me/statuses",
		`{"attitude":2,"type":0,"is_automated":true,"app_identifier":"app.one"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("automated+app: status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if body := decodeBody[map[string]any](t, rec); body["code"] != "PASSPORT_STATUS_AUTOMATED_NO_UPDATE" {
		t.Fatalf("automated+app: code = %v", body["code"])
	}

	// (2) no active status at all -> 404 PASSPORT_STATUS_NOT_FOUND.
	rec = h.do(t, http.MethodPatch, "/api/accounts/me/statuses", `{"attitude":2,"type":0}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("no status: status = %d, want 404", rec.Code)
	}
	if body := decodeBody[map[string]any](t, rec); body["code"] != "PASSPORT_STATUS_NOT_FOUND" {
		t.Fatalf("no status: code = %v", body["code"])
	}

	// (3) an automated status cannot be edited without its app identifier.
	appIdentifier := "app.one"
	h.store.statuses = []*model.AccountStatus{{
		Id: uuid.NewString(), AccountId: testAccountID.String(), IsAutomated: true,
		AppIdentifier: &appIdentifier, Attitude: model.StatusAttitudeNeutral,
	}}
	rec = h.do(t, http.MethodPatch, "/api/accounts/me/statuses", `{"attitude":1,"type":1}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("automated without app: status = %d, want 400", rec.Code)
	}
	if body := decodeBody[map[string]any](t, rec); body["code"] != "PASSPORT_STATUS_AUTOMATED_NO_UPDATE" {
		t.Fatalf("automated without app: code = %v", body["code"])
	}

	// (4) the first rule is unconditional, so even supplying the matching app
	// identifier cannot update an automated status (C# checks
	// `IsAutomated && AppIdentifier is not null` before anything else).
	rec = h.do(t, http.MethodPatch, "/api/accounts/me/statuses",
		`{"attitude":1,"type":1,"is_automated":true,"app_identifier":"app.one"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("automated with matching app: status = %d, want 400", rec.Code)
	}

	// (5) a manually owned status is patched normally, and the broadcast
	// carries the PRE-write snapshot (passing the post-write row would make
	// the broadcaster treat the change as a no-op).
	h.store.statuses = []*model.AccountStatus{{
		Id: uuid.NewString(), AccountId: testAccountID.String(),
		Attitude: model.StatusAttitudeNeutral,
	}}
	broadcastBefore := len(h.broadcast.statuses)
	rec = h.do(t, http.MethodPatch, "/api/accounts/me/statuses",
		`{"attitude":1,"type":1,"label":"Busy"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch manual status: status = %d (%s)", rec.Code, rec.Body.String())
	}
	status := decodeBody[map[string]any](t, rec)
	if status["label"] != "Busy" || status["attitude"] != float64(1) || status["type"] != float64(1) {
		t.Fatalf("patched status = %v", status)
	}
	if len(h.broadcast.statuses) != broadcastBefore+1 {
		t.Fatalf("broadcasts = %d, want %d", len(h.broadcast.statuses), broadcastBefore+1)
	}
	previous := h.broadcast.statuses[len(h.broadcast.statuses)-1]
	if previous == nil || previous.Attitude != model.StatusAttitudeNeutral || previous.Type != model.StatusTypeDefault {
		t.Fatalf("broadcast previous = %+v, want the pre-write neutral/default status", previous)
	}
}

// TestCreateStatusAutomatedBranches pins the three POST branches: same app
// updates in place, a different app's automated status is replaced, and a
// manual status is never overridden by an automated one.
func TestCreateStatusAutomatedBranches(t *testing.T) {
	h := newHarness(t)

	// Automated create on an empty account makes exactly one row.
	rec := h.do(t, http.MethodPost, "/api/accounts/me/statuses",
		`{"attitude":2,"type":0,"is_automated":true,"app_identifier":"app.one","label":"Listening"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: status = %d (%s)", rec.Code, rec.Body.String())
	}
	if len(h.store.statuses) != 1 {
		t.Fatalf("statuses = %d, want 1", len(h.store.statuses))
	}

	// Same app again: updated in place, still one row, no clear.
	rec = h.do(t, http.MethodPost, "/api/accounts/me/statuses",
		`{"attitude":2,"type":0,"is_automated":true,"app_identifier":"app.one","label":"Paused"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("same app: status = %d", rec.Code)
	}
	if body := decodeBody[map[string]any](t, rec); body["label"] != "Paused" {
		t.Fatalf("same app: label = %v", body["label"])
	}
	if len(h.store.statuses) != 1 || h.store.inserted != 0 {
		t.Fatalf("same app: rows = %d, expected the original row updated in place", len(h.store.statuses))
	}

	// A different app clears the first automated row and inserts its own.
	rec = h.do(t, http.MethodPost, "/api/accounts/me/statuses",
		`{"attitude":1,"type":1,"is_automated":true,"app_identifier":"app.two","label":"Gaming"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("other app: status = %d", rec.Code)
	}
	if h.store.cleared != 1 {
		t.Fatalf("cleared = %d, want the first app's row cleared", h.store.cleared)
	}
	if got, want := len(h.store.statuses), 2; got != want {
		t.Fatalf("rows = %d, want %d", got, want)
	}

	// A manually set status must not be overridden by an automated one.
	h.store.statuses = []*model.AccountStatus{{
		Id: uuid.NewString(), AccountId: testAccountID.String(),
		Label: new("Manual"), Attitude: model.StatusAttitudePositive,
	}}
	rec = h.do(t, http.MethodPost, "/api/accounts/me/statuses",
		`{"attitude":2,"type":0,"is_automated":true,"app_identifier":"app.one","label":"Automated"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("manual override: status = %d", rec.Code)
	}
	if body := decodeBody[map[string]any](t, rec); body["label"] != "Manual" {
		t.Fatalf("manual override: label = %v, want the untouched manual status", body["label"])
	}
	if len(h.store.statuses) != 1 {
		t.Fatalf("manual override: rows = %d, want 1", len(h.store.statuses))
	}
}

// TestStatusClearAndNotFound covers DELETE (204 then 404) and the app filter.
func TestStatusClearAndNotFound(t *testing.T) {
	h := newHarness(t)

	rec := h.do(t, http.MethodDelete, "/api/accounts/me/statuses", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete without status: status = %d, want 404", rec.Code)
	}
	if body := decodeBody[map[string]any](t, rec); body["code"] != "PASSPORT_STATUS_NOT_FOUND" {
		t.Fatalf("delete without status: code = %v", body["code"])
	}

	app := "app.one"
	h.store.statuses = []*model.AccountStatus{{
		Id: uuid.NewString(), AccountId: testAccountID.String(), IsAutomated: true,
		AppIdentifier: &app, Attitude: model.StatusAttitudeNeutral,
	}}
	rec = h.do(t, http.MethodDelete, "/api/accounts/me/statuses?app=app.one", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete with app: status = %d (%s)", rec.Code, rec.Body.String())
	}
	if h.store.cleared != 1 {
		t.Fatalf("cleared = %d, want 1", h.store.cleared)
	}
	// The cleared row is no longer active, so a second delete is a 404.
	rec = h.do(t, http.MethodDelete, "/api/accounts/me/statuses?app=app.one", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second delete: status = %d, want 404", rec.Code)
	}
}

// TestDevicePresenceVisibility pins the anonymous-vs-friend rule of
// GET /api/accounts/:name/statuses: only the owner and its friends see the
// device list; an anonymous read never does.
func TestDevicePresenceVisibility(t *testing.T) {
	h := newHarness(t)
	h.presence.online = true
	h.presence.devices = []presence.OnlineDevice{{
		Id: "device-1", DeviceId: "d1", DeviceName: "Laptop", Platform: 6,
	}}

	// Anonymous: no device list (JSON omits online_devices).
	var anonBody map[string]any
	{
		h.user = nil
		rec := h.do(t, http.MethodGet, "/api/accounts/owner/statuses", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("anonymous: status = %d (%s)", rec.Code, rec.Body.String())
		}
		anonBody = decodeBody[map[string]any](t, rec)
	}
	if _, present := anonBody["online_devices"]; present {
		t.Fatalf("anonymous read leaked online_devices: %v", anonBody["online_devices"])
	}
	if anonBody["is_online"] != true {
		t.Fatalf("anonymous is_online = %v, want true", anonBody["is_online"])
	}

	// Owner: sees the devices.
	h.user = &model.Account{Id: testAccountID.String(), Name: "owner"}
	rec := h.do(t, http.MethodGet, "/api/accounts/owner/statuses", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("owner: status = %d", rec.Code)
	}
	ownerBody := decodeBody[map[string]any](t, rec)
	devices, ok := ownerBody["online_devices"].([]any)
	if !ok || len(devices) != 1 {
		t.Fatalf("owner online_devices = %v, want one device", ownerBody["online_devices"])
	}

	// A friend sees them too; a stranger does not.
	stranger := uuid.New()
	h.presence.friends = map[uuid.UUID]map[uuid.UUID]bool{
		stranger: {testAccountID: true},
	}
	h.user = &model.Account{Id: stranger.String(), Name: "friend"}
	rec = h.do(t, http.MethodGet, "/api/accounts/owner/statuses", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("friend: status = %d", rec.Code)
	}
	friendBody := decodeBody[map[string]any](t, rec)
	if devices, ok := friendBody["online_devices"].([]any); !ok || len(devices) != 1 {
		t.Fatalf("friend online_devices = %v, want one device", friendBody["online_devices"])
	}

	other := uuid.New()
	h.user = &model.Account{Id: other.String(), Name: "stranger"}
	rec = h.do(t, http.MethodGet, "/api/accounts/owner/statuses", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("stranger: status = %d", rec.Code)
	}
	strangerBody := decodeBody[map[string]any](t, rec)
	if _, present := strangerBody["online_devices"]; present {
		t.Fatalf("stranger read leaked online_devices: %v", strangerBody["online_devices"])
	}

	// An unknown name is the C# 400 PASSPORT_ACCOUNT_NOT_FOUND.
	h.user = nil
	rec = h.do(t, http.MethodGet, "/api/accounts/nobody/statuses", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown account: status = %d, want 400", rec.Code)
	}
	if body := decodeBody[map[string]any](t, rec); body["code"] != "PASSPORT_ACCOUNT_NOT_FOUND" {
		t.Fatalf("unknown account: code = %v", body["code"])
	}
}

// TestActivityLeaseBounds pins the 1..60 lease validation on both the
// create/update path and the session start path.
func TestActivityLeaseBounds(t *testing.T) {
	h := newHarness(t)

	for _, lease := range []int{0, -1, 61, 120} {
		rec := h.do(t, http.MethodPost, "/api/activities",
			`{"type":"gaming","manual_id":"lease-test","lease_minutes":`+itoa(lease)+`}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("lease %d: status = %d, want 400 (%s)", lease, rec.Code, rec.Body.String())
		}
	}
	if h.store.inserted != 0 {
		t.Fatalf("inserted = %d after invalid leases", h.store.inserted)
	}

	// 1 and 60 are accepted.
	for _, lease := range []int{1, 60} {
		rec := h.do(t, http.MethodPost, "/api/activities",
			`{"type":"gaming","lease_minutes":`+itoa(lease)+`}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("lease %d: status = %d (%s)", lease, rec.Code, rec.Body.String())
		}
		activity := decodeBody[map[string]any](t, rec)
		if activity["lease_minutes"] != float64(lease) {
			t.Fatalf("lease_minutes = %v, want %d", activity["lease_minutes"], lease)
		}
	}

	// The session start path validates the same bounds.
	rec := h.do(t, http.MethodPost, "/api/activities/sessions/start",
		`{"manual_id":"s1","type":"coding","lease_minutes":61}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("session lease 61: status = %d, want 400", rec.Code)
	}
}

// TestManualIDUpsertDoesNotDuplicate pins POST /api/activities: a second POST
// with the same manual id updates the existing row.
func TestManualIDUpsertDoesNotDuplicate(t *testing.T) {
	h := newHarness(t)

	rec := h.do(t, http.MethodPost, "/api/activities",
		`{"type":"gaming","manual_id":"steam:1","title":"Game A","provider":"Steam"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("first post: status = %d (%s)", rec.Code, rec.Body.String())
	}
	first := decodeBody[map[string]any](t, rec)
	if first["provider"] != "steam" {
		t.Fatalf("provider = %v, want trimmed lowercase steam", first["provider"])
	}
	if len(h.store.activities) != 1 {
		t.Fatalf("activities = %d, want 1", len(h.store.activities))
	}

	rec = h.do(t, http.MethodPost, "/api/activities",
		`{"type":"gaming","manual_id":"steam:1","title":"Game B","provider":"  "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("second post: status = %d", rec.Code)
	}
	second := decodeBody[map[string]any](t, rec)
	if second["id"] != first["id"] {
		t.Fatalf("id changed: %v -> %v", first["id"], second["id"])
	}
	if second["title"] != "Game B" {
		t.Fatalf("title = %v, want the updated title", second["title"])
	}
	if _, present := second["provider"]; present {
		t.Fatalf("blank provider should normalize to nil, got %v", second["provider"])
	}
	if len(h.store.activities) != 1 {
		t.Fatalf("activities = %d after the upsert, want 1", len(h.store.activities))
	}
}

// TestActivityTypeRequired pins the create-without-type rejection.
func TestActivityTypeRequired(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, http.MethodPost, "/api/activities", `{"manual_id":"no-type"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if body := decodeBody[map[string]any](t, rec); body["code"] != "PASSPORT_ACTIVITY_TYPE_REQUIRED" {
		t.Fatalf("code = %v", body["code"])
	}
}

// TestListActivitiesTotalHeader pins X-Total plus the unfiltered fast path.
func TestListActivitiesTotalHeader(t *testing.T) {
	h := newHarness(t)

	// No filters and no includeExpired uses the active-activity read, which
	// carries no X-Total (the C# only sets it on the filtered query).
	h.store.isActive = true
	rec := h.do(t, http.MethodGet, "/api/activities", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("fast path: status = %d", rec.Code)
	}
	if got := rec.Header().Get("X-Total"); got != "" {
		t.Fatalf("fast path X-Total = %q, want unset", got)
	}

	// A filter switches to the paged query and reports the total.
	rec = h.do(t, http.MethodGet, "/api/activities?query=game", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered: status = %d", rec.Code)
	}
	if got := rec.Header().Get("X-Total"); got != "0" {
		t.Fatalf("filtered X-Total = %q, want 0", got)
	}

	// includeExpired=true also pages.
	rec = h.do(t, http.MethodGet, "/api/activities?includeExpired=true", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("includeExpired: status = %d", rec.Code)
	}
	if got := rec.Header().Get("X-Total"); got == "" {
		t.Fatal("includeExpired X-Total is unset, want the count")
	}

	// An unparsable catalogId returns an empty page with X-Total: 0.
	rec = h.do(t, http.MethodGet, "/api/activities?catalogId=not-a-uuid", "")
	if rec.Code != http.StatusOK || rec.Header().Get("X-Total") != "0" {
		t.Fatalf("bad catalogId: status = %d, X-Total = %q", rec.Code, rec.Header().Get("X-Total"))
	}
}

// TestSessionStartRefreshKeepsOneRow pins that re-starting the same manual id
// refreshes the open session instead of inserting a second row, and that a
// catalog key attaches the catalog item.
func TestSessionStartRefreshKeepsOneRow(t *testing.T) {
	h := newHarness(t)

	body := `{"manual_id":"session-1","type":"gaming","title":"Game","catalog_key":"steam:42","catalog_name":"Game"}`
	rec := h.do(t, http.MethodPost, "/api/activities/sessions/start", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("start: status = %d (%s)", rec.Code, rec.Body.String())
	}
	first := decodeBody[map[string]any](t, rec)
	if first["catalog_id"] == nil {
		t.Fatalf("catalog_id missing: %v", first)
	}
	if len(h.store.activities) != 1 || len(h.store.catalog) != 1 {
		t.Fatalf("activities = %d, catalog = %d, want 1/1", len(h.store.activities), len(h.store.catalog))
	}

	rec = h.do(t, http.MethodPost, "/api/activities/sessions/start",
		`{"manual_id":"session-1","type":"gaming","title":"Game updated","catalog_key":"steam:42"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("restart: status = %d (%s)", rec.Code, rec.Body.String())
	}
	second := decodeBody[map[string]any](t, rec)
	if second["id"] != first["id"] {
		t.Fatalf("session id changed on refresh: %v -> %v", first["id"], second["id"])
	}
	if second["title"] != "Game updated" {
		t.Fatalf("title = %v, want the refreshed title", second["title"])
	}
	if len(h.store.activities) != 1 {
		t.Fatalf("activities = %d after refresh, want 1 (no duplicate row)", len(h.store.activities))
	}
	if len(h.store.catalog) != 1 {
		t.Fatalf("catalog = %d, want the existing item reused", len(h.store.catalog))
	}

	// Ending ends the row; a second end is a 404.
	rec = h.do(t, http.MethodPost, "/api/activities/sessions/end", `{"manual_id":"session-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("end: status = %d (%s)", rec.Code, rec.Body.String())
	}
	ended := decodeBody[map[string]any](t, rec)
	if ended["ended_at"] == nil {
		t.Fatalf("ended_at missing: %v", ended)
	}
	rec = h.do(t, http.MethodPost, "/api/activities/sessions/end", `{"manual_id":"session-1"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second end: status = %d, want 404", rec.Code)
	}
	if body := decodeBody[map[string]any](t, rec); body["code"] != "PASSPORT_ACTIVITY_NOT_FOUND" {
		t.Fatalf("second end: code = %v", body["code"])
	}
}

// TestSessionEndRequiresManualID pins the required manual id.
func TestSessionEndRequiresManualID(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/api/activities/sessions/start", "/api/activities/sessions/end"} {
		rec := h.do(t, http.MethodPost, path, `{"type":"gaming"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", path, rec.Code)
		}
		if body := decodeBody[map[string]any](t, rec); body["code"] != "PASSPORT_ACTIVITY_MANUAL_ID_REQUIRED" {
			t.Fatalf("%s: code = %v", path, body["code"])
		}
	}
}

// TestDeleteActivityIDRequired pins the DELETE contract: neither id nor
// manualId is a 400, an unknown id is a 404, and a known row is a 204.
func TestDeleteActivityIDRequired(t *testing.T) {
	h := newHarness(t)

	rec := h.do(t, http.MethodDelete, "/api/activities", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("no ids: status = %d, want 400", rec.Code)
	}
	if body := decodeBody[map[string]any](t, rec); body["code"] != "PASSPORT_ACTIVITY_ID_REQUIRED" {
		t.Fatalf("no ids: code = %v", body["code"])
	}

	rec = h.do(t, http.MethodDelete, "/api/activities?id="+uuid.NewString(), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: status = %d, want 404", rec.Code)
	}

	rec = h.do(t, http.MethodPost, "/api/activities", `{"type":"gaming","manual_id":"to-delete"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: status = %d", rec.Code)
	}
	rec = h.do(t, http.MethodDelete, "/api/activities?manualId=to-delete", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("known manualId: status = %d, want 204", rec.Code)
	}
}

// TestUpdateActivityOnlyChangesSuppliedFields pins the PUT partial semantics.
func TestUpdateActivityOnlyChangesSuppliedFields(t *testing.T) {
	h := newHarness(t)

	rec := h.do(t, http.MethodPost, "/api/activities",
		`{"type":"gaming","manual_id":"m1","title":"Title","subtitle":"Sub","caption":"Cap"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: status = %d", rec.Code)
	}
	created := decodeBody[map[string]any](t, rec)

	rec = h.do(t, http.MethodPut, "/api/activities/"+created["id"].(string), `{"title":"New title"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status = %d (%s)", rec.Code, rec.Body.String())
	}
	updated := decodeBody[map[string]any](t, rec)
	if updated["title"] != "New title" {
		t.Fatalf("title = %v, want New title", updated["title"])
	}
	if updated["subtitle"] != "Sub" || updated["caption"] != "Cap" {
		t.Fatalf("unsupplied fields changed: %v", updated)
	}
	if updated["type"] != "gaming" {
		t.Fatalf("type = %v, want it preserved", updated["type"])
	}

	// Unknown id -> 404 PASSPORT_ACTIVITY_NOT_FOUND.
	rec = h.do(t, http.MethodPut, "/api/activities/"+uuid.NewString(), `{"title":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: status = %d, want 404", rec.Code)
	}
	if body := decodeBody[map[string]any](t, rec); body["code"] != "PASSPORT_ACTIVITY_NOT_FOUND" {
		t.Fatalf("unknown id: code = %v", body["code"])
	}
}

// TestNormalizers pins provider/reference/term normalization.
func TestNormalizers(t *testing.T) {
	if got := normalizeProvider(new("  Steam  ")); got == nil || *got != "steam" {
		t.Fatalf("normalizeProvider = %v, want steam", got)
	}
	if got := normalizeProvider(new("   ")); got != nil {
		t.Fatalf("blank provider = %v, want nil", got)
	}
	if got := normalizeNullableField(new("  id  ")); got == nil || *got != "id" {
		t.Fatalf("normalizeNullableField = %v, want id", got)
	}
	if got := normalizeNullableField(new("")); got != nil {
		t.Fatalf("blank reference = %v, want nil", got)
	}
	terms := normalizeQueryableTerms([]string{" Metal ", "metal", "", "   ", "Rock"})
	if len(terms) != 2 || terms[0] != "metal" || terms[1] != "rock" {
		t.Fatalf("normalizeQueryableTerms = %v, want [metal rock]", terms)
	}
}

// TestCatalogAndStatsDelegate pins the three read-only delegation routes.
func TestCatalogAndStatsDelegate(t *testing.T) {
	h := newHarness(t)

	rec := h.do(t, http.MethodGet, "/api/activities/catalog", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog: status = %d", rec.Code)
	}
	if items := decodeBody[[]any](t, rec); items == nil {
		t.Fatal("catalog body is null, want []")
	}

	rec = h.do(t, http.MethodGet, "/api/activities/stats", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("stats: status = %d", rec.Code)
	}
	if stats := decodeBody[map[string]any](t, rec); stats["catalog"] != float64(90) {
		t.Fatalf("stats = %v", stats)
	}

	rec = h.do(t, http.MethodGet, "/api/activities/tags/stats", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("tag stats: status = %d", rec.Code)
	}
	if stats := decodeBody[map[string]any](t, rec); stats["gaming"] != float64(90) {
		t.Fatalf("tag stats = %v", stats)
	}
}

// TestActivitiesByAccountNotFound pins the 404 for an unknown identifier.
func TestActivitiesByAccountNotFound(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, http.MethodGet, "/api/activities/nobody", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if body := decodeBody[map[string]any](t, rec); body["code"] != "PASSPORT_ACCOUNT_NOT_FOUND" {
		t.Fatalf("code = %v", body["code"])
	}
}

// TestRegisterRoutes pins the mounted surface.
func TestRegisterRoutes(t *testing.T) {
	h := newHarness(t)
	got := map[string]bool{}
	for _, route := range h.engine.Routes() {
		got[route.Method+" "+route.Path] = true
	}
	for _, want := range []string{
		"GET /api/accounts/me/statuses",
		"PATCH /api/accounts/me/statuses",
		"POST /api/accounts/me/statuses",
		"DELETE /api/accounts/me/statuses",
		"GET /api/accounts/:name/statuses",
		"GET /api/activities",
		"POST /api/activities",
		"PUT /api/activities/:id",
		"DELETE /api/activities",
		"GET /api/activities/:identifier",
		"POST /api/activities/sessions/start",
		"POST /api/activities/sessions/end",
		"GET /api/activities/catalog",
		"GET /api/activities/stats",
		"GET /api/activities/tags/stats",
	} {
		if !got[want] {
			t.Errorf("missing route %s", want)
		}
	}
}

// TestStatusRequestValidation pins the DataAnnotations bounds.
func TestStatusRequestValidation(t *testing.T) {
	h := newHarness(t)
	long := strings.Repeat("x", 1025)
	rec := h.do(t, http.MethodPost, "/api/accounts/me/statuses",
		`{"attitude":2,"type":0,"label":"`+long+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("long label: status = %d, want 400", rec.Code)
	}
	if body := decodeBody[map[string]any](t, rec); body["code"] != "VALIDATION_ERROR" {
		t.Fatalf("long label: code = %v", body["code"])
	}
}

// TestStatusFileNotFoundCodes pins PASSPORT_ICON_NOT_FOUND /
// PASSPORT_BACKGROUND_NOT_FOUND when Drive cannot resolve the reference. The
// harness has no Files provider and no Drive client, which is exactly the
// "file does not exist" answer of the C#.
func TestStatusFileNotFoundCodes(t *testing.T) {
	h := newHarness(t)
	// Only the PATCH path rejects an unresolvable reference; the POST paths
	// skip a missing file exactly like the C# `if (file is not null)`.
	h.store.statuses = []*model.AccountStatus{{
		Id: uuid.NewString(), AccountId: testAccountID.String(),
		Attitude: model.StatusAttitudeNeutral,
	}}

	for _, tc := range []struct {
		body     string
		wantCode string
	}{
		{`{"attitude":2,"type":0,"icon_id":"icon-1"}`, "PASSPORT_ICON_NOT_FOUND"},
		{`{"attitude":2,"type":0,"background_id":"background-1"}`, "PASSPORT_BACKGROUND_NOT_FOUND"},
	} {
		rec := h.do(t, http.MethodPatch, "/api/accounts/me/statuses", tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", tc.wantCode, rec.Code)
		}
		if body := decodeBody[map[string]any](t, rec); body["code"] != tc.wantCode {
			t.Fatalf("code = %v, want %s", body["code"], tc.wantCode)
		}
	}
}

// TestCreateStatusIgnoresMissingArtwork pins the create branch's behaviour: an
// unresolvable icon is skipped rather than rejected (C# only sets the
// reference when the file exists).
func TestCreateStatusIgnoresMissingArtwork(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, http.MethodPost, "/api/accounts/me/statuses",
		`{"attitude":2,"type":0,"icon_id":"missing"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	body := decodeBody[map[string]any](t, rec)
	if _, present := body["icon"]; present {
		t.Fatalf("icon = %v, want it unset", body["icon"])
	}
}

func itoa(value int) string {
	return strconv.Itoa(value)
}

type fakePulsarPresenceClient struct {
	gen.DyPresenceServiceClient
	called bool
	resp   *gen.DyListFriendsActivitiesResponse
}

func (f *fakePulsarPresenceClient) ListFriendsActivities(ctx context.Context, in *gen.DyListFriendsActivitiesRequest, opts ...grpc.CallOption) (*gen.DyListFriendsActivitiesResponse, error) {
	f.called = true
	return f.resp, nil
}

func TestListActivitiesDelegatesToPulsar(t *testing.T) {
	gin.SetMode(gin.TestMode)
	user := &model.Account{Id: testAccountID.String(), Name: "owner"}
	title := "Pulsar Track"
	mockPulsar := &fakePulsarPresenceClient{
		resp: &gen.DyListFriendsActivitiesResponse{
			Activities: []*gen.DyPresenceActivity{
				{
					Id:        "pulsar-activity-123",
					AccountId: testAccountID.String(),
					Title:     &title,
				},
			},
		},
	}
	deps := Deps{
		Store:    newFakeStore(testAccountID),
		Accounts: &fakeAccounts{account: user},
		Perm:     fakePerm{},
		Presence: &fakePresence{},
		Clients: &grpcclient.Clients{
			PulsarPresence: mockPulsar,
		},
	}
	engine := gin.New()
	api := engine.Group("/api")
	api.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(middleware.WithCurrentUser(c.Request.Context(), user))
	})
	Register(api, deps)

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/activities", nil)
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !mockPulsar.called {
		t.Fatalf("expected call to Pulsar")
	}
	var res []model.PresenceActivity
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res) != 1 || res[0].Id != "pulsar-activity-123" || *res[0].Title != "Pulsar Track" {
		t.Fatalf("unexpected res: %+v", res)
	}
}
