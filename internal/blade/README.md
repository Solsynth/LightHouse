# Blade

An API Gateway built in Go using Gin, ported from the .NET YARP-based gateway.

In fact, it's not the next generation gateway, it's the prev generation.
Because Solar Network is built by pure Go at the v2, and migrated to .NET at v3, now we planned to move some core services to Go again.

## Features

- **Reverse Proxy Routing** - Routes requests to backend microservices
- **Health Monitoring** - Background health checks every 10 seconds
- **Service Discovery** - Redis-backed leased instance registry over gRPC
- **L4 Relays** - Optional TCP relay nodes (`cmd/relay`) that route by TLS SNI without terminating TLS
- **Readiness Gating** - Returns 503 if core services are unhealthy
- **CORS Support** - Allows all origins with custom headers
- **Special Routes** - Fully configurable route system via `routes`
- **Route Transforms** - Strips service prefix, adds `/api` prefix
- **Maintenance Mode** - Returns 503 globally or per configured services

## Configuration

Edit `configs/config.toml`:

```toml
siteUrl = "https://solian.app"

[cache]
serializer = "JSON"

[services]
# Optional static fallbacks. Registered instances take precedence; a service
# that registers needs no entry here. Keep an entry while migrating, or remove
# it to make Blade depend entirely on discovery.
ring = { http = "http://ring:5000", grpc = "ring:5001" }
pass = { http = "http://pass:5000", grpc = "pass:5001" }
drive = { http = "http://drive:5000", grpc = "drive:5001" }
sphere = { http = "http://sphere:5000", grpc = "sphere:5001" }
develop = { http = "http://develop:5000", grpc = "develop:5001" }
insight = { http = "http://insight:5000", grpc = "insight:5001" }
zone = { http = "http://zone:5000", grpc = "zone:5001" }
messager = { http = "http://messager:5000", grpc = "messager:5001" }
wallet = { http = "http://wallet:5000", grpc = "wallet:5001" }
ideask = { http = "http://ideask:5000", grpc = "ideask:5001" }

[endpoints]
serviceNames = ["ring", "pass", "drive", "sphere", "develop", "insight", "zone", "messager", "wallet", "ideask"]
coreServiceNames = ["ring", "pass", "drive", "sphere"]

[rateLimit]
requestsPerMinute = 120
burstAllowance = 10

[health]
checkIntervalSeconds = 10

[discovery]
enabled = true
prefix = "blade:discovery"
leaseSeconds = 30
leaderLeaseSeconds = 15
# Relay control plane only; in-network gRPC registration needs no token.
registrationToken = "replace-with-a-relay-secret"
relayServiceName = "relay"

[server]
port = "6000"
readTimeout = 60
writeTimeout = 60

[websocket]
enabled = true
path = "/ws"
keepAliveSeconds = 60
maxMessageBytes = 4096

[maintenance]
enabled = false
mode = "full" # "full" or "service"
services = [] # used when mode = "service"
```

### Maintenance Mode

```toml
[maintenance]
enabled = true
mode = "full" # blocks all proxied requests with 503
```

```toml
[maintenance]
enabled = true
mode = "service" # blocks only listed services
services = ["sphere", "drive"]
```

Legacy key `maintaince` is also supported for backward compatibility.

### Environment Variables

| Variable         | Description           | Default               |
| ---------------- | --------------------- | --------------------- |
| `CONFIG_PATH`    | Path to config file   | `configs/config.toml` |
| `RELAY_CONFIG_PATH` | Path to relay config file | `configs/relay.toml` |
| `GIN_MODE`       | `debug` or `release`  | `debug`               |
| `ZEROLOG_PRETTY` | Enable pretty logging | `false`               |
| `LOG_LEVEL`      | Log level (`debug`, `info`, `warn`, `error`) | `info` (`debug` when pretty) |

### Service Discovery

When `discovery.enabled` is set, Blade requires `cache.redisUrl`. In-network
services register an HTTP/gRPC endpoint through
`DyServiceDiscoveryService/Register`, then renew the lease before it expires.
That internal gRPC API is unauthenticated, so bind it to the cluster network
only. Only the Redis-elected Blade replica probes registered HTTP endpoints; all
replicas read the same healthy instance set for proxy routing.

`discovery.registrationToken` is used only by the public relay control plane
(`PUT`/`DELETE /relays/{id}`). Leave it empty to disable relay registration while
keeping in-network service discovery enabled.

Configured `[services]` targets remain a fallback until that service has a
registered instance, allowing incremental migration. Blade also resolves its
own outbound gRPC targets (the websocket auth/profile services) through the
same registry, so those can move without editing `[services]`.

### L4 Relay Nodes

`cmd/relay` is a second binary shipped in the same image, selected with the
container command `relay` (config `configs/relay.toml`, path overridable with
`RELAY_CONFIG_PATH`). It accepts TCP connections, peeks the cleartext TLS
ClientHello for the SNI, and copies bytes to an upstream chosen from a static
allowlist. TLS is never terminated:
certificates, client certificates, and ECH-less SNI routing all stay
end-to-end. Unlisted SNI is rejected unless `relay.defaultUpstream` is set.

Relays are deployed outside the cluster network, so they announce themselves
over the gateway's **public HTTPS entry** — `PUT <discovery.url>/relays/{id}`
with `discovery.registrationToken` — publishing a `tcp` endpoint
(`relay.publicHost:relay.publicPort`), a region, and a weight. The heartbeat
carries the relay's own health report: the elected checker never dials a relay,
because it cannot reach one, and a relay that stops reporting expires out of
the catalog. Blade serves the resulting list at `GET /relays`:

```json
{"relays":[{"id":"jp-01","endpoint":"relay-jp.solian.app","port":443,"region":"jp","weight":1,"healthy":true}]}
```

`GET /relays` is the intentionally public, client-facing discovery contract:
clients fetch it anonymously and dial the returned `endpoint:port`, sorting the
picker by `region`, `weight`, and `healthy`, so those fields are part of the
client API and the route is not gated. It returns `503` when
`discovery.enabled` is false. The gateway owns the registry service name
(`discovery.relayServiceName`, default `relay`). Those routes are mounted ahead
of the readiness gate so a relay can register while core services are unhealthy.
See
[RELAY_DEPLOYMENT.md](docs/RELAY_DEPLOYMENT.md) for the node's deployment,
configuration reference, and operations.

### Special Routes Configuration

The gateway supports fully configurable special routes:

```toml
[[routes]]
path = "/.well-known/openid-configuration"
service = "pass"
target = "/auth/.well-known/openid-configuration"
prefix = false

[[routes]]
path = "/activitypub"
service = "sphere"
target = "/activitypub"
prefix = true           # true for wildcard matching
```

| Field     | Description                                                             |
| --------- | ----------------------------------------------------------------------- |
| `path`    | Source path to match (e.g., `/ws`, `/.well-known/openid-configuration`) |
| `service` | Target service name                                                     |
| `target`  | Path on the backend service                                             |
| `prefix`  | If `true`, match path as prefix (e.g., `/activitypub/**`)               |

## Build & Run

### Local

```bash
# Build
go build -o gateway ./cmd/main.go

# Run
./gateway

# Or with custom config
CONFIG_PATH=./configs/config.toml ./gateway
```

### Relay Node

```bash
# Build
go build -o relay ./cmd/relay

# Run (needs relay.listen; :443 requires privileges)
RELAY_CONFIG_PATH=./configs/relay.toml ./relay
```

### Docker

```bash
# Build — one image, both binaries
docker build -t lighthouse-blade .

# Run the gateway (the default command)
docker run -p 6000:6000 lighthouse-blade

# Run a relay node
docker run -p 443:443 lighthouse-blade relay

# Run with custom config
docker run -p 6000:6000 -v ./configs/docker.toml:/app/configs/docker.toml:ro lighthouse-blade
docker run -p 443:443 -v ./configs/relay.toml:/app/configs/relay.toml:ro lighthouse-blade relay
```

## Endpoints

| Endpoint                | Description                                                        |
| ----------------------- | ------------------------------------------------------------------ |
| `GET /health`           | Gateway health document (`application/health+json`, never gated)   |
| `GET /health/{service}` | Per-service health document (`200` pass, `503` fail, `404` unknown) |
| `GET /relays`           | Public client-facing catalog of registered L4 relay nodes |
| `/<service>/**`         | Proxied to backend service (e.g., `/ring/**` → `ring:5000/api/**`) |
| `/ws`                   | Native WebSocket gateway (configurable via `websocket.path`)       |
| `/.well-known/*`        | .well-known endpoints (configurable via `routes`)                  |
| `/activitypub/**`       | ActivityPub (configurable via `routes`)                            |
| `/swagger/<service>/**` | Swagger docs → service                                             |
| `gRPC DyServiceDiscoveryService` | Register, renew, remove, and resolve service instances             |

### Health Checks

`GET /health` answers with a health check document in the
[health+json](https://datatracker.ietf.org/doc/html/draft-inadarei-api-health-check-06)
format and is mounted ahead of the readiness gate, so it reports *why* the
gateway is not ready instead of the gate's generic `503`. `status` is `fail`
(HTTP `503`) when any core service is unhealthy, `warn` (HTTP `200`) when only
non-core services are, and `pass` (HTTP `200`) otherwise. Each tracked service
appears under `checks` as a one-element array.

```json
{
  "status": "warn",
  "serviceId": "blade",
  "description": "Solar Network API gateway",
  "output": "one or more non-core services are unhealthy",
  "checks": {
    "ring": [
      {
        "componentId": "ring",
        "componentType": "component",
        "status": "pass",
        "time": "2026-10-04T11:42:16Z",
        "links": { "self": "https://api.solian.app/health/ring" }
      }
    ],
    "sphere": [
      {
        "componentId": "sphere",
        "componentType": "component",
        "status": "fail",
        "time": "2026-10-04T11:42:16Z"
      }
    ]
  },
  "links": {
    "self": "https://api.solian.app/health"
  }
}
```

`Cache-Control: max-age` mirrors `health.checkIntervalSeconds`, the period at
which the aggregator refreshes the snapshot.

`GET /health/{service}` serves the same document scoped to one service — it is
the target a status page polls, and each `checks` entry publishes it as its
`links.self`, so the roster can be discovered from the aggregate document
alone. An unknown service is a failing document answered with `404`.

```json
{
  "status": "fail",
  "serviceId": "blade",
  "output": "service \"sphere\" is unhealthy",
  "checks": {
    "sphere": [
      {
        "componentId": "sphere",
        "componentType": "component",
        "status": "fail",
        "time": "2026-10-04T04:07:13Z",
        "links": { "self": "https://api.solian.app/health/sphere" }
      }
    ]
  },
  "links": { "self": "https://api.solian.app/health/sphere" }
}
```

### WebSocket Authentication Notes

Current implementation follows `DysonTokenAuthHandler` behavior:

- Token extraction order: `tk` query, `Authorization` header (`Bearer`, `AtField`, `AkField`), `AuthToken` cookie
- Token validation: remote gRPC call to `DyAuthService/Authenticate` using the configured `websocket.authService` target
- Request IP is forwarded to auth service as `ip_address`

## Request Flow

```
Client Request
    ↓
Rate Limiter (120 req/min/IP)
    ↓
Readiness Middleware (503 if core services unhealthy)
    ↓
Reverse Proxy (routes based on path)
    ↓
Backend Service
```
