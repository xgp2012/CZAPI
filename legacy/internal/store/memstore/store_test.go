package memstore

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"czapi/internal/domain"
	"czapi/internal/pkg/clock"
	"czapi/internal/pkg/errs"
	"czapi/internal/store"
)

var ctxBg = context.Background()

// newStore returns a store backed by a fake clock frozen at a fixed instant.
func newStore(t *testing.T) (*Store, *clock.Fake) {
	t.Helper()
	now := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	fake := clock.NewFake(now)
	return NewWithClock(fake), fake
}

// mustAccount creates an account and returns it.
func mustAccount(t *testing.T, s *Store, siteID string, clientID uint64) *domain.Account {
	t.Helper()
	a, err := s.Accounts().Upsert(ctxBg, &domain.Account{
		SiteID:      siteID,
		V10ClientID: clientID,
		Email:       "u@example.com",
		Status:      domain.AccountEnabled,
	})
	require.NoError(t, err)
	require.NotZero(t, a.ID)
	return a
}

// --------------------------------------------------------------------------
// Ledger credit semantics (docs/workstreams.md 搂5.1)
// --------------------------------------------------------------------------

func TestCreditIdempotentSequential(t *testing.T) {
	s, _ := newStore(t)
	acct := mustAccount(t, s, "site-a", 1)

	var firstBalance int64
	for i := 0; i < 10; i++ {
		applied, balance, err := s.Ledger().Credit(ctxBg, &domain.CreditLedgerEntry{
			AccountID:    acct.ID,
			Type:         domain.LedgerV10Order,
			AmountMicros: 5_000_000,
			RefType:      "v10_order",
			RefID:        "ORDER-1",
		})
		require.NoError(t, err)
		if i == 0 {
			assert.True(t, applied, "first call must apply")
			assert.Equal(t, int64(5_000_000), balance)
			firstBalance = balance
		} else {
			assert.False(t, applied, "replay #%d must not apply", i)
			assert.Equal(t, firstBalance, balance, "replay #%d must not move the balance", i)
		}
	}

	got, err := s.Accounts().GetByID(ctxBg, acct.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(5_000_000), got.BalanceCredit, "exactly one application")

	entries, err := s.Ledger().List(ctxBg, store.LedgerFilter{AccountID: acct.ID})
	require.NoError(t, err)
	require.Len(t, entries, 1, "replays must not append ledger rows")
	assert.Equal(t, int64(5_000_000), entries[0].BalanceAfter, "BalanceAfter is stamped")
}

func TestCreditIdempotentConcurrent(t *testing.T) {
	s, _ := newStore(t)
	acct := mustAccount(t, s, "site-a", 1)

	const n = 10
	var wg sync.WaitGroup
	applied := make([]bool, n)
	errsSeen := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			ok, _, err := s.Ledger().Credit(ctxBg, &domain.CreditLedgerEntry{
				AccountID:    acct.ID,
				Type:         domain.LedgerRedeemCode,
				AmountMicros: 1_000_000,
				RefType:      "redeem_code",
				RefID:        "CODE-1",
			})
			applied[i], errsSeen[i] = ok, err
		}(i)
	}
	wg.Wait()

	appliedCount := 0
	for i := 0; i < n; i++ {
		require.NoError(t, errsSeen[i])
		if applied[i] {
			appliedCount++
		}
	}
	assert.Equal(t, 1, appliedCount, "exactly one concurrent application")

	got, err := s.Accounts().GetByID(ctxBg, acct.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1_000_000), got.BalanceCredit)

	entries, err := s.Ledger().List(ctxBg, store.LedgerFilter{AccountID: acct.ID})
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestCreditDifferentRefsApplySeparately(t *testing.T) {
	s, _ := newStore(t)
	acct := mustAccount(t, s, "site-a", 1)

	for _, ref := range []string{"A", "B"} {
		ok, _, err := s.Ledger().Credit(ctxBg, &domain.CreditLedgerEntry{
			AccountID:    acct.ID,
			Type:         domain.LedgerV10Order,
			AmountMicros: 100,
			RefType:      "v10_order",
			RefID:        ref,
		})
		require.NoError(t, err)
		require.True(t, ok)
	}
	got, err := s.Accounts().GetByID(ctxBg, acct.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(200), got.BalanceCredit)
}

func TestCreditNegativeDenied(t *testing.T) {
	s, _ := newStore(t)
	acct := mustAccount(t, s, "site-a", 1)

	applied, balance, err := s.Ledger().Credit(ctxBg, &domain.CreditLedgerEntry{
		AccountID:    acct.ID,
		Type:         domain.LedgerConsume,
		AmountMicros: -1,
		RefType:      "usage",
		RefID:        "REQ-1",
	})
	require.Error(t, err)
	var e *errs.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, errs.CodeInsufficientQuota, e.Code)
	assert.False(t, applied)
	assert.Equal(t, int64(0), balance, "reported balance is the unchanged one")

	got, err := s.Accounts().GetByID(ctxBg, acct.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), got.BalanceCredit, "denied credit must not move the balance")

	entries, err := s.Ledger().List(ctxBg, store.LedgerFilter{AccountID: acct.ID})
	require.NoError(t, err)
	assert.Empty(t, entries, "denied credit must not append a row")

	// The voucher is not burned by a denial: retrying after a top-up succeeds.
	ok, _, err := s.Ledger().Credit(ctxBg, &domain.CreditLedgerEntry{
		AccountID:    acct.ID,
		Type:         domain.LedgerV10Order,
		AmountMicros: 1_000,
		RefType:      "v10_order",
		RefID:        "TOPUP",
	})
	require.NoError(t, err)
	require.True(t, ok)

	ok, balance, err = s.Ledger().Credit(ctxBg, &domain.CreditLedgerEntry{
		AccountID:    acct.ID,
		Type:         domain.LedgerConsume,
		AmountMicros: -1,
		RefType:      "usage",
		RefID:        "REQ-1",
	})
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, int64(999), balance)
}

func TestCreditNegativeAllowedByAccount(t *testing.T) {
	s, _ := newStore(t)
	a, err := s.Accounts().Upsert(ctxBg, &domain.Account{
		SiteID:        "site-a",
		V10ClientID:   2,
		Status:        domain.AccountEnabled,
		AllowNegative: true,
	})
	require.NoError(t, err)

	ok, balance, err := s.Ledger().Credit(ctxBg, &domain.CreditLedgerEntry{
		AccountID:    a.ID,
		Type:         domain.LedgerConsume,
		AmountMicros: -500,
		RefType:      "usage",
		RefID:        "REQ-2",
	})
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, int64(-500), balance)
}

func TestCreditNegativeAllowedByEntryRemark(t *testing.T) {
	s, _ := newStore(t)
	acct := mustAccount(t, s, "site-a", 1)

	ok, balance, err := s.Ledger().Credit(ctxBg, &domain.CreditLedgerEntry{
		AccountID:    acct.ID,
		Type:         domain.LedgerConsume,
		AmountMicros: -250,
		RefType:      "usage",
		RefID:        "REQ-3",
		Remark:       "allow_negative",
	})
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, int64(-250), balance)
}

func TestCreditEmptyRefRejected(t *testing.T) {
	s, _ := newStore(t)
	acct := mustAccount(t, s, "site-a", 1)

	cases := []struct {
		name string
		e    *domain.CreditLedgerEntry
	}{
		{"both empty", &domain.CreditLedgerEntry{AccountID: acct.ID, AmountMicros: 1}},
		{"empty ref_type", &domain.CreditLedgerEntry{AccountID: acct.ID, AmountMicros: 1, RefID: "X"}},
		{"empty ref_id", &domain.CreditLedgerEntry{AccountID: acct.ID, AmountMicros: 1, RefType: "v10_order"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := s.Ledger().Credit(ctxBg, tc.e)
			require.Error(t, err)
			var e *errs.Error
			require.ErrorAs(t, err, &e)
			assert.Equal(t, errs.CodeInvalidRequest, e.Code)
		})
	}

	_, _, err := s.Ledger().Credit(ctxBg, nil)
	require.Error(t, err)

	got, err := s.Accounts().GetByID(ctxBg, acct.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), got.BalanceCredit)
}

func TestCreditUnknownAccount(t *testing.T) {
	s, _ := newStore(t)
	_, _, err := s.Ledger().Credit(ctxBg, &domain.CreditLedgerEntry{
		AccountID:    999,
		AmountMicros: 1,
		RefType:      "v10_order",
		RefID:        "x",
	})
	require.Error(t, err)
	var e *errs.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, errs.CodeNotFound, e.Code)
}

func TestLedgerSumByTypeBetween(t *testing.T) {
	s, fake := newStore(t)
	acct := mustAccount(t, s, "site-a", 1)

	base := fake.Now()
	requireLedgerCredit(t, s, acct.ID, domain.LedgerV10Order, 1_000, "v10_order", "1")
	requireLedgerCredit(t, s, acct.ID, domain.LedgerV10Order, 500, "v10_order", "2")
	requireLedgerCredit(t, s, acct.ID, domain.LedgerConsume, -300, "usage", "3")

	sum, err := s.Ledger().SumByTypeBetween(ctxBg, acct.ID, domain.LedgerV10Order, base.Add(-time.Minute), base.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, int64(1_500), sum)

	// The exclusive upper bound excludes everything when it equals "from".
	sum, err = s.Ledger().SumByTypeBetween(ctxBg, acct.ID, domain.LedgerV10Order, base, base)
	require.NoError(t, err)
	assert.Equal(t, int64(0), sum)
}

// --------------------------------------------------------------------------
// Usage semantics
// --------------------------------------------------------------------------

func TestUsageInsertDuplicate(t *testing.T) {
	s, _ := newStore(t)
	acct := mustAccount(t, s, "site-a", 1)

	u := &domain.UsageRecord{
		RequestID:  "REQ-1",
		AccountID:  acct.ID,
		Model:      "gpt-4o",
		CostMicros: 1_000,
	}
	created, err := s.Usage().Insert(ctxBg, u)
	require.NoError(t, err)
	assert.True(t, created)

	dup := &domain.UsageRecord{RequestID: "REQ-1", AccountID: acct.ID, Model: "gpt-4o", CostMicros: 99}
	created, err = s.Usage().Insert(ctxBg, dup)
	require.NoError(t, err)
	assert.False(t, created, "duplicate request_id must not create a row")

	rows, err := s.Usage().List(ctxBg, store.UsageFilter{AccountID: acct.ID})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, int64(1_000), rows[0].CostMicros, "the original row is untouched")
	assert.Zero(t, dup.ID)
}

func TestUsageInsertConcurrentDuplicates(t *testing.T) {
	s, _ := newStore(t)
	acct := mustAccount(t, s, "site-a", 1)

	const n = 10
	var wg sync.WaitGroup
	createdCount := 0
	var mu sync.Mutex
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			created, err := s.Usage().Insert(ctxBg, &domain.UsageRecord{
				RequestID:  "REQ-C",
				AccountID:  acct.ID,
				CostMicros: 7,
			})
			if err == nil && created {
				mu.Lock()
				createdCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, createdCount)

	sum, err := s.Usage().Summary(ctxBg, store.UsageFilter{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), sum.Requests)
}

func TestUsageInsertRequiresRequestID(t *testing.T) {
	s, _ := newStore(t)
	_, err := s.Usage().Insert(ctxBg, &domain.UsageRecord{})
	require.Error(t, err)
	var e *errs.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, errs.CodeInvalidRequest, e.Code)
}

func TestUsageFilterShadowExclusion(t *testing.T) {
	s, _ := newStore(t)
	acct := mustAccount(t, s, "site-a", 1)

	for i, shadow := range []bool{false, true, false} {
		_, err := s.Usage().Insert(ctxBg, &domain.UsageRecord{
			RequestID:  "REQ-" + string(rune('A'+i)),
			AccountID:  acct.ID,
			CostMicros: 10,
			Shadow:     shadow,
		})
		require.NoError(t, err)
	}

	rows, err := s.Usage().List(ctxBg, store.UsageFilter{})
	require.NoError(t, err)
	assert.Len(t, rows, 2, "shadow rows are excluded by default")

	rows, err = s.Usage().List(ctxBg, store.UsageFilter{IncludeShadow: true})
	require.NoError(t, err)
	assert.Len(t, rows, 3)

	sum, err := s.Usage().Summary(ctxBg, store.UsageFilter{})
	require.NoError(t, err)
	assert.Equal(t, int64(2), sum.Requests)
	assert.Equal(t, int64(20), sum.CostMicros)

	sum, err = s.Usage().Summary(ctxBg, store.UsageFilter{IncludeShadow: true})
	require.NoError(t, err)
	assert.Equal(t, int64(30), sum.CostMicros)
}

func TestUsageFilterTimeBoundaries(t *testing.T) {
	s, fake := newStore(t)
	acct := mustAccount(t, s, "site-a", 1)
	base := fake.Now()

	for i, off := range []time.Duration{-time.Minute, 0, time.Minute} {
		_, err := s.Usage().Insert(ctxBg, &domain.UsageRecord{
			RequestID:  "REQ-T" + string(rune('0'+i)),
			AccountID:  acct.ID,
			CostMicros: 1,
			CreatedAt:  base.Add(off),
		})
		require.NoError(t, err)
	}

	// [from,to): the row stamped exactly at 'from' is in, the row at 'to' is out.
	rows, err := s.Usage().List(ctxBg, store.UsageFilter{From: base, To: base.Add(time.Minute)})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, base, rows[0].CreatedAt.UTC())

	rows, err = s.Usage().List(ctxBg, store.UsageFilter{From: base, To: base.Add(2 * time.Minute)})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, base, rows[0].CreatedAt.UTC())
	assert.Equal(t, base.Add(time.Minute), rows[1].CreatedAt.UTC())

	// Zero bounds are unbounded.
	rows, err = s.Usage().List(ctxBg, store.UsageFilter{})
	require.NoError(t, err)
	assert.Len(t, rows, 3)
}

func TestUsageFilterLimitOffset(t *testing.T) {
	s, _ := newStore(t)
	acct := mustAccount(t, s, "site-a", 1)
	for i := 0; i < 5; i++ {
		_, err := s.Usage().Insert(ctxBg, &domain.UsageRecord{
			RequestID:  "REQ-" + string(rune('A'+i)),
			AccountID:  acct.ID,
			CostMicros: 1,
		})
		require.NoError(t, err)
	}

	rows, err := s.Usage().List(ctxBg, store.UsageFilter{Limit: 0})
	require.NoError(t, err)
	assert.Len(t, rows, 5, "Limit==0 means no limit")

	rows, err = s.Usage().List(ctxBg, store.UsageFilter{Limit: 2})
	require.NoError(t, err)
	assert.Len(t, rows, 2)

	rows, err = s.Usage().List(ctxBg, store.UsageFilter{Offset: 3, Limit: 2})
	require.NoError(t, err)
	assert.Len(t, rows, 2)
	assert.Equal(t, "REQ-D", rows[0].RequestID)

	rows, err = s.Usage().List(ctxBg, store.UsageFilter{Offset: 10})
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestUsageFilterFields(t *testing.T) {
	s, _ := newStore(t)
	a1 := mustAccount(t, s, "site-a", 1)
	a2 := mustAccount(t, s, "site-b", 9)

	insert := func(req string, acct uint64, key, host uint64, model string, cost int64) {
		t.Helper()
		_, err := s.Usage().Insert(ctxBg, &domain.UsageRecord{
			RequestID: req, AccountID: acct, APIKeyID: key, V10HostID: host,
			Model: model, CostMicros: cost,
		})
		require.NoError(t, err)
	}
	insert("R1", a1.ID, 100, 1000, "gpt-4o", 10)
	insert("R2", a1.ID, 101, 1001, "gpt-4o-mini", 20)
	insert("R3", a2.ID, 200, 2000, "gpt-4o", 30)

	cases := []struct {
		name string
		f    store.UsageFilter
		want []string
	}{
		{"site resolves via account", store.UsageFilter{SiteID: "site-a"}, []string{"R1", "R2"}},
		{"account", store.UsageFilter{AccountID: a2.ID}, []string{"R3"}},
		{"api key", store.UsageFilter{APIKeyID: 101}, []string{"R2"}},
		{"v10 host", store.UsageFilter{V10HostID: 2000}, []string{"R3"}},
		{"model", store.UsageFilter{Model: "gpt-4o"}, []string{"R1", "R3"}},
		{"combined", store.UsageFilter{SiteID: "site-a", Model: "gpt-4o"}, []string{"R1"}},
		{"unknown site", store.UsageFilter{SiteID: "nope"}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := s.Usage().List(ctxBg, tc.f)
			require.NoError(t, err)
			got := make([]string, len(rows))
			for i, r := range rows {
				got[i] = r.RequestID
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSumCostBetween(t *testing.T) {
	s, fake := newStore(t)
	acct := mustAccount(t, s, "site-a", 1)
	base := fake.Now()

	insert := func(req string, key uint64, cost int64, shadow bool, at time.Time) {
		t.Helper()
		_, err := s.Usage().Insert(ctxBg, &domain.UsageRecord{
			RequestID: req, AccountID: acct.ID, APIKeyID: key,
			CostMicros: cost, Shadow: shadow, CreatedAt: at,
		})
		require.NoError(t, err)
	}
	insert("R1", 7, 100, false, base)
	insert("R2", 7, 200, false, base.Add(30*time.Second))
	insert("R3", 7, 400, true, base.Add(40*time.Second)) // shadow: excluded
	insert("R4", 8, 800, false, base.Add(time.Hour))     // other key, outside window

	from, to := base, base.Add(time.Minute)

	got, err := s.Usage().SumCostByKeyBetween(ctxBg, 7, from, to)
	require.NoError(t, err)
	assert.Equal(t, int64(300), got, "shadow rows are excluded")

	got, err = s.Usage().SumCostByAccountBetween(ctxBg, acct.ID, from, to)
	require.NoError(t, err)
	assert.Equal(t, int64(300), got)

	got, err = s.Usage().SumCostGlobalBetween(ctxBg, from, to)
	require.NoError(t, err)
	assert.Equal(t, int64(300), got)

	// Half-open interval: [from,to) with to == from is empty.
	got, err = s.Usage().SumCostByKeyBetween(ctxBg, 7, base, base)
	require.NoError(t, err)
	assert.Equal(t, int64(0), got)
}

// --------------------------------------------------------------------------
// Subscription semantics
// --------------------------------------------------------------------------

func TestSubscriptionCreateIdempotent(t *testing.T) {
	s, _ := newStore(t)
	acct := mustAccount(t, s, "site-a", 1)

	sub := &domain.Subscription{
		AccountID:   acct.ID,
		V10HostID:   55,
		PlanID:      1,
		QuotaTotal:  10_000,
		BillingMode: domain.BillingSubscription,
	}
	out, created, err := s.Subscriptions().Create(ctxBg, sub)
	require.NoError(t, err)
	assert.True(t, created)
	require.NotZero(t, out.ID)
	assert.Equal(t, domain.SubActive, out.Status, "status defaults to active")

	again, created, err := s.Subscriptions().Create(ctxBg, &domain.Subscription{
		AccountID: acct.ID, V10HostID: 55, QuotaTotal: 999_999,
	})
	require.NoError(t, err)
	assert.False(t, created, "duplicate v10_host_id must not create")
	assert.Equal(t, out.ID, again.ID)
	assert.Equal(t, int64(10_000), again.QuotaTotal, "existing row is returned unchanged")

	byHost, err := s.Subscriptions().GetByV10Host(ctxBg, 55)
	require.NoError(t, err)
	assert.Equal(t, out.ID, byHost.ID)

	all, err := s.Subscriptions().ListByAccount(ctxBg, acct.ID)
	require.NoError(t, err)
	assert.Len(t, all, 1)
}

func TestSubscriptionCreateDuplicateConcurrent(t *testing.T) {
	s, _ := newStore(t)
	acct := mustAccount(t, s, "site-a", 1)

	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	createdCount := 0
	ids := make(map[uint64]struct{})
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			out, created, err := s.Subscriptions().Create(ctxBg, &domain.Subscription{
				AccountID: acct.ID, V10HostID: 77, QuotaTotal: 100,
			})
			if err != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if created {
				createdCount++
			}
			ids[out.ID] = struct{}{}
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, createdCount)
	assert.Len(t, ids, 1)
}

func TestSubscriptionCreateRequiresHostID(t *testing.T) {
	s, _ := newStore(t)
	_, _, err := s.Subscriptions().Create(ctxBg, &domain.Subscription{})
	require.Error(t, err)
	var e *errs.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, errs.CodeInvalidRequest, e.Code)
}

func TestAddUsageConcurrent(t *testing.T) {
	s, _ := newStore(t)
	acct := mustAccount(t, s, "site-a", 1)
	sub, _, err := s.Subscriptions().Create(ctxBg, &domain.Subscription{
		AccountID: acct.ID, V10HostID: 1, QuotaTotal: 1_000_000,
	})
	require.NoError(t, err)

	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, err := s.Subscriptions().AddUsage(ctxBg, sub.ID, 1)
			assert.NoError(t, err)
		}()
	}
	wg.Wait()

	got, err := s.Subscriptions().GetByID(ctxBg, sub.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(n), got.QuotaUsed, "no lost updates")
	assert.Equal(t, int64(1_000_000-n), got.Remaining())
}

func TestAddUsageUnknownSubscription(t *testing.T) {
	s, _ := newStore(t)
	_, err := s.Subscriptions().AddUsage(ctxBg, 42, 1)
	require.Error(t, err)
}

func TestResetPeriodAndListExpired(t *testing.T) {
	s, fake := newStore(t)
	acct := mustAccount(t, s, "site-a", 1)
	base := fake.Now()

	sub, _, err := s.Subscriptions().Create(ctxBg, &domain.Subscription{
		AccountID: acct.ID, V10HostID: 3,
		PeriodStart: base.Add(-24 * time.Hour), PeriodEnd: base.Add(-time.Hour),
		QuotaTotal: 100, QuotaUsed: 100,
	})
	require.NoError(t, err)

	expired, err := s.Subscriptions().ListExpired(ctxBg, base)
	require.NoError(t, err)
	require.Len(t, expired, 1)

	require.NoError(t, s.Subscriptions().ResetPeriod(ctxBg, sub.ID, base, base.Add(30*24*time.Hour), 5_000))
	got, err := s.Subscriptions().GetByID(ctxBg, sub.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), got.QuotaUsed, "renewal clears usage")
	assert.Equal(t, int64(5_000), got.QuotaTotal)

	expired, err = s.Subscriptions().ListExpired(ctxBg, base)
	require.NoError(t, err)
	assert.Empty(t, expired)
}

// --------------------------------------------------------------------------
// API keys
// --------------------------------------------------------------------------

func TestKeysBySubscription(t *testing.T) {
	s, _ := newStore(t)
	acct := mustAccount(t, s, "site-a", 1)

	for i, hash := range []string{"h1", "h2", "h3"} {
		require.NoError(t, s.Keys().Create(ctxBg, &domain.APIKey{
			AccountID: acct.ID, SubscriptionID: 10, KeyHash: hash, Name: "k" + string(rune('0'+i)),
		}))
	}
	// A key of another subscription must not be touched.
	require.NoError(t, s.Keys().Create(ctxBg, &domain.APIKey{
		AccountID: acct.ID, SubscriptionID: 99, KeyHash: "other",
	}))

	n, err := s.Keys().CountBySubscription(ctxBg, 10)
	require.NoError(t, err)
	assert.Equal(t, 3, n)

	keys, err := s.Keys().ListBySubscription(ctxBg, 10)
	require.NoError(t, err)
	assert.Len(t, keys, 3)

	revoked, err := s.Keys().RevokeBySubscription(ctxBg, 10)
	require.NoError(t, err)
	assert.Equal(t, 3, revoked)

	revoked, err = s.Keys().RevokeBySubscription(ctxBg, 10)
	require.NoError(t, err)
	assert.Equal(t, 0, revoked, "already-revoked keys are skipped")

	n, err = s.Keys().CountBySubscription(ctxBg, 10)
	require.NoError(t, err)
	assert.Zero(t, n)

	other, err := s.Keys().GetByHash(ctxBg, "other")
	require.NoError(t, err)
	assert.Equal(t, domain.KeyActive, other.Status)
}

func TestKeysSetStatusBySubscription(t *testing.T) {
	s, _ := newStore(t)
	acct := mustAccount(t, s, "site-a", 1)
	require.NoError(t, s.Keys().Create(ctxBg, &domain.APIKey{
		AccountID: acct.ID, SubscriptionID: 5, KeyHash: "a",
	}))

	n, err := s.Keys().SetStatusBySubscription(ctxBg, 5, domain.KeySuspended)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	n, err = s.Keys().SetStatusBySubscription(ctxBg, 5, domain.KeySuspended)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "already in the target status is not counted")

	// A revoked key is never resurrected.
	k, err := s.Keys().GetByHash(ctxBg, "a")
	require.NoError(t, err)
	assert.Equal(t, domain.KeySuspended, k.Status)

	_, err = s.Keys().RevokeBySubscription(ctxBg, 5)
	require.NoError(t, err)

	n, err = s.Keys().SetStatusBySubscription(ctxBg, 5, domain.KeyActive)
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	k, err = s.Keys().GetByHash(ctxBg, "a")
	require.NoError(t, err)
	assert.Equal(t, domain.KeyRevoked, k.Status)
}

func TestKeysCreateDuplicateHashAndListOrder(t *testing.T) {
	s, _ := newStore(t)
	acct := mustAccount(t, s, "site-a", 1)

	require.NoError(t, s.Keys().Create(ctxBg, &domain.APIKey{AccountID: acct.ID, KeyHash: "dup"}))
	err := s.Keys().Create(ctxBg, &domain.APIKey{AccountID: acct.ID, KeyHash: "dup"})
	require.Error(t, err)

	require.NoError(t, s.Keys().Create(ctxBg, &domain.APIKey{AccountID: acct.ID, KeyHash: "second", Name: "2"}))
	keys, err := s.Keys().ListByAccount(ctxBg, acct.ID)
	require.NoError(t, err)
	require.Len(t, keys, 2)
	assert.Equal(t, "second", keys[0].KeyHash, "newest first")
}

// --------------------------------------------------------------------------
// Channels and models
// --------------------------------------------------------------------------

func TestChannelsListByModel(t *testing.T) {
	s, _ := newStore(t)
	create := func(name string, status int8, models []string, modelMap map[string]string) *domain.Channel {
		t.Helper()
		c := &domain.Channel{Name: name, Provider: "openai", Status: status, Models: models, ModelMap: modelMap}
		require.NoError(t, s.Channels().Create(ctxBg, c))
		return c
	}
	create("enabled-direct", 1, []string{"gpt-4o"}, nil)
	create("enabled-via-map", 1, nil, map[string]string{"gpt-4o": "upstream-4o"})
	create("disabled", 0, []string{"gpt-4o"}, nil)
	create("other-model", 1, []string{"claude-3"}, nil)

	got, err := s.Channels().ListByModel(ctxBg, "gpt-4o")
	require.NoError(t, err)
	require.Len(t, got, 2, "only Status==1 channels serving the model")
	names := []string{got[0].Name, got[1].Name}
	assert.Equal(t, []string{"enabled-direct", "enabled-via-map"}, names)
}

func TestChannelHealth(t *testing.T) {
	s, _ := newStore(t)
	c := &domain.Channel{Name: "c1", Status: 1}
	require.NoError(t, s.Channels().Create(ctxBg, c))

	h, err := s.Channels().GetHealth(ctxBg, c.ID)
	require.NoError(t, err)
	assert.Equal(t, c.ID, h.ChannelID)
	assert.False(t, h.HalfOpen)

	require.NoError(t, s.Channels().SaveHealth(ctxBg, &domain.ChannelHealth{
		ChannelID: c.ID, OpenUntil: time.Now().Add(time.Minute), Samples: 12, ErrorRate: 0.5,
	}))
	h, err = s.Channels().GetHealth(ctxBg, c.ID)
	require.NoError(t, err)
	assert.Equal(t, 12, h.Samples)
	assert.False(t, h.UpdatedAt.IsZero())

	_, err = s.Channels().GetHealth(ctxBg, 404)
	require.Error(t, err)
}

func TestChannelDeleteRemovesHealth(t *testing.T) {
	s, _ := newStore(t)
	c := &domain.Channel{Name: "c1", Status: 1}
	require.NoError(t, s.Channels().Create(ctxBg, c))
	require.NoError(t, s.Channels().SaveHealth(ctxBg, &domain.ChannelHealth{ChannelID: c.ID}))

	require.NoError(t, s.Channels().Delete(ctxBg, c.ID))
	_, err := s.Channels().GetByID(ctxBg, c.ID)
	require.Error(t, err)
	_, err = s.Channels().GetHealth(ctxBg, c.ID)
	require.Error(t, err)
}

func TestModelsUpsertAndGroups(t *testing.T) {
	s, _ := newStore(t)

	m1, err := s.Models().Upsert(ctxBg, &domain.Model{ModelName: "gpt-4o", GroupName: "default", ModelRatio: 1})
	require.NoError(t, err)
	require.NotZero(t, m1.ID)

	m1b, err := s.Models().Upsert(ctxBg, &domain.Model{ModelName: "gpt-4o", GroupName: "vip", ModelRatio: 2})
	require.NoError(t, err)
	assert.Equal(t, m1.ID, m1b.ID, "upsert is keyed by model_name")
	assert.Equal(t, 2.0, m1b.ModelRatio)

	_, err = s.Models().Upsert(ctxBg, &domain.Model{ModelName: "claude", GroupName: "vip"})
	require.NoError(t, err)

	all, err := s.Models().ListByGroup(ctxBg, "")
	require.NoError(t, err)
	assert.Len(t, all, 2)

	all, err = s.Models().ListByGroup(ctxBg, "default")
	require.NoError(t, err)
	assert.Len(t, all, 2, "\"default\" means all")

	vip, err := s.Models().ListByGroup(ctxBg, "vip")
	require.NoError(t, err)
	require.Len(t, vip, 2)

	require.NoError(t, s.Models().Delete(ctxBg, "claude"))
	_, err = s.Models().GetByName(ctxBg, "claude")
	require.Error(t, err)
}

// --------------------------------------------------------------------------
// Flags, admins, audit, accounts
// --------------------------------------------------------------------------

func TestFlagsScopeFallback(t *testing.T) {
	s, _ := newStore(t)
	require.NoError(t, s.Flags().Set(ctxBg, &domain.FlagValue{Key: "gateway_enabled", Value: true, Scope: ""}))

	got, err := s.Flags().Get(ctxBg, "gateway_enabled", "")
	require.NoError(t, err)
	assert.True(t, got.Value)

	got, err = s.Flags().Get(ctxBg, "gateway_enabled", "account:12")
	require.NoError(t, err)
	assert.True(t, got.Value, "falls back to the global value")

	require.NoError(t, s.Flags().Set(ctxBg, &domain.FlagValue{Key: "gateway_enabled", Value: false, Scope: "account:12"}))
	got, err = s.Flags().Get(ctxBg, "gateway_enabled", "account:12")
	require.NoError(t, err)
	assert.False(t, got.Value, "exact scope wins")

	_, err = s.Flags().Get(ctxBg, "missing", "")
	require.Error(t, err)

	list, err := s.Flags().List(ctxBg)
	require.NoError(t, err)
	assert.Len(t, list, 2)
}

func TestAccountsUpsertAndCount(t *testing.T) {
	s, _ := newStore(t)
	a := mustAccount(t, s, "site-a", 7)
	a.Email = "changed@example.com"
	a.BalanceCredit = 42

	out, err := s.Accounts().Upsert(ctxBg, a)
	require.NoError(t, err)
	assert.Equal(t, a.ID, out.ID, "upsert matches (site_id, v10_client_id)")

	n, err := s.Accounts().Count(ctxBg)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	byClient, err := s.Accounts().GetByV10Client(ctxBg, "site-a", 7)
	require.NoError(t, err)
	assert.Equal(t, int64(42), byClient.BalanceCredit)

	list, err := s.Accounts().List(ctxBg, 0, 0)
	require.NoError(t, err)
	assert.Len(t, list, 1)

	// The same client id on another site is a different tenant.
	other := mustAccount(t, s, "site-b", 7)
	assert.NotEqual(t, a.ID, other.ID)
}

func TestAdminCreateAndLookup(t *testing.T) {
	s, _ := newStore(t)
	a := &domain.AdminUser{Username: "root", Role: "admin", Status: 1}
	require.NoError(t, s.Admins().Create(ctxBg, a))
	assert.NotZero(t, a.ID)

	err := s.Admins().Create(ctxBg, &domain.AdminUser{Username: "root"})
	require.Error(t, err)

	got, err := s.Admins().GetByUsername(ctxBg, "root")
	require.NoError(t, err)
	assert.Equal(t, a.ID, got.ID)

	got, err = s.Admins().GetByID(ctxBg, a.ID)
	require.NoError(t, err)
	assert.Equal(t, "admin", got.Role)

	n, err := s.Admins().Count(ctxBg)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
}

func TestAuditAppendAndPaginate(t *testing.T) {
	s, _ := newStore(t)
	for i := 0; i < 3; i++ {
		require.NoError(t, s.Audit().Append(ctxBg, &domain.AuditLog{Action: "update", ActorID: "1"}))
	}
	rows, err := s.Audit().List(ctxBg, 0, 0)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	assert.Greater(t, rows[0].ID, rows[2].ID, "newest first")

	rows, err = s.Audit().List(ctxBg, 1, 1)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, uint64(2), rows[0].ID)
}

func TestSiteCreateAndList(t *testing.T) {
	s, _ := newStore(t)
	site := &domain.Site{AppID: "app-1", Name: "one", Enabled: true, IPAllow: []string{"10.0.0.0/8"}}
	require.NoError(t, s.Sites().Create(ctxBg, site))
	assert.NotZero(t, site.ID)

	err := s.Sites().Create(ctxBg, &domain.Site{AppID: "app-1"})
	require.Error(t, err)

	got, err := s.Sites().GetByAppID(ctxBg, "app-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"10.0.0.0/8"}, got.IPAllow)

	list, err := s.Sites().List(ctxBg)
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

func TestPlanUpsertByIdentifiers(t *testing.T) {
	s, _ := newStore(t)
	p, err := s.Plans().Upsert(ctxBg, &domain.Plan{PlanCode: "pro", Name: "Pro", QuotaTokens: 100})
	require.NoError(t, err)
	require.NotZero(t, p.ID)

	p2, err := s.Plans().Upsert(ctxBg, &domain.Plan{PlanCode: "pro", Name: "Pro v2", QuotaTokens: 200})
	require.NoError(t, err)
	assert.Equal(t, p.ID, p2.ID)

	got, err := s.Plans().GetByCode(ctxBg, "pro")
	require.NoError(t, err)
	assert.Equal(t, int64(200), got.QuotaTokens)

	list, err := s.Plans().List(ctxBg)
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

// --------------------------------------------------------------------------
// Deep-copy isolation
// --------------------------------------------------------------------------

func TestDeepCopyIsolation(t *testing.T) {
	s, _ := newStore(t)
	acct := mustAccount(t, s, "site-a", 1)

	// Models: mutating what we wrote and what we read must not leak.
	m := &domain.Model{ModelName: "gpt-4o", Capabilities: []string{"vision"}}
	_, err := s.Models().Upsert(ctxBg, m)
	require.NoError(t, err)
	m.Capabilities[0] = "mutated"
	got, err := s.Models().GetByName(ctxBg, "gpt-4o")
	require.NoError(t, err)
	assert.Equal(t, []string{"vision"}, got.Capabilities, "write side is copied")

	got.Capabilities[0] = "hacked"
	again, err := s.Models().GetByName(ctxBg, "gpt-4o")
	require.NoError(t, err)
	assert.Equal(t, []string{"vision"}, again.Capabilities, "read side is copied")

	// API keys: Models and IPWhitelist.
	k := &domain.APIKey{
		AccountID: acct.ID, KeyHash: "h", Models: []string{"gpt-4o"}, IPWhitelist: []string{"1.1.1.1/32"},
	}
	require.NoError(t, s.Keys().Create(ctxBg, k))
	k.Models[0] = "x"
	k.IPWhitelist[0] = "6.6.6.6/32"
	key, err := s.Keys().GetByHash(ctxBg, "h")
	require.NoError(t, err)
	assert.Equal(t, []string{"gpt-4o"}, key.Models)
	assert.Equal(t, []string{"1.1.1.1/32"}, key.IPWhitelist)

	key.Models[0] = "y"
	key2, err := s.Keys().GetByHash(ctxBg, "h")
	require.NoError(t, err)
	assert.Equal(t, []string{"gpt-4o"}, key2.Models)

	// Channels: KeyPool, Models, ModelMap.
	c := &domain.Channel{
		Name: "c", Status: 1,
		KeyPool:  []domain.ChannelKey{{Secret: "s1", Weight: 1}},
		Models:   []string{"gpt-4o"},
		ModelMap: map[string]string{"gpt-4o": "up"},
	}
	require.NoError(t, s.Channels().Create(ctxBg, c))
	c.KeyPool[0].Secret = "leaked"
	c.Models[0] = "leaked"
	c.ModelMap["gpt-4o"] = "leaked"

	ch, err := s.Channels().GetByID(ctxBg, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "s1", ch.KeyPool[0].Secret)
	assert.Equal(t, []string{"gpt-4o"}, ch.Models)
	assert.Equal(t, "up", ch.ModelMap["gpt-4o"])

	ch.KeyPool[0].Secret = "leaked2"
	ch.ModelMap["gpt-4o"] = "leaked2"
	ch2, err := s.Channels().GetByID(ctxBg, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "s1", ch2.KeyPool[0].Secret)
	assert.Equal(t, "up", ch2.ModelMap["gpt-4o"])

	// Sites: IPAllow.
	require.NoError(t, s.Sites().Create(ctxBg, &domain.Site{AppID: "a", IPAllow: []string{"10.0.0.1"}}))
	site, err := s.Sites().GetByAppID(ctxBg, "a")
	require.NoError(t, err)
	site.IPAllow[0] = "9.9.9.9"
	site, err = s.Sites().GetByAppID(ctxBg, "a")
	require.NoError(t, err)
	assert.Equal(t, []string{"10.0.0.1"}, site.IPAllow)

	// Usage rows returned by List are copies.
	_, err = s.Usage().Insert(ctxBg, &domain.UsageRecord{RequestID: "R", AccountID: acct.ID, CostMicros: 5})
	require.NoError(t, err)
	rows, err := s.Usage().List(ctxBg, store.UsageFilter{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	rows[0].CostMicros = 999
	rows, err = s.Usage().List(ctxBg, store.UsageFilter{})
	require.NoError(t, err)
	assert.Equal(t, int64(5), rows[0].CostMicros)

	// Ledger entries returned by List are copies.
	requireLedgerCredit(t, s, acct.ID, domain.LedgerAdminAdjust, 1, "adjust", "1")
	entries, err := s.Ledger().List(ctxBg, store.LedgerFilter{AccountID: acct.ID})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	entries[0].AmountMicros = 999
	entries, err = s.Ledger().List(ctxBg, store.LedgerFilter{AccountID: acct.ID})
	require.NoError(t, err)
	assert.Equal(t, int64(1), entries[0].AmountMicros)
}

// requireLedgerCredit applies one credit entry and fails the test on error.
func requireLedgerCredit(t *testing.T, s *Store, accountID uint64, typ domain.LedgerType, amount int64, refType, refID string) {
	t.Helper()
	applied, _, err := s.Ledger().Credit(ctxBg, &domain.CreditLedgerEntry{
		AccountID: accountID, Type: typ, AmountMicros: amount, RefType: refType, RefID: refID,
	})
	require.NoError(t, err)
	require.True(t, applied)
}

// --------------------------------------------------------------------------
// Aggregates
// --------------------------------------------------------------------------

func TestAggregatesUpsertAccumulates(t *testing.T) {
	s, _ := newStore(t)
	require.NoError(t, s.Aggregates().UpsertDaily(ctxBg, &domain.DailyUsage{
		Day: "2026-03-01", AccountID: 1, V10HostID: 2, APIKeyID: 3, Model: "gpt-4o",
		Requests: 1, PromptTokens: 10, CompletionTokens: 20, CostMicros: 100,
	}))
	require.NoError(t, s.Aggregates().UpsertDaily(ctxBg, &domain.DailyUsage{
		Day: "2026-03-01", AccountID: 1, V10HostID: 2, APIKeyID: 3, Model: "gpt-4o",
		Requests: 2, PromptTokens: 1, CompletionTokens: 2, CostMicros: 50,
	}))

	rows, err := s.Aggregates().ListDaily(ctxBg, store.UsageFilter{})
	require.NoError(t, err)
	require.Len(t, rows, 1, "same (day,account,host,key,model) accumulates into one row")
	assert.Equal(t, int64(3), rows[0].Requests)
	assert.Equal(t, int64(11), rows[0].PromptTokens)
	assert.Equal(t, int64(22), rows[0].CompletionTokens)
	assert.Equal(t, int64(150), rows[0].CostMicros)

	// A different model is a different row.
	require.NoError(t, s.Aggregates().UpsertDaily(ctxBg, &domain.DailyUsage{
		Day: "2026-03-01", AccountID: 1, V10HostID: 2, APIKeyID: 3, Model: "gpt-4o-mini", Requests: 1,
	}))
	rows, err = s.Aggregates().ListDaily(ctxBg, store.UsageFilter{})
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}

func TestAggregatesBucketCostOneRowPerDay(t *testing.T) {
	s, _ := newStore(t)
	for _, d := range []struct {
		day   string
		model string
		cost  int64
	}{
		{"2026-03-01", "a", 10},
		{"2026-03-01", "b", 20},
		{"2026-03-02", "a", 5},
	} {
		require.NoError(t, s.Aggregates().UpsertDaily(ctxBg, &domain.DailyUsage{
			Day: d.day, AccountID: 1, Model: d.model, Requests: 1, CostMicros: d.cost,
		}))
	}

	buckets, err := s.Aggregates().BucketCost(ctxBg, store.UsageFilter{})
	require.NoError(t, err)
	require.Len(t, buckets, 2)
	assert.Equal(t, "2026-03-01", buckets[0].Day)
	assert.Equal(t, int64(30), buckets[0].CostMicros)
	assert.Equal(t, "2026-03-02", buckets[1].Day)
	assert.Equal(t, int64(5), buckets[1].CostMicros)
}

func TestAggregatesRebuildDaily(t *testing.T) {
	s, _ := newStore(t)
	acct := mustAccount(t, s, "site-a", 1)
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	insert := func(req string, model string, cost int64, shadow bool, at time.Time) {
		t.Helper()
		_, err := s.Usage().Insert(ctxBg, &domain.UsageRecord{
			RequestID: req, AccountID: acct.ID, V10HostID: 9, APIKeyID: 4,
			Model: model, CostMicros: cost, Shadow: shadow, CreatedAt: at,
			PromptTokens: 1, CompletionTokens: 2,
		})
		require.NoError(t, err)
	}
	insert("R1", "gpt-4o", 10, false, day.Add(time.Hour))
	insert("R2", "gpt-4o", 20, false, day.Add(2*time.Hour))
	insert("R3", "gpt-4o", 100, true, day.Add(3*time.Hour)) // shadow: excluded
	insert("R4", "gpt-4o-mini", 5, false, day.Add(4*time.Hour))
	insert("R5", "gpt-4o", 999, false, day.Add(48*time.Hour)) // other day

	// Seed a stale rollup that RebuildDaily must replace.
	require.NoError(t, s.Aggregates().UpsertDaily(ctxBg, &domain.DailyUsage{
		Day: "2026-03-01", AccountID: acct.ID, Model: "stale", Requests: 77, CostMicros: 777,
	}))
	require.NoError(t, s.Aggregates().RebuildDaily(ctxBg, day))

	rows, err := s.Aggregates().ListDaily(ctxBg, store.UsageFilter{})
	require.NoError(t, err)

	byModel := make(map[string]domain.DailyUsage, len(rows))
	for _, r := range rows {
		byModel[r.Model] = r
	}
	_, stale := byModel["stale"]
	assert.False(t, stale, "stale rollups of that day are dropped")

	gpt, ok := byModel["gpt-4o"]
	require.True(t, ok)
	assert.Equal(t, int64(2), gpt.Requests, "shadow rows never contribute")
	assert.Equal(t, int64(30), gpt.CostMicros)
	assert.Equal(t, int64(2), gpt.PromptTokens)

	mini, ok := byModel["gpt-4o-mini"]
	require.True(t, ok)
	assert.Equal(t, int64(1), mini.Requests)

	assert.Len(t, rows, 2, "the other day was never rebuilt")
}

func TestAggregatesFilterByDayRange(t *testing.T) {
	s, _ := newStore(t)
	for _, day := range []string{"2026-02-28", "2026-03-01", "2026-03-02"} {
		require.NoError(t, s.Aggregates().UpsertDaily(ctxBg, &domain.DailyUsage{
			Day: day, AccountID: 1, Model: "m", Requests: 1,
		}))
	}
	rows, err := s.Aggregates().ListDaily(ctxBg, store.UsageFilter{
		From: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "2026-03-01", rows[0].Day)
}

// --------------------------------------------------------------------------
// Misc contract checks
// --------------------------------------------------------------------------

func TestPingAndClose(t *testing.T) {
	s := New()
	require.NoError(t, s.Ping(ctxBg))
	require.NoError(t, s.Close())
	require.NoError(t, s.Close(), "Close is idempotent")

	// The store stays usable after Close: it holds no external resource.
	a := mustAccount(t, s, "site-a", 1)
	assert.NotZero(t, a.ID)
}

func TestContextCancellationIsHonoured(t *testing.T) {
	s, _ := newStore(t)
	ctx, cancel := context.WithCancel(ctxBg)
	cancel()

	_, err := s.Accounts().GetByID(ctx, 1)
	require.Error(t, err)
	_, _, err = s.Ledger().Credit(ctx, &domain.CreditLedgerEntry{AccountID: 1, RefType: "t", RefID: "1"})
	require.Error(t, err)
}

func TestAutoIncrementIDsAreUnique(t *testing.T) {
	s, _ := newStore(t)
	seen := make(map[uint64]struct{})
	for i := 0; i < 50; i++ {
		c := &domain.Channel{Name: "c"}
		require.NoError(t, s.Channels().Create(ctxBg, c))
		_, dup := seen[c.ID]
		assert.False(t, dup, "channel id %d reused", c.ID)
		seen[c.ID] = struct{}{}
	}
}
