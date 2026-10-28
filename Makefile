BIN_DIR := bin

SPHERE_CONFIG ?= config/sphere.example.toml
MESSAGER_CONFIG ?= config/messager.example.toml

.PHONY: all build build-sphere build-messager run-sphere run-messager test lint tidy fmt migrate-sphere migrate-messager

all: build

build: build-sphere build-messager

build-sphere:
	go build -o $(BIN_DIR)/sphere ./cmd/sphere

build-messager:
	go build -o $(BIN_DIR)/messager ./cmd/messager

run-sphere:
	CONFIG_PATH=$(SPHERE_CONFIG) go run ./cmd/sphere

run-messager:
	CONFIG_PATH=$(MESSAGER_CONFIG) go run ./cmd/messager

test:
	go test ./...

lint:
	go vet ./...

tidy:
	go mod tidy

fmt:
	gofmt -l -w cmd internal

# Boot each service once before importing so the schema exists.
migrate-sphere:
	go run ./cmd/sphere-migrate --source-dsn "$$SPHERE_SOURCE_DSN" --target-dsn "$$SPHERE_TARGET_DSN"

migrate-messager:
	go run ./cmd/messager-migrate --source-dsn "$$MESSAGER_SOURCE_DSN" --target-dsn "$$MESSAGER_TARGET_DSN"
