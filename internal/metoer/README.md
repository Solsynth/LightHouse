# Metoer

Go replacement for **DysonNetwork.Ring** — the Solar Network's realtime
service: notifications, push delivery (FCM / APNs / UnifiedPush / SOP),
email sending plans and delivery observability. A strict 1:1 behavioral
port, following the house pattern established by the
[Stargate](https://github.com/sosys/stargate) port of Padlock/Passport.

## Layout

```
cmd/metoer/            entrypoint (HTTP :8080 + gRPC :9090, TLS optional)
internal/config        TOML config + METOER_* env overrides
internal/db, migrate   pgx pool + embedded SQL migrations (non-destructive)
internal/store         Postgres access layer (mirrors EF Core queries incl.
                       the global soft-delete filter)
internal/model         entity + wire models (snake_case JSON, nulls included)
internal/queue         pusher_queue JetStream producer + durable consumer
internal/push          PushService port: senders, SOP streams, replay,
                       websocket fan-out, invalid-token flush buffer
internal/email         SMTP sender + email sending-plan state machine
internal/observability delivery records (never break the delivery path)
internal/grpcclient    outbound clients (stargate: account/auth/permission/
                       profile/action-log; blade: websocket)
internal/grpcserver    DyRingService + capabilities + reflection + health
internal/httpserver    gin + /api controllers + swagger manifest
internal/middleware    DysonTokenAuthHandler + RemotePermissionMiddleware ports
internal/scheduler     the four Quartz jobs (UTC schedules)
internal/events        websocket_push publisher + filesystem listener
```

## Run

Requires Postgres (`dyson_ring` — the **same live database the C# fleet
uses**), Redis and NATS with JetStream.

```sh
make run            # CONFIG_PATH=config.example.toml
```

`CONFIG_PATH` selects the TOML file; `METOER_*` env vars override individual
keys (`METOER_DATABASE__DSN`, `METOER_SERVICES_STARGATE__GRPC`,
`METOER_NATS_TARGET`, …). Push keys (`Keys/Solian.json`, `Keys/Solian.p8`)
are mounted at deploy time, never baked into the image.

## Compatibility notes

- **API JSON**: snake_case with **nulls INCLUDED** (Ring's `AddJsonOptions`
  sets no `DefaultIgnoreCondition`) — unlike Stargate's omit-nulls
  convention, do not add `omitempty` to API-facing fields. Enum values are
  ints; instants are RFC3339 UTC seconds. `meta` dictionary keys are
  snake-cased on outbound responses (STJ `DictionaryKeyPolicy`).
- **Queue wire format** (`pusher_queue`, JetStream): the envelope is
  snake_case with nulls included; the `data` payload is PascalCase with
  NodaTime instants serialized as `{}` (STJ default options — verified
  empirically). The C# consumer round-trips `{}` to epoch; Go does the
  same, preserving real timestamps on the DB-write path (notifications are
  saved before enqueue).
- **NATS**: stream `pusher_queue` (subject `pusher_queue`), durable consumer
  `pusher_workers` with deliver group `pusher_workers`, AckPolicy Explicit,
  MaxDeliver 5, DeliverPolicy All on first creation. Core subject
  `websocket_push` carries the `{namespace,target,ids,excluded_device_ids,
  packet}` envelope (packet = base64 protojson `DyWebSocketPacket` with
  `notifications.new`). JetStream `account_events` /
  `accounts.last_active` (PascalCase `LastActiveEvent`) is published by the
  auth middleware (throttled 1m per account).
- **Redis keys** (shared with the C# fleet via the `dyson:` prefix):
  `ring:sop:replay:*` (+ lock), `auth:session:*`, `auth:profile:*`,
  `auth:last_seen_touch:*`.
- **Auth** runs over gRPC to Stargate (`DyAuthService.Authenticate`);
  **permissions** over `DyPermissionService` (Stargate), with the OAuth
  scope gate and superuser bypass. With no `services.stargate` target the
  middleware leaves requests anonymous (auth-gated routes 401; the C#
  `RpcException` path degrades the same way with the dependency down).
- **Filesystem listener**: `filesystem.file.updated.v1` on `filesystem_events`
  is consumed and acked; Ring's schema has no reference-typed jsonb columns,
  so the updater is a no-op (0 rows).
- **Migration DDL** is idempotent (`CREATE ... IF NOT EXISTS`, no DROPs) —
  the `dyson_ring` database stays shared with the C# fleet until cutover.

## Cutover (documented, not applied)

1. Blade config delta: `[services] metoer = { http = "http://localhost:8080",
   grpc = "localhost:9090" }`; add `metoer` to `[endpoints] serviceNames`.
2. Caddy delta so old clients keep calling `/ring/**`:
   `@ring path /ring/*` → `rewrite @ring /metoer{path}` before the final
   `reverse_proxy` (pattern from Stargate's Caddyfile).
3. Point the fleet's `services__ring__grpc__0` env at Metoer's gRPC
   endpoint, then stop the C# Ring instances (same DB, NATS and Redis).

## Known deviations (intentional, same class as Stargate's)

- Swagger UI/manifest is hand-built (byte-identity with Swashbuckle is a dev
  tool concern only).
- `meta` snake-casing handles nested `map[string]any` keys (STJ's policy
  applies to `Dictionary` instances; passthrough of raw `JsonElement`
  subtrees is not replicated).
- The auth middleware uses the Go shared-cache format for
  `auth:profile:*` (plain value vs the C# HSET envelope); both sides treat
  the other's format as a miss and refetch.
- The in-process SOP stream queue is unbounded (slice-backed cond queue),
  matching `Channel.CreateUnbounded` semantics.
