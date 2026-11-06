# Rollback runbook
#
# The gateway is on the money path. A bad deploy must be reversible in under a
# minute, and the safest first move is almost always to stop *billing* before
# stopping *service*: a free request is recoverable, a wrong charge is not.
#
# Order matters. Do not skip to step 3 while step 1 still applies.

set -uo pipefail

BIN_DIR="${1:-/opt/czapi}"
SERVICE="${2:-czapi}"

echo "== rollback 1/4: keep serving, stop charging =="
echo
echo "   Flip the billing kill switch. This is a runtime flag, not a redeploy:"
echo "     POST /admin/v1/flags/billing_enforced   {\"value\": false}"
echo
echo "   Effect: every request is still metered and logged, but settles as"
echo "   PaidByFree, so no customer balance moves. Traffic keeps flowing."
echo "   Verify with:"
echo "     curl -s localhost:9999/admin/v1/flags -H \"Authorization: Bearer \$TOKEN\""
echo
read -r -p "   Press enter when the flag reads false, or Ctrl-C to abort. " _

echo "== rollback 2/4: restore the previous binary =="
if [ -f "${BIN_DIR}/czapi-server.prev" ]; then
  echo "   Swapping ${BIN_DIR}/czapi-server.prev -> ${BIN_DIR}/czapi-server"
  mv "${BIN_DIR}/czapi-server" "${BIN_DIR}/czapi-server.broken" 2>/dev/null || true
  mv "${BIN_DIR}/czapi-server.prev" "${BIN_DIR}/czapi-server"
  chmod +x "${BIN_DIR}/czapi-server"
  echo "   Restarting ${SERVICE} ..."
  systemctl restart "${SERVICE}" || echo "   !! restart failed; check: journalctl -u ${SERVICE} -n 100"
else
  echo "   !! ${BIN_DIR}/czapi-server.prev not found."
  echo "      The previous build was not preserved. Restore it from your artifact store"
  echo "      before continuing. Do not hand-edit the current binary."
fi
read -r -p "   Press enter when /healthz returns ok. " _

echo "== rollback 3/4: re-enable billing only after verification =="
echo
echo "   Do NOT re-enable billing until all of the following hold:"
echo "     - /healthz and /readyz are ok"
echo "     - a test request settles and appears in the ledger"
echo "     - the usage rows for the bad window have been reconciled (see below)"
echo
echo "   Then: POST /admin/v1/flags/billing_enforced   {\"value\": true}"

echo "== rollback 4/4: reconcile the bad window =="
echo
echo "   A failed deploy may have written usage rows without matching ledger"
echo "   entries (or the reverse). Compare them before announcing recovery:"
echo "     GET /admin/v1/reconcile?day=YYYY-MM-DD"
echo
echo "   The response reports three totals (raw usage, daily rollup, ledger) and"
echo "   the per-account difference. Only the ledger total affects money."
echo
echo "   Credits are idempotent on (ref_type, ref_id) and usage rows are unique on"
echo "   request_id, so replaying the same V10 push is always safe: it will not"
echo "   double-charge. Re-sending is the correct repair, not manual SQL."
echo
echo "Rollback procedure complete. Record what happened in the incident log."
