package bangumisync

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/bangumi"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/config"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/store"
)

// mockBangumi is a stateful stand-in for the collection API: patches move the
// fake user's rows, so a test can assert what the mirror pushed by reading the
// same state the next pull will see.
type mockBangumi struct {
	mu          sync.Mutex
	server      *httptest.Server
	username    string
	rows        map[int][]bangumi.SubjectCollection
	patches     map[int]bangumi.CollectionPayload
	created     map[int]bangumi.CollectionPayload
	subjectByID map[int]bangumi.SlimSubject
	requests    int
}

func newMockBangumi(t *testing.T) *mockBangumi {
	t.Helper()
	mock := &mockBangumi{
		username: "alice",
		rows: map[int][]bangumi.SubjectCollection{
			bangumi.SubjectTypeAnime: {{
				SubjectID:   100,
				SubjectType: bangumi.SubjectTypeAnime,
				Type:        bangumi.CollectionDoing,
				Rate:        8,
				Comment:     "nice",
				EpStatus:    3,
				Subject: bangumi.SlimSubject{
					ID:     100,
					Type:   bangumi.SubjectTypeAnime,
					Name:   "Cowboy Bebop",
					NameCN: "星际牛仔",
					Images: bangumi.Images{Large: "https://img.example/100.jpg"},
					Tags:   []bangumi.SubjectTag{{Name: "space", Count: 10}},
				},
			}},
			bangumi.SubjectTypeBook: {{
				SubjectID:   200,
				SubjectType: bangumi.SubjectTypeBook,
				Type:        bangumi.CollectionDone,
				Rate:        9,
				Comment:     "read",
				Subject: bangumi.SlimSubject{
					ID:   200,
					Type: bangumi.SubjectTypeBook,
					Name: "Book",
				},
			}},
		},
		patches: map[int]bangumi.CollectionPayload{},
		created: map[int]bangumi.CollectionPayload{},
		subjectByID: map[int]bangumi.SlimSubject{
			999: {ID: 999, Type: bangumi.SubjectTypeAnime, Name: "New Show"},
		},
	}
	mock.server = httptest.NewServer(http.HandlerFunc(mock.handle))
	t.Cleanup(mock.server.Close)
	return mock
}

func (m *mockBangumi) handle(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests++

	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v0/users/") && strings.HasSuffix(r.URL.Path, "/collections"):
		subjectType, _ := strconv.Atoi(r.URL.Query().Get("subject_type"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 100
		}
		rows := m.rows[subjectType]
		end := offset + limit
		if end > len(rows) {
			end = len(rows)
		}
		var data []bangumi.SubjectCollection
		if offset < len(rows) {
			data = rows[offset:end]
		}
		_ = json.NewEncoder(w).Encode(bangumi.CollectionPage{
			Total:  len(rows),
			Limit:  limit,
			Offset: offset,
			Data:   data,
		})
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v0/users/-/collections/"):
		id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/v0/users/-/collections/"))
		var payload bangumi.CollectionPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		m.patches[id] = payload
		m.apply(id, payload)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v0/users/-/collections/"):
		id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/v0/users/-/collections/"))
		var payload bangumi.CollectionPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		m.created[id] = payload
		subject := m.subjectByID[id]
		row := bangumi.SubjectCollection{SubjectID: id, SubjectType: subject.Type, Subject: subject}
		m.applyRow(&row, payload)
		subjectType := subject.Type
		m.rows[subjectType] = append(m.rows[subjectType], row)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func (m *mockBangumi) apply(id int, payload bangumi.CollectionPayload) {
	for subjectType, rows := range m.rows {
		for index := range rows {
			if rows[index].SubjectID != id {
				continue
			}
			m.applyRow(&rows[index], payload)
			m.rows[subjectType] = rows
			return
		}
	}
}

func (m *mockBangumi) applyRow(row *bangumi.SubjectCollection, payload bangumi.CollectionPayload) {
	if payload.Type != nil {
		row.Type = *payload.Type
	}
	if payload.Rate != nil {
		row.Rate = *payload.Rate
	}
	if payload.Comment != nil {
		row.Comment = *payload.Comment
	}
	if payload.EpStatus != nil {
		row.EpStatus = *payload.EpStatus
	}
	if payload.VolStatus != nil {
		row.VolStatus = *payload.VolStatus
	}
	row.UpdatedAt = time.Now().UTC()
}

func (m *mockBangumi) setRow(subjectType int, row bangumi.SubjectCollection) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for index := range m.rows[subjectType] {
		if m.rows[subjectType][index].SubjectID == row.SubjectID {
			m.rows[subjectType][index] = row
			return
		}
	}
	m.rows[subjectType] = append(m.rows[subjectType], row)
}

func (m *mockBangumi) removeRow(subjectType, subjectID int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := m.rows[subjectType]
	filtered := rows[:0]
	for _, row := range rows {
		if row.SubjectID != subjectID {
			filtered = append(filtered, row)
		}
	}
	m.rows[subjectType] = filtered
}

func (m *mockBangumi) lastPatch(subjectID int) (bangumi.CollectionPayload, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	payload, ok := m.patches[subjectID]
	return payload, ok
}

func (m *mockBangumi) resetWrites() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.patches = map[int]bangumi.CollectionPayload{}
	m.created = map[int]bangumi.CollectionPayload{}
}

func testInstance(t *testing.T) (*instance, *mockBangumi, uuid.UUID, *model.ExternalSyncConnection) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.CatalogItem{},
		&model.Collection{},
		&model.CollectionItem{},
		&model.ExternalSyncConnection{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	cfg := &config.Config{}
	cfg.Bangumi.ConflictPolicy = "remote"
	coreServices := &core.Services{Config: cfg, Store: store.New(db)}
	mock := newMockBangumi(t)
	inst := &instance{
		core: coreServices,
		client: bangumi.NewClient(bangumi.Config{
			APIBaseURL: mock.server.URL,
			UserAgent:  "PulsarTest/1.0",
			Interval:   time.Millisecond,
			HTTPClient: mock.server.Client(),
		}),
		log:     slog.Default(),
		syncing: map[uuid.UUID]bool{},
	}
	account := uuid.New()
	username := "alice"
	remote := &model.ExternalSyncConnection{
		AccountID:      account,
		Provider:       provider,
		RemoteUserID:   "42",
		RemoteUsername: &username,
	}
	if _, err := coreServices.Store.EnsureSyncConnection(context.Background(), remote); err != nil {
		t.Fatalf("ensure connection: %v", err)
	}
	stored, err := coreServices.Store.GetSyncConnection(context.Background(), account, provider)
	if err != nil {
		t.Fatalf("get connection: %v", err)
	}
	inst.linkOverride = func(context.Context, uuid.UUID) (*stargateLink, error) {
		return &stargateLink{remote: remote, accessToken: "access-value", username: username}, nil
	}
	return inst, mock, account, stored
}

func TestSyncPullsThenPushesAndPullsRemoteEdits(t *testing.T) {
	inst, mock, account, conn := testInstance(t)
	ctx := context.Background()

	summary, err := inst.syncConnection(ctx, conn)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if summary.Pulled != 2 {
		t.Fatalf("pulled = %d, want 2 (%+v)", summary.Pulled, summary)
	}

	anime, item := findSyncItem(t, inst, account, model.DomainAnime, "100")
	if item.Status == nil || *item.Status != model.StatusWatching {
		t.Fatalf("status = %v", item.Status)
	}
	if item.Rating == nil || *item.Rating != 8 {
		t.Fatalf("rating = %v", item.Rating)
	}
	if item.Note == nil || *item.Note != "nice" {
		t.Fatalf("note = %v", item.Note)
	}
	if item.CatalogItem == nil || item.CatalogItem.Title != "星际牛仔" {
		t.Fatalf("catalog = %+v", item.CatalogItem)
	}

	// A local edit is pushed to Bangumi.
	status := model.StatusCompleted
	rating := 10
	note := "finished"
	if _, err := inst.core.Store.UpdateCollectionItem(ctx, anime.ID, item.ID, store.CollectionItemUpdate{
		Status: &status, Rating: &rating, Note: &note,
	}); err != nil {
		t.Fatalf("local update: %v", err)
	}
	mock.resetWrites()
	summary, err = inst.syncConnection(ctx, conn)
	if err != nil {
		t.Fatalf("push sync: %v", err)
	}
	if summary.Pushed != 1 {
		t.Fatalf("pushed = %d (%+v)", summary.Pushed, summary)
	}
	patch, ok := mock.lastPatch(100)
	if !ok {
		t.Fatal("no patch for subject 100")
	}
	if patch.Type == nil || *patch.Type != bangumi.CollectionDone {
		t.Fatalf("patched type = %v", patch.Type)
	}
	if patch.Rate == nil || *patch.Rate != 10 {
		t.Fatalf("patched rate = %v", patch.Rate)
	}
	if patch.Comment == nil || *patch.Comment != "finished" {
		t.Fatalf("patched comment = %v", patch.Comment)
	}

	// A remote edit is pulled back.
	mock.setRow(bangumi.SubjectTypeAnime, bangumi.SubjectCollection{
		SubjectID:   100,
		SubjectType: bangumi.SubjectTypeAnime,
		Type:        bangumi.CollectionOnHold,
		Rate:        6,
		Comment:     "paused",
		Subject:     bangumi.SlimSubject{ID: 100, Type: bangumi.SubjectTypeAnime, Name: "Cowboy Bebop"},
	})
	summary, err = inst.syncConnection(ctx, conn)
	if err != nil {
		t.Fatalf("pull sync: %v", err)
	}
	if summary.Pulled != 1 {
		t.Fatalf("pulled = %d (%+v)", summary.Pulled, summary)
	}
	_, item = findSyncItem(t, inst, account, model.DomainAnime, "100")
	if item.Status == nil || *item.Status != model.StatusOnHold {
		t.Fatalf("status = %v", item.Status)
	}
	if item.Rating == nil || *item.Rating != 6 {
		t.Fatalf("rating = %v", item.Rating)
	}
	if item.Note == nil || *item.Note != "paused" {
		t.Fatalf("note = %v", item.Note)
	}
}

func TestConflictPolicyLocalWins(t *testing.T) {
	inst, mock, account, conn := testInstance(t)
	ctx := context.Background()
	if _, err := inst.syncConnection(ctx, conn); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	anime, item := findSyncItem(t, inst, account, model.DomainAnime, "100")

	status := model.StatusDropped
	if _, err := inst.core.Store.UpdateCollectionItem(ctx, anime.ID, item.ID, store.CollectionItemUpdate{Status: &status}); err != nil {
		t.Fatalf("local update: %v", err)
	}
	mock.setRow(bangumi.SubjectTypeAnime, bangumi.SubjectCollection{
		SubjectID:   100,
		SubjectType: bangumi.SubjectTypeAnime,
		Type:        bangumi.CollectionDone,
		Rate:        7,
		Comment:     "done remotely",
		Subject:     bangumi.SlimSubject{ID: 100, Type: bangumi.SubjectTypeAnime, Name: "Cowboy Bebop"},
	})
	conn.ConflictPolicy = "local"
	mock.resetWrites()

	summary, err := inst.syncConnection(ctx, conn)
	if err != nil {
		t.Fatalf("conflict sync: %v", err)
	}
	if summary.Conflicts != 1 {
		t.Fatalf("conflicts = %d (%+v)", summary.Conflicts, summary)
	}
	patch, ok := mock.lastPatch(100)
	if !ok || patch.Type == nil || *patch.Type != bangumi.CollectionDropped {
		t.Fatalf("patch = %+v (ok=%v)", patch, ok)
	}
}

func TestRemoteRemovalDeletesUnchangedLocalRow(t *testing.T) {
	inst, mock, account, conn := testInstance(t)
	ctx := context.Background()
	if _, err := inst.syncConnection(ctx, conn); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	anime, _ := findSyncItem(t, inst, account, model.DomainAnime, "100")
	mock.removeRow(bangumi.SubjectTypeAnime, 100)

	summary, err := inst.syncConnection(ctx, conn)
	if err != nil {
		t.Fatalf("delete sync: %v", err)
	}
	if summary.Removed != 1 {
		t.Fatalf("removed = %d (%+v)", summary.Removed, summary)
	}
	full, err := inst.core.Store.GetCollection(ctx, anime.ID)
	if err != nil {
		t.Fatalf("get collection: %v", err)
	}
	if len(full.Items) != 0 {
		t.Fatalf("items = %d, want 0", len(full.Items))
	}
}

func TestBookProgressIsPushed(t *testing.T) {
	inst, mock, account, conn := testInstance(t)
	ctx := context.Background()
	if _, err := inst.syncConnection(ctx, conn); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	book, item := findSyncItem(t, inst, account, model.DomainBook, "200")
	progress := 5
	if _, err := inst.core.Store.UpdateCollectionItem(ctx, book.ID, item.ID, store.CollectionItemUpdate{Progress: &progress}); err != nil {
		t.Fatalf("local update: %v", err)
	}
	mock.resetWrites()
	if _, err := inst.syncConnection(ctx, conn); err != nil {
		t.Fatalf("push sync: %v", err)
	}
	patch, ok := mock.lastPatch(200)
	if !ok || patch.EpStatus == nil || *patch.EpStatus != 5 {
		t.Fatalf("book patch = %+v (ok=%v)", patch, ok)
	}
}

func TestSyncMergesIntoExistingDefaultWatchlist(t *testing.T) {
	inst, _, account, conn := testInstance(t)
	ctx := context.Background()

	defaultList := &model.Collection{
		AccountID: account,
		Domain:    model.DomainAnime,
		Kind:      model.KindWatchlist,
		Name:      "Anime Watchlist",
	}
	if err := inst.core.Store.CreateCollection(ctx, defaultList); err != nil {
		t.Fatalf("create default watchlist: %v", err)
	}

	summary, err := inst.syncConnection(ctx, conn)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if summary.Pulled != 2 {
		t.Fatalf("pulled = %d, want 2 (%+v)", summary.Pulled, summary)
	}

	watchlists := watchlistsForDomain(t, inst, account, model.DomainAnime)
	if len(watchlists) != 1 {
		t.Fatalf("anime watchlists = %d, want 1: %+v", len(watchlists), watchlists)
	}
	if watchlists[0].ID != defaultList.ID || watchlists[0].Name != "Anime Watchlist" {
		t.Fatalf("watchlist = %+v, want the account's own list", watchlists[0])
	}
	if watchlists[0].SyncProvider == nil || *watchlists[0].SyncProvider != provider {
		t.Fatalf("default watchlist was not tagged as the sync target: %+v", watchlists[0])
	}

	_, item := findSyncItem(t, inst, account, model.DomainAnime, "100")
	if item.CollectionID != defaultList.ID {
		t.Fatalf("item collection = %s, want %s", item.CollectionID, defaultList.ID)
	}
	if empty := watchlistsForDomain(t, inst, account, model.DomainGaming); len(empty) != 0 {
		t.Fatalf("gaming watchlists = %d, want none for an empty Bangumi list", len(empty))
	}
}

func TestSyncKeepsItsWatchlistAfterAnotherIsCreated(t *testing.T) {
	inst, _, account, conn := testInstance(t)
	ctx := context.Background()

	if _, err := inst.syncConnection(ctx, conn); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	chosen := watchlistsForDomain(t, inst, account, model.DomainAnime)
	if len(chosen) != 1 {
		t.Fatalf("anime watchlists = %d, want 1", len(chosen))
	}

	extra := &model.Collection{
		AccountID: account,
		Domain:    model.DomainAnime,
		Kind:      model.KindWatchlist,
		Name:      "Another Anime List",
	}
	if err := inst.core.Store.CreateCollection(ctx, extra); err != nil {
		t.Fatalf("create extra watchlist: %v", err)
	}
	if _, err := inst.syncConnection(ctx, conn); err != nil {
		t.Fatalf("second sync: %v", err)
	}

	watchlists := watchlistsForDomain(t, inst, account, model.DomainAnime)
	if len(watchlists) != 2 {
		t.Fatalf("anime watchlists = %d, want 2", len(watchlists))
	}
	_, item := findSyncItem(t, inst, account, model.DomainAnime, "100")
	if item.CollectionID != chosen[0].ID {
		t.Fatalf("item moved to %s, want the original list %s", item.CollectionID, chosen[0].ID)
	}
	full, err := inst.core.Store.GetCollection(ctx, extra.ID)
	if err != nil {
		t.Fatalf("get extra watchlist: %v", err)
	}
	if len(full.Items) != 0 {
		t.Fatalf("extra watchlist items = %d, want 0", len(full.Items))
	}
}

func TestSyncFoldsLegacyMirrorIntoDefaultWatchlist(t *testing.T) {
	inst, _, account, conn := testInstance(t)
	ctx := context.Background()

	defaultList := &model.Collection{
		AccountID: account,
		Domain:    model.DomainAnime,
		Kind:      model.KindWatchlist,
		Name:      "Anime Watchlist",
	}
	if err := inst.core.Store.CreateCollection(ctx, defaultList); err != nil {
		t.Fatalf("create default watchlist: %v", err)
	}
	catalog, err := inst.findOrCreateCatalog(ctx, model.DomainAnime, 100, bangumi.SlimSubject{
		ID: 100, Type: bangumi.SubjectTypeAnime, Name: "Cowboy Bebop",
	})
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}

	// The account already tracks the same work locally.
	localStatus := model.StatusCompleted
	localRating := 10
	if err := inst.core.Store.AddCollectionItem(ctx, &model.CollectionItem{
		CollectionID: defaultList.ID,
		CatalogID:    &catalog.ID,
		Status:       &localStatus,
		Rating:       &localRating,
	}); err != nil {
		t.Fatalf("default item: %v", err)
	}

	// An older sync put the same work in its own mirror collection.
	scope := strconv.Itoa(bangumi.SubjectTypeAnime)
	legacy := &model.Collection{
		AccountID:    account,
		Domain:       model.DomainAnime,
		Kind:         model.KindWatchlist,
		Name:         "Bangumi · Anime",
		SyncProvider: stringPointer(provider),
		SyncScope:    &scope,
	}
	if err := inst.core.Store.CreateCollection(ctx, legacy); err != nil {
		t.Fatalf("create legacy mirror: %v", err)
	}
	remoteID := "100"
	mirrorStatus := model.StatusWatching
	mirrorRating := 8
	progress := 3
	note := "nice"
	localHash := bangumi.LocalFingerprint(bangumi.LocalState{
		Status: mirrorStatus, Rating: &mirrorRating, Note: &note, Progress: &progress,
	})
	remoteHash := bangumi.RemoteFingerprint(bangumi.RemoteState{
		Type: bangumi.CollectionDoing, Rate: 8, Comment: note, EpStatus: progress,
	})
	if err := inst.core.Store.AddCollectionItem(ctx, &model.CollectionItem{
		CollectionID:          legacy.ID,
		CatalogID:             &catalog.ID,
		Status:                &mirrorStatus,
		Rating:                &mirrorRating,
		Note:                  &note,
		Progress:              &progress,
		SyncRemoteID:          &remoteID,
		SyncLocalFingerprint:  &localHash,
		SyncRemoteFingerprint: &remoteHash,
	}); err != nil {
		t.Fatalf("legacy item: %v", err)
	}

	if _, err := inst.syncConnection(ctx, conn); err != nil {
		t.Fatalf("sync: %v", err)
	}

	watchlists := watchlistsForDomain(t, inst, account, model.DomainAnime)
	if len(watchlists) != 1 {
		t.Fatalf("anime watchlists = %d, want 1: %+v", len(watchlists), watchlists)
	}
	if watchlists[0].ID != defaultList.ID {
		t.Fatalf("watchlist = %s, want %s", watchlists[0].ID, defaultList.ID)
	}

	full, err := inst.core.Store.GetCollection(ctx, defaultList.ID)
	if err != nil {
		t.Fatalf("get default watchlist: %v", err)
	}
	matches := 0
	for index := range full.Items {
		item := &full.Items[index]
		if item.CatalogID == nil || *item.CatalogID != catalog.ID {
			continue
		}
		matches++
		if item.SyncRemoteID == nil || *item.SyncRemoteID != remoteID {
			t.Fatalf("merged item has no Bangumi link: %+v", item)
		}
		if item.Status == nil || *item.Status != model.StatusCompleted {
			t.Fatalf("manual status was lost: %v", item.Status)
		}
	}
	if matches != 1 {
		t.Fatalf("catalog rows = %d, want the two entries folded into 1", matches)
	}
	if _, err := inst.core.Store.GetCollection(ctx, legacy.ID); err == nil {
		t.Fatal("legacy mirror collection still exists")
	}
}

func TestSyncAdoptsLegacyMirrorWhenNoDefaultWatchlistExists(t *testing.T) {
	inst, _, account, conn := testInstance(t)
	ctx := context.Background()

	scope := strconv.Itoa(bangumi.SubjectTypeAnime)
	legacy := &model.Collection{
		AccountID:    account,
		Domain:       model.DomainAnime,
		Kind:         model.KindWatchlist,
		Name:         "Bangumi · Anime",
		SyncProvider: stringPointer(provider),
		SyncScope:    &scope,
	}
	if err := inst.core.Store.CreateCollection(ctx, legacy); err != nil {
		t.Fatalf("create legacy mirror: %v", err)
	}
	catalog, err := inst.findOrCreateCatalog(ctx, model.DomainAnime, 100, bangumi.SlimSubject{
		ID: 100, Type: bangumi.SubjectTypeAnime, Name: "Cowboy Bebop",
	})
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	remoteID := "100"
	status := model.StatusWatching
	localHash := bangumi.LocalFingerprint(bangumi.LocalState{Status: status})
	remoteHash := bangumi.RemoteFingerprint(bangumi.RemoteState{Type: bangumi.CollectionDoing})
	if err := inst.core.Store.AddCollectionItem(ctx, &model.CollectionItem{
		CollectionID:          legacy.ID,
		CatalogID:             &catalog.ID,
		Status:                &status,
		SyncRemoteID:          &remoteID,
		SyncLocalFingerprint:  &localHash,
		SyncRemoteFingerprint: &remoteHash,
	}); err != nil {
		t.Fatalf("legacy item: %v", err)
	}

	if _, err := inst.syncConnection(ctx, conn); err != nil {
		t.Fatalf("sync: %v", err)
	}

	watchlists := watchlistsForDomain(t, inst, account, model.DomainAnime)
	if len(watchlists) != 1 {
		t.Fatalf("anime watchlists = %d, want 1: %+v", len(watchlists), watchlists)
	}
	if watchlists[0].ID != legacy.ID || watchlists[0].Name != "Anime Watchlist" {
		t.Fatalf("adopted watchlist = %+v", watchlists[0])
	}
	_, item := findSyncItem(t, inst, account, model.DomainAnime, remoteID)
	if item.CollectionID != legacy.ID {
		t.Fatalf("item collection = %s, want %s", item.CollectionID, legacy.ID)
	}
}

func watchlistsForDomain(t *testing.T, inst *instance, account uuid.UUID, domain string) []model.Collection {
	t.Helper()
	collections, _, err := inst.core.Store.ListCollections(context.Background(), account, domain, model.KindWatchlist, 0, 100)
	if err != nil {
		t.Fatalf("list watchlists: %v", err)
	}
	return collections
}

func stringPointer(value string) *string { return &value }

func findSyncItem(t *testing.T, inst *instance, account uuid.UUID, domain, remoteID string) (*model.Collection, *model.CollectionItem) {
	t.Helper()
	collections, err := inst.core.Store.ListSyncCollections(context.Background(), account, provider)
	if err != nil {
		t.Fatalf("list sync collections: %v", err)
	}
	for index := range collections {
		collection := &collections[index]
		if collection.Domain != domain {
			continue
		}
		full, err := inst.core.Store.GetCollection(context.Background(), collection.ID)
		if err != nil {
			t.Fatalf("get collection: %v", err)
		}
		for itemIndex := range full.Items {
			item := &full.Items[itemIndex]
			if item.SyncRemoteID != nil && *item.SyncRemoteID == remoteID {
				return full, item
			}
		}
	}
	t.Fatalf("no sync item %s in %s", remoteID, domain)
	return nil, nil
}
