# Lighthouse module contract

This file is the interface every feature module of the Lighthouse port is
written against. Read it fully before writing code.

## Repository

- Go module `src.solsynth.dev/sosys/lighthouse`, Go 1.27. Put `/tmp/go/bin` on
  `PATH` before running Go.
- The platform (`shared/platform/...`) and the Sphere foundation
  (`internal/sphere/{config,core,model,store,ports,migrate}`) already compile.
  **Never modify an existing file.** Create new files only. Every new entity or
  query you need goes in your own file(s).
- Build check: `go build ./...` from the repo root must pass when you finish.
- Do not run formatters or linters. Do not write tests unless a rule is subtle
  and you want a table test in your own package.

## Module system

A module is a `module.Factory[C]` plus an `Instance[C]`. Sphere modules use
`C = core.Services`; Messager modules use `C = messagercore.Services`.

```go
package posts

import (
    "context"

    "src.solsynth.dev/sosys/lighthouse/shared/platform/module"
    "src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
)

func Factory() module.Factory[core.Services] {
    return module.Factory[core.Services]{
        ID:             "app.solian.sphere.posts",   // reverse-DNS, stable
        Kind:           module.KindFeature,          // or KindIntegration
        DefaultEnabled: true,
        Requires:       []string{"app.solian.sphere.publishers"},
        Ports:          []module.PortRequirement{{ID: "accounts", Optional: true}},
        Models:         []any{store.PostEntity{}},   // AutoMigrate input
        SQL:            []module.SchemaHook{indexHook},
        Capabilities:   []string{"posts.create"},
        New:            func() module.Instance[core.Services] { return &instance{} },
    }
}
```

`Configure(ctx, host)` registers routes on `host.API`, gRPC services on
`host.GRPC`, seeds data and starts consumers/jobs. `Stop(ctx)` releases what
`Configure` started (jobs are owned by `host.Core.Jobs` and stop with the
process, so a module usually returns nil).

If a module needs to publish a port, implement
`module.PortProvider[core.Services]` (`ProvidePorts`) on the same instance.

Access the shared graph through `host.Core`:

| Field | Type | Notes |
|---|---|---|
| `Config` | `*config.Config` | Sphere configuration |
| `DB` | `*gorm.DB` | shared handle |
| `Store` | `*store.Store` | query helpers (`New`, `DB`, `GetPost`, `GetPublisher`, `GetPublisherByName`, `ParseUUID`) |
| `Redis` | `cache.CacheService` | `Set/Get/Remove/SetWithGroups/ExecuteWithLock` |
| `NATS` | `*eventbus.Client` | `Enabled()`, `Publish(ctx, subject, stream, payload)`, `Consume(ctx, stream, subject, consumer, handler)`, `PublishWS(ctx, accountID, event, payload)`, `PublishWSToAccounts(...)` |
| `Clients` | `*grpcclient.Clients` | outbound gRPC; nil fields mean "not configured" |
| `Auth` | `*authn.Service` | `RequirePermission(key)` |
| `Jobs` | `*jobs.Scheduler` | `Every(name, d, fn)`, `Now(name, fn)`, `Cron(name, expr, fn)` |
| `Log` | `*slog.Logger` | |

## HTTP

```go
host.API.GET("/posts/:id", handler)                 // group is already /api
host.API.POST("/posts", authn.RequireAuth(), host.Core.Auth.RequirePermission("posts.create"), handler)
```

Helpers from `shared/platform/httpx`:

- `httpx.PageQuery(c, maxTake) (httpx.Page, bool)` — parses `offset`/`take`,
  rejects `take > maxTake` with the C# `PAGINATION_TAKE_EXCEEDED` validation
  body. Max is `host.Core.Config.Security.MaxTake` (default 100).
- `httpx.List(c, items, total)` — writes a bare JSON array plus `X-Total`.
- `httpx.OK(c, body)`, `httpx.Created`, `httpx.NoContent(c)` (200 `{}`).
- `httpx.Fail(c, errs.NotFound("..."))`, `httpx.BadRequest(c, code, msg)`,
  `httpx.Unauthorized(c)`, `httpx.Forbidden(c, msg)`, `httpx.Internal(c)`.
- `httpx.BindJSON(c, &dst) bool`.

Auth helpers from `shared/platform/authn`:

- `authn.CurrentUser(c) *gen.DyAccount` (nil when anonymous)
- `authn.CurrentSession(c) *gen.DyAuthSession`
- `authn.AccountID(c) string`, `authn.IsSuperuser(c) bool`
- `authn.RequireAuth()`, `authn.RequireInteractive()`
- `auth.Service.RequirePermission(key)` — superusers bypass; unknown service
  fails closed with 503.

The engine already runs the optional-auth middleware and has `/health`,
`/alive`, `/healthz` and a JSON 404. Do not register those.

## Wire types

`internal/sphere/model` holds the JSON shapes (`model.Post`, `model.Publisher`,
`model.Survey`, `model.Sticker`, `model.Boost`, ...). Enums serialize as
integers; times are `*model.Time` (UTC RFC3339, seconds); a nil pointer emits
`null` (fields must NOT use omitempty unless the C# model marked them
`[JsonIgnore]`). Add new wire types to a NEW file in your own package if they
are module-specific; put them in `model` only when another module needs them —
in that case create `internal/sphere/model/<yourmodule>.go`.

## Persistence

- Entities live in `internal/sphere/store/entities.go` (already written, all
  legacy tables). Map rows to wire types with the helpers in
  `internal/sphere/store/store.go` (`PostToModel`, `PublisherToModel`,
  `SurveyToModel`, `StickerToModel`, `TimePtr`, `JSONValue`, ...).
- Legacy tables must stay importable: column names/types mirror the C#
  EF schema. `AutoMigrate` creates them from the entity tags; add `SQL` hooks
  for indexes/constraints AutoMigrate cannot express.
- Soft delete: entities embed `store.EntityBase` (`gorm.DeletedAt`), so GORM
  filters deleted rows automatically. Use `Unscoped()` only for admin reads.
- Messager entities live in `internal/messager/store/entities.go` (same rules).

## Route fidelity

Reproduce the C# routes exactly: same method, same path, same query parameter
names, same JSON field names, same status codes, same `X-Total` pagination.
Where the C# controller returns an object, return that object. Where it returns
`Ok()`, return `httpx.NoContent(c)`.

## gRPC

Register your service on `host.GRPC` from `Configure`, using the generated
server from `src.solsynth.dev/sosys/lighthouse/shared/proto` (package name `gen`), e.g.
`gen.RegisterDyPostServiceServer(host.GRPC, &postServer{core: host.Core})`.
Embed `gen.UnimplementedDyPostServiceServer`. If a method is not implementable
yet, return `status.Error(codes.Unimplemented, "...")` rather than a wrong
answer. Read `_grpc.pb.go` for exact method signatures and `*.pb.go` for
message fields — do not guess field names.

## Reporting

Finish with a short report: files created, routes registered, gRPC services,
known gaps (be explicit — never claim something works that you did not
implement).
