BIN_DIR := bin

# Every service owns its directory under internal/: one module, its own cmd/,
# and its example config. Local configs are the same paths without `.example`.
SPHERE_CONFIG ?= internal/sphere/config.example.toml
MESSAGER_CONFIG ?= internal/messager/config.example.toml
PULSAR_CONFIG ?= internal/pulsar/config.example.toml
STARGATE_CONFIG ?= internal/stargate/config.example.toml
METOER_CONFIG ?= internal/metoer/config.example.toml
BLADE_CONFIG ?= internal/blade/configs/config.toml
RELAY_CONFIG ?= internal/blade/configs/relay.toml
FILESYSTEM_CONFIG ?= internal/filesystem/config.example.toml

# internal/filesystem is not in go.work (see go.work): it pins go1.26.2, and
# its grpc/x-net pair does not compile under go1.27. Every filesystem command
# below therefore runs outside the workspace with that toolchain.
FS_GO := GOWORK=off GOTOOLCHAIN=go1.26.2 go

.PHONY: all build build-sphere build-messager build-pulsar build-stargate build-metoer build-blade build-relay build-filesystem \
	run-sphere run-messager run-pulsar run-stargate run-metoer run-blade run-relay run-filesystem \
	web-stargate web-stargate-check web-stargate-dev web-stargate-test \
	web-filesystem web-filesystem-check web-filesystem-dev web-filesystem-test \
	test test-sphere test-pulsar test-stargate test-metoer test-blade test-filesystem lint tidy fmt \
	migrate-sphere migrate-messager migrate-stargate migrate-filesystem-legacy \
	stargate-hash-password stargate-module-import

all: build

build: build-sphere build-messager build-pulsar build-stargate build-metoer build-blade build-relay build-filesystem

# Children are built from inside their module so relative config/web paths and
# their own Makefile defaults keep working.
build-sphere:
	go build -o $(BIN_DIR)/sphere ./internal/sphere/cmd/sphere

build-messager:
	go build -o $(BIN_DIR)/messager ./internal/messager/cmd/messager

build-pulsar:
	go build -o $(BIN_DIR)/pulsar ./internal/pulsar/cmd/pulsar

build-stargate:
	cd internal/stargate && go build -o ../../$(BIN_DIR)/stargate ./cmd/stargate

build-metoer:
	cd internal/metoer && go build -o ../../$(BIN_DIR)/metoer ./cmd/metoer

build-blade:
	cd internal/blade && go build -o ../../$(BIN_DIR)/blade ./cmd

build-relay:
	cd internal/blade && go build -o ../../$(BIN_DIR)/relay ./cmd/relay

build-filesystem:
	cd internal/filesystem && $(FS_GO) build -o ../../$(BIN_DIR)/dysonfs ./cmd

run-sphere:
	CONFIG_PATH=$(SPHERE_CONFIG) go run ./internal/sphere/cmd/sphere

run-messager:
	CONFIG_PATH=$(MESSAGER_CONFIG) go run ./internal/messager/cmd/messager

run-pulsar:
	CONFIG_PATH=$(PULSAR_CONFIG) go run ./internal/pulsar/cmd/pulsar

run-stargate:
	cd internal/stargate && CONFIG_PATH=$(STARGATE_CONFIG) go run ./cmd/stargate

run-metoer:
	cd internal/metoer && CONFIG_PATH=$(METOER_CONFIG) go run ./cmd/metoer

run-blade:
	cd internal/blade && CONFIG_PATH=$(BLADE_CONFIG) go run ./cmd

run-relay:
	cd internal/blade && RELAY_CONFIG_PATH=$(RELAY_CONFIG) go run ./cmd/relay

run-filesystem:
	cd internal/filesystem && CONFIG_PATH=$(FILESYSTEM_CONFIG) $(FS_GO) run ./cmd --mode master

# SPAs — bun, each in its child directory.
web-stargate:
	$(MAKE) -C internal/stargate web

web-stargate-check:
	$(MAKE) -C internal/stargate web-check

web-stargate-dev:
	$(MAKE) -C internal/stargate dev

web-stargate-test:
	$(MAKE) -C internal/stargate web-test

web-filesystem:
	$(MAKE) -C internal/filesystem web

web-filesystem-check:
	$(MAKE) -C internal/filesystem web-check

web-filesystem-dev:
	$(MAKE) -C internal/filesystem web-dev

web-filesystem-test:
	$(MAKE) -C internal/filesystem web-test

test: test-shared test-sphere test-pulsar test-stargate test-metoer test-blade test-filesystem

test-shared:
	cd shared && go test ./...

test-sphere:
	go test ./...

test-pulsar:
	go test ./internal/pulsar/...

# internal/stargate/store needs a reachable PostgreSQL (dyson_stargate); it
# fails fast without one.
test-stargate:
	cd internal/stargate && go test ./...

test-metoer:
	cd internal/metoer && go test ./...

test-blade:
	cd internal/blade && go test ./...

# internal/filesystem/service has two known failures inherited from the
# FileSystem repo (TestOverwriteFileSwapsObjectAndDeletesDereferencedSource,
# TestFastOverwriteFileUpdatesExistingObject).
test-filesystem:
	cd internal/filesystem && $(FS_GO) test ./...

lint:
	cd shared && go vet ./...
	go vet ./...
	cd internal/stargate && go vet ./...
	cd internal/metoer && go vet ./...
	cd internal/blade && go vet ./...
	cd internal/filesystem && $(FS_GO) vet ./...

tidy:
	cd shared && go mod tidy
	go mod tidy
	cd internal/stargate && go mod tidy
	cd internal/metoer && go mod tidy
	cd internal/blade && go mod tidy
	cd internal/filesystem && $(FS_GO) mod tidy

fmt:
	gofmt -l -w internal shared

# Boot each service once before importing so the schema exists.
migrate-sphere:
	go run ./internal/sphere/cmd/sphere-migrate --source-dsn "$$SPHERE_SOURCE_DSN" --target-dsn "$$SPHERE_TARGET_DSN"

migrate-messager:
	go run ./internal/messager/cmd/messager-migrate --source-dsn "$$MESSAGER_SOURCE_DSN" --target-dsn "$$MESSAGER_TARGET_DSN"

migrate-stargate:
	cd internal/stargate && go run ./cmd/stargate-migrate

# Imports the legacy C# drive database; set LEGACY_DATABASE_DSN.
migrate-filesystem-legacy:
	cd internal/filesystem && CONFIG_PATH=$(FILESYSTEM_CONFIG) $(FS_GO) run ./cmd migrate-legacy

stargate-hash-password:
	cd internal/stargate && go run ./cmd/stargate-hash-password

stargate-module-import:
	cd internal/stargate && go run ./cmd/stargate-module-import $(ARGS)
