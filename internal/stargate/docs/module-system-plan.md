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
app.solian.id.realm
app.solian.id.progression
app.solian.id.domaintrust
```

Other features:

```text
app.solian.e2ee
app.solian.admin
app.solian.wellknown
app.solian.wallet
```

Integrations:

```text
app.solian.drive
app.solian.wallet.remote
app.solian.pass
app.solian.ring
app.solian.blade
app.solian.develop
```

Third-party modules must use their own reverse-DNS prefix.

## Kernel

The SPI, registry and resolver live in the shared module
`src.solsynth.dev/sosys/lighthouse/shared/platform/module`, the same kernel
Sphere and Messager use. A service parameterises it with its own core graph;
Stargate uses `module.Factory[core.Services]`, so `Host.Core` is
`*core.Services`.

```go
type Kind string // "feature" | "integration"

type Factory[C any] struct {
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

    New func() Instance[C]
}

type Instance[C any] interface {
    Configure(context.Context, *Host[C]) error
    Stop(context.Context) error
}
```

`Host` exposes the core services plus the HTTP/gRPC mounting points and the
port registry. `ProvidePorts` is called for every enabled module before
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

Core SQL migrations stay in `internal/stargate/migrate/*.sql`. They own the tables
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
| `files` | profiles, nfc, accountevents, tickets | `app.solian.drive` |
| `badges` | profiles, nfc, progression | `app.solian.id.accountrecords` (feature), `app.solian.pass` |
| `boards` | profiles | `app.solian.id.accountrecords` (feature) |
| `verification` | profiles, nfc | `app.solian.id.accountrecords` (feature), `app.solian.pass` |
| `perks` | profiles, accountrecords, accountevents | `app.solian.wallet` (feature), `app.solian.wallet.remote` |
| `payments` | realm (boosts), progression/accountevents (points), purchases (orders) | `app.solian.wallet` (feature), `app.solian.wallet.remote` |
| `orderEvents` | realm (boosts), purchases (fulfilment) | `app.solian.wallet` (feature) |
| `email` | auth, spells, admin, tickets | `app.solian.ring` |
| `push` | auth, admin, relationships, tickets, wellbeing | `app.solian.ring` |
| `presence` | security, admin | `app.solian.blade` |
| `oauthClients` | oidc, security, token auth | `app.solian.develop` |
| `nfcTokens` | auth | `app.solian.id.nfc` (feature), `app.solian.pass` |
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
| `app.solian.id.relationships` | feature | `/api/relationships/**`, `/api/friends/overview`, followers/following, `GET /api/accounts/{name}/relationships` (anonymous totals; `mutual_friends` with a session), the six `DyProfileService` relationship RPCs | profiles | `account_relationships` |
| `app.solian.id.spells` | feature | `/api/spells/**` | accounts | `magic_spells` |
| `app.solian.id.social` | feature | `/api/auth/login/{provider}`, callbacks | auth, accounts | none |
| `app.solian.id.oidc` | feature | `/api/oidc/**`, `/.well-known/openid-configuration` | auth, accounts | none |
| `app.solian.id.realm` | feature | `/api/realms/**`, `/api/realms/public`, `/api/admin/realms/**`, the ten `DyRealmService` RPCs | auth, accounts | `realms`, `realm_members`, `realm_labels`, `realm_boost_contributions`, `realm_experience_records`, `realm_role_permissions`, `realm_user_permissions`, `realm_post_moderation_logs` |
| `app.solian.id.progression` | feature | `/api/accounts/me/progression/**`, `/api/admin/progression/**`, `/.well-known/badges`, action-log consumer | auth, accounts | `achievement_definitions`, `quest_definitions`, `account_achievements`, `account_quest_progresses`, `progress_reward_grants`, `progress_event_receipts` |
| `app.solian.id.domaintrust` | feature | `/api/domain-blocks/**` (rule CRUD, public `validate`/`check`, metrics), `DyDomainBlockService` | auth | `domain_blocks`, `domain_validation_metrics` |
| `app.solian.id.accountrecords` | feature | `/api/accounts/me/{actions,badges,leveling,credits,connections,board}`, public badge/board/credit routes, admin badge/board/credit/verification routes, `DyExperienceService`, `DySocialCreditService`; provides `badges`/`verification`/`boards` | auth, accounts | `experience_records`, `social_credit_records`, `badges`, `account_board_items` |
| `app.solian.id.accountevents` | feature | `/api/accounts/me/{check-in,calendar/**}`, public account calendar/timeline, `/api/notable-days/**`, `/api/fortune*` | accounts, accountrecords, presences | `account_check_in_results`, `user_calendar_events`, `calendar_event_subscriptions`, `notable_days` |
| `app.solian.id.tickets` | feature | `/api/tickets/**`, `/api/admin/tickets/on-call/**` | accounts | `tickets`, `ticket_messages`, `ticket_on_call_admins` |
| `app.solian.id.purchases` | feature | `/api/affiliations/**`, `/api/accounts/me/name-change-card/**` | accounts | `affiliation_spells`, `affiliation_results`, `affiliation_spell_purchases`, `name_change_card_purchases` |
| `app.solian.id.nfc` | feature | `/api/nfc/**`, `/api/admin/nfc/**`, `DyNfcService`; provides `nfcTokens` | auth, accounts | `nfc_tags` |
| `app.solian.id.passkit` | feature | `/api/passkit/v1/**`, `/api/accounts/me/passbook/member` | accounts | `apple_passes`, `apple_pass_registrations` |
| `app.solian.id.presenceartworks` | feature | `/api/presence/artworks/**` | accounts | `presence_artworks` |
| `app.solian.id.ipcheck` | feature | `/api/ip-check`, `/api/ip-check/geo` | — | none |
| `app.solian.id.rewind` | feature | `/api/rewind/**` | accounts, accountevents | `rewind_points` |
| `app.solian.id.wellbeing` | feature | `/api/admin/stats`, `/api/admin/accounts/metrics/activity`, night-owl test/job | accounts, profiles | none |
| `app.solian.id.presenceproviders` | feature | `/api/admin/accounts/presences/steam/**`, Steam/Last.fm scan jobs | accounts, presences, files/presence optional | none |
| `app.solian.e2ee` | feature | `/api/e2ee/**`, MLS gRPC | accounts | `e2ee_*`, `mls_*` (later phase) |
| `app.solian.admin` | feature | `/api/admin/**`, actions/punishments | accounts | core tables |
| `app.solian.wellknown` | feature | `/.well-known/*` docs | — | none |
| `app.solian.drive` | integration | files port | — | none |
| `app.solian.wallet` | feature | `/api/wallets`, `/api/orders`, `/api/subscriptions`, `/api/merchants`, `/api/wallet-products`, wallet gRPC | auth | `wallets`, `payment_orders`, `payment_transactions`, `wallet_*`, `merchants`, `merchant_settlements` |
| `app.solian.wallet.remote` | integration | perks port from an external wallet | — | none |
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

- [x] Shared kernel `shared/platform/module` (Factory, Instance, Host, resolver,
      ports, importer), used by Stargate, Sphere and Messager.
- [x] `internal/stargate/app`: bootstrap extracted from `cmd/stargate/main.go`.
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
- [x] gRPC: the six `DyProfileService` relationship RPCs (`GetRelationship`,
      `HasRelationship`, `ListFriends`, `ListBlocked`, `ListMuted`,
      `ListCloseFriends`) move to the module. A disabled module leaves them
      `Unimplemented`.
- [x] `GET /api/friends/overview` moves to the module. Activities are omitted
      (empty array) until `DyPresenceService` gains a batch-read RPC.
- [x] The five-minute expiry sweep moves with the table.
      `RelationshipExpiryJob` is deleted from Passport; the module runs the
      equivalent sweep (`relationship.Service.StartSweeper`, see
      `SweepExpiredRelationships`) and purges the shared Redis keys for every
      row it changes. Inactive rows are soft-deleted, which is what keeps the
      removal visible to `GET /api/relationships/sync`.
- [x] Account removal drops the rows. Passport's removal flow no longer
      deletes `account_relationships` (the table is not its own); the module
      consumes `accounts.removal.confirmed` and hard-deletes the account's
      rows in both directions, purging the shared caches.
- [ ] `GET /api/friends/overview` `activities` stay empty until
      `DyPresenceService` gains a batch-read RPC; the rest of the payload is
      at parity.
- [x] The split stops at the account read. `RemoteAccountService` stays on the
      unkeyed `DyProfileServiceClient` (`https://_grpc.passport`), which is
      Passport: Passport's `GetAccount`/`GetAccountBatch` are not passthroughs,
      they run `PopulateContactsAsync` + `PopulateBadgesAsync` on top of the
      account Stargate returns. Pointing that reader at `DyAccountService`
      would silently drop both fields from every fleet account read (Solian
      renders `account.badges` in chat member cards, from Messager's
      `/members` response), and Stargate cannot take the read over without
      first owning the badge data (Passport's `badges` table) and adding a
      batch badge read — `badgesAdapter` is one RPC per account, so a
      `GetAccountBatch` parity implementation would fan out N+1.

### Phase 4 — ports and integrations

- [x] `internal/stargate/ports` interfaces.
- [x] Integration modules publish ports (`files`, `badges`, `perks`,
      `email`, `push`, `presence`, `oauthClients`, `nfcTokens`).
- [x] Disabled integrations are filtered out of the aggregate client.
- [ ] Per-integration gRPC dialing; retire the aggregate `Clients` for
      modules.
- [ ] Refactor profile/auth/admin/security/OIDC/presence call sites to ports.
- [ ] Port selection and capability gating at every call site.

### Phase 4b — OAuth app consolidation in Express Develop Portal

- [x] Consolidate OAuth app management and configuration into Express dev portal (`app.solian.develop`).
- [x] Strip Stargate-owned `oauthapps` local tables and endpoints.
- [x] `app.solian.develop` integration provides `oauthClients` via gRPC `DyCustomAppService`.
- [x] `oidcctl`, `securityctl` and token binding resolve through the port.
      stops being the only consumer of `GetCustomAppSlug`.
- [x] Legacy importer from Develop's `custom_apps` and `custom_app_secrets`,
      with `scripts/import-oauth-apps.sh`.

### Phase 4c — realm extraction from Passport

- [x] `app.solian.id.realm` owns the eight Passport realm tables under their
      legacy names, with the EF indexes reproduced as schema hooks (the slug
      uniqueness is `(slug, deleted_at)`, so a soft-deleted realm frees its
      slug).
- [x] 42 HTTP routes (`/api/realms`, `/api/realms/public`,
      `/api/admin/realms`) with the C# paths, error codes and
      `[AskPermission]` keys.
- [x] dyRealmService: all ten RPCs (`GetRealm`, `GetRealmBatch`,
      `GetUserRealms`, `GetPublicRealms`, `SearchRealms`, `SendInviteNotify`,
      `IsMemberWithRole`, `HasPermission`, `LoadMemberAccount`,
      `LoadMemberAccounts`). Sphere and Messager already dial it.
- [x] Boost accounting: contributions are deduplicated by order id, shares are
      derived from `(currency, amount)`, and the active window is 30 days.
- [x] XP: tenure (daily, keyed by the date), activity
      (`realm.activity.<type>`, one-minute cooldown for chat), and boost
      purchases (shares × 100). The perk multiplier 1.5×/2×/2.5× comes from
      the `perks` port.
- [x] Paid boosts apply through the local wallet in-process
      (`orderEvents`), because a standalone deployment has no event bus; the
      `payment_orders` JetStream consumer stays subscribed for a remote
      wallet.
- [x] Legacy importer for all eight tables plus
      `scripts/import-realm.sh`.
- [ ] `RealmActivityEvent` producers: Sphere/Messager publish the C# subject
      today; the Go ports do not yet, so realm XP stays tenure-and-boost only
      until they do.

### Phase 4d — progression extraction from Passport

- [x] `app.solian.id.progression` owns the six Passport tables under their
      legacy names. The EF `(…, deleted_at)` unique indexes are recreated as
      partial indexes (`WHERE deleted_at IS NULL`), which is the rule the code
      relies on: a soft-deleted definition frees its identifier, a live
      duplicate is rejected. The superseded EF indexes are dropped on migrate.
- [x] Account routes
      (`/api/accounts/me/progression/{achievements,achievements/stats,quests,grants}`),
      admin routes (`/api/admin/progression/…`) with the C# permission keys
      `progression.achievements.manage`, `progression.quests.manage` and
      `progression.sync`, and the badge discovery surface
      (`/.well-known/badges`, `/.well-known/badges/icons/:iconName`, 23
      entries).
- [x] Catalog seeded on boot (49 achievements, 9 quests) and on
      `POST /api/admin/progression/sync`; seed-managed rows are overwritten, a
      hand-edited row is not. Definitions are never force re-enabled: toggling
      one off survives a sync.
- [x] `action_logs.<action>` JetStream consumer over the fleet's
      `action_log_events` stream. `progress_event_receipts` makes at-least-once
      delivery idempotent per (event, definition, period); grants are keyed by
      `reward_token` and the grant row is written before completion, so a crash
      cannot pay twice.
- [x] Quest periods (none/daily/weekly/monthly) computed in the account's time
      zone (UTC when unset), streak counting with best-streak, and meta
      equality filters.
- [x] Rewards: XP through `app.solian.id.accountrecords` (the experience
      ledger plus the profile patch), badges through the
      `badges` port, source points through the `payments` `Creditor`, and the
      `progression.completed` websocket packet through the event bus. All are
      optional; without them progress still advances.
- [x] Badge SVGs embedded with the binary; `iconsPath` under
      `[modules.settings."app.solian.id.progression"]` overrides the set.
- [x] No importer: the tables keep Passport's names and columns, so a legacy
      database is used in place.
- [x] Examination and Meet (location pins) are deliberately out of scope for
      this port; their Passport tables and routes stay in the C# service.
- [ ] Action-log publishers: Stargate publishes its own tracked actions, but
      the Go Sphere/Messager processes do not emit `action_logs.*` yet, so
      post/chat/realm achievements only advance when the C# fleet publishes.

### Phase 4e — domain trust and the last Passport gRPC dependency

- [x] `app.solian.id.domaintrust` owns `domain_blocks` and
      `domain_validation_metrics` under their legacy names, with the EF indexes
      reproduced as schema hooks (unique metric domain, pattern/active
      indexes).
- [x] `/api/domain-blocks`: authenticated rule list/get/create, permission
      gated update/delete (`domain.trust.update`, `domain.trust.delete`), the
      public `POST /validate` and `GET /check`, and the authenticated metrics
      endpoints — all with the C# snake_case bodies, `X-Total` headers and
      `PASSPORT_DOMAIN_*` error codes.
- [x] `DyDomainBlockService.ValidateUrl` and `IsDomainBlocked`, so Sphere and
      Messager read domain safety from Stargate instead of Passport.
- [x] Defaults: HTTP blocked, direct IPs blocked, private networks blocked and
      the port list from `DomainBlockSettings`, configurable under
      `[modules.settings."app.solian.id.domaintrust"]`. Validation metrics are
      buffered and flushed once a minute.
- [x] The Go platform clients dial one `[services].stargate` target for
      profiles, realms and domain blocks; the `passport` target and
      `*_SERVICES_PASSPORT__GRPC` override are gone.
- [x] The wallet seeds its subscription catalog on boot (Passport called
      `EnsureSeededAsync`), with the Stellar and Creator plans as defaults, so
      `perk_level` reaches `DyProfileService` without hand-created rows.
- [x] NFC, check-in/calendar, tickets, purchases, PassKit, rewind and the rest
      of the account domains are ported (Phase 4f/5 below); Passport is only
      needed for Examination, Meet and location pins.

### Phase 4f — the rest of Passport

- [x] `app.solian.id.accountrecords`: `experience_records`,
      `social_credit_records`, `badges` and `account_board_items`, the current
      account record routes, public badge/board/credit reads, admin
      badge/board/verification routes, `DyExperienceService` and
      `DySocialCreditService`. It provides the local `badges`/`verification`
      ports and refreshes `account_profiles.social_credits` hourly.
- [x] `app.solian.id.accountevents`: check-in with streak gold/source points/
      XP, daily and merged calendar, event CRUD/recurrence/visibility,
      subscriptions, countdown/search, timeline, the CN notable-day seed and
      the fortune surface.
- [x] `app.solian.id.tickets`: tickets, messages, status/assign, on-call
      roster, push/email notifications.
- [x] `app.solian.id.purchases`: affiliation spells/results/purchases and
      name-change cards, fulfilled through `orderEvents`.
- [x] `app.solian.id.nfc`: `nfc_tags`, literal NTAG 424 SUN crypto (RFC 4493
      vectors in tests), HTTP/admin routes, `DyNfcService` and the local
      `nfcTokens` provider.
- [x] `app.solian.id.passkit`: Apple Wallet pass registration/web service and
      signed `.pkpass` generation (PKCS#7) when configured.
- [x] `app.solian.id.presenceartworks`, `app.solian.id.ipcheck`,
      `app.solian.id.rewind` and `app.solian.id.wellbeing`: S3 artwork store,
      geo diagnostics, rewind aggregation and account activity/night-owl
      jobs.
- [x] `app.solian.id.presenceproviders`: Steam/Last.fm scans and the three
      presence stages.
- [ ] Fortune reports use Passport's deterministic fallback; there is no
      MiChan/personality client in Stargate.
- [ ] Lunar notable-day projection uses an embedded new-year table and a mean
      synodic month, so month-boundary dates can differ by a day.
- [ ] Publisher renames through a name-change card need a publisher gRPC
      client; account and realm renames are implemented.
- [ ] No legacy importer: the new modules keep Passport's table and column
      names and are used in place.

### Phase 5 — NFC

- [x] `app.solian.id.nfc` model, AutoMigrate + index SQL.
- [x] NTAG 424 SUN crypto (RFC 4493 AES-CMAC tests).
- [x] HTTP routes, admin route, `DyNfcService`.
- [x] Provides `nfcTokens`; auth NFC factor uses the port.
- [ ] Legacy NFC tag importer from Passport (tables are used in place, so a
      copy is only needed when the source and target databases differ).

### Phase 6 — private submodule

- [ ] Public SPI boundary.
- [ ] `cmd/internal/stargate/modules_private.go` behind `//go:build private`.
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
