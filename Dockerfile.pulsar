# syntax=docker/dockerfile:1

# Pulsar — universal multi-domain playback tracking, scrobbling, catalog, and collections (HTTP + gRPC).

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
      -o /out/pulsar ./internal/pulsar/cmd/pulsar

# Static binary, CA bundle for outbound HTTPS; everything else is mounted.
FROM scratch
WORKDIR /app
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/pulsar /app/pulsar
COPY internal/pulsar/config.example.toml /app/config.example.toml
EXPOSE 5073 7101
ENTRYPOINT ["/app/pulsar"]
