FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/stargate ./cmd/stargate

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/stargate /app/stargate
COPY Keys/ /app/Keys/
COPY config.example.toml /app/config.example.toml
EXPOSE 8080 9090
ENTRYPOINT ["/app/stargate"]
