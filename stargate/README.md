# Stargate

The Go replacement for **DysonNetwork.Padlock** (auth, accounts, OIDC, admin,
E2EE/MLS) plus the **Passport account/profile domain** (accounts, profiles,
relationships, followers/following). Serves the same `/api/**` routes the C#
services served (the Blade gateway adds the `/padlock` and `/passport`
prefixes) with identical JSON shapes. Account **board items stay in
Passport** (routes + `account_board_items` table); Stargate only consumes
board updates indirectly via the profile read-model.

## Layout

- `cmd/stargate` — the service (HTTP on `:8080`, gRPC on `:9090`)
- `cmd/stargate-migrate` — one-shot data copy from the legacy C# databases
- `cmd/stargate-module-import` — per-module legacy data import when a module's
  table layout changed
- `cmd/stargate-hash-password` — bcrypt hash for `[auth.singleUser]`
- `internal/module` — compile-time module registry, dependency resolution,
  ports and schema hooks
- `internal/modules/*` — feature modules and ecosystem integrations
- `internal/app` — process bootstrap and module lifecycle
- `internal/core` — shared service graph handed to modules
- `internal/ports` — interfaces between feature modules and integrations
- `internal/auth` — JWT (RS256), token validation, session/challenge logic
- `internal/httpserver/*ctl` — controller packages per domain
- `internal/grpcserver` — the gRPC surface the C# fleet calls
- `internal/store` — persistence layer: typed GORM entities + query builders
  (schema in `internal/migrate/0001_initial.sql`)
- `internal/dbtest` — dedicated-database helper for migration tests (the DDL
  starts with `DROP TABLE IF EXISTS`, so isolated migrations must run in a
  fresh database)
- `internal/migrate` — embedded DDL, applied on boot
- `internal/permission` — permission registry + evaluation + seed
- `internal/grpcclient` — outbound clients (wallet, develop, drive, pass,
  blade, ring)
- `internal/nats` — thin wrapper over the shared event bus
  (`src.solsynth.dev/sosys/go/pkg/eventbus`): JetStream events
  (`auth.session.revoked`, `websocket_push`)
- `internal/webui` — serves the built SPA (`web/dist`) with history fallback
- `web/` — the Vue 3 + Vite single-page frontend

## Modules

Stargate is a compile-time module system (Caddy style). Every surface is a
module in `internal/modules`; `cmd/stargate` selects the standard set.
Runtime config enables or disables modules without rebuilding:

```toml
[modules]
enabled = []            # empty = every compiled-in module with a default of on
disabled = ["app.solian.e2ee", "app.solian.id.relationships"]
```

Module IDs use reverse-DNS namespaces. Only `app.solian.id.auth` is required.
Identity features live under `app.solian.id.*`; optional ecosystem adapters use
`app.solian.drive`, `app.solian.wallet`, `app.solian.pass`, `app.solian.ring`,
`app.solian.blade` and `app.solian.develop`.

Disabling a module unmounts its HTTP/gRPC/consumer surface and, on a fresh
database, prevents its GORM models from being migrated. Existing tables are
never dropped. Re-enabling a module reuses the existing data.

Modules own their schema:

- `Factory.Models` is passed to `AutoMigrate` only while the module is enabled.
- `Factory.SQL` is the raw-SQL hook for indexes/constraints AutoMigrate cannot
  express.
- Core SQL migrations only contain the required auth/permission schema.
- A module never adds columns to a core table; it owns a side table.
- `app.solian.id.oauthapps` owns the OAuth/OIDC client registry
  (`oauth_apps`, `oauth_app_secrets`), so the provider does not need Develop.
  See [`docs/oauth-apps.md`](docs/oauth-apps.md).

`GET /api/modules` returns the resolved set and capabilities. The full design
is in [`docs/module-system-plan.md`](docs/module-system-plan.md).

### Single-user auth

For a minimal deployment with only `app.solian.id.auth`:

```toml
[auth]
provider = "single-user"

[auth.singleUser]
username = "admin"
passwordHash = "$2b$12$..." # go run ./cmd/stargate-hash-password
superuser = true
allowRefresh = true
```

This identity is stateless: no account row, sessions or server-side
revocation. Use `provider = "database"` for normal multi-account deployments.

### Module data import

When extracting a Passport table and changing its layout, use the module
importer:

```sh
go run ./cmd/stargate-module-import \
  --module app.solian.id.relationships \
  --source-dsn "postgres://.../dyson_passport" \
  --target-dsn "postgres://.../dyson_stargate"
```

Importers are idempotent, never truncate the target, and record progress in
`module_import_state`.

Two modules ship an importer, each with a wrapper script that reads the
password from `PGPASSWORD` and defaults the databases:

| Module | Script | Moves |
|---|---|---|
| `app.solian.id.relationships` | `scripts/import-relationships.sh` | Passport `account_relationships` |
| `app.solian.id.oauthapps` | `scripts/import-oauth-apps.sh` | Develop `custom_apps` + `custom_app_secrets` |

Both accept `PGDRY=1` for a dry run. The OAuth importer reports per-table
counts and skips rows it refuses to guess at: a slug the target already
holds, an app with no `project_id`, and a secret whose app was skipped.

## Web frontend

The SPA in `web/` is served by Stargate itself from `[web] distPath` (the
shipped `config.example.toml` uses `web/dist`); there is no separate static
file server. One command builds both and serves them on `:8080`:

```sh
make start                 # bun install + SPA build + Go build, then serve
make start CONFIG=config.toml
```

`make dev` runs the Go API and the Vite server together (Vite proxies the API,
so the console uses same-origin URLs in both modes). `make web-check` runs the
type checker, linter, unit tests, and a production build. `web/Makefile` has
the same targets scoped to the frontend.

The console ships English and Simplified Chinese, and a light and dark theme.
Both follow the browser/OS on first visit, then remember the reader's choice in
`localStorage`; the footer carries the theme toggle and language dropdown.
The sign-in screen lists a social provider only when its credentials are
complete in `[oidc]`, so no button leads to an unavailable provider. The
visual system — tokens, type, layout tiers, and component rules — is
documented in [`docs/design-system.md`](docs/design-system.md).

Routes resolve as follows:

- existing files under `web/dist` are served directly (`/assets/*` is
  fingerprinted by Vite and cached immutably; everything else revalidates);
- any other GET without a file extension returns `index.html`, so
  client-side routes survive a reload;
- `/api/**`, `/.well-known/**`, `/swagger/**`, `/health` and `/alive` never
  fall back and answer with the canonical JSON 404;
- an empty `[web] distPath` leaves the server API-only.

In development run `make dev` (or `make web-dev` for the Vite server alone)
and open `http://localhost:5173`: Vite proxies `/api`, `/.well-known`,
`/swagger`, `/health` and `/alive` to `STARGATE_DEV_PROXY` (default
`http://localhost:8080`). The Docker image builds `web/` with
Bun and copies the output to `/app/web/dist`.

## Run

```sh
cp config.example.toml config.toml   # set DSNs, keys, service targets
CONFIG_PATH=config.toml make run
```

Postgres is required for the core account and session store. Redis and NATS
are optional: without Redis, cache-dependent features (QR login, passkeys,
one-time codes, and deletion throttling) return `503` while DB-backed
authentication continues; without NATS, event consumers and realtime event
publishing are disabled. Every outbound target in `[services]` is optional
and disables only its dependent enrichment, lookup, notification, or
realtime feature when unset or unavailable.

### Local OAuth clients

Stargate normally reads custom OAuth/OIDC clients from
`DysonNetwork.Develop`. Deployments that do not run Develop can define the
clients locally instead:

```toml
[[oidcProvider.clients]]
id = "00000000-0000-0000-0000-000000000001"
slug = "my-client"
name = "My Client"
clientSecret = "replace-with-a-secret"
status = 2
redirectUris = ["https://client.example.com/oauth/callback"]
allowedScopes = ["openid", "profile", "email"]
isPublicClient = false
```

Local entries are checked before Develop. Keep each `id` stable because it is
stored in OAuth sessions. `status = 2` (Production) enforces `redirectUris`;
public clients should use PKCE and omit `clientSecret`.

## Migrate

```sh
go run ./cmd/stargate-migrate \
  --padlock-dsn  "postgres://…/dyson_padlock" \
  --passport-dsn "postgres://…/dyson_pass" \
  --target-dsn   "postgres://…/dyson_stargate"
```

Order matters: boot Stargate once (creates the schema + records migrations),
then copy data, then restart (the seed then enrolls accounts in the
permission groups). UUIDs, bcrypt password hashes and E2EE blobs are copied
verbatim; schema drift (e.g. an `epoch` column the live DB lacks) is
zero-filled per type.

## Cutover

1. Point Blade's routes for `/padlock/**` (and the moved `/passport/**`
   paths) at Stargate.
2. Point the C# fleet's `services__padlock__grpc__0` env at Stargate (the
   `_grpc.padlock`/`_grpc.passport` DNS targets stay; only the service
   address changes).
3. Keep Passport serving its remaining paths; its account lookups now hit
   Stargate.
4. Run the migration tool, restart, verify with the compat sweep in the
   plan (`local://stargate-user-domain-plan.md`).

## Compatibility notes

- JSON is globally snake_case; `ApiError.traceId` is camelCase; enums are
  ints; times are UTC RFC3339; nulls are omitted.
- JWTs use the same RSA keys as the C# fleet (`Keys/`), so in-flight tokens
  keep validating; refresh rotation bumps the session epoch and revokes
  prior tokens. The immediately previous refresh token stays acceptable for
  `auth.refreshGracePeriod` (default `60s`) after a rotation so a lost or
  timed-out refresh response retries instead of forcing a re-login.
- Session cache keys (`dyson:auth:session:*`), account versions and the
  `auth.session.revoked` JetStream events interoperate with the C# fleet
  and downstream Go services.

## Security posture

- **Trusted proxies.** The client IP used by fail2ban and the per-IP quotas
  is read from `X-Forwarded-For` counting from the **right**
  (`[security] trustedProxyHops`, default 1). The edge (Blade) must append to
  or overwrite `X-Forwarded-For` — never pass a client-supplied header
  through untouched — otherwise a client can pick the address the rate
  limiter sees. Set `trustedProxyHops` to the real proxy depth when more than
  one proxy sits in front of Stargate.
- **Captcha is fail-closed.** Account creation, the `password`-reset request
  and `/api/auth/captcha/verify` are captcha-gated. Stargate refuses to start
  when captcha is unusable unless the deployment opted out explicitly with
  `[captcha] allow_disabled = true`; without that opt-out,
  `AuthService.ValidateCaptcha` returns an error and the gated flows reject.
- **Credential guessing is throttled.** `PATCH /api/auth/challenge/{id}` and
  `POST /api/auth/token` consult the fail2ban IP block before verifying, and
  `[security] maxChallengeAttempts` caps the failed attempts one challenge
  accepts.
- **Disclosure.** `GET /.well-known/security.txt` (contact from
  `[securityTxt] contact`) and [`SECURITY.md`](SECURITY.md) describe how to
  report a vulnerability privately.
