# Lighthouse

The Go monorepo for the Solar Network services. Five services, four Go
modules, one workspace:

- **sphere** — the port of `DysonNetwork.Sphere`: posts, publishers, timelines,
  categories/tags/collections, surveys, stickers, automod, the web reader,
  livestreams and ActivityPub federation.
- **messager** — the port of `DysonNetwork.Messager`: chat rooms, members,
  groups, messages, reactions, pins, voice clips and realtime calls.
- **stargate** — the port of `DysonNetwork.Padlock` (auth, accounts, OIDC,
  admin, E2EE/MLS) plus the Passport account/profile domain. Every other
  service validates inbound tokens through it.
- **blade** — the API gateway: reverse proxy, health checks, gRPC service
  discovery, L4 TLS-SNI relays (`cmd/relay`) and the websocket gateway.
- **filesystem** — DysonFS, the file service: uploads, media processing,
  previews, WebDAV/WOPI and S3 gateways, in master, worker and storage-node
  modes.

Sphere and Messager are **separate services**: separate binaries, separate
databases, separate module sets. They share the platform layer
(`internal/platform`) and nothing else. Stargate, Blade and DysonFS live in
their own modules under `stargate/`, `blade/` and `filesystem/`, keep their own
`go.mod`, Dockerfiles and configs, and talk to the other services over
HTTP/gRPC/NATS only. DysonFS is the one child outside `go.work` (see below).

## Layout

```
go.work                   workspace: ., ./stargate, ./blade
go.mod                    module src.solsynth.dev/sosys/lighthouse
cmd/sphere                sphere process
cmd/messager              messager process
cmd/sphere-migrate        legacy Sphere/Postgres import
cmd/messager-migrate      legacy Messager/Postgres import
config/                   example TOML per Lighthouse service
docs/MODULE_CONTRACT.md   the interface every Lighthouse module is written against
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
stargate/                 module src.solsynth.dev/sosys/stargate
  cmd/stargate              the service (HTTP :8080, gRPC :9090)
  cmd/stargate-migrate      legacy Padlock/Passport import
  cmd/stargate-module-import  per-module legacy import
  cmd/stargate-hash-password  bcrypt hash for [auth.singleUser]
  internal/                 module registry, modules, controllers, store
  web/                      Vue 3 + Vite console (served by internal/webui)
  config.example.toml       see stargate/README.md
blade/                    module srv.solsynth.dev/sosys/blade
  cmd/                      gateway (:6000 HTTP, :7001 gRPC)
  cmd/relay                 L4 TLS-SNI relay node (:443)
  internal/                 proxy, discovery, health, wsgateway, relay
  configs/                  config.toml, docker.toml, relay.toml
  docs/                     see blade/README.md
filesystem/               module src.solsynth.dev/sosys/filesystem
  cmd/                      dysonfs (master/worker/storage modes)
  internal/                 module registry, media, storage, S3, WebDAV, WOPI
  web/                      Vue 3 + Vite console
  config.example.toml       see filesystem/README.md
```

## Workspace

`go.work` uses the Lighthouse, Stargate and Blade modules so one checkout
builds and tests them with a single toolchain (`go 1.27`). Nothing is published
between them yet — each service still has its own dependency graph and
`go.sum`.

Two consequences worth knowing:

- Directory patterns stop at a nested `go.mod`, so `go build ./...` at the
  root covers only sphere/messager. Use the Makefile (or `go build
  ./stargate/...` / `go build ./blade/...` from the root) for the whole tree.
- Docker images build with a child directory as context (`./stargate`,
  `./blade`, `./filesystem`), so `go.work` never enters an image and the
  children stay buildable on their own. Their `Makefile`s keep working from
  inside the child directory.

`filesystem/` is **not** in `go.work`: it pins `go1.26.2`, and under `go1.27`
its `golang.org/x/net` v0.54.0 compiles the stdlib-backed http2 server, which
no longer exports `http2.TrailerPrefix` that its `google.golang.org/grpc`
v1.79.1 still references. The root Makefile and `filesystem/Makefile` run it
with `GOWORK=off GOTOOLCHAIN=go1.26.2`; drop that once the grpc/x-net pair is
upgraded.

## Module system

Both Lighthouse services use the same compile-time registry: a
`module.Factory[C]` carries the module ID, dependencies, ports, GORM models,
schema hooks and capabilities; `Configure` mounts its HTTP routes and gRPC
services. `[modules]` in the TOML selects what runs:

```toml
[modules]
enabled = []                     # empty = every module whose default is on
disabled = ["app.solian.sphere.fediverse"]
```

Disabling a module unmounts its surface and, on a fresh database, prevents its
tables from being created. Existing tables are never dropped. `GET /api/modules`
reports the resolved set and capabilities. Stargate and DysonFS use the same
shape (see [`stargate/docs/module-system-plan.md`](stargate/docs/module-system-plan.md)
and [`filesystem/docs/module-system.md`](filesystem/docs/module-system.md)).

`docs/MODULE_CONTRACT.md` is the authoring contract: helpers, wire-type rules,
persistence conventions and route-fidelity requirements.

## Run

| binary | HTTP | gRPC | config |
|---|---|---|---|
| `sphere` | `:5071` | `:7099` | `config/sphere.toml` |
| `messager` | `:5072` | `:7100` | `config/messager.toml` |
| `stargate` | `:8080` | `:9090` | `stargate/config.toml` |
| `blade` | `:6000` | `:7001` | `blade/configs/config.toml` |
| `relay` | `:443` (TLS passthrough) | — | `blade/configs/relay.toml` |
| `dysonfs` | `:8080` | `:9090` | `filesystem/config.toml` |

`stargate` and `dysonfs` ship the same default ports; move one of them in its
TOML when both run on one host. DysonFS also exposes optional S3 gateways on
`:9000`/`:9001` and runs as `master`, `worker` or `storage` (`--mode`).

```sh
cp config/sphere.example.toml config/sphere.toml
cp config/messager.example.toml config/messager.toml
cp stargate/config.example.toml stargate/config.toml
cp filesystem/config.example.toml filesystem/config.toml
make build                       # bin/{sphere,messager,stargate,blade,relay,dysonfs}
make run-stargate                # each run target cds into its module
make run-blade
make run-filesystem
make run-sphere
CONFIG_PATH=config/messager.toml ./bin/messager
```

The consoles build separately with bun and are served by their own process:

```sh
make web-stargate                # stargate/web → stargate/web/dist
make web-stargate-dev            # Vite on :5173 proxying /api to :8080
make web-filesystem              # filesystem/web → filesystem/web/dist
```

Postgres is required. Redis, NATS and every `[services]` target are optional: a
missing one degrades only the features that depend on it (session caching falls
back to memory, events stop, enrichment/lookup/notification features are
skipped). Stargate's Postgres is its own database; it additionally validates
tokens for sphere/messager over gRPC.

## Deploy

- `stargate/Dockerfile` builds the SPA and the service; context `./stargate`.
- `blade/Dockerfile` and `blade/Dockerfile.relay` build the gateway and relay;
  context `./blade`.
- `filesystem/Dockerfile` builds the SPA and DysonFS (needs libvips); context
  `./filesystem`.
- `blade/docker-compose.yml` (gateway + its Redis registry) and
  `blade/docker-compose.relay.yml` (a PoP relay) keep their own contexts and
  run unchanged with `docker compose -f blade/docker-compose.yml up -d`.
- Root CI (`.github/workflows/{stargate,blade,filesystem}.yml`) builds and
  pushes the same images, path-filtered on their directories.

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
rows are copied with their `deleted_at` intact. Stargate's equivalent is
`make migrate-stargate`, DysonFS's is `make migrate-filesystem-legacy` (see
[`stargate/README.md`](stargate/README.md) and
[`filesystem/README.md`](filesystem/README.md)).

Deployment, the legacy-data migration and the cutover runbook — including
everything that behaves differently from the C# services — are in
[`DEPLOYMENT.md`](DEPLOYMENT.md). Status, verification and the known gaps are in
[`docs/PORT_STATUS.md`](docs/PORT_STATUS.md).

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
