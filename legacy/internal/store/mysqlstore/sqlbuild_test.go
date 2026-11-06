package mysqlstore

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"czapi/internal/domain"
	"czapi/internal/store"
)

// fixedTime is a stable timestamp so argument assertions are deterministic.
var fixedTime = time.Date(2026, 11, 5, 7, 0, 0, 0, time.UTC)

// ---------------------------------------------------------------------------
// Credit ledger
// ---------------------------------------------------------------------------

func TestCreditInsertSQL(t *testing.T) {
	e := &domain.CreditLedgerEntry{
		AccountID:    42,
		Type:         domain.LedgerV10Order,
		AmountMicros: 1_500_000,
		RefType:      "v10_order",
		RefID:        "ORD-2026-0001",
		Remark:       "top-up",
		CreatedAt:    fixedTime,
	}
	got, args := creditInsertSQL(e)

	assert.Equal(t,
		"INSERT INTO cz_credit_ledger "+
			"(account_id, type, amount_micros, balance_after, ref_type, ref_id, remark, created_at) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?)", got)
	// balance_after is written as 0 first and patched after the balance move.
	assert.Equal(t, []any{
		uint64(42), "v10_order", int64(1_500_000), int64(0),
		"v10_order", "ORD-2026-0001", "top-up", fixedTime,
	}, args)
}

func TestCreditInsertSQLOmitsEmptyRemark(t *testing.T) {
	_, args := creditInsertSQL(&domain.CreditLedgerEntry{
		AccountID: 1, Type: domain.LedgerConsume, AmountMicros: -5,
		RefType: "request", RefID: "req-1", CreatedAt: fixedTime,
	})
	assert.Nil(t, args[6], "an empty remark must bind as SQL NULL")
}

func TestCreditInsertSQLKeepsRemark(t *testing.T) {
	_, args := creditInsertSQL(&domain.CreditLedgerEntry{
		AccountID: 1, Type: domain.LedgerAdminAdjust, AmountMicros: 10,
		RefType: "admin", RefID: "adj-1", Remark: "goodwill", CreatedAt: fixedTime,
	})
	assert.Equal(t, "goodwill", args[6], "a non-empty remark binds as a plain string")
}

func TestCreditLockSQL(t *testing.T) {
	assert.Equal(t,
		"SELECT balance_credit, allow_negative FROM cz_account WHERE id = ? FOR UPDATE",
		creditLockAccountSQL())
}

func TestCreditBalanceSQL(t *testing.T) {
	assert.Equal(t,
		"UPDATE cz_account SET balance_credit = balance_credit + ? WHERE id = ?",
		creditAddBalanceSQL())
	assert.Equal(t,
		"UPDATE cz_credit_ledger SET balance_after = ? WHERE id = ?",
		creditSetBalanceAfterSQL())
	assert.Equal(t,
		"SELECT balance_credit FROM cz_account WHERE id = ?",
		creditCurrentBalanceSQL())
}

func TestLedgerListSQL(t *testing.T) {
	cases := []struct {
		name     string
		filter   store.LedgerFilter
		wantTail string
		wantArgs []any
	}{
		{
			name:     "no filter",
			filter:   store.LedgerFilter{},
			wantTail: " ORDER BY created_at DESC, id DESC",
			wantArgs: nil,
		},
		{
			name:   "account only",
			filter: store.LedgerFilter{AccountID: 7},
			wantTail: " AND account_id = ?" +
				" ORDER BY created_at DESC, id DESC",
			wantArgs: []any{uint64(7)},
		},
		{
			name: "all filters with paging",
			filter: store.LedgerFilter{
				AccountID: 7, Type: domain.LedgerRefund, RefType: "v10_refund",
				From: fixedTime, To: fixedTime.Add(24 * time.Hour),
				Limit: 20, Offset: 40,
			},
			wantTail: " AND account_id = ? AND type = ? AND ref_type = ?" +
				" AND created_at >= ? AND created_at < ?" +
				" ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?",
			wantArgs: []any{uint64(7), "refund", "v10_refund", fixedTime, fixedTime.Add(24 * time.Hour), 20, 40},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, args := ledgerListSQL(tc.filter)
			assert.Contains(t, got, "SELECT id, account_id, type, amount_micros, balance_after, "+
				"ref_type, ref_id, COALESCE(remark, ''), created_at FROM cz_credit_ledger WHERE 1 = 1")
			assert.True(t, hasSuffix(got, tc.wantTail),
				"sql tail mismatch:\n got %s\nwant suffix %s", got, tc.wantTail)
			assert.Equal(t, tc.wantArgs, args)
		})
	}
}

func TestLedgerSumSQL(t *testing.T) {
	got, args := ledgerSumSQL(9, domain.LedgerV10Order, fixedTime, fixedTime.Add(time.Hour))
	assert.Equal(t,
		"SELECT COALESCE(SUM(amount_micros), 0) FROM cz_credit_ledger "+
			"WHERE account_id = ? AND type = ? AND created_at >= ? AND created_at < ?", got)
	assert.Equal(t, []any{uint64(9), "v10_order", fixedTime, fixedTime.Add(time.Hour)}, args)

	// An empty type means "every type".
	got, args = ledgerSumSQL(9, "", fixedTime, fixedTime.Add(time.Hour))
	assert.Equal(t,
		"SELECT COALESCE(SUM(amount_micros), 0) FROM cz_credit_ledger "+
			"WHERE account_id = ? AND created_at >= ? AND created_at < ?", got)
	assert.Equal(t, []any{uint64(9), fixedTime, fixedTime.Add(time.Hour)}, args)
}

// ---------------------------------------------------------------------------
// Usage
// ---------------------------------------------------------------------------

func TestUsageInsertSQL(t *testing.T) {
	r := &domain.UsageRecord{
		RequestID:        "01JABCDEFGHJKMNPQRSTVWXYZ0",
		AccountID:        11,
		SubscriptionID:   22,
		APIKeyID:         33,
		V10HostID:        44,
		Model:            "gpt-4o-mini",
		ChannelID:        55,
		PromptTokens:     100,
		CompletionTokens: 200,
		CachedTokens:     10,
		CostMicros:       1234,
		BillingMode:      domain.PaidBySubscription,
		Estimated:        true,
		Partial:          false,
		Shadow:           false,
		Stream:           true,
		LatencyMS:        900,
		TTFTMS:           120,
		StatusCode:       200,
		ErrorCode:        "",
		CreatedAt:        fixedTime,
	}
	got, args := usageInsertSQL(r)

	assert.Equal(t,
		"INSERT INTO cz_usage_record "+
			"(request_id, account_id, subscription_id, api_key_id, v10_host_id, model, channel_id, "+
			"prompt_tokens, completion_tokens, cached_tokens, cost_micros, billing_mode, estimated, "+
			"partial, shadow, stream, latency_ms, ttft_ms, status_code, error_code, created_at) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) "+
			"ON DUPLICATE KEY UPDATE id = id", got)
	require.Len(t, args, 21)

	assert.Equal(t, "01JABCDEFGHJKMNPQRSTVWXYZ0", args[0])
	assert.Equal(t, uint64(11), args[1])
	assert.Equal(t, uint64(22), args[2], "subscription id is bound as a number when set")
	assert.Equal(t, uint64(33), args[3])
	assert.Equal(t, uint64(44), args[4])
	assert.Equal(t, "gpt-4o-mini", args[5])
	assert.Equal(t, uint64(55), args[6])
	assert.Equal(t, 100, args[7])
	assert.Equal(t, 200, args[8])
	assert.Equal(t, 10, args[9])
	assert.Equal(t, int64(1234), args[10])
	assert.Equal(t, int8(domain.PaidBySubscription), args[11])
	assert.Equal(t, int8(1), args[12], "estimated=true")
	assert.Equal(t, int8(0), args[13], "partial=false")
	assert.Equal(t, int8(0), args[14], "shadow=false")
	assert.Equal(t, int8(1), args[15], "stream=true")
	assert.Equal(t, 900, args[16])
	assert.Equal(t, 120, args[17])
	assert.Equal(t, 200, args[18])
	assert.Nil(t, args[19], "empty error_code must bind as SQL NULL")
	assert.Equal(t, fixedTime, args[20])
}

func TestUsageInsertSQLNullsOptionalColumns(t *testing.T) {
	got, args := usageInsertSQL(&domain.UsageRecord{
		RequestID: "req-1", AccountID: 1, APIKeyID: 2, Model: "m", CreatedAt: fixedTime,
	})
	assert.Contains(t, got, "ON DUPLICATE KEY UPDATE id = id")
	assert.Nil(t, args[2], "zero subscription_id must be SQL NULL")
	assert.Nil(t, args[4], "zero v10_host_id must be SQL NULL")
	assert.Nil(t, args[19], "empty error_code must be SQL NULL")
}

func TestUsageFilterWhereVariants(t *testing.T) {
	cases := []struct {
		name     string
		filter   store.UsageFilter
		want     string
		wantArgs []any
	}{
		{
			name:     "default excludes shadow",
			filter:   store.UsageFilter{},
			want:     " WHERE 1 = 1 AND shadow = 0",
			wantArgs: nil,
		},
		{
			name:     "include shadow drops the predicate",
			filter:   store.UsageFilter{IncludeShadow: true},
			want:     " WHERE 1 = 1",
			wantArgs: nil,
		},
		{
			name:     "site resolves through the account table",
			filter:   store.UsageFilter{SiteID: "app-1"},
			want:     " WHERE 1 = 1 AND shadow = 0 AND account_id IN (SELECT id FROM cz_account WHERE v10_site_id = ?)",
			wantArgs: []any{"app-1"},
		},
		{
			name:     "account",
			filter:   store.UsageFilter{AccountID: 5},
			want:     " WHERE 1 = 1 AND shadow = 0 AND account_id = ?",
			wantArgs: []any{uint64(5)},
		},
		{
			name:     "host",
			filter:   store.UsageFilter{V10HostID: 6},
			want:     " WHERE 1 = 1 AND shadow = 0 AND v10_host_id = ?",
			wantArgs: []any{uint64(6)},
		},
		{
			name:     "key",
			filter:   store.UsageFilter{APIKeyID: 7},
			want:     " WHERE 1 = 1 AND shadow = 0 AND api_key_id = ?",
			wantArgs: []any{uint64(7)},
		},
		{
			name:     "model",
			filter:   store.UsageFilter{Model: "gpt-4o"},
			want:     " WHERE 1 = 1 AND shadow = 0 AND model = ?",
			wantArgs: []any{"gpt-4o"},
		},
		{
			name:     "window",
			filter:   store.UsageFilter{From: fixedTime, To: fixedTime.Add(time.Hour)},
			want:     " WHERE 1 = 1 AND shadow = 0 AND created_at >= ? AND created_at < ?",
			wantArgs: []any{fixedTime, fixedTime.Add(time.Hour)},
		},
		{
			name: "every field together",
			filter: store.UsageFilter{
				SiteID: "app-1", AccountID: 5, V10HostID: 6, APIKeyID: 7, Model: "gpt-4o",
				From: fixedTime, To: fixedTime.Add(time.Hour), IncludeShadow: true,
				Offset: 2, Limit: 3,
			},
			want: " WHERE 1 = 1" +
				" AND account_id IN (SELECT id FROM cz_account WHERE v10_site_id = ?)" +
				" AND account_id = ? AND v10_host_id = ? AND api_key_id = ? AND model = ?" +
				" AND created_at >= ? AND created_at < ?",
			wantArgs: []any{"app-1", uint64(5), uint64(6), uint64(7), "gpt-4o", fixedTime, fixedTime.Add(time.Hour)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, args := usageFilterWhere(tc.filter)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantArgs, args)
		})
	}
}

func TestUsageListSQLLimitRules(t *testing.T) {
	const head = "SELECT " + usageColumns + " FROM cz_usage_record WHERE 1 = 1 AND shadow = 0" +
		" ORDER BY created_at DESC, id DESC"

	t.Run("zero limit means no LIMIT clause", func(t *testing.T) {
		got, args := usageListSQL(store.UsageFilter{})
		assert.Equal(t, head, got)
		assert.NotContains(t, got, "LIMIT")
		assert.Empty(t, args)
	})

	t.Run("limit only", func(t *testing.T) {
		got, args := usageListSQL(store.UsageFilter{Limit: 50})
		assert.Equal(t, head+" LIMIT ?", got)
		assert.Equal(t, []any{50}, args)
	})

	t.Run("limit and offset", func(t *testing.T) {
		got, args := usageListSQL(store.UsageFilter{Limit: 50, Offset: 100})
		assert.Equal(t, head+" LIMIT ? OFFSET ?", got)
		assert.Equal(t, []any{50, 100}, args)
	})

	t.Run("offset without limit keeps MySQL happy", func(t *testing.T) {
		got, args := usageListSQL(store.UsageFilter{Offset: 10})
		assert.Equal(t, head+" LIMIT 18446744073709551615 OFFSET ?", got)
		assert.Equal(t, []any{10}, args)
	})

	t.Run("negative limit behaves like zero", func(t *testing.T) {
		got, _ := usageListSQL(store.UsageFilter{Limit: -1})
		assert.Equal(t, head, got)
	})
}

func TestUsageSummarySQL(t *testing.T) {
	got, args := usageSummarySQL(store.UsageFilter{AccountID: 3, Limit: 10})
	assert.Equal(t,
		"SELECT COUNT(*), COALESCE(SUM(prompt_tokens), 0), "+
			"COALESCE(SUM(completion_tokens), 0), COALESCE(SUM(cost_micros), 0) "+
			"FROM cz_usage_record WHERE 1 = 1 AND shadow = 0 AND account_id = ?", got)
	// A summary ignores paging: it is one aggregate row.
	assert.Equal(t, []any{uint64(3)}, args)
	assert.NotContains(t, got, "LIMIT")
}

func TestUsageWindowWhere(t *testing.T) {
	got, args := usageWindowWhere("api_key_id", 8)
	assert.Equal(t, " WHERE shadow = 0 AND created_at >= ? AND created_at < ? AND api_key_id = ?", got)
	assert.Equal(t, []any{uint64(8)}, args)

	got, args = usageWindowWhere("", 0)
	assert.Equal(t, " WHERE shadow = 0 AND created_at >= ? AND created_at < ?", got)
	assert.Empty(t, args)
}

// ---------------------------------------------------------------------------
// Subscription
// ---------------------------------------------------------------------------

func TestSubscriptionInsertSQL(t *testing.T) {
	v := &domain.Subscription{
		AccountID:   1,
		V10HostID:   2,
		PlanID:      3,
		PeriodStart: fixedTime,
		PeriodEnd:   fixedTime.AddDate(0, 1, 0),
		QuotaTotal:  1_000_000,
		QuotaUsed:   0,
		BillingMode: domain.BillingHybrid,
		Status:      domain.SubActive,
		CreatedAt:   fixedTime,
		UpdatedAt:   fixedTime,
	}
	got, args := subscriptionInsertSQL(v)

	assert.Equal(t,
		"INSERT INTO cz_subscription "+
			"(account_id, v10_host_id, plan_id, period_start, period_end, quota_total, quota_used, "+
			"billing_mode, status, created_at, updated_at) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) "+
			"ON DUPLICATE KEY UPDATE id = id", got)
	assert.Equal(t, []any{
		uint64(1), uint64(2), uint64(3), fixedTime, fixedTime.AddDate(0, 1, 0),
		int64(1_000_000), int64(0), "hybrid", "active", fixedTime, fixedTime,
	}, args)
}

func TestSubscriptionAddUsageSQL(t *testing.T) {
	assert.Equal(t,
		"UPDATE cz_subscription SET quota_used = quota_used + ?, updated_at = ? WHERE id = ?",
		subscriptionAddUsageSQL())
	assert.Equal(t, "SELECT quota_used FROM cz_subscription WHERE id = ?", subscriptionQuotaUsedSQL())
}

func TestSubscriptionOtherSQL(t *testing.T) {
	assert.Contains(t, subscriptionByIDSQL(), "FROM cz_subscription WHERE id = ?")
	assert.Contains(t, subscriptionByHostSQL(), "FROM cz_subscription WHERE v10_host_id = ?")
	assert.Contains(t, subscriptionListByAccountSQL(), "ORDER BY period_end ASC, id ASC")
	assert.Contains(t, subscriptionExpiredSQL(), "status = 'active' AND period_end <= ?")
	assert.Contains(t, subscriptionResetPeriodSQL(), "quota_used = 0")

	args := subscriptionUpdateArgs(&domain.Subscription{
		ID: 99, AccountID: 1, V10HostID: 2, PlanID: 3,
		PeriodStart: fixedTime, PeriodEnd: fixedTime,
		QuotaTotal: 5, QuotaUsed: 6,
		BillingMode: domain.BillingBalance, Status: domain.SubSuspended,
	})
	assert.Equal(t,
		[]any{uint64(1), uint64(2), uint64(3), fixedTime, fixedTime, int64(5), int64(6),
			"balance", "suspended", uint64(99)}, args)
}

// ---------------------------------------------------------------------------
// API keys
// ---------------------------------------------------------------------------

func TestKeyRevokeSQL(t *testing.T) {
	got := keySetStatusBySubscriptionSQL()
	assert.Equal(t,
		"UPDATE cz_api_key SET status = ?, updated_at = ? "+
			"WHERE subscription_id = ? AND status <> 'revoked'", got)
	assert.Contains(t, got, "status <> 'revoked'",
		"already-revoked keys must never be resurrected")
	assert.NotContains(t, got, "status = 'active'",
		"the guard must not be limited to active keys")
}

func TestKeyCountBySubscriptionSQL(t *testing.T) {
	assert.Equal(t,
		"SELECT COUNT(*) FROM cz_api_key WHERE subscription_id = ? AND status <> 'revoked'",
		keyCountBySubscriptionSQL())
}

func TestKeyInsertArgs(t *testing.T) {
	expires := fixedTime.AddDate(0, 1, 0)
	args, err := keyInsertArgs(&domain.APIKey{
		AccountID:      1,
		SubscriptionID: 2,
		KeyHash:        "hash",
		KeyPrefix:      "sk-abc12",
		Name:           "prod",
		Models:         []string{"gpt-4o"},
		IPWhitelist:    []string{"10.0.0.0/8"},
		ExpiresAt:      &expires,
		Status:         domain.KeyActive,
		CreatedAt:      fixedTime,
		UpdatedAt:      fixedTime,
	})
	require.NoError(t, err)
	require.Len(t, args, 12)
	assert.Equal(t, uint64(1), args[0])
	assert.Equal(t, uint64(2), args[1])
	assert.Equal(t, "hash", args[2])
	assert.Equal(t, `["gpt-4o"]`, args[5].(jsonValue).String())
	assert.Equal(t, `["10.0.0.0/8"]`, args[6].(jsonValue).String())
	assert.Equal(t, expires, args[7])
	assert.Equal(t, "active", args[8])
	assert.Nil(t, args[9], "unset last_used_at must be NULL")
	assert.Equal(t, fixedTime, args[10])
	assert.Equal(t, fixedTime, args[11])
}

func TestKeyInsertArgsEmptySlicesEncodeAsArrays(t *testing.T) {
	args, err := keyInsertArgs(&domain.APIKey{
		AccountID: 1, KeyHash: "h", Status: domain.KeyActive,
		CreatedAt: fixedTime, UpdatedAt: fixedTime,
	})
	require.NoError(t, err)
	assert.Equal(t, "[]", args[5].(jsonValue).String(),
		"an empty model list stays a JSON array so consumers can tell it apart from NULL")
	assert.Equal(t, "[]", args[6].(jsonValue).String())
	assert.Nil(t, args[1], "zero subscription id must be NULL")
	assert.Nil(t, args[7], "nil expires_at must be NULL")
}

// ---------------------------------------------------------------------------
// Accounts, plans, channels, flags, rollups
// ---------------------------------------------------------------------------

func TestAccountUpsertNeverMovesMoney(t *testing.T) {
	got := accountUpsertSQL()
	assert.Contains(t, got, "ON DUPLICATE KEY UPDATE")
	assert.NotContains(t, got, "balance_credit = VALUES(balance_credit)",
		"a profile sync must never overwrite the balance")
	assert.NotContains(t, got, "balance_credit = ", "balance is only moved by the ledger")
}

func TestAccountSQL(t *testing.T) {
	assert.Equal(t,
		"INSERT INTO cz_account (v10_site_id, v10_client_id, email, status, "+
			"balance_credit, allow_negative, created_at, updated_at) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?) "+
			"ON DUPLICATE KEY UPDATE email = VALUES(email), status = VALUES(status), "+
			"allow_negative = VALUES(allow_negative), updated_at = VALUES(updated_at)",
		accountUpsertSQL())

	args := accountUpsertArgs(&domain.Account{
		SiteID: "app-1", V10ClientID: 2, Email: "a@b.c", Status: domain.AccountEnabled,
		BalanceCredit: 500, AllowNegative: true, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	})
	require.Len(t, args, 8)
	assert.Equal(t, "app-1", args[0])
	assert.Equal(t, uint64(2), args[1])
	assert.Equal(t, "a@b.c", args[2])
	assert.Equal(t, int8(1), args[3])
	assert.Equal(t, int64(500), args[4])
	assert.Equal(t, int8(1), args[5], "allow_negative=true maps to 1")
	assert.Equal(t, fixedTime, args[6])
	assert.Equal(t, fixedTime, args[7])
}

func TestAccountListSQLPaging(t *testing.T) {
	got, args := accountListSQL(0, 0)
	assert.Equal(t, "SELECT "+accountColumns+" FROM cz_account ORDER BY id ASC", got)
	assert.Empty(t, args)

	got, args = accountListSQL(25, 50)
	assert.Equal(t, "SELECT "+accountColumns+" FROM cz_account ORDER BY id ASC LIMIT ? OFFSET ?", got)
	assert.Equal(t, []any{25, 50}, args)
}

func TestPlanUpsertSQL(t *testing.T) {
	got := planUpsertSQL()
	assert.Contains(t, got, "INSERT INTO cz_plan")
	assert.Contains(t, got, "ON DUPLICATE KEY UPDATE")
	assert.Contains(t, got, "plan_code")

	args := planUpsertArgs(&domain.Plan{
		PlanCode: "starter", Name: "Starter", QuotaTokens: 1_000_000,
		Period: "month", RPMLimit: 60, TPMLimit: 100_000, Concurrency: 5,
		ModelGroup: "default", MaxKeys: 5, PriceCents: 9900, GroupRatio: 1.0,
		Status: 1, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	})
	require.Len(t, args, 14)
	assert.Equal(t, "starter", args[0])
	assert.Equal(t, int64(1_000_000), args[2])
}

func TestChannelListByModelUsesJSONContains(t *testing.T) {
	got := channelListByModelSQL()
	assert.Contains(t, got, "JSON_CONTAINS(models, JSON_QUOTE(?))",
		"model membership must not be a LIKE scan")
	assert.Contains(t, got, "status = 1")
	assert.Contains(t, got, "ORDER BY priority DESC, id ASC")
}

func TestChannelJSONRoundTrip(t *testing.T) {
	pool, err := marshalJSONAny([]domain.ChannelKey{
		{Secret: "sealed-k1", Weight: 100, Status: "active", Cooldown: 0},
		{Secret: "sealed-k2", Weight: 50, Status: "disabled", Cooldown: 1_700_000_000},
	})
	require.NoError(t, err)
	assert.Contains(t, pool.String(), `"secret":"sealed-k1"`)
	assert.Contains(t, pool.String(), `"cooldown_until_unix":1700000000`)

	var decoded jsonColumn
	require.NoError(t, decoded.Scan([]byte(pool.String())))
	keys, err := decoded.decodeChannelKeys()
	require.NoError(t, err)
	require.Len(t, keys, 2)
	assert.Equal(t, "sealed-k2", keys[1].Secret)
	assert.Equal(t, int64(1_700_000_000), keys[1].Cooldown)
}

func TestFlagSQL(t *testing.T) {
	assert.Equal(t,
		"INSERT INTO cz_flag (flag_key, scope, flag_value, updated_at) VALUES (?, ?, ?, ?) "+
			"ON DUPLICATE KEY UPDATE flag_value = VALUES(flag_value), updated_at = VALUES(updated_at)",
		flagUpsertSQL())
	assert.Equal(t, "SELECT "+flagColumns+" FROM cz_flag WHERE flag_key = ? AND scope = ?", flagGetSQL())
	assert.Contains(t, flagListSQL(), "ORDER BY flag_key ASC, scope ASC")
	assert.Equal(t, []any{"gateway_enabled", "", int8(1), fixedTime}, flagUpsertArgs(&domain.FlagValue{
		Key: "gateway_enabled", Value: true, Scope: "", UpdatedAt: fixedTime,
	}))
}

func TestDailyUsageSQL(t *testing.T) {
	accumulate := dailyUpsertSQL()
	assert.Contains(t, accumulate, "requests = requests + VALUES(requests)",
		"UpsertDaily must accumulate, not overwrite")
	assert.Contains(t, accumulate, "cost_micros = cost_micros + VALUES(cost_micros)")

	replace := dailyReplaceSQL()
	assert.Contains(t, replace, "requests = VALUES(requests)")
	assert.NotContains(t, replace, "requests = requests +",
		"RebuildDaily replaces the bucket instead of adding to it")

	args := dailyUpsertArgs(&domain.DailyUsage{
		Day: "2026-11-05", AccountID: 1, V10HostID: 2, APIKeyID: 3, Model: "m",
		Requests: 4, PromptTokens: 5, CompletionTokens: 6, CostMicros: 7,
	})
	assert.Equal(t, []any{"2026-11-05", uint64(1), uint64(2), uint64(3), "m",
		int64(4), int64(5), int64(6), int64(7)}, args)
}

func TestDailyFilterWhereUsesDateStrings(t *testing.T) {
	got, args := dailyFilterWhere(store.UsageFilter{
		AccountID: 1,
		From:      time.Date(2026, 11, 1, 13, 45, 0, 0, time.UTC),
		To:        time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
	})
	assert.Equal(t, " WHERE 1 = 1 AND account_id = ? AND day >= ? AND day < ?", got)
	assert.Equal(t, []any{uint64(1), "2026-11-01", "2026-12-01"}, args,
		"day columns are DATE, so bounds are rendered as YYYY-MM-DD")
}

func TestDailyListSQLOrdering(t *testing.T) {
	got, args := dailyListSQL(store.UsageFilter{Limit: 10, Offset: 5})
	assert.Contains(t, got, "ORDER BY day ASC, account_id ASC, v10_host_id ASC, api_key_id ASC, model ASC")
	assert.True(t, hasSuffix(got, "LIMIT ? OFFSET ?"))
	assert.Equal(t, []any{10, 5}, args)
}

func TestDailyBucketSQL(t *testing.T) {
	got, _ := dailyBucketSQL(store.UsageFilter{AccountID: 4})
	assert.Contains(t, got, "GROUP BY day ORDER BY day ASC")
	assert.Contains(t, got, "SUM(cost_micros)")
}

func TestModelListByGroup(t *testing.T) {
	got, args := modelListByGroupSQL("")
	assert.Equal(t, modelListSQL(), got)
	assert.Empty(t, args)

	got, args = modelListByGroupSQL("default")
	assert.Equal(t, modelListSQL(), got)
	assert.Empty(t, args)

	got, args = modelListByGroupSQL("vip")
	assert.Contains(t, got, "WHERE group_name = ?")
	assert.Equal(t, []any{"vip"}, args)
}

func TestSiteSQLJSON(t *testing.T) {
	args, err := siteInsertArgs(&domain.Site{
		AppID: "app-1", Name: "site", Secret: "sealed", IPAllow: []string{"1.2.3.0/24"},
		Enabled: true, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	})
	require.NoError(t, err)
	assert.Nil(t, args[3], "empty secret_prev must be NULL")
	assert.Equal(t, `["1.2.3.0/24"]`, args[4].(jsonValue).String())
	assert.Equal(t, int8(1), args[5])
}

func TestAuditListSQL(t *testing.T) {
	got, args := auditListSQL(20, 0)
	assert.Equal(t, "SELECT "+auditColumns+" FROM cz_audit_log ORDER BY id DESC LIMIT ?", got)
	assert.Equal(t, []any{20}, args)
}

// ---------------------------------------------------------------------------
// Paging helpers
// ---------------------------------------------------------------------------

func TestLimitClause(t *testing.T) {
	cases := []struct {
		limit, offset int
		wantClause    string
		wantArgs      []any
	}{
		{0, 0, "", nil},
		{-5, 0, "", nil},
		{10, 0, " LIMIT ?", []any{10}},
		{10, 5, " LIMIT ? OFFSET ?", []any{10, 5}},
		{0, 5, " LIMIT 18446744073709551615 OFFSET ?", []any{5}},
		{-1, 5, " LIMIT 18446744073709551615 OFFSET ?", []any{5}},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.wantClause, limitClause(tc.limit, tc.offset),
			"limit=%d offset=%d", tc.limit, tc.offset)
		assert.Equal(t, tc.wantArgs, pagingArgs(tc.limit, tc.offset),
			"limit=%d offset=%d", tc.limit, tc.offset)
	}
}

// ---------------------------------------------------------------------------
// Package-level invariants
// ---------------------------------------------------------------------------

func TestEveryListQueryIsDeterministic(t *testing.T) {
	queries := []string{
		ledgerListSQLString(),
		usageListSQLString(),
		accountListSQLString(),
		auditListSQLString(),
		subscriptionListByAccountSQL(),
		keyListByAccountSQL(),
		keyListBySubscriptionSQL(),
		planListSQL(),
		siteListSQL(),
		adminListSQL(),
		channelListSQL(),
		modelListSQL(),
		flagListSQL(),
	}
	for _, q := range queries {
		assert.Contains(t, q, "ORDER BY",
			"every list query must be deterministically ordered: %s", q)
	}
}

// Small wrappers so the invariant test reads as a flat list.
func ledgerListSQLString() string  { q, _ := ledgerListSQL(store.LedgerFilter{}); return q }
func usageListSQLString() string   { q, _ := usageListSQL(store.UsageFilter{}); return q }
func accountListSQLString() string { q, _ := accountListSQL(0, 0); return q }
func auditListSQLString() string   { q, _ := auditListSQL(0, 0); return q }

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// hasSuffix reports whether s ends with suffix.
func hasSuffix(s, suffix string) bool {
	if len(suffix) > len(s) {
		return false
	}
	return s[len(s)-len(suffix):] == suffix
}
