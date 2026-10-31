# syntax=docker/dockerfile:1

# Built from the repository root so the vendored shared/ module is in context:
#   docker build -f internal/filesystem/Dockerfile .

# ---- SPA build (web/) ----
FROM oven/bun:1.4-alpine AS web
WORKDIR /src/web
COPY internal/filesystem/web/package.json internal/filesystem/web/bun.lock ./
RUN --mount=type=cache,target=/root/.bun/install/cache bun install --frozen-lockfile
COPY internal/filesystem/web/ ./
# Type-check is a developer/CI gate (bun run type-check); the tsgo-backed
# compiler in this project cannot resolve .vue modules on Linux builders, so
# the image runs the Vite build directly.
RUN bun run build-only

# ---- Go build ----
FROM golang:1.26.2-bookworm AS build
WORKDIR /src
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates git pkg-config libvips-dev \
    && rm -rf /var/lib/apt/lists/*

# Keep the repository layout: internal/filesystem/go.mod replaces ../../shared.
COPY internal/filesystem/go.mod internal/filesystem/go.sum ./internal/filesystem/
COPY shared/go.mod shared/go.sum ./shared/
RUN --mount=type=cache,target=/go/pkg/mod \
    cd internal/filesystem && go mod download

COPY internal/filesystem/ ./internal/filesystem/
COPY shared/ ./shared/
ARG VERSION=dev
ARG GIT_COMMIT=unknown
# BuildKit sets TARGETARCH to the target platform (arm64 locally, amd64 on the
# CI runner); an empty value falls back to the Go host default.
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    cd internal/filesystem && CGO_ENABLED=1 GOOS=linux GOARCH=${TARGETARCH} go build \
      -trimpath -buildvcs=false \
      -ldflags="-s -w -X src.solsynth.dev/sosys/filesystem/version.Version=${VERSION} -X src.solsynth.dev/sosys/filesystem/version.GitCommit=${GIT_COMMIT}" \
      -o /out/dysonfs ./cmd

# libvips and ffmpeg are runtime dependencies of govips and ffprobe.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates ffmpeg libvips42 \
    && rm -rf /var/lib/apt/lists/* /usr/share/doc/* /usr/share/man/* /usr/share/locale/*

WORKDIR /app
COPY --from=build /out/dysonfs /usr/local/bin/dysonfs
COPY --from=web /src/web/dist /app/web/dist
EXPOSE 8080 9090 9000 9001
ENTRYPOINT ["dysonfs"]
