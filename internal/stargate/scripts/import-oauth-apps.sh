#!/usr/bin/env bash
# Import the OAuth clients from the Develop service into Stargate.
#
# Moves custom_apps and custom_app_secrets into Stargate's oauth_apps and
# oauth_app_secrets, so the OAuth/OIDC provider keeps working once Develop is
# no longer deployed. Only the OAuth-relevant subset moves: board widgets,
# payment wallets, mini apps, products and bot accounts stay in Develop.
#
# Safe to re-run; idempotent; never truncates the target. Run it with PGDRY=1
# first against a real source so you see exactly what will land before any
# write happens.
#
# Usage:
#   # Import OAuth clients (default):
#   ./scripts/import-oauth-apps.sh
#
#   # Dry run first (reads, validates, writes nothing):
#   PGDRY=1 ./scripts/import-oauth-apps.sh
#
#   # Override the databases or pass extra flags:
#   SOURCE_DB=develop_prod TARGET_DB=stargate_prod \
#     ./scripts/import-oauth-apps.sh --batch-size 200
#
# Expect skipped rows and read them:
#   * an app whose slug is already taken in the target (importing it would
#     make the provider's slug lookup ambiguous) is skipped;
#   * an app with no project_id cannot satisfy the target's one-owner
#     constraint and is skipped;
#   * a secret whose app was skipped is skipped with it.
# Skipped rows are counted in the summary and recorded per table. They are
# never overwritten or guessed at.
#
# Environment:
#   PGHOST         (default: essential01.latxa-bushi.ts.net)
#   PGPORT         (default: 5432)
#   PGUSER         (default: snchan)
#   PGPASSWORD     required; read from env, never echoed
#   PGSSLMODE      (default: disable)
#   SOURCE_DB      (default: solar_develop)
#   TARGET_DB      (default: solargate)
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

SOURCE_DB="${SOURCE_DB:-solar_develop}"
TARGET_DB="${TARGET_DB:-solargate}"

DSN_BASE="host=${PGHOST} port=${PGPORT} user=${PGUSER} password=${PGPASSWORD} sslmode=${PGSSLMODE}"
SOURCE_DSN="${DSN_BASE} dbname=${SOURCE_DB}"
TARGET_DSN="${DSN_BASE} dbname=${TARGET_DB}"

# Drop PGPASSWORD from the env once the DSNs are built, so no child process
# inherits it by accident.
unset PGPASSWORD

cd "$(dirname "$0")/.."

IMPORTER_BIN="$(mktemp)"
trap 'rm -f "$IMPORTER_BIN"' EXIT
go build -o "$IMPORTER_BIN" ./cmd/stargate-module-import

echo "==> source: ${PGUSER}@${PGHOST}:${PGPORT}/${SOURCE_DB} (Develop, password from env)"
echo "==> target: ${PGUSER}@${PGHOST}:${PGPORT}/${TARGET_DB} (Stargate)"
echo

ARGS=("--module" "app.solian.id.oauthapps"
       "--source-dsn" "$SOURCE_DSN"
       "--target-dsn" "$TARGET_DSN")
if [ "${PGDRY:-0}" = "1" ]; then
  ARGS+=("--dry-run")
fi
# Forward any extra flags from the caller (e.g. --batch-size 200).
ARGS+=("$@")

exec "$IMPORTER_BIN" "${ARGS[@]}"