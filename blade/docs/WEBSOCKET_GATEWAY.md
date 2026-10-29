# WebSocket Gateway — Multi-Tenant Namespaces

The wsgateway supports namespace-based multi-tenancy, allowing isolated connection scopes within a single gateway instance. Each namespace maintains its own connection registry, online presence, and per-namespace configuration overrides.

## Concepts

- **Namespace** — a string identifier that scopes all connection, presence, and push operations. Two clients connecting with the same `accountID` + `deviceID` in different namespaces are treated as completely independent connections.
- **Default namespace** — when a caller does not specify a namespace (empty string), the service falls back to `Config.DefaultNamespace` (defaults to `"_default"`). This ensures full backwards compatibility.

## What is isolated per namespace

| Concern | Scope |
|---|---|
| Connection registry (`connectionKey`) | namespace + accountID + deviceID |
| Online presence (Redis keys & local map) | namespace |
| Push/broadcast (`SendPacketToAccount`, `SendPacketToDevice`) | namespace |
| Session revocation disconnect | namespace |
| Debug/query endpoints | namespace |

## What is NOT isolated

- Auth: all namespaces share the same token authenticator.
- Event publishing (NATS `connect`/`disconnect` events): namespace is not part of the event subject — these are still account/device scoped.
- NATS push subject: all namespaces publish/subscribe on the same NATS subject. The namespace is embedded in the event payload.

## Configuration

```go
wsgateway.Config{
    KeepAliveInterval: 60 * time.Second, // fallback for namespaces without override
    MaxMessageBytes:   4 * 1024,
    DefaultNamespace:  "_default",
    Namespaces: map[string]wsgateway.NamespaceConfig{
        "_default": {
            KeepAliveInterval: 60 * time.Second,
            MaxMessageBytes:   4 * 1024,
        },
        "mobile-app": {
            KeepAliveInterval: 30 * time.Second,
            MaxMessageBytes:   8 * 1024,
        },
    },
}
```

Per-namespace config fields override the top-level fallback. Any field left at zero value inherits the top-level default.

### Config fields

| Field | Description |
|---|---|
| `KeepAliveInterval` | Presence refresh interval. Affects how long a stale connection survives without a heartbeat. |
| `MaxMessageBytes` | Maximum inbound packet size for connections in this namespace. |

## HTTP Connection

Clients specify namespace via the `namespace` query parameter:

```
GET /ws?namespace=mobile-app
GET /ws                          # uses DefaultNamespace
GET /ws?namespace=               # uses DefaultNamespace
```

The namespace is validated at connection time and stored on the connection for its lifetime.

## Redis Presence Keys

The key structure changed to include namespace:

```
# Before
{prefix}:account:{accountID}
{prefix}:device:{deviceID}
{prefix}:accounts

# After
{prefix}:ns:{namespace}:account:{accountID}
{prefix}:ns:{namespace}:device:{deviceID}
{prefix}:ns:{namespace}:accounts
{prefix}:ns:{namespace}:account-devices:{accountID}   # per-account live device index (member = device id, score = lease expiry)
```

**Migration note**: Existing presence data in Redis will not be visible after upgrade. Connections will re-register under the new keys naturally as clients reconnect. No manual migration is needed.

## API Changes

All presence, query, and push methods now take a `namespace` parameter as the first argument. Pass empty string `""` to use the default namespace.

### Service methods

```go
// Connection management
svc.TryAdd(auth, namespace, deviceID, conn, cancel)
svc.TryAddUniqueDevice(namespace, account, deviceID, conn)
svc.HandleConnection(ctx, auth, namespace, deviceID, conn)
svc.Disconnect(namespace, accountID, deviceID, reason)
svc.DisconnectSession(namespace, sessionID, accountID, deviceID, reason)

// Online presence
svc.GetDeviceIsConnected(namespace, deviceID) bool
svc.GetAccountIsConnected(namespace, accountID) bool
svc.GetConnectedDeviceIDs(namespace, deviceIDs) []string
svc.GetAllConnectedUserIDs(namespace) []string
svc.GetAllConnectedDeviceIDs(namespace) []string
svc.GetDevicesByAccount(namespace, accountID) []string
svc.GetAccountsByDevice(namespace, deviceID) []string
svc.GetUserConnectedDeviceIDs(namespace, accountID) []string   // cross-replica account→device query
svc.GetUsersConnectedDeviceIDs(namespace, accountIDs) map[string][]string

// Push (namespace-scoped)
svc.SendPacketToAccount(namespace, accountID, packet)
svc.SendPacketToAccountExcept(namespace, accountID, packet, excludedDeviceIDs)
svc.SendPacketToDevice(namespace, deviceID, packet)

// Cross-namespace
svc.GetConnectionSnapshots() []ConnectionSnapshot  // includes namespace field
```

### PresenceStore interface

```go
type PresenceStore interface {
    Register(ctx, namespace, accountID, deviceID, connectionID string) error
    Refresh(ctx, namespace, accountID, deviceID, connectionID string) error
    Remove(ctx, namespace, accountID, deviceID, connectionID string) error
    AccountConnected(ctx, namespace, accountID string) (bool, error)
    DeviceConnected(ctx, namespace, deviceID string) (bool, error)
    DevicesConnected(ctx, namespace string, deviceIDs []string) (map[string]bool, error)
}
```

### PushPublisher interface (NATS)

```go
type PushPublisher interface {
    PublishAccount(ctx, namespace, accountID string, packet, excluded) error
    PublishDevices(ctx, namespace string, deviceIDs []string, packet) error
}
```

The NATS push event payload now includes a `namespace` field. When subscribing to pushes, the namespace is resolved from the event and used to route to the correct connection scope. Empty namespace in the event falls back to the service's default namespace.

### gRPC

All gRPC methods pass empty namespace `""` (default) for backwards compatibility. The proto definitions are unchanged — namespace support at the gRPC layer is not yet exposed.

### Debug endpoints

Debug endpoints accept an optional `namespace` query parameter:

```
GET /debug/ws/summary?namespace=mobile-app
GET /debug/ws/account/{accountId}?namespace=mobile-app
GET /debug/ws/device/{deviceId}?namespace=mobile-app
```

`/debug/ws/summary` and `/debug/ws/connections` include namespace information in their response. `GetConnectionSnapshots()` returns snapshots across all namespaces.

## Use cases

### Multiple product lines on one gateway

Different client apps (e.g., web app vs mobile app) can use separate namespaces to avoid device ID collisions while sharing the same gateway infrastructure. Each namespace can have its own keepalive interval and message size limits tuned for its client profile.

### Per-namespace online observation

Different tenants may have different SLA requirements for online detection. Shorter `KeepAliveInterval` for real-time namespaces, longer intervals for less latency-sensitive ones.

### Gradual rollout

Deploy a new namespace alongside the default to test configuration changes without affecting existing clients. Once validated, migrate clients by having them pass the new namespace in their connection URL.

## Ack tracking / at-least-once delivery

Client-side requirements are in
[`WEBSOCKET_PUSH_ACKS_CLIENT.md`](./WEBSOCKET_PUSH_ACKS_CLIENT.md).

Push delivery used to be fire-and-forget: a full outbound queue, a write error, an
offline device or a replica restart silently lost the frame. With ack tracking
enabled, `messages.*` pushes instead survive until the client says it handled them.

### Scope

Tracking is opt-in **per connection**, not per deployment: a connection becomes
tracked only after the client sends `session.resume` or `session.ack` on it.

| Connection | `messages.*` delivery | `_ack_id` | Redis entry | Re-sends |
|---|---|---|---|---|
| Ack-capable (sent `session.resume`/`session.ack`) | At-least-once until acked, budget exhausted, or retention passes | yes | yes | up to `ackMaxAttempts` |
| Legacy (never sent either) | Best effort, byte-identical to before this change | no | no | none |

The capability gate is `shouldTrack` (`internal/wsgateway/ack.go`): the packet
family alone is not enough, because a client that cannot ack would otherwise be retried
forever for behaving exactly as it was written. The connection is marked capable
in the receive loop (`HandleConnection`) before the packet is dispatched, so the
**first** push after a `session.resume` is already tracked. A reconnect resets the
flag — every connection generation re-establishes it with its own resume.

`messages.*` is the tracked family because its client consumers key on the message
id, so a re-delivered frame is idempotent client-side. The family is the single
constant `ackedPacketPrefix` (`internal/wsgateway/ack.go`). Adding a family later
needs no client change (clients ack any frame that carries an `_ack_id`); it needs
an idempotency audit of that family's consumers.

### Wire protocol

An ack-tracked frame carries one extra JSON key. It exists only on the JSON egress
path; `DyWebSocketPacket` and `packetToProto`/`packetFromProto` are untouched, so
there is no proto change and clients that ignore unknown keys keep working.

```json
{"type":"messages.new","data":{"...":"..."},"_ack_id":"5f0d1c2e-..."}
```

The client answers with the ack id it just received:

```json
{"type":"session.ack","data":{"ack_id":"5f0d1c2e-..."}}
```

and asks for everything still pending for its account+device on connect:

```json
{"type":"session.resume","data":null}
```

Both are intercepted in `HandlePacket` before the handler/forwarder lookup, are
valid for any packet family, and produce no error frame on success. An unknown or
repeated ack id is a no-op — acks are idempotent by design.

`session.resume` replays pending frames in send order and **does not** remove them:
only an ack clears an entry. A resume and a reconciler pass may therefore deliver
the same frame twice in the same window, which is exactly why client-side
idempotency is a precondition, not a nicety.

### Redis keys

```
ws:ack:pending:<namespace>:<accountID>   # list, append order = send order, element = pending entry JSON
ws:ack:index:<namespace>:<accountID>     # hash, ack_id -> device_id
```

Acking is one `HDEL` on the index; the list element it orphans is pruned lazily by
the next `List`/`Drop`. Both keys carry `EXPIRE` = retention on every write, so an
abandoned account never leaks memory.

When checking by hand, note the two keys move at different times:

- `redis-cli HLEN ws:ack:index:<ns>:<account>` drops **immediately** on ack — this
  is the authoritative "still unacknowledged" count.
- `redis-cli LLEN ws:ack:pending:<ns>:<account>` drops on the next `List` pass (the
  reconciler runs one every 3s), because the orphaned element is pruned lazily.

A pending list that reaches `0` in both keys is the answer to "did the client
receive it".

Deleting the pending list is safe at any time (in-flight retries stop, nothing
crashes); deleting only the index makes the pending entries get pruned.

### Semantics and the residual gap

- An entry is created before the frame is written, so an instant ack always finds it.
- The reconciler (`RunAckReconciler`, one pass per `ackReconcileInterval`, 3s)
  re-sends every entry whose `next_try_at` has passed, and drops entries at
  `ackMaxAttempts` (20) attempts or `ackRetentionSeconds` of age, logging one Warn
  per dropped ack id. A device with no live connection gets its attempts counted but
  no bytes, which is what bounds the buffer while it stays offline.
- **Every replay is capped, in both directions:**
  - per frame: `ackMaxAttempts` (20) re-sends, then the entry is dropped with a Warn;
  - per resume: one `session.resume` replays at most 64 frames. The overflow is not
    lost — it stays pending and the reconciler retries it on its own schedule. This
    bounds a client that reconnects in a loop, which would otherwise re-receive the
    whole buffer on every connect.
- Replays are written through a queue wait (`sendJSONReplay`) instead of the live
  push path. The live path closes the connection when the queue is full, which is
  correct for one stuck client but catastrophic for a replay burst larger than the
  priority queue (16): it would tear down the connection it is repairing.
- The pending list is capped (`ackMaxPending`, oldest evicted first, one Warn per
  eviction). Exceeding 90% of the cap logs a "client is not acknowledging" Warn.
- **Known gap:** cross-replica pushes travel core NATS (publish/subscribe), so a
  frame lost inside the broker before it reaches the target replica is still lost —
  the tracker fixes queue-full, write-error, offline-device and replica-restart
  drops only. Making the bus durable is wiring, not new infrastructure: `eb.Bus`
  already exposes JetStream (`EnsureStream`/`Consume`).

### Configuration

```toml
[websocket]
ackTracking = false          # needs cache.redisUrl; off = fire-and-forget
ackRetrySeconds = 3          # re-send interval per unacknowledged frame
ackRetentionSeconds = 600    # retry horizon; older entries are dropped
ackMaxPending = 256          # per-account buffer cap, oldest evicted first
ackMaxAttempts = 20          # re-sends per frame before it is dropped
```

With `ackTracking = true` and no Redis configured, the gateway logs a Warn and keeps
today's behaviour. `Service.SetAckTracker` stays optional, so a service built without
a tracker (unit tests, single-node dev) behaves exactly as before.
