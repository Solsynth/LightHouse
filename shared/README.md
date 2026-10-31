# Lighthouse shared SDK

Vendored Go SDK for the Dyson Network platform. It provides generated gRPC
bindings plus reusable auth and cache helpers that match the shared
DysonNetwork contract. The module path is
`src.solsynth.dev/sosys/lighthouse/shared`.

## What You Get

- Generated protobuf and gRPC bindings in `proto/`
- HTTP auth helpers in `pkg/auth`
- Shared cache helpers in `pkg/cache`
- Cross-domain session hand-off in `pkg/handoff`
- The Lighthouse platform layer in `platform/` (bootstrap, config, DB, Redis,
  NATS, auth, HTTP/gRPC, module registry, jobs)

## Install

This module lives at `shared/` in the Lighthouse repository and is published
from it with tags such as `shared/v0.1.0`. Service modules require the tag, so
CI and Docker fetch it from git:

```go
require src.solsynth.dev/sosys/lighthouse/shared v0.1.0
```

Workspace builds resolve the local `shared/` directory through `go.work`.

## Packages

| Package            | Purpose                                                 |
| ------------------ | ------------------------------------------------------- |
| `proto`            | Generated protobuf messages and gRPC clients            |
| `pkg/auth`         | Token extraction, auth middleware, gRPC auth validation |
| `pkg/cache`        | Shared cache data, flags, groups, and locks             |
| `pkg/handoff`      | Cross-domain session hand-off between first-party apps |
| `platform/module`  | Compile-time module kernel: registry, resolver, ports, schema hooks, importer |
| `platform/app`     | Process bootstrap (HTTP + gRPC + discovery)             |
| `platform/config`  | TOML loader and environment overrides                   |
| `platform/db`      | GORM/PostgreSQL connection setup                        |
| `platform/redis`   | Redis and shared-cache wiring                           |
| `platform/eventbus`| NATS event bus                                          |
| `platform/authn`   | Inbound token validation and HTTP guards                |
| `platform/httpx`   | Gin engine, API errors and pagination                   |
| `platform/jobs`    | Interval and Quartz-cron scheduler                      |

## Module Kernel

`platform/module` is the single compile-time module kernel for the fleet. A
service parameterises it with its own core graph, so the machinery is shared
without sharing a process, a database or a module set:

```go
type Core struct {
    DB   *gorm.DB
    Log  *slog.Logger
    // ...
}

func Factory() module.Factory[Core] {
    return module.Factory[Core]{
        ID:             "app.example.feature",
        Kind:           module.KindFeature,
        DefaultEnabled: true,
        Requires:       []string{"app.example.core"},
        Ports:          []module.PortRequirement{{ID: "accounts", Optional: true}},
        Models:         []any{&Entity{}},
        SQL:            []module.SchemaHook{indexHook},
        Capabilities:   []string{"feature.read"},
        New:            func() module.Instance[Core] { return &instance{} },
    }
}
```

`module.Resolve` applies the enabled/disabled config, cascades disables,
topologically orders modules and records warnings. `module.MigrateSchema` runs
only the enabled modules' models and hooks. `module.PortRegistry` resolves
feature/integration ports with explicit `[ports]` selection, and
`module.Infos` produces the `GET /api/modules` payload. The optional importer
API (`ImportOptions`, `ImportStats`, `ImporterFor`, `RecordImport`) copies
legacy rows into a module's new schema with idempotent, dry-run capable
implementations.

Stargate, Sphere and Messager all use this kernel; DysonFS keeps its own
registry because its auth module installs middleware before the HTTP engine is
exposed (`PreHost`), which the shared lifecycle does not model yet.

## Auth Example

```go
package main

import (
 "log"

 "github.com/gin-gonic/gin"
 "src.solsynth.dev/sosys/lighthouse/shared/pkg/auth"
)

func main() {
 authenticator, err := auth.NewGrpcTokenAuthenticator(auth.GrpcAuthDialConfig{
  Target: "padlock:7003",
  UseTLS: true,
 })
 if err != nil {
  log.Fatal(err)
 }
 defer authenticator.Close()

 r := gin.Default()
 r.Use(auth.AuthMiddleware(authenticator, nil, nil))

 r.GET("/me", func(c *gin.Context) {
  result, token := auth.MustAuth(c)
  c.JSON(200, gin.H{
   "accountId": result.Account.GetId(),
   "sessionId": result.Session.GetId(),
   "tokenType": token.Type,
  })
 })

 _ = r.Run(":8080")
}
```

### Auth Helpers

```go
tokenInfo, ok := auth.ExtractToken(r)
result, err := auth.AuthenticateRequest(ctx, authenticator, r)
result, err := auth.Reauthenticate(ctx, authenticator, tokenInfo, r)
```

### Context Access

```go
result, token, ok := auth.GetAuth(c)
result, token := auth.MustAuth(c)
auth.WithAuth(c, result, tokenInfo)
```

## Shared Cache Example

```go
package main

import (
 "context"
 "log"
 "time"

 "github.com/redis/go-redis/v9"
 "src.solsynth.dev/sosys/lighthouse/shared/pkg/cache"
)

type Session struct {
 ID      string   `json:"id"`
 Scopes  []string `json:"scopes"`
 Account string   `json:"account_id"`
}

func main() {
 client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
 c := cache.NewRedisCacheService(client)
 ctx := context.Background()

 s := Session{ID: "abc123", Scopes: []string{"read"}, Account: "user-1"}
 if err := c.SetData(ctx, "auth:session:abc123", s, "Session", time.Hour); err != nil {
  log.Fatal(err)
 }

 if err := c.SetFlag(ctx, "auth:last_seen_touch:user-1", time.Minute); err != nil {
  log.Fatal(err)
 }

 if err := c.AddToGroup(ctx, "auth:session:abc123", "auth-sessions"); err != nil {
  log.Fatal(err)
 }

 lock, err := c.AcquireLock(ctx, "profile:hydrate:user-1", 30*time.Second, 5*time.Second, 100*time.Millisecond)
 if err != nil {
  log.Fatal(err)
 }
 if lock != nil {
  defer lock.Release(ctx)
 }
}
```

### Cache Contract

- Shared data is stored as a Redis hash with `data`, `codec`, `type`, and `cached_at`
- Flags are stored as plain string values with TTL
- Groups use Redis sets with reverse indexes
- Locks use `SET NX PX` plus token-based release

## Proto Example

```go
package main

import (
 "log"

 gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
 "google.golang.org/grpc"
)

func main() {
 conn, err := grpc.Dial("padlock:7003", grpc.WithInsecure())
 if err != nil {
  log.Fatal(err)
 }
 defer conn.Close()

 client := gen.NewDyAuthServiceClient(conn)
 _ = client
}
```

## Notes

- `AuthMiddleware` and `OptionalAuthMiddleware` accept a cache and profile client for shared auth state.
- `nil` cache/profile values are allowed when you only want auth validation.
- Shared cache keys use the `dyson:` prefix.
- In DysonNetwork, we use grpc with self-signed cert. So you should TURN OFF the CA validation and allowing set TLS when serving your own grpc service.

## Session Hand-off Example

A cookie has one `Domain` attribute, so two first-party apps on unrelated
domains (say `id.solian.app` and `dx.solsynth.dev`) cannot share a browser
session. `pkg/handoff` moves the session between them with a single-use code
instead of a shared cookie.

The issuer mounts both endpoints; the relying app's server redeems:

```go
// Issuer: issue from the caller's own session, redeem into a token pair.
issuer := &handoff.Issuer{
 Cache: theSharedCache,
 Clients: func(id string) (*handoff.Client, error) {
  return handoffClients.Lookup(id) // ID, Secret, AllowedRedirectHosts
 },
}
redeemer := &handoff.Redeemer{Cache: theSharedCache, Clients: handoffClients.Lookup}

api := router.Group("/api/auth")
handoff.MountIssue(api, handoff.IssueEndpointDeps{
 Issuer: issuer,
 Session: func(c *gin.Context) (handoff.SessionRef, bool) {
  session := middleware.CurrentSession(c.Request.Context())
  if session == nil {
   return handoff.SessionRef{}, false
  }
  return handoff.SessionRef{SessionID: session.Id, AccountID: session.AccountId}, true
 },
})
handoff.MountRedeem(api, handoff.RedeemEndpointDeps{
 Redeemer: redeemer,
 Mint: func(ctx context.Context, grant *handoff.Grant) (*handoff.RedeemWireResponse, error) {
  pair, err := authSvc.CreateTokenPairForAccount(ctx, grant.AccountID, grant.Scopes)
  if err != nil {
   return nil, err
  }
  return &handoff.RedeemWireResponse{
   Token: pair.AccessToken, RefreshToken: pair.RefreshToken,
   ExpiresIn: int64(time.Until(pair.AccessTokenExpiresAt).Seconds()),
  }, nil
 },
})
```

```go
// Relying app: the console opens {issuer}/sign-in?redirectUri=https://app.example/
// and its server redeems the ?code= that comes back.
client := &handoff.TokenClient{
 BaseURL:      "https://id.solian.app",
 ClientID:     "app.solian.express",
 ClientSecret: os.Getenv("HANDOFF_CLIENT_SECRET"),
}
tokens, err := client.Redeem(ctx, codeFromQuery)
```

Sharing a Redis instead of calling over HTTP is the lower-latency path: pass
the same `cache.CacheService` to `Issuer` and `Redeemer` and call
`Redeemer.Redeem` directly, skipping the round trip.

Properties the package enforces so a deployment does not have to:

- A code is single-use and consumed under a lock, so concurrent redemptions
  yield exactly one grant.
- A code is bound to the client, the session, and the validated redirect
  target. Another app, a wrong secret, and a replayed `redirect_uri` are all
  refused without consuming it.
- The target host must be registered on the client (exact host or subdomain)
  and https by default, which keeps the login page from becoming an open
  redirector.
- A client with no secret must use PKCE, since it cannot otherwise prove the
  code was issued to it.
- With no cache wired, `Issue` and `Redeem` refuse rather than degrade: a
  hand-off that cannot be single-use is not offered at all.

## Versioning

Each published version is a `shared/vX.Y.Z` tag in this repository. Bump the
tag and the `require` lines when the shared code changes; workspace builds
still use the local `shared/` directory. The generated `proto/` descriptors
still carry the upstream `go_package` value, which does not affect the Go
import path.
