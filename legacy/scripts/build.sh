#!/usr/bin/env bash
# Build the CZAPI gateway for Linux with the console bundled in.
#
# Produces a single self-contained binary: the Vue console is served from the
# embedded path, so deployment is "copy one file and a config".
#
# Usage: ./scripts/build.sh [version]
set -euo pipefail

VERSION="${1:-dev}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${ROOT}/dist"

cd "${ROOT}"

echo "==> building web console"
if [ -d web ]; then
  if command -v pnpm >/dev/null 2>&1; then
    ( cd web && pnpm install --frozen-lockfile && pnpm build )
  else
    echo "    pnpm not found; skipping console build (the gateway will serve a notice at /console)"
  fi
else
  echo "    web/ not present; skipping console build"
fi

echo "==> compiling server"
mkdir -p "${OUT}"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o "${OUT}/czapi-server" ./cmd/server

echo "==> done: ${OUT}/czapi-server (version ${VERSION})"
