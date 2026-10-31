# syntax=docker/dockerfile:1

# Metoer — notifications, push delivery, email plans and delivery
# observability (HTTP :8080 + gRPC :9090).
#
# Built from the repository root so the vendored shared/ module is in context:
#   docker build -f internal/metoer/Dockerfile .

FROM golang:1.27-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOWORK=off

RUN apk add --no-cache ca-certificates
# Keep the repository layout: internal/metoer/go.mod replaces ../../shared.
COPY internal/metoer/go.mod internal/metoer/go.sum ./internal/metoer/
COPY shared/go.mod shared/go.sum ./shared/
RUN --mount=type=cache,target=/go/pkg/mod \
    cd internal/metoer && go mod download

COPY internal/metoer/ ./internal/metoer/
COPY shared/ ./shared/
ARG VERSION=dev
ARG GIT_COMMIT=unknown
RUN --mount=type=cache,target=/root/.cache/go-build \
    cd internal/metoer && go build -trimpath -buildvcs=false \
      -ldflags="-s -w -X main.version=${VERSION} -X main.gitCommit=${GIT_COMMIT}" \
      -o /out/metoer ./cmd/metoer

# Alpine keeps tzdata for the scheduler and the SMTP/APNs clients' TLS roots.
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/metoer /app/metoer
COPY internal/metoer/config.example.toml /app/config.example.toml
EXPOSE 8080 9090
# Runtime secrets (Keys/Solian.json, Keys/Solian.p8, server.crt,
# server.key) are NOT baked into the image — mount them at deploy time,
# e.g.:
#   docker run -v /host/keys:/app/Keys ghcr.io/$PACKAGE_OWNER/lighthouse-metoer
ENTRYPOINT ["/app/metoer"]
