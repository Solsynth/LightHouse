package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// --- fakes ---

type fakeDeviceStore struct {
	clients      map[string][]model.AuthClient
	sessions     map[string][]model.AuthSession
	clientCalls  [][]uuid.UUID
	sessionCalls [][]uuid.UUID
	err          error
}

func (f *fakeDeviceStore) ListClientsByAccountIDs(_ context.Context, accountIDs []uuid.UUID) (map[string][]model.AuthClient, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.clientCalls = append(f.clientCalls, accountIDs)
	out := map[string][]model.AuthClient{}
	for _, id := range accountIDs {
		out[id.String()] = f.clients[id.String()]
	}
	return out, nil
}

func (f *fakeDeviceStore) ListSessionsByClientIDs(_ context.Context, clientIDs []uuid.UUID) (map[string][]model.AuthSession, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.sessionCalls = append(f.sessionCalls, clientIDs)
	out := map[string][]model.AuthSession{}
	for _, id := range clientIDs {
		out[id.String()] = f.sessions[id.String()]
	}
	return out, nil
}

type stubBlade struct {
	gen.WebSocketServiceClient
	devices map[string][]string
	err     error
	calls   int
	lastReq *gen.DyGetUsersConnectedWebsocketDeviceIdsRequest
}

func (s *stubBlade) GetUsersConnectedWebsocketDeviceIds(_ context.Context, req *gen.DyGetUsersConnectedWebsocketDeviceIdsRequest, _ ...grpc.CallOption) (*gen.DyGetUsersConnectedWebsocketDeviceIdsResponse, error) {
	s.calls++
	s.lastReq = req
	if s.err != nil {
		return nil, s.err
	}
	resp := &gen.DyGetUsersConnectedWebsocketDeviceIdsResponse{Devices: map[string]*gen.DyWebsocketDeviceIdList{}}
	for accountID, ids := range s.devices {
		resp.Devices[accountID] = &gen.DyWebsocketDeviceIdList{DeviceIds: ids}
	}
	return resp, nil
}

func client(id string) model.AuthClient {
	return model.AuthClient{Id: id, DeviceId: "hardware-" + id, DeviceName: "iPhone 15 Pro", Platform: model.ClientPlatformIos}
}

func session(id string, granted time.Time, expired *time.Time) model.AuthSession {
	s := model.AuthSession{Id: id}
	if !granted.IsZero() {
		s.LastGrantedAt = model.NewTime(granted)
	}
	if expired != nil {
		s.ExpiredAt = model.NewTime(*expired)
	}
	return s
}

// --- matchOnlineDevices ---

func TestMatchOnlineDevices(t *testing.T) {
	onlineID := "11111111-1111-1111-1111-111111111111"
	otherID := "22222222-2222-2222-2222-222222222222"
	clients := []model.AuthClient{client(otherID), client(onlineID)}

	got := matchOnlineDevices(clients, []string{"  " + onlineID + " ", onlineID, "", "33333333-3333-3333-3333-333333333333"})
	if len(got) != 1 {
		t.Fatalf("devices = %d, want 1 (%#v)", len(got), got)
	}
	if got[0].Client.Id != onlineID || got[0].DeviceKey != onlineID {
		t.Errorf("device = %+v, want client/key %s", got[0], onlineID)
	}

	// Deterministic order: sorted by client id regardless of input order.
	got = matchOnlineDevices(clients, []string{otherID, onlineID})
	if len(got) != 2 || got[0].Client.Id != onlineID || got[1].Client.Id != otherID {
		t.Errorf("order = %#v, want [%s %s]", got, onlineID, otherID)
	}

	// No live connection (and unknown/blank ids only) → nothing online.
	if got := matchOnlineDevices(clients, []string{"", "  "}); len(got) != 0 {
		t.Errorf("devices = %#v, want none", got)
	}
}

// --- ForAccounts ---

func TestForAccountsJoinsLiveDevices(t *testing.T) {
	accountID := uuid.NewString()
	clientID := uuid.NewString()
	store := &fakeDeviceStore{
		clients: map[string][]model.AuthClient{accountID: {client(clientID)}},
		sessions: map[string][]model.AuthSession{clientID: {
			session("old", time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), nil),
			session("expired", time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC), ptrTime(time.Now().UTC().Add(-time.Hour))),
			session("new", time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC), nil),
		}},
	}
	blade := &stubBlade{devices: map[string][]string{accountID: {clientID, uuid.NewString()}}}
	presence := NewDevicePresence(store, blade, nil)

	got, err := presence.ForAccounts(context.Background(), []string{accountID}, "dev.solsynth.solian")
	if err != nil {
		t.Fatalf("ForAccounts: %v", err)
	}
	devices := got[accountID]
	if len(devices) != 1 {
		t.Fatalf("devices = %#v, want exactly the known client", devices)
	}
	if devices[0].Client.Id != clientID || devices[0].DeviceKey != clientID {
		t.Errorf("device = %+v, want client %s", devices[0], clientID)
	}
	if len(devices[0].SessionIDs) != 2 || devices[0].SessionIDs[0] != "new" || devices[0].SessionIDs[1] != "old" {
		t.Errorf("session ids = %v, want [new old] (expired filtered, newest first)", devices[0].SessionIDs)
	}
	if devices[0].LastGrantedAt == nil || !devices[0].LastGrantedAt.Time().Equal(time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("last granted = %v, want 2025-03-01", devices[0].LastGrantedAt)
	}
	if blade.lastReq.GetNamespace() != "dev.solsynth.solian" {
		t.Errorf("namespace = %q, want passed through", blade.lastReq.GetNamespace())
	}
	if len(blade.lastReq.GetUserIds()) != 1 || blade.lastReq.GetUserIds()[0] != accountID {
		t.Errorf("user ids = %v, want [%s]", blade.lastReq.GetUserIds(), accountID)
	}
}

func TestForAccountsOfflineAndUnparseableAccounts(t *testing.T) {
	accountID := uuid.NewString()
	store := &fakeDeviceStore{clients: map[string][]model.AuthClient{}, sessions: map[string][]model.AuthSession{}}
	blade := &stubBlade{devices: map[string][]string{accountID: {uuid.NewString()}}}
	presence := NewDevicePresence(store, blade, nil)

	got, err := presence.ForAccounts(context.Background(), []string{accountID, "not-a-uuid", accountID, "  "}, "")
	if err != nil {
		t.Fatalf("ForAccounts: %v", err)
	}
	// Every requested account gets an entry, including the unparseable one.
	if len(got) != 2 {
		t.Fatalf("accounts = %#v, want 2 entries", got)
	}
	for _, id := range []string{accountID, "not-a-uuid"} {
		devices, ok := got[id]
		if !ok {
			t.Fatalf("missing entry for %q", id)
		}
		if devices == nil || len(devices) != 0 {
			t.Errorf("devices for %q = %#v, want empty slice", id, devices)
		}
	}
	// Only the parseable account reaches the store.
	if len(store.clientCalls) != 1 || len(store.clientCalls[0]) != 1 || store.clientCalls[0][0].String() != accountID {
		t.Errorf("store calls = %#v, want only the parseable account", store.clientCalls)
	}
}

func TestForAccountsWithoutBlade(t *testing.T) {
	accountID := uuid.NewString()
	clientID := uuid.NewString()
	store := &fakeDeviceStore{
		clients:  map[string][]model.AuthClient{accountID: {client(clientID)}},
		sessions: map[string][]model.AuthSession{},
	}
	presence := NewDevicePresence(store, nil, nil)

	got, err := presence.ForAccounts(context.Background(), []string{accountID}, "")
	if err != nil {
		t.Fatalf("ForAccounts: %v", err)
	}
	if devices := got[accountID]; devices == nil || len(devices) != 0 {
		t.Errorf("devices = %#v, want empty slice and no error", devices)
	}
}

func TestForAccountsBladeFailure(t *testing.T) {
	accountID := uuid.NewString()
	store := &fakeDeviceStore{clients: map[string][]model.AuthClient{}, sessions: map[string][]model.AuthSession{}}
	presence := NewDevicePresence(store, &stubBlade{err: errors.New("blade down")}, nil)

	_, err := presence.ForAccounts(context.Background(), []string{accountID}, "")
	if err == nil {
		t.Fatal("ForAccounts: want error")
	}
	if !errors.Is(err, ErrOnlineDevicesUnavailable) {
		t.Errorf("error = %v, want ErrOnlineDevicesUnavailable", err)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
