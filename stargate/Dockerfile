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
COPY config.example.toml /app/config.example.toml
EXPOSE 8080 9090
# Runtime secrets (Keys/PrivateKey.pem, PublicKey.pem, Solarpass.p8,
# GeoLite2-City.mmdb) are NOT baked into the image — mount them at deploy
# time, e.g.:
#   docker run -v /host/keys:/app/Keys ghcr.io/$PACKAGE_OWNER/stargate
ENTRYPOINT ["/app/stargate"]
