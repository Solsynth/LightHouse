# syntax=docker/dockerfile:1

# ---- SPA build (web/) ----
FROM oven/bun:1.4-alpine AS web
WORKDIR /web
COPY web/package.json web/bun.lock ./
RUN --mount=type=cache,target=/root/.bun/install/cache bun install --frozen-lockfile
COPY web/ ./
# Skip vue-tsc here: the build stage must not depend on the host-only tsgo
# bridge, and the CI Go image has no Node toolchain to run it.
RUN bun run build-only

# ---- Go build ----
FROM golang:1.26-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0

RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG VERSION=dev
ARG GIT_COMMIT=unknown
RUN --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -buildvcs=false \
      -ldflags="-s -w -X main.version=${VERSION} -X main.gitCommit=${GIT_COMMIT}" \
      -o /out/stargate ./cmd/stargate

FROM scratch
WORKDIR /app
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/stargate /app/stargate
COPY --from=web /web/dist /app/web/dist
COPY config.example.toml /app/config.example.toml
EXPOSE 8080 9090
# Runtime secrets (Keys/PrivateKey.pem, PublicKey.pem, Solarpass.p8,
# GeoLite2-City.mmdb, server.crt, server.key) are NOT baked into the image —
# mount them at deploy time, e.g.:
#   docker run -v /host/keys:/app/Keys ghcr.io/$PACKAGE_OWNER/stargate
ENTRYPOINT ["/app/stargate"]
