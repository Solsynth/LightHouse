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

	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/store"
)

// ErrOnlineDevicesUnavailable marks a failed live-connection lookup against
// the wsgateway (Blade unreachable or erroring). ForAccounts wraps it so
// callers can degrade this case separately from local store failures.
var ErrOnlineDevicesUnavailable = errors.New("resolve online devices")

// deviceStore is the read surface device presence needs; *store.Store
// satisfies it. Narrowed to an interface so the join can be tested without
// Postgres.
//
// Presence never lists devices per account: a 128-account batch used to load
// every device of every account (730 rows for one observed call) plus every
// session ever attached to them, only to discard all but the handful Blade
// reported online. It resolves the online client ids (which ARE auth_clients
// ids) by primary key instead.
type deviceStore interface {
	GetClientsByIDs(ctx context.Context, clientIDs []uuid.UUID) (map[string]model.AuthClient, error)
	ListDeviceSessionsByClientIDs(ctx context.Context, clientIDs []uuid.UUID) (map[string][]store.DeviceSession, error)
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
	seen := make(map[string]bool, len(accountIDs))
	for _, raw := range accountIDs {
		accountID := strings.TrimSpace(raw)
		if accountID == "" || seen[accountID] {
			continue
		}
		seen[accountID] = true
		requested = append(requested, accountID)
		result[accountID] = []OnlineDevice{}
	}
	if p.Store == nil || len(requested) == 0 {
		return result, nil
	}

	var deviceIDsByAccount map[string][]string
	if p.Blade != nil {
		resp, err := p.Blade.GetUsersConnectedWebsocketDeviceIds(ctx, &gen.DyGetUsersConnectedWebsocketDeviceIdsRequest{
			UserIds:   requested,
			Namespace: namespace,
		})
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrOnlineDevicesUnavailable, err)
		}
		deviceIDsByAccount = make(map[string][]string, len(resp.GetDevices()))
		for accountID, list := range resp.GetDevices() {
			if list != nil {
				deviceIDsByAccount[accountID] = list.GetDeviceIds()
			}
		}
	}

	// Blade reports the online device ids (auth client ids). Resolve just
	// those rows: everything else about the accounts is irrelevant here.
	onlineClientIDs := make([]uuid.UUID, 0, len(requested))
	seenClient := map[string]bool{}
	for _, accountID := range requested {
		for _, raw := range deviceIDsByAccount[accountID] {
			id := strings.TrimSpace(raw)
			if id == "" || seenClient[id] {
				continue
			}
			parsed, err := uuid.Parse(id)
			if err != nil {
				continue
			}
			seenClient[id] = true
			onlineClientIDs = append(onlineClientIDs, parsed)
		}
	}
	if len(onlineClientIDs) == 0 {
		return result, nil
	}
	clients, err := p.Store.GetClientsByIDs(ctx, onlineClientIDs)
	if err != nil {
		return nil, fmt.Errorf("load online clients: %w", err)
	}

	// A client is reported under its own account; ids Blade invented (its
	// fallback device ids) have no auth_client row and drop out here.
	matchedClientIDs := make([]uuid.UUID, 0, len(clients))
	for _, accountID := range requested {
		devices := matchOnlineDevices(clients, deviceIDsByAccount[accountID])
		result[accountID] = devices
		for _, device := range devices {
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
	sessionsByClient, err := p.Store.ListDeviceSessionsByClientIDs(ctx, matchedClientIDs)
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

// matchOnlineDevices joins wsgateway device ids onto the resolved clients: the
// device id IS the auth client id, so only clients whose id was reported
// online are returned. Ids with no matching client (e.g. Blade's generated
// fallback device ids) are dropped. Deterministic: sorted by client id.
func matchOnlineDevices(clients map[string]model.AuthClient, deviceIDs []string) []OnlineDevice {
	devices := make([]OnlineDevice, 0, len(deviceIDs))
	seen := make(map[string]bool, len(deviceIDs))
	for _, raw := range deviceIDs {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		client, ok := clients[id]
		if !ok {
			continue
		}
		seen[id] = true
		devices = append(devices, OnlineDevice{Client: client, DeviceKey: client.Id})
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].Client.Id < devices[j].Client.Id })
	return devices
}

// liveSessions filters a device's sessions down to the non-expired ones and
// orders them newest last_granted_at first (nil timestamps last, id as the
// tie-break so the order is stable).
func liveSessions(sessions []store.DeviceSession, now time.Time) ([]string, *model.Time) {
	kept := make([]store.DeviceSession, 0, len(sessions))
	for _, session := range sessions {
		if session.ExpiredAt != nil && !session.ExpiredAt.After(now) {
			continue
		}
		kept = append(kept, session)
	}
	sort.Slice(kept, func(i, j int) bool {
		a, b := kept[i].LastGrantedAt, kept[j].LastGrantedAt
		if a == nil || b == nil {
			if a == nil && b == nil {
				return kept[i].ID.String() < kept[j].ID.String()
			}
			return b == nil
		}
		if a.Equal(*b) {
			return kept[i].ID.String() < kept[j].ID.String()
		}
		return a.After(*b)
	})
	ids := make([]string, 0, len(kept))
	var newest *model.Time
	for _, session := range kept {
		ids = append(ids, session.ID.String())
		if newest == nil && session.LastGrantedAt != nil {
			newest = model.NewTime(*session.LastGrantedAt)
		}
	}
	return ids, newest
}
