BINARY := stargate
CONFIG ?= config.toml

.PHONY: build run start dev web web-dev web-test web-check test lint tidy

build:
	go build -o bin/$(BINARY) ./cmd/stargate

# Build the SPA into web/dist; Stargate serves it from [web] distPath.
web:
	cd web && bun install --frozen-lockfile && bun run build

web-check:
	$(MAKE) -C web check

web-dev:
	cd web && bun run dev

web-test:
	cd web && bun run test:unit --run

run:
	CONFIG_PATH=$(CONFIG) go run ./cmd/stargate

# One command: build the console and the service, then serve both on :8080.
start: web build
	CONFIG_PATH=$(CONFIG) ./bin/$(BINARY)

# Two processes for development: Go API on :8080, Vite on :5173 with /api proxied.
dev:
	@CONFIG_PATH=$(CONFIG) go run ./cmd/stargate & \
	server=$$!; trap 'kill $$server 2>/dev/null' INT TERM EXIT; \
	$(MAKE) -C web dev

migrate:
	go run ./cmd/stargate-migrate

test:
	go test ./...

lint:
	go vet ./...

tidy:
	go mod tidy
