package wsgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

// memAckTracker is an in-memory PacketAckTracker that also reports how the
// service used it.
type memAckTracker struct {
	mu         sync.Mutex
	pending    map[string][]PendingPacket
	appendHits int
	ackHits    int
	listHits   int
	attempts   int
	dropHits   int
	retention  time.Duration
	maxPending int
}

func newMemAckTracker() *memAckTracker {
	return &memAckTracker{
		pending:    make(map[string][]PendingPacket),
		retention:  ackDefaultRetention,
		maxPending: ackDefaultMaxPending,
	}
}

func (m *memAckTracker) Retention() time.Duration { return m.retention }

func (m *memAckTracker) MaxPending() int { return m.maxPending }

func (m *memAckTracker) key(namespace, accountID string) string {
	return namespace + "/" + accountID
}

func (m *memAckTracker) Append(_ context.Context, namespace, accountID string, entry PendingPacket) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appendHits++
	key := m.key(namespace, accountID)
	m.pending[key] = append(m.pending[key], entry)
	return len(m.pending[key]), nil
}

func (m *memAckTracker) Ack(_ context.Context, namespace, accountID, ackID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ackHits++
	key := m.key(namespace, accountID)
	entries := m.pending[key]
	for i, entry := range entries {
		if entry.AckID != ackID {
			continue
		}
		m.pending[key] = append(entries[:i:i], entries[i+1:]...)
		return true, nil
	}
	return false, nil
}

func (m *memAckTracker) List(_ context.Context, namespace, accountID string) ([]PendingPacket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listHits++
	entries := m.pending[m.key(namespace, accountID)]
	return append([]PendingPacket(nil), entries...), nil
}

func (m *memAckTracker) CountAttempt(_ context.Context, namespace, accountID, ackID string, nextTry time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attempts++
	key := m.key(namespace, accountID)
	entries := m.pending[key]
	for i := range entries {
		if entries[i].AckID != ackID {
			continue
		}
		entries[i].Attempts++
		entries[i].NextTryAt = nextTry
	}
	return nil
}

func (m *memAckTracker) Drop(_ context.Context, namespace, accountID string, ackIDs []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dropHits++
	key := m.key(namespace, accountID)
	drop := make(map[string]struct{}, len(ackIDs))
	for _, ackID := range ackIDs {
		drop[ackID] = struct{}{}
	}
	kept := make([]PendingPacket, 0, len(m.pending[key]))
	for _, entry := range m.pending[key] {
		if _, ok := drop[entry.AckID]; ok {
			continue
		}
		kept = append(kept, entry)
	}
	m.pending[key] = kept
	return nil
}

func (m *memAckTracker) entries(namespace, accountID string) []PendingPacket {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]PendingPacket(nil), m.pending[m.key(namespace, accountID)]...)
}

func (m *memAckTracker) mutate(namespace, accountID string, fn func(*PendingPacket)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entries := m.pending[m.key(namespace, accountID)]
	for i := range entries {
		fn(&entries[i])
	}
}

// newWSPair returns the server end (what the gateway writes to and the test can
// observe) and the client end of one live websocket connection.
func newWSPair(t *testing.T) (server *websocket.Conn, client *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		accepted <- c
		<-release
		_ = c.CloseNow()
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})

	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("failed to dial test websocket: %v", err)
	}
	// CloseNow, not Close: a close handshake against a peer that is being torn
	// down would spend its fixed 5s+5s timeouts in every test.
	t.Cleanup(func() { _ = client.CloseNow() })

	select {
	case server = <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the server side of the test websocket")
	}
	t.Cleanup(func() { _ = server.CloseNow() })
	return server, client
}

// trackConnection registers one live connection for account/device and starts
// its writer so frames actually reach the peer. It marks the connection as
// ack-capable, which is what a real client does by sending session.resume.
func trackConnection(t *testing.T, svc *Service, server *websocket.Conn, accountID, deviceID string) *wsConnection {
	t.Helper()
	entry := trackLegacyConnection(t, svc, server, accountID, deviceID)
	entry.markAckCapable()
	return entry
}

// trackLegacyConnection is a connection from a client that never speaks the
// ack protocol (an un-upgraded build).
func trackLegacyConnection(t *testing.T, svc *Service, server *websocket.Conn, accountID, deviceID string) *wsConnection {
	t.Helper()
	entry := &wsConnection{
		namespace: svc.cfg.DefaultNamespace,
		account:   &gen.DyAccount{Id: accountID},
		deviceID:  deviceID,
		conn:      server,
		outbound:  make(chan Packet, outboundQueueSize),
		priority:  make(chan Packet, priorityQueueSize),
		done:      make(chan struct{}),
	}
	entry.startWriter()
	svc.mu.Lock()
	svc.connections[connectionKey{namespace: svc.cfg.DefaultNamespace, accountID: accountID, deviceID: deviceID}] = entry
	svc.mu.Unlock()
	return entry
}

// readPacket reads one client frame; ok is false when nothing arrives in time.
func readPacket(t *testing.T, conn *websocket.Conn, timeout time.Duration) (Packet, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, raw, err := conn.Read(ctx)
	if err != nil {
		// A timeout cancels the read, which closes this connection; every
		// caller uses a miss as the last read on that connection.
		return Packet{}, false
	}
	var packet Packet
	if err := json.Unmarshal(raw, &packet); err != nil {
		t.Fatalf("failed to decode pushed frame: %v", err)
	}
	return packet, true
}

func TestServiceSendPacketToAccount_StampsAckIDAndPersistsFrame(t *testing.T) {
	svc := NewService(Config{}, nil, nil, nil, nil, nil, nil)
	tracker := newMemAckTracker()
	svc.SetAckTracker(tracker)

	server, client := newWSPair(t)
	trackConnection(t, svc, server, "u1", "d1")

	svc.SendPacketToAccount("", "u1", &gen.DyWebSocketPacket{Type: "messages.new", Data: []byte(`{"id":"m1"}`)})

	frame, ok := readPacket(t, client, 2*time.Second)
	if !ok {
		t.Fatal("expected the client to receive the pushed frame")
	}
	if frame.Type != "messages.new" {
		t.Fatalf("expected messages.new, got %q", frame.Type)
	}
	if frame.AckID == "" {
		t.Fatal("expected the tracked frame to carry an ack id")
	}

	entries := tracker.entries(svc.cfg.DefaultNamespace, "u1")
	if len(entries) != 1 {
		t.Fatalf("expected 1 pending entry, got %d", len(entries))
	}
	if entries[0].AckID != frame.AckID {
		t.Fatalf("expected pending ack id %q, got %q", frame.AckID, entries[0].AckID)
	}
	if entries[0].DeviceID != "d1" {
		t.Fatalf("expected pending device d1, got %q", entries[0].DeviceID)
	}
	var stored Packet
	if err := json.Unmarshal(entries[0].Frame, &stored); err != nil {
		t.Fatalf("stored frame is not decodable: %v", err)
	}
	if stored.AckID != frame.AckID || string(stored.Data) != `{"id":"m1"}` {
		t.Fatalf("stored frame does not match the delivered frame: %s", string(entries[0].Frame))
	}
}

func TestServiceHandlePacket_AckClearsPendingAndIsIdempotent(t *testing.T) {
	svc := NewService(Config{}, nil, nil, nil, nil, nil, nil)
	tracker := newMemAckTracker()
	svc.SetAckTracker(tracker)

	server, client := newWSPair(t)
	trackConnection(t, svc, server, "u1", "d1")

	svc.SendPacketToAccount("", "u1", &gen.DyWebSocketPacket{Type: "messages.new", Data: []byte(`{"id":"m1"}`)})
	frame, ok := readPacket(t, client, 2*time.Second)
	if !ok {
		t.Fatal("expected the client to receive the pushed frame")
	}

	account := &gen.DyAccount{Id: "u1"}
	ack := Packet{Type: PacketTypeAck, Data: json.RawMessage(`{"ack_id":"` + frame.AckID + `"}`)}
	if err := svc.HandlePacket(context.Background(), account, svc.cfg.DefaultNamespace, "d1", ack); err != nil {
		t.Fatalf("expected ack to be accepted, got %v", err)
	}
	if got := tracker.entries(svc.cfg.DefaultNamespace, "u1"); len(got) != 0 {
		t.Fatalf("expected ack to clear the pending entry, got %d", len(got))
	}

	// A repeat ack must be a silent no-op, not an error frame.
	if err := svc.HandlePacket(context.Background(), account, svc.cfg.DefaultNamespace, "d1", ack); err != nil {
		t.Fatalf("expected repeated ack to be accepted, got %v", err)
	}
	if frame, ok := readPacket(t, client, 200*time.Millisecond); ok {
		t.Fatalf("expected no frame for a repeated ack, got %q", frame.Type)
	}

	// An ack without a usable id is accepted and ignored.
	if err := svc.HandlePacket(context.Background(), account, svc.cfg.DefaultNamespace, "d1", Packet{Type: PacketTypeAck}); err != nil {
		t.Fatalf("expected empty ack payload to be accepted, got %v", err)
	}
	if tracker.ackHits != 2 {
		t.Fatalf("expected 2 tracker ack calls (one empty payload skipped), got %d", tracker.ackHits)
	}
}

func TestServiceReconcileAcks_ResendsUntilBudgetExhausted(t *testing.T) {
	svc := NewService(Config{}, nil, nil, nil, nil, nil, nil)
	tracker := newMemAckTracker()
	svc.SetAckTracker(tracker)
	svc.SetAckRetryInterval(time.Millisecond)

	server, client := newWSPair(t)
	trackConnection(t, svc, server, "u1", "d1")

	svc.SendPacketToAccount("", "u1", &gen.DyWebSocketPacket{Type: "messages.new", Data: []byte(`{"id":"m1"}`)})
	first, ok := readPacket(t, client, 2*time.Second)
	if !ok {
		t.Fatal("expected the client to receive the pushed frame")
	}

	// Make the entry due for a retry.
	tracker.mutate(svc.cfg.DefaultNamespace, "u1", func(entry *PendingPacket) {
		entry.NextTryAt = time.Now().Add(-time.Second)
	})
	svc.reconcileAcksOnce(context.Background())

	replayed, ok := readPacket(t, client, 2*time.Second)
	if !ok {
		t.Fatal("expected the reconciler to re-send the unacknowledged frame")
	}
	if replayed.AckID != first.AckID {
		t.Fatalf("expected the re-send to reuse ack id %q, got %q", first.AckID, replayed.AckID)
	}
	entries := tracker.entries(svc.cfg.DefaultNamespace, "u1")
	if len(entries) != 1 || entries[0].Attempts != 1 {
		t.Fatalf("expected 1 pending entry with 1 attempt, got %#v", entries)
	}

	// Exhaust the budget: the entry is dropped and never re-sent again.
	tracker.mutate(svc.cfg.DefaultNamespace, "u1", func(entry *PendingPacket) {
		entry.Attempts = svc.ackAttemptBudget()
		entry.NextTryAt = time.Now().Add(-time.Second)
	})
	svc.reconcileAcksOnce(context.Background())

	if got := tracker.entries(svc.cfg.DefaultNamespace, "u1"); len(got) != 0 {
		t.Fatalf("expected the entry to be dropped, got %#v", got)
	}
	if tracker.dropHits != 1 {
		t.Fatalf("expected 1 drop call, got %d", tracker.dropHits)
	}
	if frame, ok := readPacket(t, client, 200*time.Millisecond); ok {
		t.Fatalf("expected no frame after the retry budget, got %q", frame.Type)
	}
}

func TestServiceSendPacketToAccount_UntrackedFamilyIsNotPersisted(t *testing.T) {
	svc := NewService(Config{}, nil, nil, nil, nil, nil, nil)
	tracker := newMemAckTracker()
	svc.SetAckTracker(tracker)

	server, client := newWSPair(t)
	trackConnection(t, svc, server, "u1", "d1")

	svc.SendPacketToAccount("", "u1", &gen.DyWebSocketPacket{Type: "notifications.new", Data: []byte(`{"id":"n1"}`)})

	frame, ok := readPacket(t, client, 2*time.Second)
	if !ok {
		t.Fatal("expected the client to receive the pushed frame")
	}
	if frame.AckID != "" {
		t.Fatalf("expected an untracked frame to carry no ack id, got %q", frame.AckID)
	}
	if tracker.appendHits != 0 {
		t.Fatalf("expected the tracker to be untouched, got %d appends", tracker.appendHits)
	}
}

func TestServiceHandlePacket_ResumeReplaysPendingFramesInOrder(t *testing.T) {
	svc := NewService(Config{}, nil, nil, nil, nil, nil, nil)
	tracker := newMemAckTracker()
	svc.SetAckTracker(tracker)

	server, client := newWSPair(t)
	trackConnection(t, svc, server, "u1", "d1")

	for _, id := range []string{"m1", "m2"} {
		svc.SendPacketToAccount("", "u1", &gen.DyWebSocketPacket{Type: "messages.new", Data: []byte(`{"id":"` + id + `"}`)})
		if _, ok := readPacket(t, client, 2*time.Second); !ok {
			t.Fatalf("expected the client to receive the %s frame", id)
		}
	}

	// Simulate a reconnect where the push never reached the client: the frames
	// are replayed on request in append order.
	if err := svc.HandlePacket(context.Background(), &gen.DyAccount{Id: "u1"}, svc.cfg.DefaultNamespace, "d1", Packet{Type: PacketTypeResume}); err != nil {
		t.Fatalf("expected resume to be accepted, got %v", err)
	}
	for _, want := range []string{"m1", "m2"} {
		frame, ok := readPacket(t, client, 2*time.Second)
		if !ok {
			t.Fatalf("expected replayed frame for %s", want)
		}
		if want == "m1" && !strings.Contains(string(frame.Data), `"m1"`) {
			t.Fatalf("expected m1 first, got %s", string(frame.Data))
		}
		if frame.AckID == "" {
			t.Fatal("expected a replayed frame to keep its ack id")
		}
	}
	// Resume never clears: only an ack does.
	if got := tracker.entries(svc.cfg.DefaultNamespace, "u1"); len(got) != 2 {
		t.Fatalf("expected resume to keep both entries, got %d", len(got))
	}
}

func TestServiceHandlePacket_ResumeSkipsOtherDevices(t *testing.T) {
	svc := NewService(Config{}, nil, nil, nil, nil, nil, nil)
	tracker := newMemAckTracker()
	svc.SetAckTracker(tracker)

	server, client := newWSPair(t)
	trackConnection(t, svc, server, "u1", "d1")

	// A frame pending for a different device of the same account must not be
	// replayed on this connection.
	if _, err := tracker.Append(context.Background(), svc.cfg.DefaultNamespace, "u1", PendingPacket{
		AckID:    "ack-other",
		DeviceID: "d2",
		Frame:    []byte(`{"type":"messages.new","data":{"id":"m9"},"_ack_id":"ack-other"}`),
	}); err != nil {
		t.Fatalf("unexpected append error: %v", err)
	}

	if err := svc.HandlePacket(context.Background(), &gen.DyAccount{Id: "u1"}, svc.cfg.DefaultNamespace, "d1", Packet{Type: PacketTypeResume}); err != nil {
		t.Fatalf("expected resume to be accepted, got %v", err)
	}
	if frame, ok := readPacket(t, client, 200*time.Millisecond); ok {
		t.Fatalf("expected no replay for another device, got %q", frame.Type)
	}
	if got := tracker.entries(svc.cfg.DefaultNamespace, "u1"); len(got) != 1 {
		t.Fatalf("expected the other device's entry to survive, got %d", len(got))
	}
}

func TestServiceEnqueueTracked_WithoutTrackerKeepsProtoPath(t *testing.T) {
	svc := NewService(Config{}, nil, nil, nil, nil, nil, nil)
	server, client := newWSPair(t)
	trackConnection(t, svc, server, "u1", "d1")

	svc.SendPacketToAccount("", "u1", &gen.DyWebSocketPacket{Type: "messages.new", Data: []byte(`{"id":"m1"}`)})

	frame, ok := readPacket(t, client, 2*time.Second)
	if !ok {
		t.Fatal("expected the client to receive the pushed frame")
	}
	if frame.AckID != "" {
		t.Fatalf("expected no ack id without a tracker, got %q", frame.AckID)
	}
}

func TestServiceRunAckReconciler_ReturnsWhenContextIsDone(t *testing.T) {
	svc := NewService(Config{}, nil, nil, nil, nil, nil, nil)
	svc.SetAckTracker(newMemAckTracker())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		svc.RunAckReconciler(ctx)
		close(done)
	}()
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected the reconciler to stop on context cancellation")
	}
}

func TestRedisAckTracker_NilClientIsNoOp(t *testing.T) {
	tracker := NewRedisAckTracker(nil, 0, 0)
	ctx := context.Background()

	depth, err := tracker.Append(ctx, "ns", "u1", PendingPacket{AckID: "a1"})
	if depth != 0 || err != nil {
		t.Fatalf("expected a no-op append, got %d/%v", depth, err)
	}
	entries, err := tracker.List(ctx, "ns", "u1")
	if entries != nil || err != nil {
		t.Fatalf("expected a no-op list, got %#v/%v", entries, err)
	}
	found, err := tracker.Ack(ctx, "ns", "u1", "a1")
	if found || err != nil {
		t.Fatalf("expected a no-op ack, got %v/%v", found, err)
	}
	if err := tracker.CountAttempt(ctx, "ns", "u1", "a1", time.Now()); err != nil {
		t.Fatalf("expected a no-op attempt, got %v", err)
	}
	if err := tracker.Drop(ctx, "ns", "u1", []string{"a1"}); err != nil {
		t.Fatalf("expected a no-op drop, got %v", err)
	}
	if tracker.Retention() != ackDefaultRetention || tracker.MaxPending() != ackDefaultMaxPending {
		t.Fatalf("expected defaults, got %v/%d", tracker.Retention(), tracker.MaxPending())
	}
}

func TestServiceSendPacketToAccount_LegacyClientIsNeverTracked(t *testing.T) {
	svc := NewService(Config{}, nil, nil, nil, nil, nil, nil)
	tracker := newMemAckTracker()
	svc.SetAckTracker(tracker)

	server, client := newWSPair(t)
	trackLegacyConnection(t, svc, server, "u1", "d1")

	svc.SendPacketToAccount("", "u1", &gen.DyWebSocketPacket{Type: "messages.new", Data: []byte(`{"id":"m1"}`)})

	frame, ok := readPacket(t, client, 2*time.Second)
	if !ok {
		t.Fatal("expected the legacy client to receive the pushed frame")
	}
	if frame.AckID != "" {
		t.Fatalf("expected no ack id for a client that cannot ack, got %q", frame.AckID)
	}
	if tracker.appendHits != 0 {
		t.Fatalf("expected nothing persisted for a legacy client, got %d appends", tracker.appendHits)
	}
}

func TestServiceReconcileAcks_DoesNotReplayIntoLegacyClient(t *testing.T) {
	svc := NewService(Config{}, nil, nil, nil, nil, nil, nil)
	tracker := newMemAckTracker()
	svc.SetAckTracker(tracker)
	svc.SetAckRetryInterval(time.Millisecond)

	server, client := newWSPair(t)
	// Pending entries exist from an earlier ack-capable session; the device is
	// now on a downgraded build that cannot clear them.
	trackLegacyConnection(t, svc, server, "u1", "d1")
	if _, err := tracker.Append(context.Background(), svc.cfg.DefaultNamespace, "u1", PendingPacket{
		AckID:     "ack-old",
		DeviceID:  "d1",
		Frame:     []byte(`{"type":"messages.new","data":{"id":"m1"},"_ack_id":"ack-old"}`),
		CreatedAt: time.Now(),
		NextTryAt: time.Now().Add(-time.Second),
	}); err != nil {
		t.Fatalf("unexpected append error: %v", err)
	}

	svc.reconcileAcksOnce(context.Background())

	if frame, ok := readPacket(t, client, 300*time.Millisecond); ok {
		t.Fatalf("expected no replay into a legacy client, got %q", frame.Type)
	}
	// The attempt is still counted, so the entry expires on schedule.
	entries := tracker.entries(svc.cfg.DefaultNamespace, "u1")
	if len(entries) != 1 || entries[0].Attempts != 1 {
		t.Fatalf("expected the attempt to be counted, got %#v", entries)
	}
}

func TestServiceHandlePacket_MarksConnectionAckCapable(t *testing.T) {
	svc := NewService(Config{}, nil, nil, nil, nil, nil, nil)
	svc.SetAckTracker(newMemAckTracker())

	server, client := newWSPair(t)
	entry := trackLegacyConnection(t, svc, server, "u1", "d1")
	if entry.supportsAcks() {
		t.Fatal("a fresh connection must not be ack-capable")
	}

	// What the real receive loop does before dispatching.
	if err := svc.HandlePacket(context.Background(), &gen.DyAccount{Id: "u1"}, svc.cfg.DefaultNamespace, "d1", Packet{Type: PacketTypeResume}); err != nil {
		t.Fatalf("expected resume to be accepted, got %v", err)
	}
	entry.markAckCapable()
	if !entry.supportsAcks() {
		t.Fatal("expected the connection to be ack-capable after resume")
	}

	// The now-capable connection gets tracked frames.
	svc.SendPacketToAccount("", "u1", &gen.DyWebSocketPacket{Type: "messages.new", Data: []byte(`{"id":"m2"}`)})
	frame, ok := readPacket(t, client, 2*time.Second)
	if !ok {
		t.Fatal("expected the client to receive the pushed frame")
	}
	if frame.AckID == "" {
		t.Fatal("expected an ack-capable connection to receive a tracked frame")
	}
}

func TestServiceAckAttemptBudget_IsConfigurable(t *testing.T) {
	svc := NewService(Config{}, nil, nil, nil, nil, nil, nil)
	if got := svc.ackAttemptBudget(); got != ackDefaultMaxAttempts {
		t.Fatalf("expected the default budget, got %d", got)
	}

	svc.SetAckMaxAttempts(0)
	if got := svc.ackAttemptBudget(); got != ackDefaultMaxAttempts {
		t.Fatalf("a non-positive override must be ignored, got %d", got)
	}

	svc.SetAckMaxAttempts(3)
	if got := svc.ackAttemptBudget(); got != 3 {
		t.Fatalf("expected the override to apply, got %d", got)
	}
}

func TestServiceReconcileAcks_HonoursConfiguredAttemptBudget(t *testing.T) {
	svc := NewService(Config{}, nil, nil, nil, nil, nil, nil)
	tracker := newMemAckTracker()
	svc.SetAckTracker(tracker)
	svc.SetAckRetryInterval(time.Millisecond)
	svc.SetAckMaxAttempts(3)

	server, client := newWSPair(t)
	trackConnection(t, svc, server, "u1", "d1")

	svc.SendPacketToAccount("", "u1", &gen.DyWebSocketPacket{Type: "messages.new", Data: []byte(`{"id":"m1"}`)})
	if _, ok := readPacket(t, client, 2*time.Second); !ok {
		t.Fatal("expected the client to receive the pushed frame")
	}

	// The budget is the number of re-sends allowed, so the entry survives
	// exactly `budget` passes and is dropped by the one after them.
	for attempt := 1; attempt <= 3; attempt++ {
		tracker.mutate(svc.cfg.DefaultNamespace, "u1", func(entry *PendingPacket) {
			entry.NextTryAt = time.Now().Add(-time.Second)
		})
		svc.reconcileAcksOnce(context.Background())

		entries := tracker.entries(svc.cfg.DefaultNamespace, "u1")
		if len(entries) != 1 || entries[0].Attempts != attempt {
			t.Fatalf("attempt %d: expected 1 entry with %d attempts, got %#v", attempt, attempt, entries)
		}
		if _, ok := readPacket(t, client, 2*time.Second); !ok {
			t.Fatalf("attempt %d: expected a re-send", attempt)
		}
	}

	tracker.mutate(svc.cfg.DefaultNamespace, "u1", func(entry *PendingPacket) {
		entry.NextTryAt = time.Now().Add(-time.Second)
	})
	svc.reconcileAcksOnce(context.Background())
	if entries := tracker.entries(svc.cfg.DefaultNamespace, "u1"); len(entries) != 0 {
		t.Fatalf("expected the entry dropped once the budget was spent, got %#v", entries)
	}
	if frame, ok := readPacket(t, client, 200*time.Millisecond); ok {
		t.Fatalf("expected no frame past the budget, got %q", frame.Type)
	}
}

func TestServiceHandlePacket_ResumeIsCapped(t *testing.T) {
	svc := NewService(Config{}, nil, nil, nil, nil, nil, nil)
	tracker := newMemAckTracker()
	svc.SetAckTracker(tracker)

	server, client := newWSPair(t)
	trackConnection(t, svc, server, "u1", "d1")

	// More pending frames than one resume may replay.
	total := ackMaxResumeReplay + 5
	for i := 0; i < total; i++ {
		if _, err := tracker.Append(context.Background(), svc.cfg.DefaultNamespace, "u1", PendingPacket{
			AckID:     fmt.Sprintf("ack-%03d", i),
			DeviceID:  "d1",
			Frame:     []byte(fmt.Sprintf(`{"type":"messages.new","data":{"n":%d},"_ack_id":"ack-%03d"}`, i, i)),
			CreatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("unexpected append error: %v", err)
		}
	}

	if err := svc.HandlePacket(context.Background(), &gen.DyAccount{Id: "u1"}, svc.cfg.DefaultNamespace, "d1", Packet{Type: PacketTypeResume}); err != nil {
		t.Fatalf("expected resume to be accepted, got %v", err)
	}

	replayed := 0
	for {
		if _, ok := readPacket(t, client, 300*time.Millisecond); !ok {
			break
		}
		replayed++
	}
	if replayed != ackMaxResumeReplay {
		t.Fatalf("expected the resume capped at %d replays, got %d", ackMaxResumeReplay, replayed)
	}
	// Deferred frames are not lost: they stay pending for the reconciler.
	if got := tracker.entries(svc.cfg.DefaultNamespace, "u1"); len(got) != total {
		t.Fatalf("expected all %d entries to stay pending, got %d", total, len(got))
	}
}
