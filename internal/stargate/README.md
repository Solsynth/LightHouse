# Stargate

The Go replacement for **DysonNetwork.Padlock** (auth, accounts, OIDC, admin,
E2EE/MLS) and the **Passport identity domains** (profiles, relationships,
realms, progression, domain trust, account records/events, tickets, NFC,
passes, purchases, rewind and presence providers). Serves the same
`/api/**` routes the C# services served (the Blade gateway adds the `/padlock`
and `/passport` prefixes) with identical JSON shapes. Only Passport's
Examination, Meet, location-pin and BLE-nearby surfaces are deliberately left out.

## Layout

- `cmd/stargate` — the service (HTTP on `:8080`, gRPC on `:9090`)
- `cmd/stargate-migrate` — one-shot data copy from the legacy C# databases
- `cmd/stargate-module-import` — per-module legacy data import when a module's
  table layout changed
- `cmd/stargate-hash-password` — bcrypt hash for `[auth.singleUser]`
- `modules/*` — feature modules and ecosystem integrations; the shared kernel
  (`shared/platform/module`) provides the registry, dependency resolution,
  ports and schema hooks
- `app` — process bootstrap and module lifecycle
- `core` — shared service graph handed to modules
- `ports` — interfaces between feature modules and integrations
- `auth` — JWT (RS256), token validation, session/challenge logic
- `httpserver/*ctl` — controller packages per domain
- `grpcserver` — the gRPC surface the C# fleet calls
- `store` — persistence layer: typed GORM entities + query builders
  (schema in `migrate/0001_initial.sql`)
- `dbtest` — dedicated-database helper for migration tests (the DDL
  starts with `DROP TABLE IF EXISTS`, so isolated migrations must run in a
  fresh database)
- `migrate` — embedded DDL, applied on boot
- `permission` — permission registry + evaluation + seed
- `grpcclient` — outbound clients (wallet, develop, drive, pass,
  blade, ring)
- `nats` — thin wrapper over the shared event bus
  (`src.solsynth.dev/sosys/lighthouse/shared/pkg/eventbus`): JetStream events
  (`auth.session.revoked`, `websocket_push`, `account_events`)
- `broadcast` — friends-only websocket fan-out of account status and
  presence-activity changes (`account.status.updated`,
  `account.presence.activities.updated`)
- `presence` — the computed half of an account status: live devices,
  idle state and device-presence visibility
- `webui` — serves the built SPA (`web/dist`) with history fallback
- `web/` — the Vue 3 + Vite single-page frontend

## Modules

Stargate is a compile-time module system (Caddy style). Every surface is a
module in `modules`; `cmd/stargate` selects the standard set.
Runtime config enables or disables modules without rebuilding:

```toml
[modules]
enabled = []            # empty = every compiled-in module with a default of on
disabled = ["app.solian.e2ee", "app.solian.id.relationships"]
```

Module IDs use reverse-DNS namespaces. Only `app.solian.id.auth` is required.
Identity features live under `app.solian.id.*`. The ported wallet service is
`app.solian.wallet`; optional ecosystem adapters use `app.solian.drive`,
`app.solian.wallet.remote`, `app.solian.pass`, `app.solian.ring`,
`app.solian.blade` and `app.solian.develop`. The `.remote` suffix marks the
adapter that dials an external wallet; when the local wallet module is enabled
it provides the same `perks` port as a feature and the registry prefers it.

Disabling a module unmounts its HTTP/gRPC/consumer surface and, on a fresh
database, prevents its GORM models from being migrated. Existing tables are
never dropped. Re-enabling a module reuses the existing data.

Modules own their schema:

- `Factory.Models` is passed to `AutoMigrate` only while the module is enabled.
- `Factory.SQL` is the raw-SQL hook for indexes/constraints AutoMigrate cannot
  express.
- Core SQL migrations only contain the required auth/permission schema.
- A module never adds columns to a core table; it owns a side table.
- `app.solian.id.realm` owns the ported Passport realm domain: the eight
  `realm*` tables, `/api/realms` and `/api/admin/realms`, and the ten
  `DyRealmService` RPCs Sphere and Messager dial. Boosts are paid through the
  `payments` port (local wallet or remote), applied in-process by the local
  wallet and through `payment_orders` when a bus is configured; tenure and
  activity XP run as module jobs. See
  [`docs/module-system-plan.md`](docs/module-system-plan.md).
- `app.solian.id.progression` owns the ported Passport progression domain: the
  six `achievement_definitions`/`quest_definitions`/progress/receipt tables,
  `/api/accounts/me/progression`, `/api/admin/progression` and the badge
  discovery surface (`/.well-known/badges`, embedded SVG icons). The catalog
  (49 achievements, 9 quests) is seeded on boot and stays idempotent on
  `POST /api/admin/progression/sync`. Progress is driven by the
  `action_logs.<action>` JetStream consumer; rewards pay out through the
  optional `badges` and `payments` ports, and completion packets go to the
  websocket bus. See
  [`docs/module-system-plan.md`](docs/module-system-plan.md).
- `app.solian.id.domaintrust` owns the ported Passport domain-trust domain:
  `domain_blocks` and `domain_validation_metrics`, `/api/domain-blocks`
  (rule CRUD, public `validate`/`check`, metrics) and the
  `DyDomainBlockService` gRPC port the Go services call for URL safety.
  HTTP-block, private-IP and blocked-port defaults mirror `DomainBlock`
  settings; metrics are buffered and flushed once a minute.
- `app.solian.id.accountrecords` owns experience/social-credit records,
  `badges`, the account board and the `DyExperienceService` /
  `DySocialCreditService` gRPC receivers. It provides the local `badges` and
  `verification`/`boards` ports, so badge, verification and board hydration no longer dial Passport.
- `app.solian.id.accountevents` owns `account_check_in_results`,
  `user_calendar_events`, calendar subscriptions and `notable_days`:
  check-in/rewards, calendar/merged/countdown/search, the CN holiday seed,
  timeline and the fortune surface.
- `app.solian.id.tickets` owns `tickets`, `ticket_messages` and
  `ticket_on_call_admins`; `app.solian.id.purchases` owns affiliation spells
  and name-change cards and fulfils wallet orders through `orderEvents`.
- `app.solian.id.nfc` owns `nfc_tags`, the NTAG 424 SUN crypto, the HTTP/gRPC
  surface and the local `nfcTokens` port; `app.solian.id.passkit` owns Apple
  Wallet registrations and signed `.pkpass` generation.
- `app.solian.id.presenceartworks`, `app.solian.id.ipcheck`,
  `app.solian.id.rewind`, `app.solian.id.presenceproviders` and
  `app.solian.id.wellbeing` cover S3-backed artwork, geo diagnostics, rewind
  points, Steam/Last.fm presence scans and account activity/night-owl
  reminders.
- `app.solian.wallet` owns the ported DysonNetwork.Wallet service: wallets,
  pockets, the transaction ledger, orders, inbound provider orders,
  subscriptions, gifts, coupons, merchants and settlements, under the legacy
  table names (`wallets`, `payment_orders`, `payment_transactions`, ...). It
  serves `/api/wallets`, `/api/orders`, `/api/subscriptions`,
  `/api/merchants`, `/api/wallet-products` and the payment/subscription admin
  routes, plus the `DyWalletService`, `DyPaymentService`,
  `DySubscriptionService` and `DyMerchantService` gRPC surfaces.

Wallet settings live under `[modules.settings."app.solian.wallet"]` (see
[`config.example.toml`](config.example.toml)): `payment.auth.{paddle,afdian,appleStore}`
hold the provider credentials, `subscriptionCatalog` seeds definitions, and
`currencyExchange` sets the points/golds rates. Provider webhooks deduplicate
through `inbound_orders`, so replayed callbacks are safe.

Known differences from the C# service: realm-wallet membership needs a
publisher directory Stargate does not have ported (realm wallets are
superuser-only), notification copy is English and without localized topics,
the subscription/perk cache is not resumed, and account hydration on admin
listings is limited to the wallet database.

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

Modules that migrate legacy data ship an importer script:

| Module | Script | Moves |
|---|---|---|
| `app.solian.id.relationships` | `scripts/import-relationships.sh` | Passport `account_relationships` |
| `app.solian.id.realm` | `scripts/import-realm.sh` | Passport `realms`, `realm_members`, `realm_labels`, `realm_boost_contributions`, `realm_experience_records`, `realm_role_permissions`, `realm_user_permissions`, `realm_post_moderation_logs` |

Both accept `PGDRY=1` for a dry run.

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

Profile art and every other Drive-backed picture are drawn by `sk-media` from
`@solsynth/sunkenland`, a public npm package — no registry token is needed.
`VITE_SNFS_API_URL` points the elements at the origin that answers
`/drive/files/{id}` (default `https://api.solian.app`), and `VITE_SNFS_URL` at
the origin serving the picker page (default `https://fs.solian.app`).

The console ships English and Simplified Chinese, and a light and dark theme.
Both follow the browser/OS on first visit, then remember the reader's choice in
`localStorage`; the footer carries the theme toggle and language dropdown.
The sign-in screen lists a social provider only when its credentials are
complete in `[oidc]`, so no button leads to an unavailable provider. It also
offers sign-in by passkey and by QR code: the browser starts a challenge, a
signed-in device scans and approves it, and the browser redeems the result.
Other apps on the cookie domain reuse that page instead of building their own:
`/sign-in?redirectUri={callback}` returns the person to the app once they are
signed in. The session cookie is scoped by `[auth] cookieDomain`, and only a
host that cookie reaches is accepted as a landing target, so a shared link
cannot turn the page into an open redirect. A target on one of those hosts — any
subdomain, such as `web.solian.app` under `.solian.app` — is a plain navigation,
because the browser already carries the session there: nothing is minted, and
no hand-off client has to be registered for it.
An app on another registrable domain cannot share that cookie — a cookie has
one `Domain` attribute and the browser refuses one that does not match the
request host — so it is handed a single-use code instead: the console mints one
for the app's host (`[auth.handoffClients]`), the app's own server trades it at
`POST /api/auth/handoff/redeem` for tokens it stores in its own cookie, and
`POST /api/auth/handoff/renew` keeps that session alive past the access token's
lifetime. See [`docs/design-system.md`](docs/design-system.md) for the flow.
Linking a provider from **Settings → Linked Identities** uses the same login
endpoint with `flow=connect`, which attaches the identity to the signed-in
account and returns to the console.
The emails Stargate sends for contact verification and password reset link to
`{siteUrl}/spells/{word}`, which the console serves as a page: the reset form
asks for a new password, and the verification form confirms the contact. A link
that is wrong, already spent, or past its 24 hours says so instead of offering a
form that cannot work.
The visual system — tokens, type, layout tiers, and component rules — is
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
  --wallet-dsn   "postgres://…/dyson_wallet" \
  --target-dsn   "postgres://…/dyson_stargate"
```

Order matters: boot Stargate once (creates the schema + records migrations),
then copy data, then restart (the seed then enrolls accounts in the
permission groups). UUIDs, bcrypt password hashes, E2EE blobs and wallet
balances are copied verbatim; schema drift (e.g. an `epoch` column the live
DB lacks) is zero-filled per type. The passport and wallet sources are
optional: without their DSN the tool neither copies nor truncates those
tables.

`--incremental` resumes: it skips tables already copied and inserts only the
rows missing from the target. A table missing from the source, or from the
target because its module is disabled, is reported as skipped and gets no
completion record, so enabling the module and re-running still copies it. A
completion record whose target table is empty is discarded and the table is
copied again.

## Cutover

1. Point Blade's routes for `/padlock/**`, `/passport/**`, `/api/realms/**`,
   `/api/domain-blocks/**` and `/.well-known/badges` at Stargate.
2. Point the C# fleet's `services__padlock__grpc__0` and
   `services__passport__grpc__0` env at Stargate; the `_grpc.padlock` /
   `_grpc.passport` DNS targets stay, only the service address changes.
3. The Go services keep a single `[services].stargate` target: profiles,
   realms and domain blocks are read from it, and the former `passport` key is
   ignored. Remove it from deployment configs.
4. Run the migration tool (it copies the wallet tables and the realm,
   progression, domain-trust, account-record/event, ticket, purchase, NFC,
   PassKit, artwork and rewind tables too), restart, and verify with the
   compat sweep in the plan (`local://stargate-user-domain-plan.md`).
5. Keep Passport serving Examination, Meet and the location-pin routes; every
   other Passport domain is served by Stargate.

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
- **A challenge id is a bearer credential.** `GET /api/auth/challenge/{id}`,
  its `/factors` and code-request sub-paths, and the `PATCH` that verifies a
  factor serve whoever presents the id; elevation (`sudo`) challenges
  additionally require the session they were bound to. The id is a random v4
  UUID returned only to the client that created the challenge, so it must never
  be logged where other operators can read it, embedded in a URL that leaks to
  third parties, or minted from a guessable value.
- **Disclosure.** `GET /.well-known/security.txt` (contact from
  `[securityTxt] contact`) and [`SECURITY.md`](SECURITY.md) describe how to
  report a vulnerability privately.
