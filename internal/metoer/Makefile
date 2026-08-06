BINARY := metoer
CONFIG ?= config.example.toml

.PHONY: build run test lint tidy

build:
	go build -o bin/$(BINARY) ./cmd/metoer

run:
	CONFIG_PATH=$(CONFIG) go run ./cmd/metoer

test:
	go test ./...

lint:
	go vet ./...

tidy:
	go mod tidy
