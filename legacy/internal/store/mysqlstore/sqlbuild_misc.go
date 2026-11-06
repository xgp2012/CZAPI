package mysqlstore

import (
	"strings"
	"time"

	"czapi/internal/domain"
	"czapi/internal/store"
)

// This file holds the SQL builders for the administrative and catalogue tables:
// sites, accounts, plans, channels, channel health, models, admins, audit logs,
// feature flags and daily rollups.

// ---------------------------------------------------------------------------
// cz_site
// ---------------------------------------------------------------------------

// siteColumns is the SELECT list for sites.
const siteColumns = "id, app_id, name, secret, COALESCE(secret_prev, ''), ip_allow, " +
	"enabled, created_at, updated_at"

// siteInsertSQL inserts a V10 installation registration.
func siteInsertSQL() string {
	return "INSERT INTO cz_site (app_id, name, secret, secret_prev, ip_allow, enabled, " +
		"created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)"
}

// siteInsertArgs returns the argument list matching siteInsertSQL.
func siteInsertArgs(s *domain.Site) ([]any, error) {
	ips, err := marshalJSONSlice(s.IPAllow)
	if err != nil {
		return nil, err
	}
	return []any{
		s.AppID,
		s.Name,
		s.Secret,
		nullString(s.SecretPrev),
		ips,
		boolInt(s.Enabled),
		s.CreatedAt,
		s.UpdatedAt,
	}, nil
}

// siteUpdateSQL replaces the mutable columns of a site.
func siteUpdateSQL() string {
	return "UPDATE cz_site SET app_id = ?, name = ?, secret = ?, secret_prev = ?, " +
		"ip_allow = ?, enabled = ? WHERE id = ?"
}

// siteUpdateArgs returns the argument list matching siteUpdateSQL.
func siteUpdateArgs(s *domain.Site) ([]any, error) {
	ips, err := marshalJSONSlice(s.IPAllow)
	if err != nil {
		return nil, err
	}
	return []any{
		s.AppID,
		s.Name,
		s.Secret,
		nullString(s.SecretPrev),
		ips,
		boolInt(s.Enabled),
		s.ID,
	}, nil
}

// siteByAppIDSQL selects one site by its app id.
func siteByAppIDSQL() string {
	return "SELECT " + siteColumns + " FROM cz_site WHERE app_id = ?"
}

// siteListSQL lists sites in creation order.
func siteListSQL() string {
	return "SELECT " + siteColumns + " FROM cz_site ORDER BY id ASC"
}

// ---------------------------------------------------------------------------
// cz_account
// ---------------------------------------------------------------------------

// accountColumns is the SELECT list for accounts.
const accountColumns = "id, v10_site_id, v10_client_id, COALESCE(email, ''), status, " +
	"balance_credit, allow_negative, created_at, updated_at"

// accountUpsertSQL creates or refreshes the tenant row identified by
// (v10_site_id, v10_client_id) -the uk_site_client unique key.
//
// The balance is deliberately left untouched on update: a V10 profile sync must
// never move money, only the ledger may do that.
func accountUpsertSQL() string {
	return "INSERT INTO cz_account (v10_site_id, v10_client_id, email, status, " +
		"balance_credit, allow_negative, created_at, updated_at) " +
		"VALUES (?, ?, ?, ?, ?, ?, ?, ?) " +
		"ON DUPLICATE KEY UPDATE email = VALUES(email), status = VALUES(status), " +
		"allow_negative = VALUES(allow_negative), updated_at = VALUES(updated_at)"
}

// accountUpsertArgs returns the argument list matching accountUpsertSQL.
func accountUpsertArgs(a *domain.Account) []any {
	return []any{
		a.SiteID,
		a.V10ClientID,
		nullString(a.Email),
		int8(a.Status),
		a.BalanceCredit,
		boolInt(a.AllowNegative),
		a.CreatedAt,
		a.UpdatedAt,
	}
}

// accountByIDSQL selects one account by primary key.
func accountByIDSQL() string {
	return "SELECT " + accountColumns + " FROM cz_account WHERE id = ?"
}

// accountByV10ClientSQL selects one account by its V10 identity.
func accountByV10ClientSQL() string {
	return "SELECT " + accountColumns + " FROM cz_account " +
		"WHERE v10_site_id = ? AND v10_client_id = ? LIMIT 1"
}

// accountUpdateSQL replaces the mutable columns of an account.
//
// Note that balance_credit is included: the ledger writes it through its own
// locked path, but the admin console also performs explicit adjustments, so the
// column stays writable here.
func accountUpdateSQL() string {
	return "UPDATE cz_account SET v10_site_id = ?, v10_client_id = ?, email = ?, status = ?, " +
		"balance_credit = ?, allow_negative = ? WHERE id = ?"
}

// accountUpdateArgs returns the argument list matching accountUpdateSQL.
func accountUpdateArgs(a *domain.Account) []any {
	return []any{
		a.SiteID,
		a.V10ClientID,
		nullString(a.Email),
		int8(a.Status),
		a.BalanceCredit,
		boolInt(a.AllowNegative),
		a.ID,
	}
}

// accountListSQL pages through accounts in creation order.
func accountListSQL(limit, offset int) (string, []any) {
	q := "SELECT " + accountColumns + " FROM cz_account ORDER BY id ASC" + limitClause(limit, offset)
	return q, pagingArgs(limit, offset)
}

// accountCountSQL counts accounts.
func accountCountSQL() string { return "SELECT COUNT(*) FROM cz_account" }

// ---------------------------------------------------------------------------
// cz_plan
// ---------------------------------------------------------------------------

// planColumns is the SELECT list for plans.
const planColumns = "id, plan_code, name, quota_tokens, period, rpm_limit, tpm_limit, " +
	"concurrency, model_group, max_keys, price_cents, group_ratio, status, created_at, updated_at"

// planUpsertSQL creates or refreshes a plan identified by plan_code (uk_plan_code).
func planUpsertSQL() string {
	return "INSERT INTO cz_plan (plan_code, name, quota_tokens, period, rpm_limit, tpm_limit, " +
		"concurrency, model_group, max_keys, price_cents, group_ratio, status, created_at, updated_at) " +
		"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) " +
		"ON DUPLICATE KEY UPDATE name = VALUES(name), quota_tokens = VALUES(quota_tokens), " +
		"period = VALUES(period), rpm_limit = VALUES(rpm_limit), tpm_limit = VALUES(tpm_limit), " +
		"concurrency = VALUES(concurrency), model_group = VALUES(model_group), " +
		"max_keys = VALUES(max_keys), price_cents = VALUES(price_cents), " +
		"group_ratio = VALUES(group_ratio), status = VALUES(status), updated_at = VALUES(updated_at)"
}

// planUpsertArgs returns the argument list matching planUpsertSQL.
func planUpsertArgs(p *domain.Plan) []any {
	return []any{
		p.PlanCode,
		p.Name,
		p.QuotaTokens,
		p.Period,
		p.RPMLimit,
		p.TPMLimit,
		p.Concurrency,
		p.ModelGroup,
		p.MaxKeys,
		p.PriceCents,
		p.GroupRatio,
		p.Status,
		p.CreatedAt,
		p.UpdatedAt,
	}
}

// planByIDSQL selects one plan by primary key.
func planByIDSQL() string {
	return "SELECT " + planColumns + " FROM cz_plan WHERE id = ?"
}

// planByCodeSQL selects one plan by its code.
func planByCodeSQL() string {
	return "SELECT " + planColumns + " FROM cz_plan WHERE plan_code = ?"
}

// planListSQL lists plans in creation order.
func planListSQL() string {
	return "SELECT " + planColumns + " FROM cz_plan ORDER BY id ASC"
}

// ---------------------------------------------------------------------------
// cz_channel / cz_channel_health
// ---------------------------------------------------------------------------

// channelColumns is the SELECT list for channels.
const channelColumns = "id, name, provider, base_url, key_pool, models, model_map, weight, " +
	"priority, max_concurrency, timeout_ms, status, created_at, updated_at"

// channelInsertSQL inserts an upstream channel.
func channelInsertSQL() string {
	return "INSERT INTO cz_channel (name, provider, base_url, key_pool, models, model_map, " +
		"weight, priority, max_concurrency, timeout_ms, status, created_at, updated_at) " +
		"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
}

// channelInsertArgs returns the argument list matching channelInsertSQL.
func channelInsertArgs(c *domain.Channel) ([]any, error) {
	pool, err := marshalJSONAny(c.KeyPool)
	if err != nil {
		return nil, err
	}
	models, err := marshalJSONSlice(c.Models)
	if err != nil {
		return nil, err
	}
	modelMap, err := marshalJSONMap(c.ModelMap)
	if err != nil {
		return nil, err
	}
	return []any{
		c.Name,
		c.Provider,
		c.BaseURL,
		pool,
		models,
		modelMap,
		c.Weight,
		c.Priority,
		c.MaxConcurrency,
		c.TimeoutMS,
		c.Status,
		c.CreatedAt,
		c.UpdatedAt,
	}, nil
}

// channelUpdateSQL replaces the mutable columns of a channel.
func channelUpdateSQL() string {
	return "UPDATE cz_channel SET name = ?, provider = ?, base_url = ?, key_pool = ?, " +
		"models = ?, model_map = ?, weight = ?, priority = ?, max_concurrency = ?, " +
		"timeout_ms = ?, status = ? WHERE id = ?"
}

// channelUpdateArgs returns the argument list matching channelUpdateSQL.
func channelUpdateArgs(c *domain.Channel) ([]any, error) {
	pool, err := marshalJSONAny(c.KeyPool)
	if err != nil {
		return nil, err
	}
	models, err := marshalJSONSlice(c.Models)
	if err != nil {
		return nil, err
	}
	modelMap, err := marshalJSONMap(c.ModelMap)
	if err != nil {
		return nil, err
	}
	return []any{
		c.Name,
		c.Provider,
		c.BaseURL,
		pool,
		models,
		modelMap,
		c.Weight,
		c.Priority,
		c.MaxConcurrency,
		c.TimeoutMS,
		c.Status,
		c.ID,
	}, nil
}

// channelByIDSQL selects one channel by primary key.
func channelByIDSQL() string {
	return "SELECT " + channelColumns + " FROM cz_channel WHERE id = ?"
}

// channelListSQL lists every channel in creation order.
func channelListSQL() string {
	return "SELECT " + channelColumns + " FROM cz_channel ORDER BY id ASC"
}

// channelListByModelSQL lists enabled channels serving one external model.
//
// The models column is a JSON array, so membership is tested with
// JSON_CONTAINS rather than a LIKE scan (which would match substrings).
func channelListByModelSQL() string {
	return "SELECT " + channelColumns + " FROM cz_channel " +
		"WHERE status = 1 AND JSON_CONTAINS(models, JSON_QUOTE(?)) " +
		"ORDER BY priority DESC, id ASC"
}

// channelDeleteSQL removes a channel.
func channelDeleteSQL() string {
	return "DELETE FROM cz_channel WHERE id = ?"
}

// channelHealthColumns is the SELECT list for breaker state.
const channelHealthColumns = "channel_id, open_until, half_open, error_rate, samples, updated_at"

// healthGetSQL selects a channel's breaker state.
func healthGetSQL() string {
	return "SELECT " + channelHealthColumns + " FROM cz_channel_health WHERE channel_id = ?"
}

// healthUpsertSQL writes a channel's breaker state.
func healthUpsertSQL() string {
	return "INSERT INTO cz_channel_health (channel_id, open_until, half_open, error_rate, " +
		"samples, updated_at) VALUES (?, ?, ?, ?, ?, ?) " +
		"ON DUPLICATE KEY UPDATE open_until = VALUES(open_until), half_open = VALUES(half_open), " +
		"error_rate = VALUES(error_rate), samples = VALUES(samples), updated_at = VALUES(updated_at)"
}

// healthUpsertArgs returns the argument list matching healthUpsertSQL.
func healthUpsertArgs(h *domain.ChannelHealth) []any {
	return []any{
		h.ChannelID,
		toNullTime(&h.OpenUntil),
		boolInt(h.HalfOpen),
		h.ErrorRate,
		h.Samples,
		h.UpdatedAt,
	}
}

// ---------------------------------------------------------------------------
// cz_model
// ---------------------------------------------------------------------------

// modelColumns is the SELECT list for the model catalogue.
const modelColumns = "id, model_name, display_name, group_name, context_len, model_ratio, " +
	"completion_ratio, cached_ratio, input_micros, output_micros, capabilities, status, " +
	"created_at, updated_at"

// modelUpsertSQL creates or refreshes a catalogue entry identified by its
// model_name (uk_model).
func modelUpsertSQL() string {
	return "INSERT INTO cz_model (model_name, display_name, group_name, context_len, " +
		"model_ratio, completion_ratio, cached_ratio, input_micros, output_micros, capabilities, " +
		"status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) " +
		"ON DUPLICATE KEY UPDATE display_name = VALUES(display_name), " +
		"group_name = VALUES(group_name), context_len = VALUES(context_len), " +
		"model_ratio = VALUES(model_ratio), completion_ratio = VALUES(completion_ratio), " +
		"cached_ratio = VALUES(cached_ratio), input_micros = VALUES(input_micros), " +
		"output_micros = VALUES(output_micros), capabilities = VALUES(capabilities), " +
		"status = VALUES(status), updated_at = VALUES(updated_at)"
}

// modelUpsertArgs returns the argument list matching modelUpsertSQL.
func modelUpsertArgs(m *domain.Model) ([]any, error) {
	caps, err := marshalJSONSlice(m.Capabilities)
	if err != nil {
		return nil, err
	}
	return []any{
		m.ModelName,
		m.DisplayName,
		m.GroupName,
		m.ContextLen,
		m.ModelRatio,
		m.CompletionRatio,
		m.CachedRatio,
		m.InputMicros,
		m.OutputMicros,
		caps,
		m.Status,
		m.CreatedAt,
		m.UpdatedAt,
	}, nil
}

// modelByNameSQL selects one catalogue entry by its model name.
func modelByNameSQL() string {
	return "SELECT " + modelColumns + " FROM cz_model WHERE model_name = ?"
}

// modelListSQL lists the whole catalogue in creation order.
func modelListSQL() string {
	return "SELECT " + modelColumns + " FROM cz_model ORDER BY id ASC"
}

// modelListByGroupSQL lists a group's models. An empty or "default" group means
// "every model", matching the memstore reference.
func modelListByGroupSQL(group string) (string, []any) {
	if group == "" || group == "default" {
		return modelListSQL(), nil
	}
	return "SELECT " + modelColumns + " FROM cz_model WHERE group_name = ? ORDER BY id ASC",
		[]any{group}
}

// modelDeleteSQL removes a catalogue entry.
func modelDeleteSQL() string {
	return "DELETE FROM cz_model WHERE model_name = ?"
}

// ---------------------------------------------------------------------------
// cz_admin_user
// ---------------------------------------------------------------------------

// adminColumns is the SELECT list for console operators.
const adminColumns = "id, username, password_hash, role, status, created_at, updated_at"

// adminInsertSQL inserts a console operator.
func adminInsertSQL() string {
	return "INSERT INTO cz_admin_user (username, password_hash, role, status, created_at, " +
		"updated_at) VALUES (?, ?, ?, ?, ?, ?)"
}

// adminInsertArgs returns the argument list matching adminInsertSQL.
func adminInsertArgs(a *domain.AdminUser) []any {
	return []any{a.Username, a.PasswordHash, a.Role, a.Status, a.CreatedAt, a.UpdatedAt}
}

// adminUpdateSQL replaces the mutable columns of a console operator.
func adminUpdateSQL() string {
	return "UPDATE cz_admin_user SET username = ?, password_hash = ?, role = ?, status = ? " +
		"WHERE id = ?"
}

// adminUpdateArgs returns the argument list matching adminUpdateSQL.
func adminUpdateArgs(a *domain.AdminUser) []any {
	return []any{a.Username, a.PasswordHash, a.Role, a.Status, a.ID}
}

// adminByIDSQL selects one operator by primary key.
func adminByIDSQL() string {
	return "SELECT " + adminColumns + " FROM cz_admin_user WHERE id = ?"
}

// adminByUsernameSQL selects one operator by username.
func adminByUsernameSQL() string {
	return "SELECT " + adminColumns + " FROM cz_admin_user WHERE username = ?"
}

// adminListSQL lists operators in creation order.
func adminListSQL() string {
	return "SELECT " + adminColumns + " FROM cz_admin_user ORDER BY id ASC"
}

// adminCountSQL counts operators.
func adminCountSQL() string { return "SELECT COUNT(*) FROM cz_admin_user" }

// ---------------------------------------------------------------------------
// cz_audit_log
// ---------------------------------------------------------------------------

// auditColumns is the SELECT list for audit entries.
const auditColumns = "id, actor_type, actor_id, action, object_type, object_id, " +
	"COALESCE(before_json, ''), COALESCE(after_json, ''), ip, created_at"

// auditInsertSQL appends an audit entry.
func auditInsertSQL() string {
	return "INSERT INTO cz_audit_log (actor_type, actor_id, action, object_type, object_id, " +
		"before_json, after_json, ip, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)"
}

// auditInsertArgs returns the argument list matching auditInsertSQL.
func auditInsertArgs(l *domain.AuditLog) []any {
	return []any{
		l.ActorType,
		l.ActorID,
		l.Action,
		l.ObjectType,
		l.ObjectID,
		nullString(l.Before),
		nullString(l.After),
		l.IP,
		l.CreatedAt,
	}
}

// auditListSQL pages through audit entries, newest first.
func auditListSQL(limit, offset int) (string, []any) {
	q := "SELECT " + auditColumns + " FROM cz_audit_log ORDER BY id DESC" + limitClause(limit, offset)
	return q, pagingArgs(limit, offset)
}

// ---------------------------------------------------------------------------
// cz_flag
// ---------------------------------------------------------------------------

// flagColumns is the SELECT list for feature switches.
const flagColumns = "flag_key, scope, flag_value, updated_at"

// flagUpsertSQL writes a feature switch identified by (flag_key, scope).
func flagUpsertSQL() string {
	return "INSERT INTO cz_flag (flag_key, scope, flag_value, updated_at) VALUES (?, ?, ?, ?) " +
		"ON DUPLICATE KEY UPDATE flag_value = VALUES(flag_value), updated_at = VALUES(updated_at)"
}

// flagUpsertArgs returns the argument list matching flagUpsertSQL.
func flagUpsertArgs(f *domain.FlagValue) []any {
	return []any{f.Key, f.Scope, boolInt(f.Value), f.UpdatedAt}
}

// flagGetSQL selects one feature switch.
func flagGetSQL() string {
	return "SELECT " + flagColumns + " FROM cz_flag WHERE flag_key = ? AND scope = ?"
}

// flagListSQL lists every feature switch in a deterministic order.
func flagListSQL() string {
	return "SELECT " + flagColumns + " FROM cz_flag ORDER BY flag_key ASC, scope ASC"
}

// ---------------------------------------------------------------------------
// cz_daily_usage
// ---------------------------------------------------------------------------

// dailyColumns is the SELECT list for daily rollups.
const dailyColumns = "day, account_id, v10_host_id, api_key_id, model, requests, " +
	"prompt_tokens, completion_tokens, cost_micros"

// dailyUpsertSQL accumulates a rollup row identified by the composite primary
// key (day, account_id, v10_host_id, api_key_id, model).
//
// The update path *adds* the deltas, matching memstore's accumulate semantics.
func dailyUpsertSQL() string {
	return "INSERT INTO cz_daily_usage (day, account_id, v10_host_id, api_key_id, model, " +
		"requests, prompt_tokens, completion_tokens, cost_micros) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) " +
		"ON DUPLICATE KEY UPDATE requests = requests + VALUES(requests), " +
		"prompt_tokens = prompt_tokens + VALUES(prompt_tokens), " +
		"completion_tokens = completion_tokens + VALUES(completion_tokens), " +
		"cost_micros = cost_micros + VALUES(cost_micros)"
}

// dailyUpsertArgs returns the argument list matching dailyUpsertSQL.
func dailyUpsertArgs(d *domain.DailyUsage) []any {
	return []any{
		d.Day,
		d.AccountID,
		d.V10HostID,
		d.APIKeyID,
		d.Model,
		d.Requests,
		d.PromptTokens,
		d.CompletionTokens,
		d.CostMicros,
	}
}

// dailyReplaceSQL overwrites a rollup row, used by RebuildDaily.
func dailyReplaceSQL() string {
	return "INSERT INTO cz_daily_usage (day, account_id, v10_host_id, api_key_id, model, " +
		"requests, prompt_tokens, completion_tokens, cost_micros) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) " +
		"ON DUPLICATE KEY UPDATE requests = VALUES(requests), " +
		"prompt_tokens = VALUES(prompt_tokens), " +
		"completion_tokens = VALUES(completion_tokens), cost_micros = VALUES(cost_micros)"
}

// dailyFilterWhere builds the WHERE clause for rollup queries. UsageFilter's
// time window is compared against the day column as a date.
func dailyFilterWhere(f store.UsageFilter) (string, []any) {
	var b strings.Builder
	b.WriteString(" WHERE 1 = 1")
	var args []any
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
		b.WriteString(" AND day >= ?")
		args = append(args, dayString(f.From))
	}
	if !f.To.IsZero() {
		b.WriteString(" AND day < ?")
		args = append(args, dayString(f.To))
	}
	return b.String(), args
}

// dailyListSQL pages through rollups in a deterministic order.
func dailyListSQL(f store.UsageFilter) (string, []any) {
	where, args := dailyFilterWhere(f)
	q := "SELECT " + dailyColumns + " FROM cz_daily_usage" + where +
		" ORDER BY day ASC, account_id ASC, v10_host_id ASC, api_key_id ASC, model ASC" +
		limitClause(f.Limit, f.Offset)
	args = append(args, pagingArgs(f.Limit, f.Offset)...)
	return q, args
}

// dailyBucketSQL groups rollups by day for the trend charts.
func dailyBucketSQL(f store.UsageFilter) (string, []any) {
	where, args := dailyFilterWhere(f)
	const q = "SELECT day, 0, 0, 0, '', SUM(requests), SUM(prompt_tokens), " +
		"SUM(completion_tokens), SUM(cost_micros) FROM cz_daily_usage"
	return q + where + " GROUP BY day ORDER BY day ASC", args
}

// dailyDeleteDaySQL clears one day's rollups before a rebuild.
func dailyDeleteDaySQL() string {
	return "DELETE FROM cz_daily_usage WHERE day = ?"
}

// dayString renders a time as the YYYY-MM-DD literal stored in cz_daily_usage.
// Days are always UTC so the rollup key is unambiguous.
func dayString(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}
