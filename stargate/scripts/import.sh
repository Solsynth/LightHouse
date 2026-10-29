#!/usr/bin/env bash
# Import legacy Passport tables into Stargate, module by module.
#
# Safe to re-run; idempotent; never truncates the target. Run it with --dry-run
# first against a real source so you see exactly what will land before any
# write happens.
#
# Usage:
#   # Import the relationships module (default):
#   ./scripts/import.sh
#
#   # Import every module that declares an importer:
#   ./scripts/import.sh --module all
#
#   # Dry run first (reads, validates, writes nothing):
#   PGDRY=1 ./scripts/import.sh
#
#   # Override the module, pass extra flags:
#   ./scripts/import.sh --module app.solian.id.relationships --batch-size 200
#
# Environment:
#   PGHOST         (default: essential01.latxa-bushi.ts.net)
#   PGPORT         (default: 5432)
#   PGUSER         (default: snchan)
#   PGPASSWORD     required; read from env, never echoed
#   PGSSLMODE      (default: disable)
#   PGDRY=1        shortcut for --dry-run

set -euo pipefail

PGHOST="${PGHOST:-essential01.latxa-bushi.ts.net}"
PGPORT="${PGPORT:-5432}"
PGUSER="${PGUSER:-snchan}"
PGSSLMODE="${PGSSLMODE:-disable}"

if [ -z "${PGPASSWORD:-}" ]; then
  echo "error: PGPASSWORD is required (read from env, never echoed)" >&2
  exit 1
fi
export PGPASSWORD

SOURCE_DB="${SOURCE_DB:-solar_passport}"
TARGET_DB="${TARGET_DB:-solargate}"

DSN_BASE="host=${PGHOST} port=${PGPORT} user=${PGUSER} password=${PGPASSWORD} sslmode=${PGSSLMODE}"
SOURCE_DSN="${DSN_BASE} dbname=${SOURCE_DB}"
TARGET_DSN="${DSN_BASE} dbname=${TARGET_DB}"

unset PGPASSWORD

cd "$(dirname "$0")/.."

IMPORTER_BIN="$(mktemp)"
trap 'rm -f "$IMPORTER_BIN"' EXIT
go build -o "$IMPORTER_BIN" ./cmd/stargate-module-import

echo "==> source: ${PGUSER}@${PGHOST}:${PGPORT}/${SOURCE_DB}"
echo "==> target: ${PGUSER}@${PGHOST}:${PGPORT}/${TARGET_DB}"
echo

# Default module is relationships; the caller can override with --module ….
has_module=0
for arg in "$@"; do
  if [ "$arg" = "--module" ]; then has_module=1; break; fi
done
ARGS=("--source-dsn" "$SOURCE_DSN" "--target-dsn" "$TARGET_DSN")
if [ "$has_module" = "0" ]; then
  ARGS+=("--module" "app.solian.id.relationships")
fi
if [ "${PGDRY:-0}" = "1" ]; then
  ARGS+=("--dry-run")
fi
ARGS+=("$@")

exec "$IMPORTER_BIN" "${ARGS[@]}"
