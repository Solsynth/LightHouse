# Lighthouse

The Go monorepo for the Solar Network services. Six services, five Go
modules, one workspace:

- **sphere** — the port of `DysonNetwork.Sphere`: posts, publishers, timelines,
  categories/tags/collections, surveys, stickers, automod, the web reader,
  livestreams and ActivityPub federation.
- **messager** — the port of `DysonNetwork.Messager`: chat rooms, members,
  groups, messages, reactions, pins, voice clips and realtime calls.
- **stargate** — the port of `DysonNetwork.Padlock` (auth, accounts, OIDC,
  admin, E2EE/MLS), the Passport account/profile domain, and the ported
  `DysonNetwork.Wallet` (wallets, ledger, orders, subscriptions, merchants) as
  a module. Every other service validates inbound tokens through it.
- **metoer** — the port of `DysonNetwork.Ring`: notifications, push delivery
  (FCM/APNs/UnifiedPush/SOP), email sending plans and delivery observability.
- **blade** — the API gateway: reverse proxy, health checks, gRPC service
  discovery, L4 TLS-SNI relays (`cmd/relay`) and the websocket gateway.
- **filesystem** — DysonFS, the file service: uploads, media processing,
  previews, WebDAV/WOPI and S3 gateways, in master, worker and storage-node
  modes.

Sphere and Messager are **separate services**: separate binaries, separate
databases, separate module sets. They share the vendored fleet SDK and platform
layer (`shared` and `shared/platform`). Stargate, Metoer, Blade and DysonFS
live under `internal/` next to them, each as its own Go module with its own
`go.mod`, Dockerfiles and configs, and they talk to the other services over
HTTP/gRPC/NATS only. DysonFS is the one child outside `go.work` (see below).

## Layout

```
go.work                   workspace: ., ./shared, ./internal/stargate, ./internal/metoer, ./internal/blade
go.mod                    module src.solsynth.dev/sosys/lighthouse
shared/                   module src.solsynth.dev/sosys/lighthouse/shared (fleet SDK + platform)
docs/MODULE_CONTRACT.md   the interface every Lighthouse module is written against
shared/platform/          shared house infrastructure
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
internal/sphere/          sphere service (module src.solsynth.dev/sosys/lighthouse)
  cmd/sphere                sphere process
  cmd/sphere-migrate        legacy Sphere/Postgres import
  config.example.toml       example TOML (config.toml is the local copy)
  config/ core/ app/ model/ store/ ports/ migrate/
  modules/*                 feature modules (posts, publishers, ...)
internal/messager/        messager service (same module, same shape)
  cmd/messager              messager process
  cmd/messager-migrate      legacy Messager/Postgres import
  config.example.toml       example TOML
internal/stargate/        module src.solsynth.dev/sosys/stargate
  cmd/stargate              the service (HTTP :8080, gRPC :9090)
  cmd/stargate-migrate      legacy Padlock/Passport/Wallet import
  cmd/stargate-module-import  per-module legacy import
  cmd/stargate-hash-password  bcrypt hash for [auth.singleUser]
  module/ modules/ ...      module registry, controllers, wallet, store
  web/                      Vue 3 + Vite console (served by webui)
  config.example.toml       see internal/stargate/README.md
internal/metoer/          module src.solsynth.dev/sosys/metoer
  cmd/metoer                the service (HTTP :8080, gRPC :9090)
  config/ store/ model/     config, persistence and wire models
  push/ email/ queue/       delivery pipeline and in-process dispatcher
  scheduler/ events/        Quartz jobs and NATS/websocket fan-out
  config.example.toml       see internal/metoer/README.md
internal/blade/           module srv.solsynth.dev/sosys/blade
  cmd/                      gateway (:6000 HTTP, :7001 gRPC)
  cmd/relay                 L4 TLS-SNI relay node (:443)
  proxy/ discovery/ ...     proxy, discovery, health, wsgateway, relay
  configs/                  config.toml, docker.toml, relay.toml
  docs/                     see internal/blade/README.md
internal/filesystem/      module src.solsynth.dev/sosys/filesystem
  cmd/                      dysonfs (master/worker/storage modes)
  module/ media/ storage/   module registry, media, S3, WebDAV, WOPI
  web/                      Vue 3 + Vite console
  config.example.toml       see internal/filesystem/README.md
```

Every service directory is laid out the same way: `cmd/` for its binaries,
`config.example.toml` for its example config, and one package directory per
domain at the top level. No service nests an `internal/` inside its own
directory — the whole tree already lives under the repository's `internal/`.

## Workspace

`go.work` uses the Lighthouse, `shared`, Stargate, Metoer and Blade modules so
one checkout builds and tests them with a single toolchain (`go 1.27`). Nothing
is published between them yet — each service still has its own dependency graph
and `go.sum`. A service's location under `internal/` is only a directory
choice: the module path in its own `go.mod` is what its packages import by.
Every module requires `src.solsynth.dev/sosys/lighthouse/shared` at a
`shared/vX.Y.Z` tag published from this repository. The workspace resolves it
from the local `shared/` directory, while Docker and CI fetches the tagged
module from git; the old published module `src.solsynth.dev/sosys/go` is no
longer used.

Two consequences worth knowing:

- Directory patterns stop at a nested `go.mod`, so `go build ./...` at the
  root covers only sphere/messager. Use the Makefile (or `go build
  ./internal/stargate/...` / `go build ./internal/metoer/...` from the root)
  for the whole tree.
- Docker images build with the service directory as context, so `go.work`
  never enters an image. Each `go.mod` pins a `shared/vX.Y.Z` tag and fetches
  it from git. Their `Makefile`s keep working from inside the service
  directory.

`internal/filesystem` is **not** in `go.work`: it pins `go1.26.2`, and under
`go1.27` its `golang.org/x/net` v0.54.0 compiles the stdlib-backed http2
server, which no longer exports `http2.TrailerPrefix` that its
`google.golang.org/grpc` v1.79.1 still references. The root Makefile and
`internal/filesystem/Makefile` run it with `GOWORK=off GOTOOLCHAIN=go1.26.2`;
drop that once the grpc/x-net pair is upgraded.

## Module system

All Lighthouse services that use the shared bootstrap (Sphere, Messager and
Stargate) use the same compile-time registry from `shared/platform/module`: a
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
reports the resolved set and capabilities. Stargate uses the same kernel with
`C = core.Services` (see
[`internal/stargate/docs/module-system-plan.md`](internal/stargate/docs/module-system-plan.md));
DysonFS keeps its own registry shape (see
[`internal/filesystem/docs/module-system.md`](internal/filesystem/docs/module-system.md)).

`docs/MODULE_CONTRACT.md` is the authoring contract: helpers, wire-type rules,
persistence conventions and route-fidelity requirements.

## Run

| binary | HTTP | gRPC | config |
|---|---|---|---|
| `sphere` | `:5071` | `:7099` | `internal/sphere/config.toml` |
| `messager` | `:5072` | `:7100` | `internal/messager/config.toml` |
| `stargate` | `:8080` | `:9090` | `internal/stargate/config.toml` |
| `metoer` | `:8080` | `:9090` | `internal/metoer/config.toml` |
| `blade` | `:6000` | `:7001` | `internal/blade/configs/config.toml` |
| `relay` | `:443` (TLS passthrough) | — | `internal/blade/configs/relay.toml` |
| `dysonfs` | `:8080` | `:9090` | `internal/filesystem/config.toml` |

`stargate`, `metoer` and `dysonfs` ship the same default ports; move one of
them in its TOML when they run on one host. DysonFS also exposes optional S3
gateways on `:9000`/`:9001` and runs as `master`, `worker` or `storage`
(`--mode`).

```sh
cp internal/sphere/config.example.toml internal/sphere/config.toml
cp internal/messager/config.example.toml internal/messager/config.toml
cp internal/stargate/config.example.toml internal/stargate/config.toml
cp internal/metoer/config.example.toml internal/metoer/config.toml
cp internal/filesystem/config.example.toml internal/filesystem/config.toml
make build                       # bin/{sphere,messager,stargate,metoer,blade,relay,dysonfs}
make run-stargate                # each run target cds into its service
make run-blade
make run-metoer
make run-filesystem
make run-sphere
CONFIG_PATH=internal/messager/config.toml ./bin/messager
```

The consoles build separately with bun and are served by their own process:

```sh
make web-stargate                # internal/stargate/web → web/dist
make web-stargate-dev            # Vite on :5173 proxying /api to :8080
make web-filesystem              # internal/filesystem/web → web/dist
```

Postgres is required. Redis, NATS and every `[services]` target are optional: a
missing one degrades only the features that depend on it (session caching falls
back to memory, events stop, enrichment/lookup/notification features are
skipped). Stargate's Postgres is its own database; it additionally validates
tokens for sphere/messager/metoer over gRPC.

## Deploy

- `Dockerfile.sphere` and `Dockerfile.messager` build the root-module
  services; context `.`. Both produce static `scratch` images.
- `internal/stargate/Dockerfile` builds the SPA and the service; context
  `./internal/stargate`, static `scratch` runtime.
- `internal/metoer/Dockerfile` builds the service; context `./internal/metoer`,
  Alpine runtime for ca-certificates and tzdata.
- `internal/blade/Dockerfile` builds both Blade binaries into one image;
  context `./internal/blade`. The gateway is the default process, `relay` as
  the container command runs the L4 node, and Alpine stays for the compose
  healthcheck.
- `internal/filesystem/Dockerfile` builds the SPA and DysonFS; context
  `./internal/filesystem`. The runtime keeps libvips and ffmpeg.
- `internal/blade/docker-compose.yml` (gateway + its Redis registry) and
  `internal/blade/docker-compose.relay.yml` (a PoP relay) keep their own
  contexts and run unchanged with
  `docker compose -f internal/blade/docker-compose.yml up -d`.
- Root CI (`.github/workflows/{sphere,messager,stargate,metoer,blade,filesystem}.yml`)
  builds and pushes one image per service — `lighthouse-sphere`,
  `lighthouse-messager`, `lighthouse-stargate`, `lighthouse-metoer`,
  `lighthouse-blade`, `lighthouse-dysonfs` — path-filtered on its sources.

## Legacy data

The Go entities keep the legacy table and column names, so the C# databases
import directly:

```sh
# 1. boot the service once so the schema exists
CONFIG_PATH=internal/sphere/config.toml ./bin/sphere
# 2. copy the rows (idempotent; never truncates)
SPHERE_SOURCE_DSN=postgres://…/dyson_sphere \
SPHERE_TARGET_DSN=postgres://…/dyson_sphere_go \
  go run ./internal/sphere/cmd/sphere-migrate --dry-run
# 3. restart the service
```

`sphere-migrate` / `messager-migrate` COPY the source rows into a staging table
and then `INSERT … ON CONFLICT DO NOTHING`, so re-running is safe. Soft-deleted
rows are copied with their `deleted_at` intact. Stargate's equivalent is
`make migrate-stargate`, DysonFS's is `make migrate-filesystem-legacy` (see
[`internal/stargate/README.md`](internal/stargate/README.md) and
[`internal/filesystem/README.md`](internal/filesystem/README.md)).

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
