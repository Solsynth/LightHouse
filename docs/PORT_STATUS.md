# Port status

What is implemented in this monorepo today, what was verified against a live
PostgreSQL, and what is knowingly missing. Everything below was exercised
either by starting the service and calling it, or by the module's own test.

## Library sync (Pulsar module)

`internal/pulsar/modules/librarysync` mirrors one account's GroovyBox music
library so a second device can adopt it. It is a Pulsar feature module
(`app.solian.pulsar.librarysync`), not a separate service: scrobbles and live
now-playing stay with their own modules, and the mirror shares Pulsar's
database, auth and jobs.

Seven tables (`pulsar_library_cursors`, `pulsar_library_tracks`,
`pulsar_library_links`, `pulsar_library_lyrics`, `pulsar_library_plays`,
`pulsar_library_session`, `pulsar_library_settings`), a monotone
`(account_id, rev)` cursor per account, and three endpoints —
`POST /api/library/push`, `GET /api/library/pull`, `GET /api/library/status` —
gated on the `library.sync` permission key.

- Every entity table keeps a live row or a tombstone (`deleted_at IS NOT NULL`).
  Last-write-wins compares each entity's own clock (`updated_at`,
  `last_seen_at`, `fetched_at`, `play.clock`, the document clock); a stale write
  is rejected and reported by index in the push response.
- A pull page is a contiguous revision range across all six data tables, so a
  device cursor can never skip an entity another table queued at the same
  revision. Tombstones travel in the `removed` map with their clock.
- A `play` is append-only and merges column-wise: two devices' progress folds
  into the wider one instead of colliding.
- `source_id = "local"` links are rejected: a local file's path belongs to one
  device.
- The tombstone purge job runs every 24h and deletes tombstones older than
  `librarySync.retentionDays` (default 90); the cursor and the two per-account
  documents are exempt.
- `librarySync.devAccountHeader` names a request header trusted as the account
  id for local runs without a Stargate. It bypasses fleet auth entirely and must
  stay empty in any deployed config.

Verified: `go build ./...`, `go vet`, `go test ./internal/pulsar/...`
(LWW accept/reject, local-link skip, play merge, tombstone, session and settings
clocks, page boundary, HTTP push→pull round trips, status), and a manual smoke
against a scratch PostgreSQL — push answers `{"rev":1,"rejected":[]}`, pull
returns the track, a stale push is rejected, a delete shows up under
`removed.track`, a `limit=2` page over four tracks splits at the boundary, and
`/status` reports distinct devices.

## Analytics (Sphere module)

`internal/sphere/modules/analytics` (`app.solian.sphere.analytics`) is a
general-purpose, append-only event pipeline, not a C# port. Sibling modules
record events through `ports.Analytics`; browsers can post to
`POST /api/analytics/events`. Event identity is the account when signed in,
otherwise the `sol_visitor` cookie, plus the client address and user agent.

- One table, `analytics_events` (`kind`, `resource_id`, `event`, `account_id`,
  `visitor_id`, `ip_hash`, `user_agent`, `client_class`, `client_name`,
  `platform`, `is_mobile`, `referrer`, `created_at`), indexed on
  `(kind, resource_id, event, created_at)`, `created_at`, `account_id` and
  `visitor_id`.
- `Record` never blocks a request: events enter a bounded in-memory buffer
  (`analytics.bufferSize`) and the flush job writes them in batches
  (`batchSize`, `flushIntervalSeconds`). Overflow drops events and logs the
  count; a failed batch is logged and the still-buffered events stay queued.
- User agents are classified on the flush goroutine: a crawler/preview/HTTP
  client marker table, then `mssola/useragent`, producing
  human/bot/unknown plus browser, platform and mobile. Empty user agents count
  as bots.
- Client addresses are stored as a truncated keyed SHA-256 digest
  (`analytics.ipPepper`); `retentionDays` (default 180, `-1` forever) is swept
  nightly in bounded delete batches.
- Posts records `view`, `reaction.add`/`.remove`, `bookmark.add`/`.remove`,
  `boost.add`/`.remove`, `reply` and `award` events from its read/write paths;
  a disabled analytics module degrades to no-ops.
- `GET /api/posts/:id/analytics?days=30` returns per-event totals, the
  human/bot split, unique visitors and a daily series to publisher editors and
  owners (superusers bypass the check). Read endpoints deliberately live in the
  module that owns the resource, so ownership is never checked by the generic
  analytics module.

Verified: `go build ./...`, `go vet ./internal/sphere/...`,
`go test ./internal/sphere/...` (classification table, enqueue→flush→summary
over sqlite, ingest endpoint + cookie issuance, retention sweep, posts event
recording, owner/outsider/anonymous authorization, and a module wiring test
that mounts both modules on one gin engine, ingests an event, waits for the
scheduled flush and reads it back through the port).
No live PostgreSQL run yet: the batch insert, `DELETE … LIMIT` retention and
summary SQL were exercised on sqlite only.

## Verification performed

- `go build ./...`, `go vet ./...`, `go test ./...` pass.
- Both binaries start on a **fresh** database: Sphere creates 46 tables (one
  more with the analytics module), Messager 9, with
  `uuid`/`timestamptz`/`numeric`/`jsonb` column types matching the C# EF
  schema.
- `GET /health`, `/alive`, `/api/modules`, the JSON 404 and the canonical
  `ApiError` bodies behave as documented.
- Anonymous reads answer: posts list/detail/replies/reactions, categories, tags,
  stickers search, automod rules, timeline, publisher profile/stats/heatmap/
  content-stats/leaderboard/search/ads.
- Authenticated routes answer `401 {"code":"UNAUTHORIZED",…}` when no auth
  service is configured (they do not fall back to allowing the request).
- gRPC: health `SERVING` on both services; `DyPostService.ListPosts`,
  Sphere `DyRewindService.GetRewindEvent` and Messager
  `DyRewindService.GetRewindEvent` answer with real payloads.
- Legacy import: a legacy-shaped database was copied with
  `sphere-migrate` / `messager-migrate`, read back through the HTTP API, and a
  second run reported 0 rows (idempotent).
- Chat read path was exercised against rows inserted directly into the imported
  schema (public room, member, message).
- Chat websocket consumer was exercised against a local NATS JetStream:
  `messages.send` produced `messages.new` + `messages.delivered`,
  `messages.read`, `messages.typing`, `messages.test`, placeholder
  update/finalize and the unknown-type `error` packet.
- Timeline view signals, sponsored-slot insertion, interest upserts, search
  indexing and the sponsored read model were exercised with a throwaway
  program against the live database.

## Known gaps

These are deliberate, reported gaps — not silent omissions. Each is a place
where the C# service depended on a service or behaviour this port does not
reproduce yet.

### Sphere

- **Realm permission precision.** Realm snapshots, membership checks and the
  realm list filter work through `DyRealmService`, but `ports.Realms` exposes no
  `HasPermission`/role query, so realm moderation uses the global
  `posts.moderate`/realm client rather than the realm's own role matrix.
- **Semantic search.** `searchEngine` is accepted but ignored; search is
  `ILIKE` + `pg_trgm`. `post_indices.embedding` is written as NULL because no
  embedding client is configured.
- **Friend-only visibility** is resolved per row via `HasRelationship` (the
  accounts port exposes no friend list), so an `X-Total` can exceed the page.
- **Notification fan-out** covers mentions, reply authors, subscribers
  (publisher and topic), close friends and reactions. Post pushes carry the C#
  `BuildPostNotificationMeta` payload (post header, publisher identity, the
  acting subject's avatar, image attachments, tags and categories); chain and
  forward notifications are still not sent.
- **ActivityPub rendering** emits Markdown/text rather than HTML (no Markdown
  renderer is a dependency), the remote-outbox merge for actor posts is not
  implemented, follower/following counts are not fetched, and federation fan-out
  is synchronous per inbox with no shared-inbox batching.
- **Livestream awards**: the C# payout transferred 90% through the wallet; the
  `payments` port only exposes order creation, so `DistributedAwardAmount` is
  left untouched and the shortfall is logged.
- **Matcher fidelity**: automod's C# 5-minute cross-replica cache is now a
  process-local snapshot (same staleness class); the reader's mention of
  author/published-date fields is absent from `model.LinkEmbed`.

### Messager

- **Bot identities** (`?identity=`), bot chat configs and `/bots/commands`
  payloads are not ported — `/bots/commands` returns `{}` and identity-scoped
  sends are refused.
- **Per-device websocket targeting**: `ports.Realtime` addresses accounts, so
  acks/receipts reach every device of the account instead of only the sending
  `device_id`; `excluded_device_ids` on read receipts cannot be expressed.
- **Presence activities**: the C# consumer refreshed a room presence cache from
  `account.presence.activities.updated`; the Go port resolves presence live, so
  there is no cache to refresh.
- **LiveKit surface** covers the five room RPCs the controller needs; the
  remaining Twirp methods are absent.
- **Realm titles/suffixes** in call titles and localized invite copy are
  reduced (English plus `zh-hans`).
- **Voice MIME allow-list** is the C# extension table rather than any
  `audio/*`.

### Cross-cutting

- The fleet's `GET /api/modules` capability list is served, but the gRPC
  `DyCapabilitiesService` can only express the five shared enum capabilities, so
  a service whose features are all service-specific reports zero there (same as
  Stargate).
- Localization covers the Stargate notification copy (auth, punishments, and the
  wallet's `Resources/Locales` keys) in `internal/stargate/localization/` for
  `en` and `zh-hans`. It resolves against the account row's stored language, so
  a `zh-Hans` account gets Chinese even though the locale key is `zh-hans`.
  Notification *titles* follow the C# templates; the push port
  (`ports.Push.SendToUser`) carries only title and body, so the C# `Topic`,
  `Subtitle` and `Meta` fields are still absent.
- Action logs are emitted by Stargate's own surfaces through
  `actionlog.Service`. The wallet module writes the five wallet actions
  (`wallets.create`, `subscriptions.create`, `subscriptions.activate_switch`,
  `subscriptions.cancel`, `stellar.support.month`) through the same sink rather
  than the C# `RemoteActionLogService` gRPC round-trip.

### Wallet

The `DysonNetwork.Wallet` port lives in `internal/stargate/wallet/` and mounts
all five C# gRPC services, including `DySubscriptionCatalogService`
(`UpsertDefinition`/`ListDefinitions`), which the earlier port folded away.

- **Catalog wire format.** `definition_json` keeps the C# `System.Text.Json`
  shape: PascalCase keys, unquoted decimals. Input keys are matched ignoring
  case and separators, so a snake_case caller also works.
- **Provider order notifications** are sent once per inbound order, guarded by
  `inbound_orders.notification_sent_at`.
- **Transaction and order-payment push notifications** (`transactionNewTitle`,
  `orderPaidTitle`) are not sent: the port emits WebSocket packets instead, and
  the C# subscriber for those pushes has no counterpart here. The locale keys
  are present and unused.
- **The sponsor badge grant** (`HandleSponsorBadgeSubscriptionAsync`) is skipped
  — the wallet module has no profile client port.

### Realm

The `DysonNetwork.Passport` realm domain lives in `internal/stargate/realm/`
(entities, store, service, gRPC), `internal/stargate/httpserver/realmctl/`
(HTTP) and `internal/stargate/modules/realm/` (wiring).

Verified against a live PostgreSQL (scratch database, single-user auth):

- the module creates its eight tables on a fresh database and none when it is
  disabled; re-enabling later AutoMigrates them onto the existing database
  without touching anything else;
- `POST /api/realms` enforces the tiered quota from the account's level
  (`level >= 10` → 1 + perk level), `GET /api/realms/quota` reports it;
- member listing, `GET /:slug/members/me`, labels, boosts, role/user
  permissions and moderation logs answer with the C# payload shapes;
- a boost order is created through the local wallet
  (`product_identifier = realms.boost`), paying it raises the realm to boost
  level 1, and the label cap follows (3 labels, then
  `REALM_LABEL_LIMIT_REACHED`); replaying the same payment is a no-op;
- the boost purchase grants shares × 100 realm XP, which the member payload
  reports as experience/level;
- `DyRealmService` answers all ten RPCs with real rows (`GetRealm`,
  `GetRealmBatch`, `GetUserRealms`, `GetPublicRealms`, `SearchRealms`,
  `IsMemberWithRole`, `HasPermission`, `LoadMemberAccount`,
  `LoadMemberAccounts`; `SendInviteNotify` requires a push provider);
- the legacy importer copies a Passport-shaped database, and a second run
  writes 0 rows.

Known gaps:

- **`RealmActivityEvent` producers.** Sphere and Messager emit
  `realm.activity.<type>` in the C# fleet; the Go ports do not publish it yet,
  so activity XP is only exercised by the consumer's own contract. Tenure and
  boost XP are live.
- **`POST /:slug/boosts` without a payments provider** returns 503
  `SERVICE_UNAVAILABLE`. Passport always had a wallet target, so the C# code
  had no such branch.
- **Boost points persistence.** Passport recomputed `realms.boost_points` in
  memory per read and never wrote the column. Reads here are equally pure
  (a GET never overwrites the column); the stored value is refreshed only
  when a boost contribution is recorded, so it is a snapshot, not the read
  path's source of truth.
- **Realm post moderation log** has no inbound writer yet: Passport recorded a
  removal through `RealmService.ModeratePost`, which Sphere called in-process.
  Sphere's Go port removes posts itself and only asks
  `DyRealmService.HasPermission(…, "post.moderate")`, so
  `realm_post_moderation_logs` stays empty and
  `GET /api/realms/:slug/posts/moderation-logs` answers with an empty page.
- **Realm wallet and boost→publisher revenue** are not modelled: the C# boost
  flow only created an order, and this port keeps that scope.

### Progression

The `DysonNetwork.Passport` progression domain lives in
`internal/stargate/progression/` (entities, catalog, store, service,
consumer), `internal/stargate/httpserver/progressionctl/` (HTTP and badge
discovery) and `internal/stargate/modules/progression/` (wiring).

Verified against a live PostgreSQL (scratch database, single-user auth) and a
local NATS JetStream:

- the module creates its six tables under the legacy Passport names and seeds
  the generated catalog (49 achievements, 9 quests); booting again and
  `POST /api/admin/progression/sync` leave 49/9 rows, so the seed is
  idempotent, and a definition toggled off is not re-enabled by the sync;
- `GET /api/accounts/me/progression/{achievements,achievements/stats,quests,grants}`
  answer with the C# payload shapes, including required JSON timestamps
  (`RFC3339`, not `{}`);
- `GET /.well-known/badges` lists 23 entries and
  `/.well-known/badges/icons/draw` answers `200 image/svg+xml` from the SVGs
  embedded in the binary; traversal attempts (`..%2f…`) answer 404. Setting
  `iconsPath` serves an operator set instead;
- admin create/update/toggle/quest-create/sync/test-ws-packet answer 200 with
  the seeded `progression.admin`, `progression.achievements.manage`,
  `progression.quests.manage` and `progression.sync` permission nodes;
- the `action_logs.*` consumer advances progress end to end: publishing one
  `action_logs.posts.create` event produced 8 achievement progress rows (two
  of them completed) and 3 quest progresses, with
  `progress_reward_grants` recording badge, experience, source-point and
  notification timestamps;
- replaying the same `event_id` on the stream is a no-op per definition:
  telemetry shows one receipt per (event, definition, period) and a single
  increment, so redelivery cannot double-count or double-pay;
- meta equality filters (`meta_equals`) and streak mode were exercised with
  throwaway probes: a `thumb_down` reaction on a publisher post advances
  `gots-fired-5` while `heart` does not, and a second event on the same local
  day restores the same streak count instead of incrementing it;
- booting with several consumers on a cold JetStream creates the streams
  without `stream name already in use` errors (the consumers' `EnsureStream`
  calls are serialized in `nats.Client.Consume`).

Known gaps:

- **Action-log producers.** Stargate publishes the tracked actions its own
  stores write (`accounts.*`, `relationships.*`, `login`), but the Go Sphere
  and Messager processes do not publish `action_logs.*` yet. Post, chat and
  realm achievements therefore only advance when the C# fleet (or a future Go
  publisher) emits the events; Stargate's own actions are live.
- **Legacy data.** No importer ships with this module: the tables keep
  Passport's names and columns and are used in place. The EF unique indexes
  are replaced on migrate by partial `WHERE deleted_at IS NULL` indexes, which
  is the uniqueness rule the code actually depends on; dropping an index moves
  no data.
- **Optional rewards.** Without the `badges` port, badge rewards are recorded
  as granted without a grant (a read-only provider cannot grant). Without the
  `payments` `Creditor`, source points are skipped the same way. Without a
  NATS bus, completion packets are skipped. Progress tracking itself never
  depends on them.
- **No permanent regression test.** The idempotency, meta-filter, streak and
  consumer proofs were throwaway live probes (they need PostgreSQL and a
  JetStream server); no progression test is committed.

### Domain trust, Stellar subscriptions and the Passport gRPC cutover

`app.solian.id.domaintrust` lives in `internal/stargate/domaintrust/`,
`internal/stargate/httpserver/domaintrustctl/` and
`internal/stargate/modules/domaintrust/`; it is the last Passport gRPC service
the Go services dialed.

Verified against a live PostgreSQL and a running Stargate:

- the module creates `domain_blocks` and `domain_validation_metrics` with the
  Passport indexes; `GET /api/domain-blocks/check` blocks `http://` URLs and
  private IPs and allows `https://example.com`, and `DyDomainBlockService`
  answered both `ValidateUrl` and `IsDomainBlocked` over gRPC with the same
  decisions;
- the Go platform clients now dial one `[services].stargate` target. With
  Sphere pointed at Stargate, `GET /api/publishers` returned the
  realm-linked organization publisher with its full `realm` object
  (`id`, `slug`, `name`, `description`, `is_public`) plus the member and
  quota records; `GET /api/publishers/realmorg` returned the same snapshot.
  This is the path a stale `[services].passport` override used to break;
- the wallet seeds `wallet_subscription_definitions` on boot (5 rows: three
  Stellar tiers and two Creator plans) without any hand-written config. An
  active `solian.stellar.primary` subscription made
  `GET /api/publishers/quota` report `perk_level: 1` and `total: 4`, and
  switching it to `solian.stellar.nova` moved both to `2` and `6`, proving
  `DyProfileService.GetAccount` carries `perk_level` from the in-process
  wallet to Sphere;
- the realm snapshot Sphere embeds now carries the full `SnRealm` field set
  (`is_community`, `verification`, `account_id`, numeric `boost_points`,
  `boost_level`), not just id/slug/name/description;
- `stargate-migrate` copies `realms`, the `realm_*` tables, the six progression
  tables, both domain-trust tables, the new Passport
  account/event/ticket/purchase/NFC/pass/artwork/rewind tables, the
  account-status and presence-activity tables, `magic_spells`, and the
  wallet's `wallets`, `payment_*`, `wallet_*`, `merchants` and
  `merchant_settlements` tables from `--wallet-dsn`. A run against a
  legacy-shaped source copied realm, domain-trust, progression, check-in,
  status, presence and magic-spell rows; a second `--incremental` run copied 0
  rows and skipped the completed tables. A table missing on either side is
  skipped and **not** recorded as migrated, so a later run still copies it;
- the presence tables keep the types migration 0014 declares. Their entity tags
  pin `uuid`/`integer`/`varchar(n)` so AutoMigrate neither widens them nor
  leaves an already-widened database broken: booting the module converts a
  `text`/`bigint` column back and keeps its rows, which is what made the COPY
  into `account_statuses` and `presence_*` fail;
- the module tables store UUID columns as `uuid`, not the `text` GORM chooses
  by default. Fresh databases get the right type from the entity tags and an
  existing database is converted by a schema hook (primary keys included), so
  the legacy importer and the wire values agree.

Known gaps:

- **Stellar purchase path.** The catalog and perk resolution are verified;
  buying a plan through an external provider (Paddle/Afdian/Apple) was not
  exercised in this environment, so provider webhooks still need the
  credentials set in `[modules.settings."app.solian.wallet"]`.
- **NFC, presence providers and PassKit generation** need their runtime
  credentials (NFC tag keys, Steam/Last.fm API keys, Apple Wallet
  certificates) before the positive paths can be exercised; the routes and
  crypto/pass tests are unit-tested, and unconfigured endpoints answer 503 or
  per-item `missing_api_key` rather than failing the process.
- **Domain trust metrics are buffered per process** and flushed once a minute,
  so a crash loses at most the last minute of validation counters (Passport
  used the same buffer model).

## Account records, events and Passport extras

The remaining Passport domains live in:

- `accountrecords/` + `httpserver/accountrecordsctl/` +
  `modules/accountrecords/` (`app.solian.id.accountrecords`);
- `accountevents/` + `httpserver/accounteventsctl/` +
  `modules/accountevents/` (`app.solian.id.accountevents`);
- `tickets/`, `purchases/`, `nfc/`, `passkit/`, `presenceartworks/`,
  `ipcheck/`, `rewind/`, `wellbeing/` and `presenceproviders/`
  with their controller/module counterparts.

Verified against a fresh PostgreSQL and a running Stargate (single-user auth,
one seeded account, permissions inserted directly):

- the module set boots on a fresh PostgreSQL; all 20 new tables and the
  partial live-unique indexes are created, and the notable-day seed writes 15
  CN rows idempotently;
- `/api/accounts/me` hydrates experience/social-credit/leveling fields;
  `/credits`, `/credits/history`, `/leveling`, `/badges`, `/actions`,
  `/connections` and `/board` answer with the C# shapes, and the hourly social
  credit recalculation moved `account_profiles.social_credits` from 100 to 80
  after a −20 record;
- check-in writes `account_check_in_results`, grants 10 source points and 100
  XP (the XP record is visible in `/leveling`), refuses a second same-day
  check-in with `PASSPORT_CHECK_IN_NOT_AVAILABLE`, and returns the
  version-2 fallback fortune report; the calendar returns every day of the
  month, event CRUD/tags/list/countdown work, and the public
  calendar/timeline routes answer for a named account;
- a ticket creates with its first message and hydrates creator/messages;
  affiliation create/consume and name-change-card order work; the name-change
  card correctly refuses to use an unpaid card;
- NFC tag listing/admin, PassKit root and registration routes, IP diagnostics,
  rewind creation/public toggles and the S3-not-configured 503 path answer
  as documented;
- `GET /api/admin/stats` and `/api/admin/accounts/metrics/activity` report the
  seeded account; the night-owl test endpoint returns 503 when no push port is
  configured; presence-provider admin scans return an empty `items` list when
  no connections exist.

Known gaps:

- **AI fortune**: no MiChan/personality client exists, so check-in reports are
  always Passport's deterministic fallback. The fallback text is Chinese in
  both locales, matching the C# fallback.
- **Lunar notable days**: no lunar-calendar dependency; projection uses an
  embedded 2020–2040 new-year table plus a mean synodic month, so a
  month-boundary date can be one day off.
- **Publisher renames**: name-change cards support account and realm targets;
  the publisher target needs a publisher gRPC client and answers
  `NAME_CHANGE_CARD_USE_FAILED` until one is configured.
- **Push fidelity**: `ports.Push` carries only title/body, so ticket and
  night-owl topics/action URIs/savable flags are not transmitted.
- **Presence providers** were not exercised against live Steam/Last.fm APIs;
  the parser, stage classification and route registration are tested, and
  missing keys produce `missing_api_key` items.
- **PassKit** generation was not exercised with real Apple certificates; the
  signed-archive/manifest tests cover the packaging and PKCS#7 path, and an
  unconfigured deployment answers 503.
- **No importers** ship for these modules: the tables keep Passport's names
  and columns, so the legacy database can be used in place.
- **Examination and Meet/location pins remain Passport-only** by request.
