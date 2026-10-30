// Package broadcast fans Passport's presence events out to the accounts that
// are allowed to see them, on Blade's websocket_push subject.
//
// It is the Go port of two C# halves: HandleWebSocketStatusChanged
// (DysonNetwork.Passport/Startup/ServiceCollectionExtensions.cs) and
// AccountEventService.BroadcastPresenceActivitiesUpdated. Both address the
// account's own friends, so the audience is already known to be allowed to
// see device presence and no per-viewer relationship check is needed.
package broadcast

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"src.solsynth.dev/sosys/go/pkg/cache"

	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/nats"
	"src.solsynth.dev/sosys/stargate/presence"
	"src.solsynth.dev/sosys/stargate/redis"
	"src.solsynth.dev/sosys/stargate/relationship"
	"src.solsynth.dev/sosys/stargate/store"
)

// Websocket packet types (DysonNetwork.Shared.Models.WebSocketPacketType).
const (
	// StatusUpdatedEvent is WebSocketPacketType.AccountStatusUpdated.
	StatusUpdatedEvent = "account.status.updated"
	// ActivitiesUpdatedEvent is WebSocketPacketType.AccountPresenceActivitiesUpdated.
	ActivitiesUpdatedEvent = "account.presence.activities.updated"
)

// Status cache keys mirror AccountEventService exactly. Stargate does not
// cache status reads — the only value kept here is the previous status, which
// HandleWebSocketStatusChanged compares the freshly assembled one against to
// suppress no-op broadcasts. The prefix is shared so a C# writer and a Go
// reader agree during the cutover.
const (
	statusCacheKeyPrefix         = "account:status:"
	previousStatusCacheKeyPrefix = "account:status:prev:"
	previousStatusTTL            = 5 * time.Minute
)

// wsPublisher is the narrow push surface this package needs; *nats.Client
// implements it. Narrowed so the fan-out is testable without a broker.
type wsPublisher interface {
	PublishWSToAccounts(ctx context.Context, accountIDs []string, event string, payload any, namespace string) error
}

// eventPublisher is the account_events half of the activity broadcast
// (AccountPresenceActivitiesUpdatedEvent, consumed by Messager).
type eventPublisher interface {
	PublishPresenceActivitiesUpdated(ctx context.Context, accountID string, activities []model.PresenceActivity, at time.Time) error
}

// Broadcaster pushes account status and presence-activity updates to friends.
// A nil Broadcaster is a no-op, matching the fleet convention for optional
// infrastructure.
type Broadcaster struct {
	ws     wsPublisher
	events eventPublisher
	store  *store.Store
	rel    *relationship.Service
	res    *presence.Resolver
	cache  cache.CacheService
	log    *slog.Logger
}

// New builds the broadcaster. nc, rel, res and rc may all be nil (events,
// relationships, Blade presence or Redis not configured); the corresponding
// step is then skipped rather than failing the caller.
func New(nc *nats.Client, st *store.Store, rel *relationship.Service, res *presence.Resolver, rc *redis.Client, log *slog.Logger) *Broadcaster {
	if log == nil {
		log = slog.Default()
	}
	b := &Broadcaster{store: st, rel: rel, res: res, log: log}
	if nc != nil {
		b.ws = nc
		b.events = nc
	}
	if rc.Available() {
		b.cache = rc.Cache
	}
	return b
}

// StatusUpdatedPayload is the `account.status.updated` packet data. Keys are
// snake_case (InfraObjectCoder on the C# side); Status is the assembled
// account status including the device list, because the recipients are the
// account's friends.
type StatusUpdatedPayload struct {
	AccountID string               `json:"account_id"`
	Status    *model.AccountStatus `json:"status"`
	DeviceID  string               `json:"device_id"`
	Timestamp time.Time            `json:"timestamp"`
}

// ActivitiesUpdatedPayload is the `account.presence.activities.updated`
// packet data (AccountEventService.BroadcastPresenceActivitiesUpdated).
type ActivitiesUpdatedPayload struct {
	AccountID  string                   `json:"account_id"`
	Activities []model.PresenceActivity `json:"activities"`
	Timestamp  time.Time                `json:"timestamp"`
}

// StatusChanged broadcasts the account's status when it differs from
// previous, the status the caller captured before its write (nil when the
// account had no active status). It mirrors HandleWebSocketStatusChanged:
// purge the caches, assemble the current status, skip when nothing visible
// changed, then push to friends.
func (b *Broadcaster) StatusChanged(ctx context.Context, accountID uuid.UUID, deviceID string, previous *model.AccountStatus, at time.Time) error {
	if b == nil {
		return nil
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	b.purgeStatusCache(ctx, accountID)
	current, err := b.currentStatus(ctx, accountID)
	if err != nil {
		return err
	}
	// Keep the previous-status key warm for the connection-event path, which
	// has no caller-supplied snapshot. Passport did this inside GetStatus.
	b.storePreviousStatus(ctx, accountID, current)
	if previous != nil && StatusesEqual(previous, current) {
		return nil
	}
	return b.pushStatus(ctx, accountID, deviceID, current, at)
}

// WebSocketStatusChanged is StatusChanged for the connection-event consumers:
// the previous status comes from the shared cache key instead of a caller
// snapshot, exactly like the C# HandleWebSocketStatusChanged.
func (b *Broadcaster) WebSocketStatusChanged(ctx context.Context, accountID uuid.UUID, deviceID string, at time.Time) error {
	if b == nil {
		return nil
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	previous := b.cachedPreviousStatus(ctx, accountID)
	b.purgeStatusCache(ctx, accountID)
	current, err := b.currentStatus(ctx, accountID)
	if err != nil {
		return err
	}
	b.storePreviousStatus(ctx, accountID, current)
	if previous != nil && StatusesEqual(previous, current) {
		return nil
	}
	return b.pushStatus(ctx, accountID, deviceID, current, at)
}

func (b *Broadcaster) pushStatus(ctx context.Context, accountID uuid.UUID, deviceID string, status *model.AccountStatus, at time.Time) error {
	friends, err := b.friendIDs(ctx, accountID)
	if err != nil {
		return err
	}
	if len(friends) == 0 {
		return nil
	}
	if err := b.publish(ctx, friends, StatusUpdatedEvent, StatusUpdatedPayload{
		AccountID: accountID.String(),
		Status:    status,
		DeviceID:  deviceID,
		Timestamp: at.UTC(),
	}); err != nil {
		return err
	}
	b.log.Debug("broadcast account status update",
		"account_id", accountID.String(), "friends", len(friends))
	return nil
}

// ActivitiesChanged republishes the account's live presence activities: the
// AccountPresenceActivitiesUpdatedEvent other services consume from
// account_events, and the friends-only websocket push
// (AccountEventService.BroadcastPresenceActivitiesUpdated). Callers advertise
// create/update/delete and session start/end through it after the write
// landed.
func (b *Broadcaster) ActivitiesChanged(ctx context.Context, accountID uuid.UUID) error {
	if b == nil || b.store == nil {
		return nil
	}
	activities, err := b.store.GetActiveActivities(ctx, accountID)
	if err != nil {
		return err
	}
	if activities == nil {
		activities = []model.PresenceActivity{}
	}
	at := time.Now().UTC()
	// Messager refreshes chat presence from the event, so it is published
	// even when the account has no friends to push to.
	if b.events != nil {
		if err := b.events.PublishPresenceActivitiesUpdated(ctx, accountID.String(), activities, at); err != nil {
			return err
		}
	}
	friends, err := b.friendIDs(ctx, accountID)
	if err != nil {
		return err
	}
	if len(friends) == 0 {
		return nil
	}
	if err := b.publish(ctx, friends, ActivitiesUpdatedEvent, ActivitiesUpdatedPayload{
		AccountID:  accountID.String(),
		Activities: activities,
		Timestamp:  at,
	}); err != nil {
		return err
	}
	b.log.Debug("broadcast account presence activities update",
		"account_id", accountID.String(), "friends", len(friends), "activities", len(activities))
	return nil
}

// currentStatus assembles the status HandleWebSocketStatusChanged broadcasts:
// the newest active status row (or the synthetic Online/Offline default),
// with device presence and idle state resolved. includeDevicePresence is
// always true here — the audience is the account's friends.
func (b *Broadcaster) currentStatus(ctx context.Context, accountID uuid.UUID) (*model.AccountStatus, error) {
	if b.store == nil {
		return nil, errors.New("broadcast: no store")
	}
	status, err := b.store.GetCurrentStatus(ctx, accountID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if errors.Is(err, store.ErrNotFound) {
		status = nil
	}

	online := false
	devices := []model.OnlineDevice{}
	if b.res != nil {
		online, devices, err = b.res.ResolveOnlineDevices(ctx, accountID)
		if err != nil {
			return nil, err
		}
	}

	if status == nil {
		// AccountEventService.GetStatus's no-row default: neutral attitude,
		// uncustomized, labelled from the live presence.
		label := "Offline"
		if online {
			label = "Online"
		}
		status = &model.AccountStatus{
			AccountId:    accountID.String(),
			Attitude:     model.StatusAttitudeNeutral,
			Label:        &label,
			IsCustomized: false,
		}
	}

	if b.res != nil {
		var idle bool
		var idleSince *time.Time
		if online {
			idle, idleSince, err = b.res.GetAccountIdleState(ctx, accountID)
			if err != nil {
				return nil, err
			}
		}
		if err := b.res.ApplyPresence(ctx, status, online, devices, idle, idleSince); err != nil {
			return nil, err
		}
	}
	return status, nil
}

// friendIDs lists the accounts that should receive the broadcast
// (RelationshipService.ListAccountFriends: friends and close friends).
func (b *Broadcaster) friendIDs(ctx context.Context, accountID uuid.UUID) ([]string, error) {
	if b.rel == nil || !b.rel.Available() {
		return nil, nil
	}
	return b.rel.ListRelated(ctx, accountID, model.RelationshipFriends, false)
}

func (b *Broadcaster) publish(ctx context.Context, accountIDs []string, event string, payload any) error {
	if b.ws == nil {
		return nil
	}
	return b.ws.PublishWSToAccounts(ctx, accountIDs, event, payload, presence.NamespaceSolian)
}

func (b *Broadcaster) purgeStatusCache(ctx context.Context, accountID uuid.UUID) {
	if b.cache == nil {
		return
	}
	id := accountID.String()
	if err := b.cache.Remove(ctx, statusCacheKeyPrefix+id); err != nil {
		b.log.Warn("purge status cache", "account_id", id, "error", err)
	}
	if err := b.cache.Remove(ctx, previousStatusCacheKeyPrefix+id); err != nil {
		b.log.Warn("purge previous status cache", "account_id", id, "error", err)
	}
}

func (b *Broadcaster) storePreviousStatus(ctx context.Context, accountID uuid.UUID, status *model.AccountStatus) {
	if b.cache == nil || status == nil {
		return
	}
	if err := b.cache.Set(ctx, previousStatusCacheKeyPrefix+accountID.String(), status, previousStatusTTL); err != nil {
		b.log.Warn("store previous status", "account_id", accountID.String(), "error", err)
	}
}

func (b *Broadcaster) cachedPreviousStatus(ctx context.Context, accountID uuid.UUID) *model.AccountStatus {
	if b.cache == nil {
		return nil
	}
	var previous *model.AccountStatus
	if _, err := b.cache.Get(ctx, previousStatusCacheKeyPrefix+accountID.String(), &previous); err != nil {
		b.log.Warn("read previous status", "account_id", accountID.String(), "error", err)
		return nil
	}
	return previous
}

// StatusesEqual is BroadcastEventHandler.StatusesEqual: the fields that are
// visible to a friend and therefore worth a broadcast. A nil side never
// compares equal, so the first status after an account had none always
// broadcasts.
func StatusesEqual(a, b *model.AccountStatus) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Attitude == b.Attitude &&
		a.IsOnline == b.IsOnline &&
		a.IsIdle == b.IsIdle &&
		timesEqual(a.IdleSince, b.IdleSince) &&
		a.IsCustomized == b.IsCustomized &&
		stringsEqual(a.Label, b.Label) &&
		stringsEqual(a.Symbol, b.Symbol) &&
		a.Type == b.Type
}

func stringsEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func timesEqual(a, b *model.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Time().Equal(b.Time())
}
