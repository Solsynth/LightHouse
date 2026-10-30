# Port status

What is implemented in this monorepo today, what was verified against a live
PostgreSQL, and what is knowingly missing. Everything below was exercised
either by starting the service and calling it, or by the module's own test.

## Verification performed

- `go build ./...`, `go vet ./...`, `go test ./...` pass.
- Both binaries start on a **fresh** database: Sphere creates 45 tables,
  Messager 9, with `uuid`/`timestamptz`/`numeric`/`jsonb` column types matching
  the C# EF schema.
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
- **Notification fan-out** covers mentions and reply authors (watch
  preferences honoured); subscriber and chain/forward notifications are not
  sent.
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
- `stargate-migrate` now copies `realms`, the `realm_*` tables, the six
  progression tables and both domain-trust tables. A run against a
  legacy-shaped source copied realm, domain-trust and progression rows
  (including `progress_reward_grants.reward_token`); a second
  `--incremental` run copied 0 rows and skipped all 41 completed tables;
- the module tables store UUID columns as `uuid`, not the `text` GORM chooses
  by default. Fresh databases get the right type from the entity tags and an
  existing database is converted by a schema hook (primary keys included), so
  the legacy importer and the wire values agree.

Known gaps:

- **Stellar purchase path.** The catalog and perk resolution are verified;
  buying a plan through an external provider (Paddle/Afdian/Apple) was not
  exercised in this environment, so provider webhooks still need the
  credentials set in `[modules.settings."app.solian.wallet"]`.
- **NFC** remains Passport-only. No Go service dials it, so it does not block
  removing the Passport target, but the NFC routes and `DyNfcService` are not
  served by Stargate yet.
- **Domain trust metrics are buffered per process** and flushed once a minute,
  so a crash loses at most the last minute of validation counters (Passport
  used the same buffer model).
