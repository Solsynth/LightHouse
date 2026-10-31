# Service Discovery Client Guide

Blade exposes `proto.DyServiceDiscoveryService` on its configured gRPC port.
Each running service instance registers itself, continuously renews a lease, and
removes itself during graceful shutdown. Blade uses the resulting healthy
instance set for HTTP gateway routing and makes it available to other services
through `Resolve`.

The contract is defined in `Spec/proto/discovery.proto` and generated in the
shared package `src.solsynth.dev/sosys/lighthouse/shared/proto`.

## Prerequisites

Blade must be configured with discovery enabled and Redis:

```toml
[cache]
redisUrl = "redis://redis:6379/0"

[discovery]
enabled = true
prefix = "blade:discovery"
leaseSeconds = 30
leaderLeaseSeconds = 15
```

The internal gRPC registration API is intentionally unauthenticated. Use an
internal address for Blade's gRPC server and do not expose it on the public
internet. Production deployments should isolate it at the network layer (or use
mTLS) rather than relying on a shared token.

`registrationToken` remains in Blade's config for the **public relay control
plane** (`PUT`/`DELETE /relays/{id}`); in-network services do not use it.

## Instance identity and endpoints

Every replica needs a stable, unique `instance_id` for its lifetime. Good
values include a Kubernetes pod UID, Nomad allocation ID, or a process UUID
generated during startup. Do not use only the service name: replicas would
overwrite each other.

Register endpoints by protocol when the service provides them:

```text
service:     sphere
instance_id: sphere-7f4d8b9c-pod-1
endpoints:
  http: http://sphere-7f4d8b9c-pod-1:8000
  grpc: sphere-7f4d8b9c-pod-1:7005
  nats: nats://broker/sphere
weight: 1
```

The `http` endpoint must be an absolute URL. Blade actively probes
`<http endpoint>/health`; a newly registered instance is not routable until
that check succeeds. A gRPC-only instance can be returned by `Resolve` with
`healthy_only = false`, but it cannot currently become healthy through Blade's
HTTP health checker. Give it an HTTP health endpoint until gRPC health probing
is added.

Blade uses the `http` and `grpc` map entries for its own proxying and
capabilities requests. Other protocol keys are preserved and returned by
`Resolve` for consumers that understand them. Legacy `http_endpoint` and
`grpc_endpoint` fields remain supported, but corresponding map entries take
precedence.

## Registration lifecycle

1. Create the gRPC channel to Blade.
2. Call `Register` after the service is ready to answer `/health`.
3. Renew well before expiry—typically every one-third of the granted lease.
4. If `Renew` returns `NotFound`, call `Register` again; the lease likely
   expired while Blade or Redis was unavailable.
5. On graceful shutdown, stop accepting work, call `Deregister`, then close
   the gRPC channel. Redis TTL remains the safety net if the process crashes.

`Register` always starts an instance unhealthy. Blade has one Redis-elected
health-check leader, so scaling Blade does not multiply active probes.

## Go client example

```go
package main

import (
    "context"
    "time"

    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials/insecure"
    gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

func register(ctx context.Context, bladeAddr, instanceID string) (gen.DyServiceDiscoveryServiceClient, *grpc.ClientConn, error) {
    conn, err := grpc.NewClient(bladeAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
    if err != nil {
        return nil, nil, err
    }
    client := gen.NewDyServiceDiscoveryServiceClient(conn)
    _, err = client.Register(ctx, &gen.DyRegisterServiceInstanceRequest{
        Instance: &gen.DyServiceInstance{
            Service: "sphere", InstanceId: instanceID,
            Endpoints: map[string]string{
                "http": "http://sphere:8000",
                "grpc": "sphere:7005",
            },
            Weight: 1,
        },
        LeaseSeconds: 30,
    })
    if err != nil {
        conn.Close()
        return nil, nil, err
    }
    return client, conn, nil
}

func renewLoop(ctx context.Context, client gen.DyServiceDiscoveryServiceClient, instanceID string) {
    ticker := time.NewTicker(10 * time.Second) // one-third of a 30-second lease
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            _, _ = client.Deregister(ctx, &gen.DyDeregisterServiceInstanceRequest{
                Service: "sphere", InstanceId: instanceID,
            })
            return
        case <-ticker.C:
            // Retry transient failures with bounded backoff. If this returns
            // NotFound, register the instance again before the next renewal.
            _, _ = client.Renew(ctx, &gen.DyRenewServiceLeaseRequest{
                Service: "sphere", InstanceId: instanceID, LeaseSeconds: 30,
            })
        }
    }
}
```

Use TLS credentials rather than `insecure.NewCredentials()` outside trusted
local networks.

`internal/blade/relay/registration.go` (used by `cmd/relay`) is the one client that
does *not* use this gRPC contract: relays are deployed outside the cluster
network, so they register, renew, and report their own health over the
gateway's public HTTPS entry instead. The cadence is the same — bounded retries
with 5s-to-30s backoff, renewal at one-third of the granted lease, and a
best-effort withdrawal on shutdown — and Blade exposes those instances at the
public, client-facing `GET /relays`; see
[SERVICE_REGISTRY.md](SERVICE_REGISTRY.md).

## Resolving another service

`Resolve` is read-only and does not require the registration secret. Ask for
healthy instances in normal request paths and choose an endpoint locally:

```go
response, err := client.Resolve(ctx, &gen.DyResolveServiceRequest{
    Service: "ring", HealthyOnly: true,
})
if err != nil || len(response.Instances) == 0 {
    // Return a retryable dependency-unavailable error.
}

// Choose one endpoint with round-robin, weighted round-robin, or a client-side
// load balancer, then maintain/reuse its gRPC connection.
endpoint := response.Instances[0].GrpcEndpoint
```

Cache resolved instances briefly (about one second) and retry a different
healthy instance on a connection failure. Do not cache forever: registrations,
health state, and leases can change at any time.

## Using discovery for outbound gRPC

The Go services do not have to be configured with a sibling's address. When
`[discovery] enabled = true`, the shared platform installs a `blade:///` gRPC
resolver backed by this same `Resolve` API. Every outbound client follows the
live registry, and the `[services]` entry becomes a fallback that is used only
while the sibling has no registration:

```toml
[discovery]
enabled = true
target = "blade:7001"          # the one bootstrap address

[services]
stargate = { grpc = "" }       # discovered, no hardcoded address
blade    = { grpc = "" }       # falls back to discovery.target (Blade does not register itself)
```

A bare address (`stargate:9090`) is still accepted as a static fallback, so a
rollout can keep the old table while discovery takes over one service at a time.
If a service has registered instances but none is healthy, the resolver does
not fall back to the static address; that mirrors Blade's HTTP routing rule.

Blade itself uses the same resolver in-process: its websocket gateway resolves
`websocket.authService` and `websocket.profileService` from the registry, with
the matching `[services]` gRPC entry as the fallback. This means a registered
stargate can move or scale without editing Blade's configuration.

The gRPC resolver polls every five seconds and asks for an immediate refresh
when gRPC reports a connection failure. Endpoints are deduplicated and served
with round-robin load balancing. Discovery failures degrade to the configured
fallback; they never prevent a service from starting.

## Failure behavior

- A crashed or partitioned instance disappears after its Redis lease expires.
- A failed `/health` probe marks the instance unhealthy, removing it from
  Blade proxy routing and `Resolve(healthy_only = true)`.
- A Redis outage prevents new registration and renewal. Existing process-local
  callers should use bounded retries and surface dependency-unavailable errors
  rather than assuming a stale endpoint remains valid.
- Until a service has a registered instance, Blade continues to use its static
  `[services]` HTTP target. Once at least one discovered instance exists, only
  healthy discovered HTTP instances are selected for gateway routing.
