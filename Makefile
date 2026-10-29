BIN_DIR := bin

SPHERE_CONFIG ?= config/sphere.example.toml
MESSAGER_CONFIG ?= config/messager.example.toml
STARGATE_CONFIG ?= config.example.toml
BLADE_CONFIG ?= configs/config.toml
RELAY_CONFIG ?= configs/relay.toml

# Every target is workspace-aware (go.work). Directory patterns such as ./...
# stop at a nested go.mod, so each module gets its own pass.
MODULES := . stargate blade

.PHONY: all build build-sphere build-messager build-stargate build-blade build-relay \
	run-sphere run-messager run-stargate run-blade run-relay \
	web web-check web-dev web-test \
	test test-sphere test-stargate test-blade lint tidy fmt \
	migrate-sphere migrate-messager migrate-stargate stargate-hash-password stargate-module-import

all: build

build: build-sphere build-messager build-stargate build-blade build-relay

build-sphere:
	go build -o $(BIN_DIR)/sphere ./cmd/sphere

build-messager:
	go build -o $(BIN_DIR)/messager ./cmd/messager

# Built from inside the child module so relative config/web paths keep working.
build-stargate:
	cd stargate && go build -o ../$(BIN_DIR)/stargate ./cmd/stargate

build-blade:
	cd blade && go build -o ../$(BIN_DIR)/blade ./cmd

build-relay:
	cd blade && go build -o ../$(BIN_DIR)/relay ./cmd/relay

run-sphere:
	CONFIG_PATH=$(SPHERE_CONFIG) go run ./cmd/sphere

run-messager:
	CONFIG_PATH=$(MESSAGER_CONFIG) go run ./cmd/messager

run-stargate:
	cd stargate && CONFIG_PATH=$(STARGATE_CONFIG) go run ./cmd/stargate

run-blade:
	cd blade && CONFIG_PATH=$(BLADE_CONFIG) go run ./cmd

run-relay:
	cd blade && RELAY_CONFIG_PATH=$(RELAY_CONFIG) go run ./cmd/relay

# Stargate's SPA (stargate/web) — bun.
web:
	$(MAKE) -C stargate web
web-check:
	$(MAKE) -C stargate web-check

web-dev:
	$(MAKE) -C stargate dev

web-test:
	$(MAKE) -C stargate web-test

test: test-sphere test-stargate test-blade

test-sphere:
	go test ./...

# internal/store needs a reachable PostgreSQL (dyson_stargate); it fails fast without one.
test-stargate:
	cd stargate && go test ./...

test-blade:
	cd blade && go test ./...

lint:
	go vet ./...
	cd stargate && go vet ./...
	cd blade && go vet ./...

tidy:
	go mod tidy
	cd stargate && go mod tidy
	cd blade && go mod tidy

fmt:
	gofmt -l -w cmd internal stargate blade

# Boot each service once before importing so the schema exists.
migrate-sphere:
	go run ./cmd/sphere-migrate --source-dsn "$$SPHERE_SOURCE_DSN" --target-dsn "$$SPHERE_TARGET_DSN"

migrate-messager:
	go run ./cmd/messager-migrate --source-dsn "$$MESSAGER_SOURCE_DSN" --target-dsn "$$MESSAGER_TARGET_DSN"

migrate-stargate:
	cd stargate && go run ./cmd/stargate-migrate

stargate-hash-password:
	cd stargate && go run ./cmd/stargate-hash-password

stargate-module-import:
	cd stargate && go run ./cmd/stargate-module-import $(ARGS)
