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
- `internal/auth` — JWT (RS256), token validation, session/challenge logic
- `internal/httpserver/*ctl` — controller packages per domain
- `internal/grpcserver` — the gRPC surface the C# fleet calls
- `internal/store` — SQL queries (schema in `internal/migrate/0001_initial.sql`)
- `internal/migrate` — embedded DDL, applied on boot
- `internal/permission` — permission registry + evaluation + seed
- `internal/grpcclient` — outbound clients (wallet, develop, drive, pass,
  blade, ring)
- `internal/nats` — thin wrapper over the shared event bus
  (`src.solsynth.dev/sosys/go/pkg/eventbus`): JetStream events
  (`auth.session.revoked`, `websocket_push`)

## Run

```sh
cp config.example.toml config.toml   # set DSNs, keys, service targets
CONFIG_PATH=config.toml make run
```

Requires Postgres, Redis and NATS (JetStream enabled: `nats-server -js`).

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
  prior tokens.
- Session cache keys (`dyson:auth:session:*`), account versions and the
  `auth.session.revoked` JetStream events interoperate with the C# fleet
  and downstream Go services.
