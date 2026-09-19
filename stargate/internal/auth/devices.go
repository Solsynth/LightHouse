package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// ErrOnlineDevicesUnavailable marks a failed live-connection lookup against
// the wsgateway (Blade unreachable or erroring). ForAccounts wraps it so
// callers can degrade this case separately from local store failures.
var ErrOnlineDevicesUnavailable = errors.New("resolve online devices")

// deviceStore is the read surface device presence needs; *store.Store
// satisfies it. Narrowed to an interface so the join can be tested without
// Postgres.
type deviceStore interface {
	ListClientsByAccountIDs(ctx context.Context, accountIDs []uuid.UUID) (map[string][]model.AuthClient, error)
	ListSessionsByClientIDs(ctx context.Context, clientIDs []uuid.UUID) (map[string][]model.AuthSession, error)
}

// OnlineDevice is a device of an account that currently has a live wsgateway
// connection, enriched with its non-expired sessions.
type OnlineDevice struct {
	Client model.AuthClient
	// DeviceKey is the wsgateway device id of the live connection, which is
	// the auth client id (auth_clients.id).
	DeviceKey string
	// SessionIDs are the device's non-expired sessions, newest
	// last_granted_at first.
	SessionIDs []string
	// LastGrantedAt is the newest last_granted_at among those sessions.
	LastGrantedAt *model.Time
}

// DevicePresence joins device identity (authoritative in auth_clients) with
// live connection presence (authoritative in Blade).
type DevicePresence struct {
	Store deviceStore
	Blade gen.WebSocketServiceClient
	Log   *slog.Logger
}

// NewDevicePresence wires the device presence service. blade may be nil when
// the wsgateway is not configured; every account then reports no device.
func NewDevicePresence(st deviceStore, blade gen.WebSocketServiceClient, log *slog.Logger) *DevicePresence {
	return &DevicePresence{Store: st, Blade: blade, Log: log}
}

// ForAccounts returns, for every requested account id, the devices that have
// a live wsgateway connection in the given namespace (empty = Blade's default
// namespace). Accounts with no live device map to an empty slice; unparseable
// account ids are skipped but still get an entry.
func (p *DevicePresence) ForAccounts(ctx context.Context, accountIDs []string, namespace string) (map[string][]OnlineDevice, error) {
	result := make(map[string][]OnlineDevice, len(accountIDs))
	requested := make([]string, 0, len(accountIDs))
	parsed := make([]uuid.UUID, 0, len(accountIDs))
	seen := make(map[string]bool, len(accountIDs))
	for _, raw := range accountIDs {
		accountID := strings.TrimSpace(raw)
		if accountID == "" || seen[accountID] {
			continue
		}
		seen[accountID] = true
		requested = append(requested, accountID)
		result[accountID] = []OnlineDevice{}
		if id, err := uuid.Parse(accountID); err == nil {
			parsed = append(parsed, id)
		}
	}
	if p.Store == nil || len(parsed) == 0 {
		return result, nil
	}

	clientsByAccount, err := p.Store.ListClientsByAccountIDs(ctx, parsed)
	if err != nil {
		return nil, fmt.Errorf("list clients by account ids: %w", err)
	}

	deviceIDsByAccount := map[string][]string{}
	if p.Blade != nil {
		resp, err := p.Blade.GetUsersConnectedWebsocketDeviceIds(ctx, &gen.DyGetUsersConnectedWebsocketDeviceIdsRequest{
			UserIds:   requested,
			Namespace: namespace,
		})
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrOnlineDevicesUnavailable, err)
		}
		for accountID, list := range resp.GetDevices() {
			if list != nil {
				deviceIDsByAccount[accountID] = list.GetDeviceIds()
			}
		}
	}

	matchedClientIDs := make([]uuid.UUID, 0, len(requested))
	seenClient := map[string]bool{}
	for _, accountID := range requested {
		devices := matchOnlineDevices(clientsByAccount[accountID], deviceIDsByAccount[accountID])
		result[accountID] = devices
		for _, device := range devices {
			if seenClient[device.Client.Id] {
				continue
			}
			seenClient[device.Client.Id] = true
			if id, err := uuid.Parse(device.Client.Id); err == nil {
				matchedClientIDs = append(matchedClientIDs, id)
			}
		}
	}
	if len(matchedClientIDs) == 0 {
		return result, nil
	}

	// Sessions attach per client: all non-expired sessions of a live client
	// count as online (there is no session-scoped presence key).
	sessionsByClient, err := p.Store.ListSessionsByClientIDs(ctx, matchedClientIDs)
	if err != nil {
		return nil, fmt.Errorf("list sessions by client ids: %w", err)
	}
	now := time.Now().UTC()
	for _, devices := range result {
		for i := range devices {
			devices[i].SessionIDs, devices[i].LastGrantedAt = liveSessions(sessionsByClient[devices[i].Client.Id], now)
		}
	}
	return result, nil
}

// matchOnlineDevices joins wsgateway device ids onto auth clients: the device
// id IS the auth client id, so only clients whose id was reported online are
// returned. Ids with no matching client (e.g. Blade's generated fallback
// device ids) are dropped. Deterministic: sorted by client id.
func matchOnlineDevices(clients []model.AuthClient, deviceIDs []string) []OnlineDevice {
	online := make(map[string]bool, len(deviceIDs))
	for _, raw := range deviceIDs {
		if id := strings.TrimSpace(raw); id != "" {
			online[id] = true
		}
	}
	devices := make([]OnlineDevice, 0, len(online))
	for _, client := range clients {
		if !online[client.Id] {
			continue
		}
		devices = append(devices, OnlineDevice{Client: client, DeviceKey: client.Id})
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].Client.Id < devices[j].Client.Id })
	return devices
}

// liveSessions filters a device's sessions down to the non-expired ones and
// orders them newest last_granted_at first (nil timestamps last, id as the
// tie-break so the order is stable).
func liveSessions(sessions []model.AuthSession, now time.Time) ([]string, *model.Time) {
	kept := make([]model.AuthSession, 0, len(sessions))
	for _, session := range sessions {
		if session.ExpiredAt != nil && !session.ExpiredAt.Time().After(now) {
			continue
		}
		kept = append(kept, session)
	}
	sort.Slice(kept, func(i, j int) bool {
		a, b := kept[i].LastGrantedAt, kept[j].LastGrantedAt
		if a == nil || b == nil {
			if a == nil && b == nil {
				return kept[i].Id < kept[j].Id
			}
			return b == nil
		}
		if a.Time().Equal(b.Time()) {
			return kept[i].Id < kept[j].Id
		}
		return a.Time().After(b.Time())
	})
	ids := make([]string, 0, len(kept))
	var newest *model.Time
	for _, session := range kept {
		ids = append(ids, session.Id)
		if newest == nil && session.LastGrantedAt != nil {
			newest = session.LastGrantedAt
		}
	}
	return ids, newest
}
