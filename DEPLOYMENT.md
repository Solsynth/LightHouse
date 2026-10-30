# Deploying Lighthouse

How to run **sphere** and **messager** (the Go ports of `DysonNetwork.Sphere` and
`DysonNetwork.Messager`), how to move the legacy data across, and what changes
for the rest of the fleet when the C# services are retired.

The repository is one workspace (`go.work`) over three Go modules: this module
(sphere + messager), `internal/stargate/` and `internal/blade/`. Stargate is the identity service
every authenticated request goes through, and Blade is the gateway that routes
public traffic to sphere and messager — so a replacement touches all of them
even though only sphere and messager are being ported here.

Read §7 before you start a cutover: it lists everything that behaves differently
from the C# services at runtime.

---

## 1. What you are deploying

| Service | Binary | HTTP | gRPC | Database | Config |
|---|---|---|---|---|---|
| Sphere | `bin/sphere` | `:5071` | `:7099` | `dyson_sphere` | `config/sphere.toml` |
| Messager | `bin/messager` | `:5072` | `:7100` | `dyson_messager` | `config/messager.toml` |
| Stargate | `bin/stargate` | `:8080` | `:9090` | `dyson_stargate` | `internal/stargate/config.toml` |
| Blade | `bin/blade` | `:6000` | `:7001` | – | `internal/blade/configs/config.toml` |

The sphere and messager port defaults match the C# services' `BaseUrl` and
`Service:Url`. Blade's shipped upstream table uses different numbers (§6.1) —
decide which side you align.

They are separate processes with separate databases and separate module sets.
They share only `internal/platform` code. Neither writes to the other's tables.

Both expose:

- `GET /health`, `GET /alive`, `GET /healthz` — probe targets for Blade and
  load balancers.
- `GET /api/modules` — the resolved module set and capabilities. Use it after
  every `[modules]` change.
- A canonical JSON 404 and the fleet `ApiError` envelope.

Images: `Dockerfile.sphere` and `Dockerfile.messager` (root context). Both are
`golang:1.27-alpine` build → `scratch` runtime: a static CGO-free binary, the
CA bundle, and `config/sphere.example.toml` (resp. messager) as
`/app/config.example.toml`. They take their config from `CONFIG_PATH`. No
secrets are baked in — mount `Keys/` at deploy time. CI publishes
`ghcr.io/<owner>/{sphere-next,messager-next}`; the `-next` suffix keeps the
rewrite's packages apart from the ones the legacy fleet still publishes.

Build locally (every target is workspace-aware; `./...` stops at a nested
`go.mod`, so the Makefile builds each module in turn):

```sh
make build            # sphere, messager, stargate, blade, relay
make build-sphere     # just bin/sphere
make run-sphere       # CONFIG_PATH=config/sphere.example.toml go run ./cmd/sphere
make run-messager
make run-stargate
make run-blade
```

---

## 2. Prerequisites

### PostgreSQL

Required. Both services refuse to start without a reachable database.

Extensions are created at boot and **must already be installed in the server**:

| Extension | Sphere | Messager | Needed by |
|---|---|---|---|
| `uuid-ossp` | ✓ | ✓ | legacy defaults |
| `pg_trgm` | ✓ | ✓ | trigram search (posts, chat message search) |
| `vector` (pgvector) | ✓ | – | `post_indices.embedding` |

pgvector is the one people miss. Without it the boot aborts with:

```
core migrations: apply "CREATE EXTENSION IF NOT EXISTS vector":
ERROR: extension "vector" is not available (SQLSTATE 0A000)
```

Install it in the image/host before the first start
(`postgresql-<major>-pgvector`, or `shared_preload_libraries` on managed
Postgres).

### Redis

Optional. Without `[redis].addr` the service falls back to a **process-local**
in-memory cache. See §7.7 — that is fine for one replica and wrong for many.

### NATS

Optional, but required for realtime chat and for award/sponsor settlement.
Chat's inbound websocket consumer needs JetStream enabled. See §7.8 and §7.9.

### Stargate and Passport

`internal/stargate/` in this repo is the identity service. Required for anything
authenticated. Bearer tokens are validated by calling
Stargate's `DyAuthService.Authenticate` — the same contract as the C#
`DysonTokenAuthHandler` — and `[AskPermission]` equivalents call
`DyPermissionService.HasPermission`.

The C# fleet splits those services over two hosts, so there are two targets:

| Target | Logical name | Services used by the port |
|---|---|---|
| `[services].stargate` | `https://_grpc.stargate` | `DyAuthService`, `DyPermissionService`, `DyAccountService`, `DyActionLogService`, `DyPresenceService`, `DyMlsService` |
| `[services].passport` | `https://_grpc.passport` | `DyProfileService`, `DyRealmService`, `DyDomainBlockService` |

Leave `passport` empty and the Stargate target is used for both, which suits a
deployment that serves them from one address.

### Blade

Optional but normal in the fleet: service discovery registration and the
`websocket_push` channel both go through Blade.

---

## 3. Configuration

One TOML per service, selected with `CONFIG_PATH` (default
`config.example.toml`). Every setting has an environment override, prefixed
`SPHERE_` / `MESSAGER_` with `__` for nesting:

| Env var | Section |
|---|---|
| `SPHERE_DATABASE__DSN` / `MESSAGER_DATABASE__DSN` | `[database]` |
| `SPHERE_HTTP_PORT` / `MESSAGER_HTTP_PORT` | `[http].port` |
| `SPHERE_GRPC_PORT` / `MESSAGER_GRPC_PORT` | `[grpc].port` |
| `SPHERE_SERVICES_STARGATE__GRPC` | `[services].stargate.grpc` |
| `SPHERE_SERVICES_PASSPORT__GRPC` | `[services].passport.grpc` |
| `SPHERE_SERVICES_SPHERE__GRPC` (messager only) | `[services].sphere.grpc` |
| `SPHERE_NATS_TARGET` / `MESSAGER_NATS_TARGET` | `[nats].target` |
| `SPHERE_REDIS_ADDR` / `MESSAGER_REDIS_ADDR` | `[redis].addr` |
| `SPHERE_MODULES_ENABLED` / `…_DISABLED` | `[modules]` (comma separated) |
| `SPHERE_SECURITY_TRUSTEDPROXYHOPS` | `[security].trustedProxyHops` |
| `SPHERE_DISCOVERY_*` | `[discovery]` (Blade registration) |

Sections worth reading before production:

```toml
[database]
dsn = "host=db port=5432 user=postgres dbname=dyson_sphere sslmode=require"

[grpc]
port = "7099"
useTLS = true            # fleet gRPC is TLS; keep this on
certFile = "Keys/server.crt"
keyFile  = "Keys/server.key"

[services]
stargate = { grpc = "stargate:9090" }
passport = { grpc = "" }                    # empty = reuse stargate
ring     = { grpc = "ring:7002" }
drive    = { grpc = "drive:7004" }
wallet   = { grpc = "wallet:7010" }
blade    = { grpc = "http://blade:7001" }   # plaintext h2c
sphere   = { grpc = "sphere:7099" }         # messager only

[security]
trustedProxyHops = 1     # Blade appends one X-Forwarded-For entry
maxTake = 100            # pagination cap; chat message list uses 1000

[modules]
enabled = []             # empty = every compiled-in module whose default is on
disabled = []            # e.g. ["app.solian.sphere.fediverse"]

[discovery]
enabled = true
target = "blade:7001"
registrationToken = "{{TOKEN}}"
service = "sphere"
```

Outbound targets are lazy and independent: an empty one disables only the
features that depend on it (push notifications, file hydration, wallet orders,
domain-block checks, …). The service still starts.

---

## 4. First boot

Boot each service once against the target database. This creates the schema
(AutoMigrate plus the modules' SQL hooks), seeds the config-driven rules
(`[automod.rules]`, `[fediverseModeration.rules]`, upserted by name) and starts
the jobs.

```sh
CONFIG_PATH=config/sphere.toml ./bin/sphere
CONFIG_PATH=config/messager.toml ./bin/messager
```

Check `/api/modules` and the log: every enabled module should appear, and the
only expected warnings are for integrations you deliberately left unconfigured.

---

## 5. Migrating data from the C# databases

The Go entities keep the legacy table and column names, so an import is a
straight copy — no schema translation, no idempotency bookkeeping table.

**Order: boot → import → restart.** The schema must exist before the importer
runs; seeds and backfills must see the imported rows, so restart afterwards.

```sh
# 1. boot once so dyson_sphere has its schema, then
SPHERE_SOURCE_DSN="postgres://…/dyson_sphere" \
SPHERE_TARGET_DSN="postgres://…/dyson_sphere" \
  go run ./cmd/sphere-migrate --dry-run

# 2. same command without --dry-run to write
make migrate-sphere        # or cmd/sphere-migrate with explicit flags

# 3. restart the service
```

`cmd/messager-migrate` is the same for Messager.

Properties of the importer:

- **Idempotent.** Rows are `COPY`-streamed into a staging table, then
  `INSERT … ON CONFLICT DO NOTHING`. Running it twice reports 0 rows the second
  time. It never truncates and never deletes.
- **Never blocks on a missing table.** A table the legacy database does not have
  is reported as skipped; a table the target does not have (you have not booted
  the service that owns it) is reported the same way.
- **Column-aware.** Only columns present in *both* databases are copied. A
  column the port added that the legacy table lacks keeps its default and is
  printed as `target-only columns keep defaults: …`. A column the legacy
  database has that the port does not is ignored.
- **Soft-deleted rows are copied with their `deleted_at`**, so history survives.

What is deliberately **not** copied:

- Realm permission tables (`sn_realm_role_permission`,
  `sn_realm_user_permission`) — those belong to Passport, not to these services.
- Legacy tables the port does not own at all (`fediverse_actors`, `polls`,
  `publisher_follow_requests`, …) are simply absent from the target; the
  importer reports them as skipped.

---

## 6. Cutting over

Blade is the gateway in this repo (`internal/blade/configs/config.toml`). It has two
things to change: the upstream table and the public route table.

### 6.1 Point the upstreams at the Go services

```toml
[services]
sphere   = { http = "http://sphere:5071",   grpc = "sphere:7099" }
messager = { http = "http://messager:5072", grpc = "messager:7100" }
```

The shipped table uses the C# fleet's container ports
(`sphere = { http = "http://localhost:8004", grpc = "localhost:7005" }`,
`messager = { http = "http://localhost:8008", grpc = "localhost:7009" }`).
Either edit these two entries, or set `[http].port` / `[grpc].port` in the
service configs to the old numbers. Whichever you pick, the HTTP and gRPC
numbers must both be right — Blade health-checks the HTTP endpoint and proxies
gRPC calls through the `grpc` address.

### 6.2 Add the public routes

Blade's route rule is `{ path, service, target, prefix }`. With `prefix = true`
Blade strips the matched prefix and forwards `target` plus the remainder:

```toml
# /sphere/posts?take=3  →  GET http://sphere:5071/api/posts?take=3
[[routes]]
path = "/sphere"
service = "sphere"
target = "/api"
prefix = true

# Whatever public prefix Messager already uses; target is the internal path.
[[routes]]
path = "/im"
service = "messager"
target = "/api/chat"
prefix = true
```

The shipped table only routes Stargate's `/.well-known/*` documents and
Sphere's `/.well-known/webfinger`, `/activitypub` and `/api/activitypub` — the
`/api/**` routes for posts, chat and the rest have to be added. Keep the
`/.well-known/webfinger` rule: it targets `/fediverse/.well-known/webfinger`,
which is the fediverse module's handler.

### 6.3 Keep the fleet's gRPC names working

Other services dial the logical name `https://_grpc.sphere`. Two ways to keep
that resolving:

- register the instance with Blade discovery — `[discovery] enabled = true`,
  `target = "blade:7001"`, `service = "sphere"` (Blade's gRPC listener; HTTP is
  `:6000`), or
- change the callers' service address in their own configuration.

### 6.4 The sequence

1. **Run both fleets side by side.** Deploy sphere and messager against
   *copy* databases (`dyson_sphere_go`, `dyson_messager_go`) first, import, and
   verify (§8) while the C# services keep serving.
2. **Add the Blade upstreams and routes above** but leave the C# entries in
   place; switch the route `service` field (or the upstream address) when you
   are ready.
3. **Flip the databases.** Re-run the importer one last time with writes
   stopped (it is idempotent), then switch `[database] dsn` and restart.
4. **Retire the C# services.** Leave their databases intact for rollback.

## 7. Noticeable things when replacing

Everything here is a real behavioural difference found by running the ports
against the legacy schema and code.

**7.1 Extensions must exist before the first boot.** §2. A missing pgvector
aborts Sphere before it listens on any port — deploy it with the image, not
after.

**7.2 HTTP and gRPC are separate listeners with separate flags.** The C# service
had one Kestrel host serving both; here `[http].port` and `[grpc].port` are
independent, and gRPC TLS is opt-in (`[grpc] useTLS`). The fleet dials
`https://_grpc.sphere`, so leave `useTLS = true` with the fleet certificates —
with it off, every `_grpc.sphere` caller fails the handshake.

**7.3 The route target matters.** The Go services serve `/api/**` internally.
With Blade's `prefix = true` rule the forwarded path is `target` + the remainder
of the public path, so `path = "/sphere"`, `target = "/api"` maps
`/sphere/posts` to `/api/posts`. A rule with `target = "/sphere"` (or no target
at all) forwards `/sphere/posts` unchanged and every request 404s.

**7.4 Two identity endpoints, not one.** The port reads auth/permissions
/accounts from `[services].stargate` and profiles/realms/domain blocks from
`[services].passport`. Setting only `stargate` works when both services are
reached there; setting `stargate` but not `passport` in a fleet where Passport
is a separate host makes realm lookups, profile hydration and domain-block
checks fail (§7.6 turns that into 503s and skipped enrichment). Configure both.

**7.5 No auth service configured means 401, not a fallback.** With
`[services].stargate.grpc` empty, the service starts and serves anonymous
routes, and every authenticated route answers
`401 {"code":"UNAUTHORIZED",…}`. There is no "dev mode" that lets requests
through.

**7.6 Permission checks fail closed.** If the permission gRPC is unreachable,
protected mutations answer `503 SERVICE_UNAVAILABLE` — they are never allowed
through. The same is true for a post that claims a `realm_id` while the realm
service or its membership API is down (`REALM_SERVICE_UNAVAILABLE`,
`REALM_MEMBERSHIP_UNVERIFIED`). Expect 503s, not silent success, when a
dependency is down.

**7.7 Redis is optional but one replica.** Without Redis the session cache, the
chat subscriber map, view dedupe and the various rate/interest caches are
process-local. With one replica that is equivalent; with several it is not — a
token revoked on one replica can stay valid on another for the cache TTL (1 h),
and read receipts/subscriptions stop being shared. Configure Redis before
scaling out.

**7.8 NATS events are the settlement path.** Sphere consumes:

| Stream | Subject | Consumer |
|---|---|---|
| `payment_events` | `payment_orders` | `sphere_posts_payment_handler` |
| `payment_events` | `payment_orders` | `sphere_livestream_awards` |
| `account_events` | `account.deleted` | `sphere_posts_account_deleted_handler` |

Awards (`posts.award`), livestream awards (`livestreams.award`) and sponsor bids
(`ads.sponsor`) are created as wallet orders and *confirmed only when those
events arrive*. Without NATS the order is created and never settles. If the C#
services already run consumers on `payment_events` under other names, both sets
coexist — JetStream consumers are independent, and the Go service creates its
own on first boot.

**7.9 Chat realtime moved from core NATS to a JetStream consumer.** The
Messager C# service consumed `WebSocketPacketEvent` on core NATS; the Go port
consumes `websocket_messager` on the `websocket_events` stream through a durable
consumer named `messager_websocket_packets`. That stream is created on first
boot if it does not exist. Blade must keep publishing the same subject and
envelope (`{event_id, timestamp, stream_name, account_id, device_id,
packet_bytes}` with a base64 `WebSocketPacket`). Outbound fan-out is unchanged:
the `websocket_push` subject and envelope are exactly as before.

**7.10 Schema compatibility is one-way.** The port keeps every legacy table and
column, so legacy rows import verbatim and a rollback to the C# services still
finds its data. The tables the C# model declared through `ModelBase` carry the
same `created_at`/`updated_at`/`deleted_at` columns here, including the ones
built from raw DDL or a composite key (`post_indices`, `publisher_members`,
`post_aggregated_stats`, `activity_pub_deliveries`,
`realm_post_moderation_logs`). The importer only fills columns both sides have,
so a table whose layout differs still copies.

**7.11 Module flags unmount surfaces, they do not drop tables.** After
`[modules] disabled = ["app.solian.sphere.fediverse"]`, that module's HTTP
routes answer 404 and its gRPC services answer `Unimplemented`, while its tables
stay in place with their data. Re-enabling reuses them. Check `/api/modules`
after every change — a silently disabled module looks exactly like a routing bug.

**7.12 Scheduled work runs per replica.** The Quartz jobs became in-process
goroutines (`0 0 0 * * ?` settlement, `0 0 6 * * ?` follow-request cleanup,
`0 0 3 * * ?` delivery cleanup, `0 */5 * * * ?` delivery health,
`0 0 * * * ?` sponsor auction, every 15 s scheduled publishing, every minute
view/interest/sticker flushes, every 5 minutes livestream ingress cleanup,
every 10 minutes post-index backfill). There is no leader election: with N
replicas each replica runs every job. Scale background workers to a single
replica, or accept duplicate runs (all of them are idempotent, but settlement
and delivery retries would run N×).

**7.13 The soft-delete recycling job is not ported.** The C# services
hard-deleted rows with `deleted_at` older than seven days on a daily schedule.
The Go ports keep them forever, so:

- deleted rows and their `deleted_at` (which several legacy unique indexes key
  on) accumulate — plan for the growth, or add your own retention job;
- if you later restore the C# services, they will find rows they expected to
  have been recycled.

**7.14 Realm progression events are not published.** The C# Sphere published
`realm_activity.post_created` on `realm_activity_events` (realm XP for posting).
That publisher is not ported: realm XP stops advancing for new posts unless
another producer emits the event.

**7.15 Localization is gone.** Messages are English-only; the C# services
localized error text and chat notifications by culture.

**7.16 Response-shape parity.** JSON is snake_case, enums are integers, times
are UTC RFC3339 with seconds, nullable fields are emitted as `null`, and
`ApiError.traceId` stays camelCase. Lists return a bare array with the total in
`X-Total`. `take` above `[security].maxTake` (default 100) answers
`PAGINATION_TAKE_EXCEEDED`; the chat message list is the documented exception
(cap 1000).

**7.17 Websocket packet surface.** Inbound chat packets (`messages.send`,
`messages.read`, `messages.typing`, `messages.subscribe`/`unsubscribe`,
`messages.test`, placeholder update/finalize) are implemented, but packets are
addressed per *account*, not per device: an ack reaches every device of the
account instead of only the sending `device_id`, and read receipts cannot
exclude a device.

**7.18 Some C# features are documented gaps.** See
[`docs/PORT_STATUS.md`](docs/PORT_STATUS.md) for the honest list — semantic
search, bot identities, ActivityPub HTML rendering, the realm role matrix,
livestream award payout transfer, localization, action logs.

---

## 8. Post-cutover verification

Run against the live endpoint (not localhost) after each step:

```sh
BASE=https://api…

curl -sf  $BASE/health                      # 200
curl -s  $BASE/api/modules | jq '.modules[].id'
curl -s  "$BASE/sphere/posts?take=3"        # Blade route → /api/posts
curl -s  "$BASE/sphere/timeline?take=5"
curl -s  "$BASE/sphere/publishers/search?query=<a known publisher>"
curl -s  -o /dev/null -w '%{http_code}\n' "$BASE/sphere/posts/drafts"   # 401
```

Then, with a real account token:

- sign in and read a timeline (proves Stargate auth + permissions);
- post, react, bookmark and reply (proves the payment/order and realtime paths
  when NATS is up);
- send a chat message from one device and confirm it arrives on another (proves
  the websocket consumer and Blade's `websocket_push`);
- `docker exec … psql -c '\dt'` on both databases and compare against
  `internal/sphere/migrate.Tables` / `internal/messager/migrate.Tables`;
- watch the logs for `WARN`/`ERROR` for the first hour — a missing integration
  logs once at boot, a failing one logs on every request.

---

## 9. Rollback

Nothing the port writes can damage the legacy state:

1. The C# services and their databases are never written by the port.
2. Point Blade's `[services]` upstreams (and the public routes, if you moved
   them) back at the C# instances, and drop the `_grpc.sphere` /
   `_grpc.messager` discovery registrations.
3. Stop the Go replicas.

The import is one-way — there is no reverse importer — so keep the C# database
dump until you are confident in the new deployment.

---

## 10. Operations reference

| Task | How |
|---|---|
| Health | `GET /health` on the HTTP port |
| Module state | `GET /api/modules` |
| Logs | stdout, `LOG_LEVEL` (`debug`/`info`/`warn`/`error`, default `info`) |
| Schema | created on boot; modules own their tables; nothing is ever dropped |
| Data import | `cmd/sphere-migrate`, `cmd/messager-migrate` (`--dry-run` first) |
| Container build | `Dockerfile.sphere`, `Dockerfile.messager`, `internal/stargate/Dockerfile`, `internal/blade/Dockerfile` (gateway and relay), `internal/filesystem/Dockerfile` |
| Local run | `make run-sphere`, `make run-messager` |
| Whole workspace | `make build` builds sphere, messager, stargate, blade, relay, dysonfs |
| Checks | `make test`, `make lint` |
