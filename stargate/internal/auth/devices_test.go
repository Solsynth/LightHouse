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
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// --- fakes ---

type fakeDeviceStore struct {
	clients      map[string]model.AuthClient
	sessions     map[string][]store.DeviceSession
	clientCalls  [][]uuid.UUID
	sessionCalls [][]uuid.UUID
	err          error
}

func (f *fakeDeviceStore) GetClientsByIDs(_ context.Context, clientIDs []uuid.UUID) (map[string]model.AuthClient, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.clientCalls = append(f.clientCalls, clientIDs)
	out := map[string]model.AuthClient{}
	for _, id := range clientIDs {
		if client, ok := f.clients[id.String()]; ok {
			out[id.String()] = client
		}
	}
	return out, nil
}

func (f *fakeDeviceStore) ListDeviceSessionsByClientIDs(_ context.Context, clientIDs []uuid.UUID) (map[string][]store.DeviceSession, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.sessionCalls = append(f.sessionCalls, clientIDs)
	out := map[string][]store.DeviceSession{}
	for _, id := range clientIDs {
		if sessions, ok := f.sessions[id.String()]; ok {
			out[id.String()] = sessions
		}
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

func session(id string, granted time.Time, expired *time.Time) store.DeviceSession {
	s := store.DeviceSession{ID: uuid.MustParse(id)}
	if !granted.IsZero() {
		granted := granted
		s.LastGrantedAt = &granted
	}
	if expired != nil {
		expired := *expired
		s.ExpiredAt = &expired
	}
	return s
}

// --- matchOnlineDevices ---

func TestMatchOnlineDevices(t *testing.T) {
	onlineID := "11111111-1111-1111-1111-111111111111"
	otherID := "22222222-2222-2222-2222-222222222222"
	clients := map[string]model.AuthClient{
		onlineID: client(onlineID),
		otherID:  client(otherID),
	}

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
	oldSession, expiredSession, newSession := uuid.NewString(), uuid.NewString(), uuid.NewString()
	store := &fakeDeviceStore{
		clients: map[string]model.AuthClient{clientID: client(clientID)},
		sessions: map[string][]store.DeviceSession{clientID: {
			session(oldSession, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), nil),
			session(expiredSession, time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC), ptrTime(time.Now().UTC().Add(-time.Hour))),
			session(newSession, time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC), nil),
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
	if len(devices[0].SessionIDs) != 2 || devices[0].SessionIDs[0] != newSession || devices[0].SessionIDs[1] != oldSession {
		t.Errorf("session ids = %v, want [%s %s] (expired filtered, newest first)", devices[0].SessionIDs, newSession, oldSession)
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
	// The clients are resolved by the live device ids Blade reported, never
	// by account id: a 128-account batch must not load every device row.
	if len(store.clientCalls) != 1 || len(store.clientCalls[0]) != 2 {
		t.Fatalf("client lookups = %#v, want one call with the two reported device ids", store.clientCalls)
	}
	if store.clientCalls[0][0].String() != clientID {
		t.Errorf("client lookup ids = %v, want %s first", store.clientCalls[0], clientID)
	}
}

func TestForAccountsOfflineAndUnparseableAccounts(t *testing.T) {
	accountID := uuid.NewString()
	offlineDevice := uuid.NewString()
	store := &fakeDeviceStore{clients: map[string]model.AuthClient{}, sessions: map[string][]store.DeviceSession{}}
	blade := &stubBlade{devices: map[string][]string{accountID: {offlineDevice}}}
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
	// Only the reported live device id reaches the store; unparseable input
	// (the account id "not-a-uuid" included) never does.
	if len(store.clientCalls) != 1 || len(store.clientCalls[0]) != 1 || store.clientCalls[0][0].String() != offlineDevice {
		t.Errorf("store calls = %#v, want only the reported device id", store.clientCalls)
	}
	if len(store.sessionCalls) != 0 {
		t.Errorf("session lookups = %#v, want none (no client matched)", store.sessionCalls)
	}
}

func TestForAccountsWithoutBlade(t *testing.T) {
	accountID := uuid.NewString()
	clientID := uuid.NewString()
	store := &fakeDeviceStore{
		clients:  map[string]model.AuthClient{clientID: client(clientID)},
		sessions: map[string][]store.DeviceSession{},
	}
	presence := NewDevicePresence(store, nil, nil)

	got, err := presence.ForAccounts(context.Background(), []string{accountID}, "")
	if err != nil {
		t.Fatalf("ForAccounts: %v", err)
	}
	if devices := got[accountID]; devices == nil || len(devices) != 0 {
		t.Errorf("devices = %#v, want empty slice and no error", devices)
	}
	// No wsgateway means no presence query at all.
	if len(store.clientCalls) != 0 {
		t.Errorf("store calls = %#v, want none", store.clientCalls)
	}
}

func TestForAccountsBladeFailure(t *testing.T) {
	accountID := uuid.NewString()
	store := &fakeDeviceStore{clients: map[string]model.AuthClient{}, sessions: map[string][]store.DeviceSession{}}
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
