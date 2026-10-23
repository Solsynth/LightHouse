# syntax=docker/dockerfile:1

# ---- SPA build (web/) ----
FROM oven/bun:1.4-alpine AS web
WORKDIR /web
# @solsynth/sunkenland lives on GitHub Packages, which authenticates every
# install — public packages included. The token is a build arg, so it never
# lands in a layer.
COPY web/package.json web/bun.lock web/.npmrc ./
ARG NODE_AUTH_TOKEN
RUN bun install --frozen-lockfile
COPY web/ ./
# Skip vue-tsc here: the build stage must not depend on the host-only tsgo
# bridge, and the CI Go image has no Node toolchain to run it.
RUN bun run build-only

# ---- Go build ----
FROM golang:1.26-alpine AS build
ARG VERSION=dev
ARG GIT_COMMIT=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-X main.version=${VERSION} -X main.gitCommit=${GIT_COMMIT}" -o /out/stargate ./cmd/stargate

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/stargate /app/stargate
COPY --from=web /web/dist /app/web/dist
COPY config.example.toml /app/config.example.toml
EXPOSE 8080 9090
# Runtime secrets (Keys/PrivateKey.pem, PublicKey.pem, Solarpass.p8,
# GeoLite2-City.mmdb, server.crt, server.key) are NOT baked into the image —
# mount them at deploy time, e.g.:
#   docker run -v /host/keys:/app/Keys ghcr.io/$PACKAGE_OWNER/stargate
ENTRYPOINT ["/app/stargate"]
