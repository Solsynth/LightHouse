// Package presence resolves the computed half of an account status: live
// device presence, idle state, and who may see another account's devices. It
// is the Go port of AccountEventService.GetStatus's non-persisted parts
// (Passport Account/AccountEventService.cs).
//
// Nothing here caches statuses or activities: Passport's Redis status/activity
// caches were an optimization, and a stale read is worse than the query.
// The one thing that IS Redis-backed is the websocket connection idle state,
// because it is written by the connection consumers, not derived from the
// database. It uses Passport's exact key names so a C# writer and a Go reader
// agree during the cutover.
package presence

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"src.solsynth.dev/sosys/go/pkg/cache"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// NamespaceSolian is Blade's namespace for the Solian client fleet. Presence
// from any other namespace (other tenants sharing Blade) is not this app's.
const NamespaceSolian = "dev.solsynth.solian"

// Idle-state Redis keys and timings mirror AccountEventService exactly: the
// fleet shares these keys with the C# service.
const (
	connectionIdleStateKeyPrefix  = "account:connection-idle-state:"
	connectionIdleStateLockPrefix = "account:connection-idle-state:lock:"
	connectionIdleStateTTL        = 7 * 24 * time.Hour
	connectionIdleStateLockExpiry = 10 * time.Second
	connectionIdleStateLockWait   = 2 * time.Second
	connectionIdleStateLockRetry  = 100 * time.Millisecond
)

// OnlineDevice is the wire shape of one live device (SnOnlineDevice).
type OnlineDevice = model.OnlineDevice

// ErrNilStatus marks an ApplyPresence call without a status to populate.
var ErrNilStatus = errors.New("presence: nil account status")

// relationshipLister is the friend lookup CanViewDevicePresence needs;
// *store.Store satisfies it. Narrowed so the visibility matrix is testable
// without Postgres.
type relationshipLister interface {
	ListRelatedAccountIDs(ctx context.Context, accountID uuid.UUID, status model.RelationshipStatus, isRelated bool) ([]string, error)
}

// devicePresence is the live-connection lookup (auth.DevicePresence);
// narrowed for the same reason.
type devicePresence interface {
	ForAccounts(ctx context.Context, accountIDs []string, namespace string) (map[string][]auth.OnlineDevice, error)
}

// Resolver computes presence for account status reads.
type Resolver struct {
	relationships relationshipLister
	presence      devicePresence
	cache         cache.CacheService
}

// NewResolver wires the resolver. rc may be nil (Redis not configured): idle
// state then reads as "not idle" and updates are no-ops. presence may be nil
// (Blade not configured): every account reports no live device.
func NewResolver(st *store.Store, rc *redis.Client, presence *auth.DevicePresence) *Resolver {
	r := &Resolver{}
	if st != nil {
		r.relationships = st
	}
	if presence != nil {
		r.presence = presence
	}
	if rc.Available() {
		r.cache = rc.Cache
	}
	return r
}

// ResolveOnlineDevices returns whether the account has any live device in the
// Solian namespace, together with those devices. A presence backend failure is
// returned to the caller, which degrades (Passport fell back to a coarser
// Blade check there; Stargate IS the device source).
func (r *Resolver) ResolveOnlineDevices(ctx context.Context, accountID uuid.UUID) (bool, []OnlineDevice, error) {
	if r.presence == nil {
		return false, []OnlineDevice{}, nil
	}
	byAccount, err := r.presence.ForAccounts(ctx, []string{accountID.String()}, NamespaceSolian)
	if err != nil {
		return false, nil, err
	}
	devices := onlineDevices(byAccount[accountID.String()])
	return len(devices) > 0, devices, nil
}

// ResolveOnlineDevicesBatch resolves presence for a whole batch in one
// lookup. Every requested account gets an entry (an empty slice when offline).
func (r *Resolver) ResolveOnlineDevicesBatch(ctx context.Context, accountIDs []uuid.UUID) (map[uuid.UUID][]OnlineDevice, error) {
	result := make(map[uuid.UUID][]OnlineDevice, len(accountIDs))
	if len(accountIDs) == 0 {
		return result, nil
	}
	requested := make([]string, 0, len(accountIDs))
	seen := make(map[uuid.UUID]bool, len(accountIDs))
	for _, id := range accountIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		requested = append(requested, id.String())
		result[id] = []OnlineDevice{}
	}
	if r.presence == nil {
		return result, nil
	}
	byAccount, err := r.presence.ForAccounts(ctx, requested, NamespaceSolian)
	if err != nil {
		return nil, err
	}
	for _, id := range requested {
		parsed, err := uuid.Parse(id)
		if err != nil {
			continue
		}
		result[parsed] = onlineDevices(byAccount[id])
	}
	return result, nil
}

// onlineDevices maps the device presence join onto the wire shape.
func onlineDevices(devices []auth.OnlineDevice) []OnlineDevice {
	out := make([]OnlineDevice, 0, len(devices))
	for _, d := range devices {
		out = append(out, OnlineDevice{
			Id:            d.Client.Id,
			DeviceId:      d.Client.DeviceId,
			DeviceName:    d.Client.DeviceName,
			DeviceLabel:   d.Client.DeviceLabel,
			Platform:      int(d.Client.Platform),
			LastGrantedAt: d.LastGrantedAt,
		})
	}
	return out
}

// presenceState is one device's connection state (the C# WebSocketConnection
// PresenceState). idle_since is always written, matching the C# cache
// serializer's DefaultIgnoreCondition.Never.
type presenceState struct {
	IdleSince *model.Time `json:"idle_since"`
}

// UpdateWebSocketConnectionState records a device connecting (or its idle
// transition) and drops it on disconnect. It is a read-modify-write under the
// per-account Redis lock, exactly like the C# original: the state is a map,
// and two connections of the same account race otherwise.
func (r *Resolver) UpdateWebSocketConnectionState(ctx context.Context, accountID uuid.UUID, deviceID string, isConnected *bool, idleSince *time.Time, updateIdleSince bool) error {
	if r.cache == nil {
		return nil
	}
	stateKey := connectionIdleStateKeyPrefix + accountID.String()
	lockKey := connectionIdleStateLockPrefix + accountID.String()
	// A missed lock returns acquired=false: a concurrent writer owns the key,
	// and its own read-modify-write covers the transition this call
	// describes, so the call is a successful no-op (as in the C# helper).
	_, err := r.cache.ExecuteWithLock(ctx, lockKey, connectionIdleStateLockExpiry, connectionIdleStateLockWait, connectionIdleStateLockRetry, func(ctx context.Context) error {
		states := map[string]presenceState{}
		if _, err := r.cache.Get(ctx, stateKey, &states); err != nil {
			return err
		}
		if states == nil {
			states = map[string]presenceState{}
		}
		if isConnected != nil && !*isConnected {
			delete(states, deviceID)
		} else {
			state := states[deviceID]
			if updateIdleSince {
				state.IdleSince = model.NewTime(derefTime(idleSince))
			}
			states[deviceID] = state
		}
		if len(states) == 0 {
			return r.cache.Remove(ctx, stateKey)
		}
		return r.cache.Set(ctx, stateKey, states, connectionIdleStateTTL)
	})
	return err
}

// ClearWebSocketConnectionStates forgets every device's idle state for an
// account. Passport does this whenever an account reads as offline, so a
// stale map cannot make the next session look idle.
func (r *Resolver) ClearWebSocketConnectionStates(ctx context.Context, accountID uuid.UUID) error {
	if r.cache == nil {
		return nil
	}
	return r.cache.Remove(ctx, connectionIdleStateKeyPrefix+accountID.String())
}

// GetAccountIdleState reports whether every connected device is idle and,
// when so, since when (the newest idle_since among them — the account has
// been idle no longer than its most recently idle device).
func (r *Resolver) GetAccountIdleState(ctx context.Context, accountID uuid.UUID) (bool, *time.Time, error) {
	if r.cache == nil {
		return false, nil, nil
	}
	states := map[string]presenceState{}
	found, err := r.cache.Get(ctx, connectionIdleStateKeyPrefix+accountID.String(), &states)
	if err != nil {
		return false, nil, err
	}
	if !found || len(states) == 0 {
		return false, nil, nil
	}
	var newest *time.Time
	for _, state := range states {
		if state.IdleSince == nil {
			return false, nil, nil
		}
		since := state.IdleSince.Time()
		if newest == nil || since.After(*newest) {
			newest = &since
		}
	}
	return true, newest, nil
}

// ApplyPresence fills a status' computed fields from resolved presence:
// invisible statuses never read as online, devices are listed only while
// online, idle only counts while online, and an offline account's idle state
// key is cleared. A nil status is an error rather than a silent no-op.
func (r *Resolver) ApplyPresence(ctx context.Context, status *model.AccountStatus, online bool, devices []OnlineDevice, idle bool, idleSince *time.Time) error {
	if status == nil {
		return ErrNilStatus
	}
	status.IsOnline = online && status.Type != model.StatusTypeInvisible
	// The type-derived flags are read-time values too, so the projection
	// fills them rather than leaving every caller to remember.
	status.IsInvisible = status.Type == model.StatusTypeInvisible
	status.IsNotDisturb = status.Type == model.StatusTypeDoNotDisturb
	if status.IsOnline {
		if devices == nil {
			devices = []OnlineDevice{}
		}
		status.OnlineDevices = devices
	} else {
		status.OnlineDevices = []OnlineDevice{}
	}
	status.IsIdle = status.IsOnline && idle
	if status.IsIdle && idleSince != nil {
		status.IdleSince = model.NewTime(*idleSince)
	} else {
		status.IdleSince = nil
	}
	if !status.IsOnline {
		return r.ClearWebSocketConnectionStates(ctx, uuidOrNil(status.AccountId))
	}
	return nil
}

// CanViewDevicePresence reports whether the viewer may see the owner's
// device list: any caller with an already-known-allowed audience, the owner
// itself, or one of the owner's friends. An anonymous viewer sees none.
func (r *Resolver) CanViewDevicePresence(ctx context.Context, ownerID uuid.UUID, viewerID *uuid.UUID, includeDevicePresence bool) (bool, error) {
	if includeDevicePresence {
		return true, nil
	}
	if viewerID == nil {
		return false, nil
	}
	if *viewerID == ownerID {
		return true, nil
	}
	if r.relationships == nil {
		return false, nil
	}
	friends, err := r.relationships.ListRelatedAccountIDs(ctx, *viewerID, model.RelationshipFriends, false)
	if err != nil {
		return false, err
	}
	return slices.Contains(friends, ownerID.String()), nil
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// uuidOrNil parses the model's string account id; an unparseable id yields
// the nil UUID, whose idle key is harmless to clear.
func uuidOrNil(raw string) uuid.UUID {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil
	}
	return id
}
