# syntax=docker/dockerfile:1

# Blade gateway — public HTTP/WebSocket entry and gRPC service registry.

FROM golang:1.26.2-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/blade ./cmd

# Alpine keeps busybox wget for the compose healthcheck.
FROM alpine:3.24
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/blade /app/blade
COPY configs/docker.toml /app/configs/docker.toml
ENV CONFIG_PATH=/app/configs/docker.toml \
    GIN_MODE=release \
    ZEROLOG_PRETTY=true
EXPOSE 6000 7001
ENTRYPOINT ["/app/blade"]
