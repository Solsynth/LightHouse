# syntax=docker/dockerfile:1

# Blade — one image, two binaries. The gateway is the default process; the L4
# relay node runs when the container is started with `relay` as its command.

FROM golang:1.26.2-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/blade ./cmd \
    && go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/relay ./cmd/relay

# Alpine keeps busybox wget for the gateway compose healthcheck.
FROM alpine:3.24
RUN apk add --no-cache ca-certificates git
WORKDIR /app
COPY --from=build /out/blade /app/blade
COPY --from=build /out/relay /app/relay
COPY configs/docker.toml /app/configs/docker.toml
COPY configs/relay.toml /app/configs/relay.toml
COPY --chmod=0755 entrypoint.sh /app/entrypoint.sh
ENV CONFIG_PATH=/app/configs/docker.toml \
    RELAY_CONFIG_PATH=/app/configs/relay.toml \
    GIN_MODE=release \
    ZEROLOG_PRETTY=true
# 6000/7001 gateway HTTP/gRPC, 443 relay traffic.
EXPOSE 6000 7001 443
ENTRYPOINT ["/app/entrypoint.sh"]
