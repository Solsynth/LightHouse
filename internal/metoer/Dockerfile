# syntax=docker/dockerfile:1

# Metoer — notifications, push delivery, email plans and delivery
# observability (HTTP :8080 + gRPC :9090).

FROM golang:1.27-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOWORK=off

RUN apk add --no-cache ca-certificates git
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG VERSION=dev
ARG GIT_COMMIT=unknown
RUN --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -buildvcs=false \
      -ldflags="-s -w -X main.version=${VERSION} -X main.gitCommit=${GIT_COMMIT}" \
      -o /out/metoer ./cmd/metoer

# Alpine keeps tzdata for the scheduler and the SMTP/APNs clients' TLS roots.
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/metoer /app/metoer
COPY config.example.toml /app/config.example.toml
EXPOSE 8080 9090
# Runtime secrets (Keys/Solian.json, Keys/Solian.p8, server.crt,
# server.key) are NOT baked into the image — mount them at deploy time,
# e.g.:
#   docker run -v /host/keys:/app/Keys ghcr.io/$PACKAGE_OWNER/lighthouse-metoer
ENTRYPOINT ["/app/metoer"]
