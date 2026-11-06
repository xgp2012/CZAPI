#!/usr/bin/env bash
# Seed a fresh MySQL database with the CZAPI schema.
#
# The gateway does not migrate automatically by default: a schema change applied
# by a rolling process is how money tables get corrupted. Run this deliberately.
#
# Usage: ./scripts/migrate.sh "dsn" [--dry-run]
set -euo pipefail

DSN="${1:?usage: $0 \"user:pass@tcp(host:3306)/db?charset=utf8mb4\" [--dry-run]}"
DRY="${2:-}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

if ! command -v mysql >/dev/null 2>&1; then
  echo "mysql client not found. Apply migrations/0001_init.sql with any client." >&2
  exit 1
fi

# Strip the DSN down to discrete parts for the CLI client.
USERPASS="${DSN%%@tcp*}"
HOSTPART="${DSN#*@tcp(}"
HOSTPORT="${HOSTPART%%)*}"
DBPART="${HOSTPART#*)/}"
DBNAME="${DBPART%%\?*}"

USER="${USERPASS%%:*}"
PASS="${USERPASS#*:}"
HOST="${HOSTPORT%%:*}"
PORT="${HOSTPORT#*:}"

echo "== database: ${DBNAME} on ${HOST}:${PORT} as ${USER} =="

if [ "${DRY}" = "--dry-run" ]; then
  echo "== dry run: would apply the following files =="
  ls -1 migrations/*.sql
  exit 0
fi

read -r -p "This will modify ${DBNAME}. Type the database name to continue: " CONFIRM
if [ "${CONFIRM}" != "${DBNAME}" ]; then
  echo "aborted" >&2
  exit 1
fi

for f in migrations/*.sql; do
  echo "==> applying ${f}"
  MYSQL_PWD="${PASS}" mysql -h "${HOST}" -P "${PORT}" -u "${USER}" "${DBNAME}" < "${f}"
done

echo "==> schema applied. Verify with:"
echo "    SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='${DBNAME}';"
