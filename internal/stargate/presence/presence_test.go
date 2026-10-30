package presence

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"src.solsynth.dev/sosys/go/pkg/cache"

	"src.solsynth.dev/sosys/stargate/auth"
	"src.solsynth.dev/sosys/stargate/model"
)

// --- fakes ---

type fakePresence struct {
	byAccount map[string][]auth.OnlineDevice
	err       error
	calls     [][]string
}

func (f *fakePresence) ForAccounts(_ context.Context, accountIDs []string, _ string) (map[string][]auth.OnlineDevice, error) {
	f.calls = append(f.calls, accountIDs)
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[string][]auth.OnlineDevice, len(accountIDs))
	for _, id := range accountIDs {
		out[id] = f.byAccount[id]
	}
	return out, nil
}

type fakeRelationships struct {
	friends map[string][]string
	err     error
}

func (f *fakeRelationships) ListRelatedAccountIDs(_ context.Context, accountID uuid.UUID, _ model.RelationshipStatus, _ bool) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.friends[accountID.String()], nil
}

func liveDevice(clientID string) auth.OnlineDevice {
	label := "work"
	return auth.OnlineDevice{
		Client: model.AuthClient{
			Id:          clientID,
			DeviceId:    "hardware-" + clientID,
			DeviceName:  "MacBook Pro",
			DeviceLabel: &label,
			Platform:    model.ClientPlatformMacOs,
		},
	}
}

// --- online device resolution ---

func TestResolveOnlineDevicesUsesSolianNamespace(t *testing.T) {
	accountID := uuid.New()
	presence := &fakePresence{byAccount: map[string][]auth.OnlineDevice{
		accountID.String(): {liveDevice(uuid.NewString())},
	}}
	r := &Resolver{presence: presence}

	online, devices, err := r.ResolveOnlineDevices(context.Background(), accountID)
	if err != nil {
		t.Fatalf("ResolveOnlineDevices: %v", err)
	}
	if !online {
		t.Fatal("account with a live device must be online")
	}
	if len(devices) != 1 {
		t.Fatalf("devices = %d, want 1", len(devices))
	}
	if devices[0].Platform != int(model.ClientPlatformMacOs) {
		t.Errorf("platform = %d, want %d", devices[0].Platform, model.ClientPlatformMacOs)
	}
	if len(presence.calls) != 1 || len(presence.calls[0]) != 1 || presence.calls[0][0] != accountID.String() {
		t.Errorf("lookup accounts = %v, want [%s]", presence.calls, accountID)
	}
}

func TestResolveOnlineDevicesWithoutBackend(t *testing.T) {
	r := &Resolver{}
	online, devices, err := r.ResolveOnlineDevices(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("ResolveOnlineDevices: %v", err)
	}
	if online || devices == nil || len(devices) != 0 {
		t.Errorf("got online=%v devices=%v, want false and empty", online, devices)
	}
}

func TestResolveOnlineDevicesBatch(t *testing.T) {
	onlineID, offlineID := uuid.New(), uuid.New()
	presence := &fakePresence{byAccount: map[string][]auth.OnlineDevice{
		onlineID.String(): {liveDevice(uuid.NewString()), liveDevice(uuid.NewString())},
	}}
	r := &Resolver{presence: presence}

	// Duplicated request ids must not become duplicate lookups.
	got, err := r.ResolveOnlineDevicesBatch(context.Background(), []uuid.UUID{onlineID, offlineID, onlineID})
	if err != nil {
		t.Fatalf("ResolveOnlineDevicesBatch: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("map has %d entries, want 2", len(got))
	}
	if len(got[onlineID]) != 2 {
		t.Errorf("online account devices = %d, want 2", len(got[onlineID]))
	}
	offline, ok := got[offlineID]
	if !ok {
		t.Fatal("offline account missing from the result map")
	}
	if offline == nil || len(offline) != 0 {
		t.Errorf("offline account devices = %v, want empty non-nil", offline)
	}
	if len(presence.calls) != 1 {
		t.Fatalf("presence lookups = %d, want 1 batched call", len(presence.calls))
	}
	if len(presence.calls[0]) != 2 {
		t.Errorf("batched lookup accounts = %v, want 2 distinct", presence.calls[0])
	}
}

func TestResolveOnlineDevicesBatchEmpty(t *testing.T) {
	presence := &fakePresence{}
	r := &Resolver{presence: presence}
	got, err := r.ResolveOnlineDevicesBatch(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveOnlineDevicesBatch: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty batch returned %d entries", len(got))
	}
	if len(presence.calls) != 0 {
		t.Errorf("empty batch triggered %d lookups", len(presence.calls))
	}
}

func TestResolveOnlineDevicesBatchPropagatesError(t *testing.T) {
	boom := errors.New("blade down")
	r := &Resolver{presence: &fakePresence{err: boom}}
	if _, err := r.ResolveOnlineDevicesBatch(context.Background(), []uuid.UUID{uuid.New()}); !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

// --- ApplyPresence ---

func TestApplyPresenceOnlinePopulatesDevices(t *testing.T) {
	device := OnlineDevice{Id: uuid.NewString(), DeviceId: "d", DeviceName: "Phone"}
	status := &model.AccountStatus{AccountId: uuid.NewString(), Label: new("Working")}

	if err := (&Resolver{}).ApplyPresence(context.Background(), status, true, []OnlineDevice{device}, false, nil); err != nil {
		t.Fatalf("ApplyPresence: %v", err)
	}
	if !status.IsOnline {
		t.Error("online account must read as online")
	}
	if len(status.OnlineDevices) != 1 || status.OnlineDevices[0].Id != device.Id {
		t.Errorf("OnlineDevices = %v, want the resolved device", status.OnlineDevices)
	}
	if status.IsIdle || status.IdleSince != nil {
		t.Errorf("idle = %v since %v, want false/nil", status.IsIdle, status.IdleSince)
	}
}

func TestApplyPresenceOnlineIdle(t *testing.T) {
	since := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	status := &model.AccountStatus{AccountId: uuid.NewString()}

	if err := (&Resolver{}).ApplyPresence(context.Background(), status, true, nil, true, &since); err != nil {
		t.Fatalf("ApplyPresence: %v", err)
	}
	if !status.IsIdle {
		t.Fatal("online + all devices idle must read as idle")
	}
	if status.IdleSince == nil || !status.IdleSince.Time().Equal(since) {
		t.Errorf("IdleSince = %v, want %v", status.IdleSince, since)
	}
	if status.OnlineDevices == nil || len(status.OnlineDevices) != 0 {
		t.Errorf("OnlineDevices = %v, want empty non-nil for an online account without devices", status.OnlineDevices)
	}
}

func TestApplyPresenceInvisibleStatusSuppressesOnline(t *testing.T) {
	rc := newTestCache(t)
	r := &Resolver{cache: rc}
	accountID := uuid.NewString()
	status := &model.AccountStatus{AccountId: accountID, Type: model.StatusTypeInvisible}
	since := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

	if err := r.ApplyPresence(context.Background(), status, true, []OnlineDevice{{Id: uuid.NewString()}}, true, &since); err != nil {
		t.Fatalf("ApplyPresence: %v", err)
	}
	if status.IsOnline {
		t.Error("invisible status must never read as online")
	}
	if !status.IsInvisible || status.IsNotDisturb {
		t.Errorf("derived flags invisible=%v not_disturb=%v, want true/false", status.IsInvisible, status.IsNotDisturb)
	}
	if len(status.OnlineDevices) != 0 {
		t.Errorf("OnlineDevices = %v, want none while invisible", status.OnlineDevices)
	}
	if status.IsIdle || status.IdleSince != nil {
		t.Errorf("idle = %v since %v, want false/nil while offline", status.IsIdle, status.IdleSince)
	}
}

func TestApplyPresenceOfflineClearsDevicesAndIdleKey(t *testing.T) {
	rc := newTestCache(t)
	r := &Resolver{cache: rc}
	ctx := context.Background()
	accountID := uuid.New()
	status := &model.AccountStatus{AccountId: accountID.String()}
	since := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

	// Seed an idle-state map, as a websocket consumer would.
	if err := r.UpdateWebSocketConnectionState(ctx, accountID, "device-1", new(true), &since, true); err != nil {
		t.Fatalf("UpdateWebSocketConnectionState: %v", err)
	}

	if err := r.ApplyPresence(ctx, status, false, []OnlineDevice{{Id: uuid.NewString()}}, true, &since); err != nil {
		t.Fatalf("ApplyPresence: %v", err)
	}
	if status.IsOnline || status.IsIdle {
		t.Errorf("offline account reads online=%v idle=%v", status.IsOnline, status.IsIdle)
	}
	if len(status.OnlineDevices) != 0 {
		t.Errorf("OnlineDevices = %v, want none while offline", status.OnlineDevices)
	}
	if status.IdleSince != nil {
		t.Errorf("IdleSince = %v, want nil while offline", status.IdleSince)
	}
	found, err := rc.Get(ctx, connectionIdleStateKeyPrefix+accountID.String(), &map[string]presenceState{})
	if err != nil {
		t.Fatalf("read idle state: %v", err)
	}
	if found {
		t.Error("idle state key must be cleared once the account reads offline")
	}
}

func TestApplyPresenceNilStatus(t *testing.T) {
	if err := (&Resolver{}).ApplyPresence(context.Background(), nil, true, nil, false, nil); !errors.Is(err, ErrNilStatus) {
		t.Errorf("err = %v, want %v", err, ErrNilStatus)
	}
}

// --- idle state ---

func TestGetAccountIdleState(t *testing.T) {
	rc := newTestCache(t)
	r := &Resolver{cache: rc}
	ctx := context.Background()
	accountID := uuid.New()
	earlier := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	later := time.Date(2026, 10, 26, 11, 30, 0, 0, time.UTC)

	if idle, since, err := r.GetAccountIdleState(ctx, accountID); err != nil || idle || since != nil {
		t.Fatalf("no state: idle=%v since=%v err=%v, want false/nil/nil", idle, since, err)
	}

	if err := r.UpdateWebSocketConnectionState(ctx, accountID, "a", new(true), &earlier, true); err != nil {
		t.Fatalf("connect a: %v", err)
	}
	if err := r.UpdateWebSocketConnectionState(ctx, accountID, "b", new(true), &later, true); err != nil {
		t.Fatalf("connect b: %v", err)
	}
	idle, since, err := r.GetAccountIdleState(ctx, accountID)
	if err != nil || !idle || since == nil {
		t.Fatalf("both idle: idle=%v since=%v err=%v", idle, since, err)
	}
	if !since.Equal(later) {
		t.Errorf("idle since = %v, want the max idle_since %v", since, later)
	}

	// A device that is not idle clears the account's idle state entirely.
	if err := r.UpdateWebSocketConnectionState(ctx, accountID, "c", new(true), nil, true); err != nil {
		t.Fatalf("connect c: %v", err)
	}
	if idle, since, err := r.GetAccountIdleState(ctx, accountID); err != nil || idle || since != nil {
		t.Errorf("one active device: idle=%v since=%v err=%v, want false/nil/nil", idle, since, err)
	}

	// Disconnecting the active device restores idleness from the rest.
	if err := r.UpdateWebSocketConnectionState(ctx, accountID, "c", new(false), nil, false); err != nil {
		t.Fatalf("disconnect c: %v", err)
	}
	if idle, _, err := r.GetAccountIdleState(ctx, accountID); err != nil || !idle {
		t.Errorf("after disconnect: idle=%v err=%v, want true", idle, err)
	}

	// Disconnecting every device removes the key (and the 7-day TTL with it).
	for _, device := range []string{"a", "b"} {
		if err := r.UpdateWebSocketConnectionState(ctx, accountID, device, new(false), nil, false); err != nil {
			t.Fatalf("disconnect %s: %v", device, err)
		}
	}
	found, err := rc.Get(ctx, connectionIdleStateKeyPrefix+accountID.String(), &map[string]presenceState{})
	if err != nil || found {
		t.Errorf("state key found=%v err=%v, want removed", found, err)
	}
}

func TestUpdateWebSocketConnectionStateWithoutCache(t *testing.T) {
	accountID := uuid.New()
	r := &Resolver{}
	if err := r.UpdateWebSocketConnectionState(context.Background(), accountID, "a", new(false), nil, false); err != nil {
		t.Errorf("nil cache update: %v", err)
	}
	if idle, since, err := r.GetAccountIdleState(context.Background(), accountID); err != nil || idle || since != nil {
		t.Errorf("nil cache idle: %v %v %v", idle, since, err)
	}
}

// --- device visibility ---

func TestCanViewDevicePresenceMatrix(t *testing.T) {
	owner, friend, stranger := uuid.New(), uuid.New(), uuid.New()
	r := &Resolver{relationships: &fakeRelationships{
		friends: map[string][]string{friend.String(): {owner.String()}},
	}}
	ctx := context.Background()

	cases := []struct {
		name        string
		owner       uuid.UUID
		viewer      *uuid.UUID
		include     bool
		wantVisible bool
	}{
		{name: "include flag grants strangers", owner: owner, viewer: &stranger, include: true, wantVisible: true},
		{name: "include flag grants anonymous", owner: owner, viewer: nil, include: true, wantVisible: true},
		{name: "owner sees own devices", owner: owner, viewer: &owner, wantVisible: true},
		{name: "friend sees devices", owner: owner, viewer: &friend, wantVisible: true},
		{name: "stranger does not", owner: owner, viewer: &stranger},
		{name: "anonymous does not", owner: owner, viewer: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.CanViewDevicePresence(ctx, tc.owner, tc.viewer, tc.include)
			if err != nil {
				t.Fatalf("CanViewDevicePresence: %v", err)
			}
			if got != tc.wantVisible {
				t.Errorf("visible = %v, want %v", got, tc.wantVisible)
			}
		})
	}
}

func TestCanViewDevicePresenceFriendLookupError(t *testing.T) {
	boom := errors.New("db down")
	r := &Resolver{relationships: &fakeRelationships{err: boom}}
	viewer := uuid.New()
	if _, err := r.CanViewDevicePresence(context.Background(), uuid.New(), &viewer, false); !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

// --- helpers ---

func newTestCache(t *testing.T) *cache.MemoryCacheService {
	t.Helper()
	rc := cache.NewMemoryCacheService(128)
	t.Cleanup(rc.Close)
	return rc
}
