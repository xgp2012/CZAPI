package mysqlstore

import (
	"strings"

	"czapi/internal/domain"
	"czapi/internal/store"
)

// This file holds every raw SQL statement used by the ledger, usage,
// subscription and API-key repositories.
//
// Each builder is a pure function returning (sql, args) so the exact text and
// argument order can be asserted by unit tests without a database. The
// database/sql methods in repos.go are deliberately thin: execute, scan, map.
//
// Placeholders are always positional '?' and args are always in the textual
// order of the placeholders.

// ---------------------------------------------------------------------------
// cz_credit_ledger (idempotent credits, docs/workstreams.md §5.1)
// ---------------------------------------------------------------------------

// creditInsertSQL builds the ledger row insert.
//
// balance_after is written as 0 here and patched with creditSetBalanceAfterSQL
// once the account row has been locked and updated, because the final balance is
// only known after the balance move.
//
// The schema carries UNIQUE KEY uk_ref (ref_type, ref_id), so a replayed voucher
// fails with errno 1062 and the whole transaction is rolled back.
func creditInsertSQL(e *domain.CreditLedgerEntry) (string, []any) {
	const q = "INSERT INTO cz_credit_ledger " +
		"(account_id, type, amount_micros, balance_after, ref_type, ref_id, remark, created_at) " +
		"VALUES (?, ?, ?, ?, ?, ?, ?, ?)"
	args := []any{
		e.AccountID,
		string(e.Type),
		e.AmountMicros,
		int64(0),
		e.RefType,
		e.RefID,
		nullString(e.Remark),
		e.CreatedAt,
	}
	return q, args
}

// creditLockAccountSQL locks the account row and reads the fields needed to
// enforce the negative-balance rule. The account id is bound by the caller.
func creditLockAccountSQL() string {
	return "SELECT balance_credit, allow_negative FROM cz_account WHERE id = ? FOR UPDATE"
}

// creditAddBalanceSQL applies a signed delta to the account balance under the
// row lock taken by creditLockAccountSQL.
func creditAddBalanceSQL() string {
	return "UPDATE cz_account SET balance_credit = balance_credit + ? WHERE id = ?"
}

// creditSetBalanceAfterSQL writes the resulting balance back onto the ledger
// row that was just inserted.
func creditSetBalanceAfterSQL() string {
	return "UPDATE cz_credit_ledger SET balance_after = ? WHERE id = ?"
}

// creditCurrentBalanceSQL reads the balance without locking; it is used on the
// replay path, after the duplicate-key rollback.
func creditCurrentBalanceSQL() string {
	return "SELECT balance_credit FROM cz_account WHERE id = ?"
}

// ledgerListSQL builds the ledger listing.
func ledgerListSQL(f store.LedgerFilter) (string, []any) {
	var b strings.Builder
	b.WriteString("SELECT id, account_id, type, amount_micros, balance_after, ref_type, ref_id, " +
		"COALESCE(remark, ''), created_at FROM cz_credit_ledger WHERE 1 = 1")
	var args []any
	if f.AccountID != 0 {
		b.WriteString(" AND account_id = ?")
		args = append(args, f.AccountID)
	}
	if f.Type != "" {
		b.WriteString(" AND type = ?")
		args = append(args, string(f.Type))
	}
	if f.RefType != "" {
		b.WriteString(" AND ref_type = ?")
		args = append(args, f.RefType)
	}
	if !f.From.IsZero() {
		b.WriteString(" AND created_at >= ?")
		args = append(args, f.From.UTC())
	}
	if !f.To.IsZero() {
		b.WriteString(" AND created_at < ?")
		args = append(args, f.To.UTC())
	}
	b.WriteString(" ORDER BY created_at DESC, id DESC")
	b.WriteString(limitClause(f.Limit, f.Offset))
	args = append(args, pagingArgs(f.Limit, f.Offset)...)
	return b.String(), args
}

// ledgerSumSQL builds the signed sum query for SumByTypeBetween.
func ledgerSumSQL(accountID uint64, t domain.LedgerType, from, to any) (string, []any) {
	var b strings.Builder
	b.WriteString("SELECT COALESCE(SUM(amount_micros), 0) FROM cz_credit_ledger WHERE account_id = ?")
	args := []any{accountID}
	if t != "" {
		b.WriteString(" AND type = ?")
		args = append(args, string(t))
	}
	b.WriteString(" AND created_at >= ? AND created_at < ?")
	args = append(args, from, to)
	return b.String(), args
}

// ---------------------------------------------------------------------------
// cz_usage_record
// ---------------------------------------------------------------------------

// usageColumns is the SELECT list for usage rows, in domain.UsageRecord order.
const usageColumns = "id, request_id, account_id, COALESCE(subscription_id, 0), api_key_id, " +
	"COALESCE(v10_host_id, 0), model, channel_id, prompt_tokens, completion_tokens, cached_tokens, " +
	"cost_micros, billing_mode, estimated, partial, shadow, stream, latency_ms, ttft_ms, " +
	"status_code, COALESCE(error_code, ''), created_at"

// usageInsertSQL builds the metering insert.
//
// The statement is ON DUPLICATE KEY UPDATE id = id so that a replayed
// request_id is a no-op whose RowsAffected is 0 (1 means a real insert), which
// is how Insert reports created=false.
//
// Idempotency note: cz_usage_record is partitioned by created_at, so the unique
// key is (request_id, created_at). Callers must therefore pass the *original*
// created_at when replaying an event; the worker preserves the event timestamp
// for exactly this reason.
func usageInsertSQL(r *domain.UsageRecord) (string, []any) {
	const q = "INSERT INTO cz_usage_record " +
		"(request_id, account_id, subscription_id, api_key_id, v10_host_id, model, channel_id, " +
		"prompt_tokens, completion_tokens, cached_tokens, cost_micros, billing_mode, estimated, " +
		"partial, shadow, stream, latency_ms, ttft_ms, status_code, error_code, created_at) " +
		"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) " +
		"ON DUPLICATE KEY UPDATE id = id"
	args := []any{
		r.RequestID,
		r.AccountID,
		nilIfZero(r.SubscriptionID),
		r.APIKeyID,
		nilIfZero(r.V10HostID),
		r.Model,
		r.ChannelID,
		r.PromptTokens,
		r.CompletionTokens,
		r.CachedTokens,
		r.CostMicros,
		int8(r.BillingMode),
		boolInt(r.Estimated),
		boolInt(r.Partial),
		boolInt(r.Shadow),
		boolInt(r.Stream),
		r.LatencyMS,
		r.TTFTMS,
		r.StatusCode,
		nullString(r.ErrorCode),
		r.CreatedAt,
	}
	return q, args
}

// usageFilterWhere builds the WHERE clause (including the leading " WHERE") and
// its arguments for a usage query.
//
// Rules, matching the memstore reference implementation:
//
//   - zero values mean "no constraint";
//   - From is inclusive, To is exclusive;
//   - shadow rows are excluded unless IncludeShadow is set;
//   - SiteID is matched through the owning account, because a usage row has no
//     site column of its own.
func usageFilterWhere(f store.UsageFilter) (string, []any) {
	var b strings.Builder
	b.WriteString(" WHERE 1 = 1")
	var args []any
	if !f.IncludeShadow {
		b.WriteString(" AND shadow = 0")
	}
	if f.SiteID != "" {
		b.WriteString(" AND account_id IN (SELECT id FROM cz_account WHERE v10_site_id = ?)")
		args = append(args, f.SiteID)
	}
	if f.AccountID != 0 {
		b.WriteString(" AND account_id = ?")
		args = append(args, f.AccountID)
	}
	if f.V10HostID != 0 {
		b.WriteString(" AND v10_host_id = ?")
		args = append(args, f.V10HostID)
	}
	if f.APIKeyID != 0 {
		b.WriteString(" AND api_key_id = ?")
		args = append(args, f.APIKeyID)
	}
	if f.Model != "" {
		b.WriteString(" AND model = ?")
		args = append(args, f.Model)
	}
	if !f.From.IsZero() {
		b.WriteString(" AND created_at >= ?")
		args = append(args, f.From.UTC())
	}
	if !f.To.IsZero() {
		b.WriteString(" AND created_at < ?")
		args = append(args, f.To.UTC())
	}
	return b.String(), args
}

// usageListSQL builds the paginated usage listing, newest first.
func usageListSQL(f store.UsageFilter) (string, []any) {
	where, args := usageFilterWhere(f)
	var b strings.Builder
	b.WriteString("SELECT " + usageColumns + " FROM cz_usage_record")
	b.WriteString(where)
	b.WriteString(" ORDER BY created_at DESC, id DESC")
	b.WriteString(limitClause(f.Limit, f.Offset))
	args = append(args, pagingArgs(f.Limit, f.Offset)...)
	return b.String(), args
}

// usageSummarySQL builds the aggregate used by /internal/v1/usage/summary.
func usageSummarySQL(f store.UsageFilter) (string, []any) {
	where, args := usageFilterWhere(f)
	const q = "SELECT COUNT(*), COALESCE(SUM(prompt_tokens), 0), " +
		"COALESCE(SUM(completion_tokens), 0), COALESCE(SUM(cost_micros), 0) " +
		"FROM cz_usage_record"
	return q + where, args
}

// usageCostSumSQL builds a SUM(cost_micros) query over a WHERE clause.
func usageCostSumSQL(where string, args []any) (string, []any) {
	return "SELECT COALESCE(SUM(cost_micros), 0) FROM cz_usage_record" + where, args
}

// usageWindowWhere builds the WHERE clause used by the three spend-cap queries.
// Shadow rows never count towards a cap.
func usageWindowWhere(column string, id uint64) (string, []any) {
	var b strings.Builder
	b.WriteString(" WHERE shadow = 0 AND created_at >= ? AND created_at < ?")
	var args []any
	if column != "" && id != 0 {
		b.WriteString(" AND " + column + " = ?")
		args = append(args, id)
	}
	// The timestamps are appended by the caller in From, To order.
	return b.String(), args
}

// ---------------------------------------------------------------------------
// cz_subscription
// ---------------------------------------------------------------------------

// subscriptionColumns is the SELECT list for subscriptions.
const subscriptionColumns = "id, account_id, v10_host_id, plan_id, period_start, period_end, " +
	"quota_total, quota_used, billing_mode, status, created_at, updated_at"

// subscriptionInsertSQL builds the idempotent subscription insert.
//
// uk_host makes v10_host_id unique, so ON DUPLICATE KEY UPDATE id = id turns a
// replay into a no-op with RowsAffected == 0, which Create maps to
// created=false after re-reading the stored row.
func subscriptionInsertSQL(v *domain.Subscription) (string, []any) {
	const q = "INSERT INTO cz_subscription " +
		"(account_id, v10_host_id, plan_id, period_start, period_end, quota_total, quota_used, " +
		"billing_mode, status, created_at, updated_at) " +
		"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) " +
		"ON DUPLICATE KEY UPDATE id = id"
	args := []any{
		v.AccountID,
		v.V10HostID,
		v.PlanID,
		v.PeriodStart,
		v.PeriodEnd,
		v.QuotaTotal,
		v.QuotaUsed,
		string(v.BillingMode),
		string(v.Status),
		v.CreatedAt,
		v.UpdatedAt,
	}
	return q, args
}

// subscriptionByIDSQL selects one subscription by primary key.
func subscriptionByIDSQL() string {
	return "SELECT " + subscriptionColumns + " FROM cz_subscription WHERE id = ?"
}

// subscriptionByHostSQL selects one subscription by its V10 host id.
func subscriptionByHostSQL() string {
	return "SELECT " + subscriptionColumns + " FROM cz_subscription WHERE v10_host_id = ?"
}

// subscriptionListByAccountSQL lists an account's subscriptions, soonest expiry
// first.
func subscriptionListByAccountSQL() string {
	return "SELECT " + subscriptionColumns + " FROM cz_subscription " +
		"WHERE account_id = ? ORDER BY period_end ASC, id ASC"
}

// subscriptionUpdateSQL replaces the mutable columns of a subscription.
func subscriptionUpdateSQL() string {
	return "UPDATE cz_subscription SET account_id = ?, v10_host_id = ?, plan_id = ?, " +
		"period_start = ?, period_end = ?, quota_total = ?, quota_used = ?, billing_mode = ?, " +
		"status = ? WHERE id = ?"
}

// subscriptionUpdateArgs returns the argument list matching subscriptionUpdateSQL.
func subscriptionUpdateArgs(v *domain.Subscription) []any {
	return []any{
		v.AccountID,
		v.V10HostID,
		v.PlanID,
		v.PeriodStart,
		v.PeriodEnd,
		v.QuotaTotal,
		v.QuotaUsed,
		string(v.BillingMode),
		string(v.Status),
		v.ID,
	}
}

// subscriptionAddUsageSQL increments the consumed quota in one statement.
func subscriptionAddUsageSQL() string {
	return "UPDATE cz_subscription SET quota_used = quota_used + ?, updated_at = ? WHERE id = ?"
}

// subscriptionQuotaUsedSQL reads the value written by subscriptionAddUsageSQL.
func subscriptionQuotaUsedSQL() string {
	return "SELECT quota_used FROM cz_subscription WHERE id = ?"
}

// subscriptionResetPeriodSQL starts a new billing period and clears usage.
func subscriptionResetPeriodSQL() string {
	return "UPDATE cz_subscription SET period_start = ?, period_end = ?, quota_total = ?, " +
		"quota_used = 0, updated_at = ? WHERE id = ?"
}

// subscriptionExpiredSQL lists active subscriptions whose period has ended.
func subscriptionExpiredSQL() string {
	return "SELECT " + subscriptionColumns + " FROM cz_subscription " +
		"WHERE status = 'active' AND period_end <= ? ORDER BY period_end ASC, id ASC"
}

// ---------------------------------------------------------------------------
// cz_api_key
// ---------------------------------------------------------------------------

// keyColumns is the SELECT list for API keys.
const keyColumns = "id, account_id, COALESCE(subscription_id, 0), key_hash, key_prefix, name, " +
	"models, ip_whitelist, expires_at, status, last_used_at, created_at, updated_at"

// keyInsertSQL inserts a new API key.
func keyInsertSQL() string {
	return "INSERT INTO cz_api_key (account_id, subscription_id, key_hash, key_prefix, name, " +
		"models, ip_whitelist, expires_at, status, last_used_at, created_at, updated_at) " +
		"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
}

// keyInsertArgs returns the argument list matching keyInsertSQL.
func keyInsertArgs(k *domain.APIKey) ([]any, error) {
	models, err := marshalJSONSlice(k.Models)
	if err != nil {
		return nil, err
	}
	ips, err := marshalJSONSlice(k.IPWhitelist)
	if err != nil {
		return nil, err
	}
	return []any{
		k.AccountID,
		nilIfZero(k.SubscriptionID),
		k.KeyHash,
		k.KeyPrefix,
		k.Name,
		models,
		ips,
		toNullTime(k.ExpiresAt),
		string(k.Status),
		toNullTime(k.LastUsedAt),
		k.CreatedAt,
		k.UpdatedAt,
	}, nil
}

// keyByIDSQL selects one key by primary key.
func keyByIDSQL() string {
	return "SELECT " + keyColumns + " FROM cz_api_key WHERE id = ?"
}

// keyByHashSQL selects one key by its lookup hash.
func keyByHashSQL() string {
	return "SELECT " + keyColumns + " FROM cz_api_key WHERE key_hash = ?"
}

// keyListByAccountSQL lists an account's keys, newest creation first.
func keyListByAccountSQL() string {
	return "SELECT " + keyColumns + " FROM cz_api_key WHERE account_id = ? " +
		"ORDER BY created_at DESC, id DESC"
}

// keyListBySubscriptionSQL lists the keys bound to a subscription.
func keyListBySubscriptionSQL() string {
	return "SELECT " + keyColumns + " FROM cz_api_key WHERE subscription_id = ? " +
		"ORDER BY created_at DESC, id DESC"
}

// keyUpdateSQL replaces the mutable columns of a key.
func keyUpdateSQL() string {
	return "UPDATE cz_api_key SET account_id = ?, subscription_id = ?, key_hash = ?, " +
		"key_prefix = ?, name = ?, models = ?, ip_whitelist = ?, expires_at = ?, status = ? " +
		"WHERE id = ?"
}

// keyUpdateArgs returns the argument list matching keyUpdateSQL.
func keyUpdateArgs(k *domain.APIKey) ([]any, error) {
	models, err := marshalJSONSlice(k.Models)
	if err != nil {
		return nil, err
	}
	ips, err := marshalJSONSlice(k.IPWhitelist)
	if err != nil {
		return nil, err
	}
	return []any{
		k.AccountID,
		nilIfZero(k.SubscriptionID),
		k.KeyHash,
		k.KeyPrefix,
		k.Name,
		models,
		ips,
		toNullTime(k.ExpiresAt),
		string(k.Status),
		k.ID,
	}, nil
}

// keyCountBySubscriptionSQL counts a subscription's non-revoked keys.
func keyCountBySubscriptionSQL() string {
	return "SELECT COUNT(*) FROM cz_api_key WHERE subscription_id = ? AND status <> 'revoked'"
}

// keySetStatusBySubscriptionSQL builds the bulk status flip.
//
// Already-revoked keys are never resurrected, which is why the guard is part of
// the statement rather than a loop in Go: RowsAffected is then exactly the
// number of keys actually flipped.
func keySetStatusBySubscriptionSQL() string {
	return "UPDATE cz_api_key SET status = ?, updated_at = ? " +
		"WHERE subscription_id = ? AND status <> 'revoked'"
}

// keyTouchLastUsedSQL records the last usage timestamp of a key.
func keyTouchLastUsedSQL() string {
	return "UPDATE cz_api_key SET last_used_at = ? WHERE id = ?"
}

// keyDeleteSQL removes a key.
func keyDeleteSQL() string {
	return "DELETE FROM cz_api_key WHERE id = ?"
}

// ---------------------------------------------------------------------------
// Paging helpers
// ---------------------------------------------------------------------------

// noLimit is MySQL's documented "unbounded" LIMIT, used when an offset is
// requested without a limit. MySQL has no standalone OFFSET clause.
const noLimit = "18446744073709551615"

// limitClause renders the paging suffix for a list query.
//
// Rules:
//   - Limit <= 0 and Offset <= 0: no clause at all (docs/workstreams.md: a zero
//     limit means "no LIMIT clause").
//   - Limit > 0, Offset == 0: " LIMIT ?".
//   - Limit > 0, Offset > 0: " LIMIT ? OFFSET ?".
//   - Limit <= 0, Offset > 0: " LIMIT <max> OFFSET ?", because MySQL cannot skip
//     rows without a LIMIT; the cap is far above any reachable row count.
func limitClause(limit, offset int) string {
	switch {
	case limit <= 0 && offset <= 0:
		return ""
	case limit <= 0:
		return " LIMIT " + noLimit + " OFFSET ?"
	case offset <= 0:
		return " LIMIT ?"
	default:
		return " LIMIT ? OFFSET ?"
	}
}

// pagingArgs returns the placeholders' values for limitClause.
func pagingArgs(limit, offset int) []any {
	switch {
	case limit <= 0 && offset <= 0:
		return nil
	case limit <= 0:
		if offset < 0 {
			offset = 0
		}
		return []any{offset}
	case offset <= 0:
		return []any{limit}
	default:
		return []any{limit, offset}
	}
}
