#!/usr/bin/env bash
# Import the eight realm tables from the legacy Passport database into
# Stargate. Safe to re-run; idempotent; never truncates the target.
#
# Moves: realms, realm_members, realm_labels, realm_boost_contributions,
# realm_experience_records, realm_role_permissions, realm_user_permissions,
# realm_post_moderation_logs.
#
# Usage:
#   # Read the password from the PGPASSWORD env (already set in your shell):
#   ./scripts/import-realm.sh
#
#   # Or inline:
#   PGPASSWORD=… ./scripts/import-realm.sh
#
#   # Dry run first (reads, validates, writes nothing):
#   PGDRY=1 ./scripts/import-realm.sh
#
# Flags forwarded to stargate-module-import:
#   --dry-run      read+validate only; no writes
#   --batch-size N rows per insert batch (default 500)
#
# Environment:
#   PGHOST         (default: essential01.latxa-bushi.ts.net)
#   PGPORT         (default: 5432)
#   PGUSER         (default: snchan)
#   PGPASSWORD     required; read from env, never echoed
#   PGSSLMODE      (default: disable)

set -euo pipefail

PGHOST="${PGHOST:-essential01.latxa-bushi.ts.net}"
PGPORT="${PGPORT:-5432}"
PGUSER="${PGUSER:-snchan}"
PGSSLMODE="${PGSSLMODE:-disable}"

if [ -z "${PGPASSWORD:-}" ]; then
  echo "error: PGPASSWORD is required (read from env, never echoed)" >&2
  exit 1
fi
export PGPASSWORD  # so any psql/sql subtool also picks it up

SOURCE_DB="solar_passport"
TARGET_DB="solargate"

DSN_BASE="host=${PGHOST} port=${PGPORT} user=${PGUSER} password=${PGPASSWORD} sslmode=${PGSSLMODE}"
SOURCE_DSN="${DSN_BASE} dbname=${SOURCE_DB}"
TARGET_DSN="${DSN_BASE} dbname=${TARGET_DB}"

# Drop PGPASSWORD from the env after we've built the DSNs so no child process
# that doesn't need it accidentally inherits it.
unset PGPASSWORD

cd "$(dirname "$0")/.."

# Build the importer binary once; reuse across runs.
IMPORTER_BIN="$(mktemp)"
trap 'rm -f "$IMPORTER_BIN"' EXIT
go build -o "$IMPORTER_BIN" ./cmd/stargate-module-import

echo "==> source: ${PGUSER}@${PGHOST}:${PGPORT}/${SOURCE_DB} (password from env)"
echo "==> target: ${PGUSER}@${PGHOST}:${PGPORT}/${TARGET_DB}"
echo

# Default: real import. Set PGDRY=1 for a dry run, or pass --dry-run.
ARGS=("--module" "app.solian.id.realm"
       "--source-dsn" "$SOURCE_DSN"
       "--target-dsn" "$TARGET_DSN")
if [ "${PGDRY:-0}" = "1" ]; then
  ARGS+=("--dry-run")
fi
# Forward any extra flags from the caller (e.g. --batch-size 200).
ARGS+=("$@")

exec "$IMPORTER_BIN" "${ARGS[@]}"
