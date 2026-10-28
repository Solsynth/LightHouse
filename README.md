# Lighthouse

The Go monorepo for the Solar Network services. Current contents:

- **sphere** — the port of `DysonNetwork.Sphere`: posts, publishers, timelines,
  categories/tags/collections, surveys, stickers, automod, the web reader,
  livestreams and ActivityPub federation.
- **messager** — the port of `DysonNetwork.Messager`: chat rooms, members,
  groups, messages, reactions, pins, voice clips and realtime calls.

Sphere and Messager are **separate services**: separate binaries, separate
databases, separate module sets. They share the platform layer
(`internal/platform`) and nothing else. Blade and Stargate move into this
monorepo later; the layout leaves room for them.

## Layout

```
cmd/sphere                sphere process
cmd/messager              messager process
cmd/sphere-migrate        legacy Sphere/Postgres import
cmd/messager-migrate      legacy Messager/Postgres import
config/                   example TOML per service
docs/MODULE_CONTRACT.md   the interface every module is written against
internal/platform/        shared house infrastructure
  module/                 compile-time module registry (Caddy style)
  app/                    process bootstrap (HTTP + gRPC + discovery)
  config/                 TOML loader + env overrides
  db/ redis/ eventbus/    shared dependencies
  authn/                  inbound token validation through Stargate
  grpcclient/             outbound fleet clients
  grpccore/               health, reflection, capabilities
  httpx/                  gin engine, ApiError, pagination
  jobs/                   interval + Quartz-cron scheduler
  discovery/ legacyimport/
internal/sphere/          sphere service
  config/ core/ app/ model/ store/ ports/ migrate/
  modules/*               feature modules (posts, publishers, ...)
internal/messager/        messager service (same shape)
```

## Module system

Both services use the same compile-time registry: a `module.Factory[C]` carries
the module ID, dependencies, ports, GORM models, schema hooks and capabilities;
`Configure` mounts its HTTP routes and gRPC services. `[modules]` in the TOML
selects what runs:

```toml
[modules]
enabled = []                     # empty = every module whose default is on
disabled = ["app.solian.sphere.fediverse"]
```

Disabling a module unmounts its surface and, on a fresh database, prevents its
tables from being created. Existing tables are never dropped. `GET /api/modules`
reports the resolved set and capabilities.

`docs/MODULE_CONTRACT.md` is the authoring contract: helpers, wire-type rules,
persistence conventions and route-fidelity requirements.

## Run

```sh
cp config/sphere.example.toml config/sphere.toml
cp config/messager.example.toml config/messager.toml
make build
CONFIG_PATH=config/sphere.toml ./bin/sphere
CONFIG_PATH=config/messager.toml ./bin/messager
```

Postgres is required. Redis, NATS and every `[services]` target are optional: a
missing one degrades only the features that depend on it (session caching falls
back to memory, events stop, enrichment/lookup/notification features are
skipped).

Sphere listens on `:5071` (HTTP) and `:7099` (gRPC); Messager on `:5072` and
`:7100`, matching the C# services. Both expose `/health`, `/alive`, `/api/modules`
and a canonical JSON 404.

## Legacy data

The Go entities keep the legacy table and column names, so the C# databases
import directly:

```sh
# 1. boot the service once so the schema exists
CONFIG_PATH=config/sphere.toml ./bin/sphere
# 2. copy the rows (idempotent; never truncates)
SPHERE_SOURCE_DSN=postgres://…/dyson_sphere \
SPHERE_TARGET_DSN=postgres://…/dyson_sphere_go \
  go run ./cmd/sphere-migrate --dry-run
# 3. restart the service
```

`sphere-migrate` / `messager-migrate` COPY the source rows into a staging table
and then `INSERT … ON CONFLICT DO NOTHING`, so re-running is safe. Soft-deleted
rows are copied with their `deleted_at` intact.

Status, verification and the known gaps are in [`docs/PORT_STATUS.md`](docs/PORT_STATUS.md).

## Compatibility notes

- JSON is snake_case; enums are integers; times are UTC RFC3339 (seconds);
  nullable fields are emitted as `null` unless the C# model marked them
  `[JsonIgnore]`. `ApiError.traceId` stays camelCase.
- List endpoints return a bare array with the total in `X-Total`, and `take`
  above the configured cap answers `PAGINATION_TAKE_EXCEEDED`.
- Inbound tokens are validated by Stargate's `DyAuthService.Authenticate` over
  gRPC, exactly like the C# `DysonTokenAuthHandler`; no service validates JWTs
  locally.
- Outbound events keep the fleet wire shapes: JetStream subjects and the
  `websocket_push` envelope Blade's gateway consumes.
