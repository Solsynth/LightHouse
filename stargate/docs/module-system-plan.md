# Modular Stargate — implementation plan

Status: in progress. This document is the source of truth for the module
system; keep it updated as phases land.

## Goal

Stargate must be a standalone account service that can also plug into the
Solar Network ecosystem. Every optional surface is a module:

- **Feature modules** add a domain and can run without any sibling
  microservice.
- **Integration modules** adapt a port to an external service
  (`app.solian.drive`, `app.solian.wallet`, `app.solian.pass`,
  `app.solian.ring`, `app.solian.blade`, `app.solian.develop`).

Modules are compiled into the binary (Caddy-style). Runtime config selects
which ones are enabled. No Go `.so` loading.

Only `app.solian.id.auth` is required. Disabling a module disables its
routes, gRPC services, consumers and (on a fresh database) its tables. It
never drops existing data.

## Module IDs

Identity-adjacent:

```text
app.solian.id.auth
app.solian.id.accounts
app.solian.id.profiles
app.solian.id.relationships
app.solian.id.spells
app.solian.id.social
app.solian.id.oidc
app.solian.id.nfc
```

Other features:

```text
app.solian.e2ee
app.solian.admin
app.solian.wellknown
```

Integrations:

```text
app.solian.drive
app.solian.wallet
app.solian.pass
app.solian.ring
app.solian.blade
app.solian.develop
```

Third-party modules must use their own reverse-DNS prefix.

## Kernel

Package `internal/module` contains the SPI, the registry and the resolver.

```go
type Kind string // "feature" | "integration"

type Factory struct {
    ID       string
    Kind     Kind
    DefaultEnabled bool
    Required bool

    // Module dependency: used for ordering, cascading disables and
    // validation. A feature module depends on another feature module, never
    // on an integration.
    Requires []string

    // Port dependency: an integration or feature module may provide an
    // interface and consumers resolve it at runtime.
    Ports    []PortRequirement
    Provides []PortID

    // Schema owned by this module. Only migrated while the module is enabled.
    Models []any
    SQL    []SchemaHook

    // Capability names exposed through DyCapabilitiesService and
    // GET /api/modules.
    Capabilities []string

    // Optional legacy-data importer. Used when a module changes the table
    // layout of a table that previously lived in Padlock/Passport.
    Importer func() Importer

    New func() Instance
}

type Instance interface {
    Configure(context.Context, *Host) error
    Stop(context.Context) error
}
```

`Host` exposes the core services plus the HTTP/gRPC mounting points and the
port registry. `EnsurePorts` is called for every enabled module before
`Configure`, so integration modules can publish ports without ordering
constraints.

### Resolution

1. Start from `DefaultEnabled`.
2. If `[modules] enabled` is non-empty, it becomes the allowlist.
3. Always include required modules.
4. Apply `[modules] disabled`.
5. Remove dependents whose `Requires` module is no longer enabled. Log every
   cascade removal.
6. Topologically sort by `Requires`, then by ID for deterministic order.
7. Fail on unknown IDs, duplicate IDs, cycles, or disabling a required
   module.

### Config

```toml
[modules]
# Empty enabled = every compiled-in module with DefaultEnabled.
enabled = []
disabled = []

[modules.settings."com.example.audit"]
level = "debug"
```

Module IDs are validated against the compiled registry. Env overrides:

```text
STARGATE_MODULES_ENABLED=app.solian.id.auth,app.solian.id.accounts
STARGATE_MODULES_DISABLED=app.solian.e2ee
```

## Schema ownership

### Core

Core SQL migrations stay in `internal/migrate/*.sql`. They own the tables
needed by the required auth module and the permission system, including the
`pg_trgm` extension and trigram indexes.

### Modules

A module declares GORM models:

```go
Models: []any{
    &store.RelationshipEntity{},
},
```

and optional raw SQL for indexes/constraints AutoMigrate cannot express:

```go
SQL: []module.SchemaHook{
    func(ctx context.Context, db *gorm.DB) error {
        return db.WithContext(ctx).Exec(`
            CREATE UNIQUE INDEX IF NOT EXISTS ix_magic_spells_spell
            ON magic_spells (spell, deleted_at)`).Error
    },
},
```

Boot order:

1. core SQL
2. for each enabled module: `AutoMigrate(Models...)`, then run `SQL`
3. module `Configure`
4. serve

Rules:

- A disabled module's models are never passed to AutoMigrate.
- AutoMigrate is additive. Never drop.
- A module never adds columns to a core table. Use a module-owned side table.
- Core code never queries a module table directly; it uses a port.
- Existing databases keep their tables when a module is disabled. Re-enabling
  reuses the data.

### Fresh vs existing database

| Situation | Result |
|---|---|
| fresh DB, module enabled | AutoMigrate creates its tables |
| fresh DB, module disabled | tables do not exist |
| existing DB, module disabled | tables stay, data untouched |
| existing DB, module enabled later | AutoMigrate reconciles and reuses |

No prune command. Disabling is not a data-deletion operation.

## Auth-only mode

Only `app.solian.id.auth` is required. It supports two providers:

### database (default)

Current behavior: accounts, challenges, sessions and factors in Postgres.

### single-user

```toml
[auth]
provider = "single-user"

[auth.singleUser]
id = "00000000-0000-0000-0000-000000000001"
username = "admin"
passwordHash = "$2b$12$..." # bcrypt; never plaintext
displayName = "Administrator"
superuser = true
allowRefresh = true
```

- No account row is required. The identity is synthesized from config with a
  stable UUID.
- Access tokens are normal JWTs.
- Single-user mode is intentionally stateless: no session rotation, device
  list or server-side revocation. Refresh is either disabled or a stateless
  long-lived token.
- Registration, verification, session, device and account-management routes
  are not mounted.
- `stargate hash-password` generates the bcrypt hash.

## Ports

Ports are the contract between feature modules and the outside world. A
missing optional port disables the dependent feature; a missing required port
fails startup.

| Port | Consumers | Providers |
|---|---|---|
| `files` | profiles, nfc | `app.solian.drive` |
| `badges` | profiles, nfc | `app.solian.pass` |
| `verification` | profiles, nfc | `app.solian.pass` |
| `perks` | profiles | `app.solian.wallet` |
| `email` | auth, spells, admin | `app.solian.ring` |
| `push` | auth, admin, relationships | `app.solian.ring` |
| `presence` | security, admin | `app.solian.blade` |
| `oauthClients` | oidc, security | `app.solian.develop` |
| `nfcTokens` | auth | `app.solian.id.nfc`, `app.solian.pass` |
| `verificationSpells` | auth | `app.solian.id.spells` |

Provider selection:

```toml
[ports]
files = "app.solian.drive"
nfcTokens = "app.solian.id.nfc"
```

If only one provider is enabled it is selected automatically. If a feature
provider and an integration provider both exist, the feature provider wins
(local NFC beats remote Passport). Ambiguity between two integrations fails
startup.

## Module inventory

| ID | Kind | Surface | Depends | Schema |
|---|---|---|---|---|
| `app.solian.id.auth` | feature | `/api/auth/**`, auth gRPC, registration routes | — | core tables |
| `app.solian.id.accounts` | feature | `/api/{identity,factors,sessions,devices,contacts,api-keys,connections}`, account/bot/authorized-app gRPC | auth | core tables |
| `app.solian.id.profiles` | feature | `/api/accounts/**` profile/public routes, profile gRPC | accounts | core tables |
| `app.solian.id.relationships` | feature | `/api/relationships/**`, followers/following | profiles | `account_relationships` |
| `app.solian.id.spells` | feature | `/api/spells/**` | accounts | `magic_spells`, `affiliation_*` (later phase) |
| `app.solian.id.social` | feature | `/api/auth/login/{provider}`, callbacks | auth, accounts | none |
| `app.solian.id.oidc` | feature | `/api/oidc/**`, `/.well-known/openid-configuration` | auth, accounts | none |
| `app.solian.e2ee` | feature | `/api/e2ee/**`, MLS gRPC | accounts | `e2ee_*`, `mls_*` (later phase) |
| `app.solian.admin` | feature | `/api/admin/**`, actions/punishments | accounts | core tables |
| `app.solian.wellknown` | feature | `/.well-known/*` docs | — | none |
| `app.solian.drive` | integration | files port | — | none |
| `app.solian.wallet` | integration | perks port | — | optional cache later |
| `app.solian.pass` | integration | badges/verification/nfcTokens | — | none |
| `app.solian.ring` | integration | email/push | — | none |
| `app.solian.blade` | integration | presence/discovery | — | none |
| `app.solian.develop` | integration | OAuth clients | — | none |

## Legacy data import

Extracting a feature from Passport may improve its schema. The importer gives
us a supported path from the old table to the new one.

```go
type ImportOptions struct {
    DryRun   bool
    BatchSize int
}

type ImportStats struct {
    Module  string
    Table   string
    Read    int64
    Written int64
    Skipped int64
}

type Importer interface {
    Import(ctx context.Context, source, target *gorm.DB, opts ImportOptions) (ImportStats, error)
}
```

CLI:

```sh
stargate-module-import \
  --module app.solian.id.relationships \
  --source-dsn "postgres://.../dyson_passport" \
  --target-dsn "postgres://.../dyson_stargate" \
  --dry-run
```

Importers are idempotent and record completion in `module_import_state`.
They must never truncate the target. The existing `stargate-migrate` tool
remains the full-fleet cutover tool; module import is for a single module
whose schema diverged.

## Phases

### Phase 0 — kernel and wrapping

- [x] `internal/module`: Factory, Instance, Host, resolver, ports, importer.
- [x] `internal/app`: bootstrap extracted from `cmd/stargate/main.go`.
- [x] Config `[modules]` and env overrides.
- [x] Wrapper modules for every existing HTTP/gRPC surface.
- [x] `GET /api/modules`.
- [x] Route/JSON parity with all modules enabled (existing controller tests).
- [x] Disable tests for relationships, e2ee and one integration (resolver).
- [ ] Full-process boot test with a live Postgres.

### Phase 1 — auth providers

- [x] `IdentityProvider`-style split via `auth.SingleUser` and the existing
      database service.
- [x] database provider wraps current auth service.
- [x] single-user stateless provider.
- [x] `stargate-hash-password`.
- [x] auth-only resolution test and single-user token tests.
- [ ] gRPC parity for every single-user admin RPC.

### Phase 2 — schema ownership

- [x] `Models` + `SQL` hooks run only for enabled modules.
- [x] Move `account_relationships` out of core SQL into the relationships
      module.
- [x] Add legacy importer framework and relationships importer.
- [ ] Later: move `e2ee_*`/`mls_*` and `magic_spells`/`affiliation_*`.
- [ ] Core-only and all-modules schema tests on live Postgres.
- [ ] Legacy-data preservation test on live Postgres.

### Phase 3 — relationship module

- [x] Split `profilectl.Register` into profiles and relationships.
- [x] Relationship routes and followers/following move to the module.
- [x] Relationship model only migrates when enabled.

### Phase 4 — ports and integrations

- [x] `internal/ports` interfaces.
- [x] Integration modules publish ports (`files`, `badges`, `perks`,
      `email`, `push`, `presence`, `oauthClients`, `nfcTokens`).
- [x] Disabled integrations are filtered out of the aggregate client.
- [ ] Per-integration gRPC dialing; retire the aggregate `Clients` for
      modules.
- [ ] Refactor profile/auth/admin/security/OIDC/presence call sites to ports.
- [ ] Port selection and capability gating at every call site.

### Phase 5 — NFC

- [ ] `app.solian.id.nfc` model, AutoMigrate + index SQL.
- [ ] NTAG 424 SUN crypto (RFC 4493 AES-CMAC tests).
- [ ] HTTP routes, admin route, `DyNfcService`.
- [ ] Provides `nfcTokens`; auth NFC factor uses the port.
- [ ] Legacy NFC tag importer from Passport.

### Phase 6 — private submodule

- [ ] Public SPI boundary.
- [ ] `cmd/stargate/modules_private.go` behind `//go:build private`.
- [ ] Private submodule docs. No nested `go.mod` if it needs `internal/`.

## Invariants

1. All modules enabled matches today's routes, JSON and gRPC.
2. A disabled module is fully unmounted; no half-registered routes.
3. A disabled integration removes its dial and gates its features.
4. Core never queries a module table.
5. Modules never add columns to core tables.
6. AutoMigrate runs only for enabled modules.
7. Nothing is dropped automatically.
8. `app.solian.id.auth` alone boots and authenticates.
9. Existing databases survive every module-schema move without data loss.
