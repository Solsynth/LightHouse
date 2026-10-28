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
- Localization is not ported: responses carry English text.
- Action logs are not emitted (no port).
