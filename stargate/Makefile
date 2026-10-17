BINARY := stargate
CONFIG ?= config.example.toml

.PHONY: build run web web-dev web-test test lint tidy

build:
	go build -o bin/$(BINARY) ./cmd/stargate

# Build the SPA into web/dist; Stargate serves it from [web] distPath.
web:
	cd web && bun install --frozen-lockfile && bun run build

web-dev:
	cd web && bun run dev

web-test:
	cd web && bun run test:unit --run

run:
	CONFIG_PATH=$(CONFIG) go run ./cmd/stargate

migrate:
	go run ./cmd/stargate-migrate

test:
	go test ./...

lint:
	go vet ./...

tidy:
	go mod tidy
