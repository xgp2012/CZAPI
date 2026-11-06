#!/usr/bin/env bash
# Pre-flight checks. Run this before every deploy and after every config change.
#
# It answers one question: "is it safe to send production traffic to this build?"
# Anything other than all-PASS means do not deploy.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

FAIL=0
pass() { printf '  \033[32mPASS\033[0m  %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m  %s\n' "$1"; FAIL=$((FAIL + 1)); }
info() { printf '  ....  %s\n' "$1"; }

echo "== 1. formatting =="
UNFMT="$(gofmt -l internal cmd 2>/dev/null || true)"
if [ -z "${UNFMT}" ]; then pass "gofmt clean"; else fail "gofmt needed: ${UNFMT}"; fi

echo "== 2. vet =="
if go vet ./... >/dev/null 2>&1; then pass "go vet"; else fail "go vet reported issues"; fi

echo "== 3. build =="
if go build ./... >/dev/null 2>&1; then pass "go build"; else fail "go build failed"; fi

echo "== 4. tests =="
if go test -timeout 300s -count=1 ./... >/dev/null 2>&1; then
  pass "all packages"
else
  fail "test failures (run: go test -count=1 ./...)"
fi

echo "== 5. money-path tests specifically =="
for pkg in ./internal/billing/... ./internal/quota/... ./internal/test/e2e/... ./test/e2e/...; do
  [ -d "${pkg}" ] || continue
  if go test -timeout 300s -count=1 "${pkg}" >/dev/null 2>&1; then
    pass "${pkg}"
  else
    fail "${pkg}"
  fi
done

echo "== 6. PHP plugin syntax =="
if command -v php >/dev/null 2>&1 && [ -d v10 ]; then
  BAD=0
  while IFS= read -r f; do
    php -l "${f}" >/dev/null 2>&1 || { fail "php -l ${f}"; BAD=1; }
  done < <(find v10 -name '*.php')
  [ "${BAD}" -eq 0 ] && pass "all v10/*.php"
else
  info "php CLI or v10/ absent; skipped"
fi

echo "== 7. console build =="
if [ -f internal/api/console/dist/index.html ]; then
  pass "console dist present"
else
  info "console dist missing; /console will serve a JSON notice (not fatal)"
fi

echo "== 8. production config hazards =="
CFG="${1:-}"
if [ -n "${CFG}" ] && [ -f "${CFG}" ]; then
  grep -q 'internal_allow_no_app: *true' "${CFG}" \
    && fail "internal_allow_no_app: true (removes /internal/v1 authentication)" \
    || pass "internal signature enforced"
  grep -q 'key_hash_salt: *""' "${CFG}" \
    && fail "key_hash_salt is empty (unsalted key hashes)" \
    || pass "key_hash_salt set"
  grep -q 'admin_jwt_secret: *""' "${CFG}" \
    && fail "admin_jwt_secret is empty" \
    || pass "admin_jwt_secret set"
else
  info "no config file passed; skipped (usage: $0 /path/to/config.yaml)"
fi

echo
if [ "${FAIL}" -eq 0 ]; then
  echo "RESULT: all checks passed"
  exit 0
fi
echo "RESULT: ${FAIL} check(s) failed -- DO NOT DEPLOY"
exit 1
