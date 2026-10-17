# Stargate web console — one-word entry points.
#
#   make install   install dependencies
#   make dev       Vite dev server on :5173 (proxies /api to :8080)
#   make serve     build, then run the Go server that hosts dist/ and /api
#   make check     type-check, lint, tests, production build
#
# CONFIG points at a Stargate TOML read by the Go server from the repo root.

BUN ?= bun
CONFIG ?= config.toml

.PHONY: install dev build preview test type-check lint format check serve

install:
	$(BUN) install

dev:
	$(BUN) run dev

build:
	$(BUN) run build

preview:
	$(BUN) run preview

test:
	$(BUN) run test:unit --run

type-check:
	$(BUN) run type-check

lint:
	$(BUN) run lint

format:
	$(BUN) run format

check: type-check lint test build

# The SPA is served by Stargate itself in production (internal/webui), so the
# session cookie stays first-party. Build here, run the Go binary from the root.
serve: build
	cd .. && CONFIG_PATH=$(CONFIG) go run ./cmd/stargate
