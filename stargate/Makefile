BINARY := stargate
CONFIG ?= config.example.toml

.PHONY: build run test lint tidy

build:
	go build -o bin/$(BINARY) ./cmd/stargate

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
