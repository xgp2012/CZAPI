package quota

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"czapi/internal/billing"
	"czapi/internal/cache"
	"czapi/internal/cache/memcache"
	"czapi/internal/domain"
	"czapi/internal/metrics"
	"czapi/internal/pkg/errs"
	"czapi/internal/queue/memqueue"
	"czapi/internal/store/memstore"
)

// fixedTime is the reference instant for deterministic windows.
var fixedTime = time.Date(2026, 11, 5, 7, 0, 0, 0, time.UTC)

// testClock is a movable clock.
type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }

// harness bundles the quota service with its collaborators.
type harness struct {
	svc   *Service
	store *memstore.Store
	cache *memcache.Cache
	queue *memqueue.Queue
	clk   *testClock
	eng   billing.Engine
}

// newHarness builds an isolated service backed by the in-memory drivers.
func newHarness(t *testing.T) *harness {
	t.Helper()
	clk := &testClock{now: fixedTime}
	st := memstore.NewWithClock(clk)
	ce := memcache.NewWithClock(clk, time.Minute)
	q := memqueue.NewWithClock(clk)
	mx := metrics.New()
	eng := billing.NewEngine(billing.EngineConfig{TokenToMicros: 1})

	svc := New(st, ce, q, eng, clk, mx, Config{
		EnforceBilling:       true,
		KeyDailyMicros:       50_000_000,  // 50 yuan/day
		AccountMonthlyMicros: 500_000_000, // 500 yuan/month
		GlobalDailyMicros:    0,           // disabled
	})
	return &harness{svc: svc, store: st, cache: ce, queue: q, clk: clk, eng: eng}
}

// seedPlan creates a plan with a known quota and ratio.
func (h *harness) seedPlan(t *testing.T, code string, quotaTokens int64) *domain.Plan {
	t.Helper()
	p, err := h.store.Plans().Upsert(context.Background(), &domain.Plan{
		PlanCode: code, Name: code, QuotaTokens: quotaTokens, Period: "month",
		RPMLimit: 60, TPMLimit: 100000, Concurrency: 5, ModelGroup: "default",
		MaxKeys: 5, GroupRatio: 1, Status: 1,
	})
	require.NoError(t, err)
	return p
}

// seedAccount creates an account with a balance.
func (h *harness) seedAccount(t *testing.T, clientID uint64, balanceMicros int64) *domain.Account {
	t.Helper()
	acc, err := h.store.Accounts().Upsert(context.Background(), &domain.Account{
		SiteID: "app-1", V10ClientID: clientID, Email: "u@example.com",
		Status: domain.AccountEnabled, BalanceCredit: balanceMicros,
		CreatedAt: fixedTime, UpdatedAt: fixedTime,
	})
	require.NoError(t, err)
	return acc
}

// seedSubscription creates an active subscription.
func (h *harness) seedSubscription(t *testing.T, accountID, hostID, planID uint64, quota int64, mode domain.BillingMode) *domain.Subscription {
	t.Helper()
	sub, _, err := h.store.Subscriptions().Create(context.Background(), &domain.Subscription{
		AccountID: accountID, V10HostID: hostID, PlanID: planID,
		PeriodStart: fixedTime, PeriodEnd: fixedTime.AddDate(0, 1, 0),
		QuotaTotal: quota, BillingMode: mode, Status: domain.SubActive,
		CreatedAt: fixedTime, UpdatedAt: fixedTime,
	})
	require.NoError(t, err)
	return sub
}

// price is a simple one-to-one price table.
func (h *harness) price() billing.Price {
	return billing.Price{ModelRatio: 1, GroupRatio: 1, CompletionRatio: 1, CachedRatio: 0.5}
}

// ---------------------------------------------------------------------------
// Credit
// ---------------------------------------------------------------------------

func TestCreditRequiresReference(t *testing.T) {
	h := newHarness(t)
	acc := h.seedAccount(t, 1001, 0)

	// Every ledger entry must carry (ref_type, ref_id) so a V10 retry can be
	// deduplicated; without one the credit is rejected outright.
	_, err := h.svc.Credit(context.Background(), billing.CreditRequest{
		SiteID: "app-1", V10ClientID: acc.V10ClientID,
		Kind: domain.LedgerV10Order, AmountMicros: 10_000_000,
	})
	require.Error(t, err)
	e := errs.As(err)
	require.NotNil(t, e)
	assert.Equal(t, errs.CodeInvalidRequest, e.Code)

	_, err = h.svc.Credit(context.Background(), billing.CreditRequest{
		SiteID: "app-1", V10ClientID: acc.V10ClientID,
		Kind: domain.LedgerV10Order, AmountMicros: 10_000_000, RefType: "v10_order",
	})
	require.Error(t, err, "a missing ref_id must also be rejected")
}

func TestCreditAppliesOncePerReference(t *testing.T) {
	h := newHarness(t)
	h.seedAccount(t, 1002, 0)

	req := billing.CreditRequest{
		SiteID: "app-1", V10ClientID: 1002,
		Kind: domain.LedgerV10Order, AmountMicros: 30_000_000,
		RefType: "v10_order", RefID: "ORD-1",
	}

	first, err := h.svc.Credit(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, first.Applied)
	assert.Equal(t, int64(30_000_000), first.BalanceAfter)

	// A retried webhook must not double-credit the customer.
	second, err := h.svc.Credit(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, second.Applied, "the second credit must be a no-op")
	assert.Equal(t, int64(30_000_000), second.BalanceAfter, "the balance must not move again")

	// A different reference does apply.
	req.RefID = "ORD-2"
	third, err := h.svc.Credit(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, third.Applied)
	assert.Equal(t, int64(60_000_000), third.BalanceAfter)
}

func TestCreditRejectsUnknownKind(t *testing.T) {
	h := newHarness(t)
	h.seedAccount(t, 1003, 0)

	_, err := h.svc.Credit(context.Background(), billing.CreditRequest{
		SiteID: "app-1", V10ClientID: 1003,
		Kind: domain.LedgerType("not_a_kind"), AmountMicros: 1,
		RefType: "test", RefID: "1",
	})
	require.Error(t, err)
	assert.Equal(t, errs.CodeInvalidRequest, errs.As(err).Code)
}

func TestCreditRejectsZeroAmount(t *testing.T) {
	h := newHarness(t)
	h.seedAccount(t, 1004, 0)

	_, err := h.svc.Credit(context.Background(), billing.CreditRequest{
		SiteID: "app-1", V10ClientID: 1004,
		Kind: domain.LedgerV10Order, AmountMicros: 0,
		RefType: "v10_order", RefID: "ORD-0",
	})
	require.Error(t, err)
	assert.Equal(t, errs.CodeInvalidRequest, errs.As(err).Code)
}

func TestRefundMayPushBalanceNegative(t *testing.T) {
	h := newHarness(t)
	acc := h.seedAccount(t, 1005, 5_000_000)

	res, err := h.svc.Credit(context.Background(), billing.CreditRequest{
		SiteID: "app-1", V10ClientID: acc.V10ClientID,
		Kind: domain.LedgerRefund, AmountMicros: -8_000_000,
		RefType: "v10_refund", RefID: "RF-1", Remark: "chargeback",
	})
	require.NoError(t, err, "a refund must be allowed to create a debt")
	assert.True(t, res.Applied)
	assert.Equal(t, int64(-3_000_000), res.BalanceAfter, "the debt must be visible")
}

// ---------------------------------------------------------------------------
// Prehold
// ---------------------------------------------------------------------------

func TestPreholdRejectsInsufficientSubscription(t *testing.T) {
	h := newHarness(t)
	plan := h.seedPlan(t, "tiny", 1_000) // 1000 tokens
	acc := h.seedAccount(t, 1007, 0)
	sub := h.seedSubscription(t, acc.ID, 9001, plan.ID, plan.QuotaTokens, domain.BillingSubscription)

	_, err := h.svc.Prehold(context.Background(), billing.PreholdRequest{
		AccountID: acc.ID, APIKeyID: 1, SubscriptionID: sub.ID, V10HostID: sub.V10HostID,
		Model: "m", GroupRatio: 1, EstimateMicros: 50_000_000,
		BillingMode: domain.BillingSubscription, Plan: plan,
	})
	require.Error(t, err)
	assert.Equal(t, errs.CodeInsufficientQuota, errs.As(err).Code)
}

func TestPreholdRejectsEmptyBalance(t *testing.T) {
	h := newHarness(t)
	plan := h.seedPlan(t, "balance", 0)
	acc := h.seedAccount(t, 1008, 0) // no money

	_, err := h.svc.Prehold(context.Background(), billing.PreholdRequest{
		AccountID: acc.ID, APIKeyID: 1, Model: "m", GroupRatio: 1,
		EstimateMicros: 1_000_000, BillingMode: domain.BillingBalance, Plan: plan,
	})
	require.Error(t, err)
	assert.Equal(t, errs.CodeInsufficientQuota, errs.As(err).Code)
}

func TestPreholdThenSettleChargesExactlyActual(t *testing.T) {
	h := newHarness(t)
	plan := h.seedPlan(t, "std", 1_000_000)
	acc := h.seedAccount(t, 1009, 100_000_000)
	sub := h.seedSubscription(t, acc.ID, 9002, plan.ID, plan.QuotaTokens, domain.BillingSubscription)
	ctx := context.Background()

	// Reserve for a pessimistic 1000 tokens.
	dec, err := h.svc.Prehold(ctx, billing.PreholdRequest{
		AccountID: acc.ID, APIKeyID: 1, SubscriptionID: sub.ID, V10HostID: sub.V10HostID,
		Model: "m", GroupRatio: 1, EstimateMicros: 1_000,
		BillingMode: domain.BillingSubscription, Plan: plan,
	})
	require.NoError(t, err)
	require.Equal(t, domain.PaidBySubscription, dec.Mode)

	// The request only used 100 tokens, so only 100 are charged.
	rec, err := h.svc.Settle(ctx, billing.SettleRequest{
		Decision: dec, Usage: billing.Usage{PromptTokens: 100, CompletionTokens: 0},
		Model: "m", ModelPricing: h.price(), RequestID: "req-1",
		APIKeyID: 1, AccountID: acc.ID, V10HostID: sub.V10HostID,
		StatusCode: 200, At: h.clk.Now(),
	})
	require.NoError(t, err)
	assert.Equal(t, 100, rec.PromptTokens)

	updated, err := h.store.Subscriptions().GetByID(ctx, sub.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(100), updated.QuotaUsed, "only the actual usage is deducted")
}

func TestBalanceSettlementDeductsMicros(t *testing.T) {
	h := newHarness(t)
	plan := h.seedPlan(t, "bal", 0)
	acc := h.seedAccount(t, 1010, 10_000_000)
	ctx := context.Background()

	dec, err := h.svc.Prehold(ctx, billing.PreholdRequest{
		AccountID: acc.ID, APIKeyID: 1, Model: "m", GroupRatio: 1,
		EstimateMicros: 1_000, BillingMode: domain.BillingBalance, Plan: plan,
	})
	require.NoError(t, err)
	require.Equal(t, domain.PaidByBalance, dec.Mode)

	_, err = h.svc.Settle(ctx, billing.SettleRequest{
		Decision: dec, Usage: billing.Usage{PromptTokens: 300, CompletionTokens: 200},
		Model: "m", ModelPricing: h.price(), RequestID: "req-bal",
		APIKeyID: 1, AccountID: acc.ID, StatusCode: 200, At: h.clk.Now(),
	})
	require.NoError(t, err)

	updated, err := h.store.Accounts().GetByID(ctx, acc.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(10_000_000-500), updated.BalanceCredit,
		"500 tokens at TokenToMicros=1 deducts 500 micro-yuan")
}

func TestHybridFallsBackToBalanceWhenSubscriptionExhausted(t *testing.T) {
	h := newHarness(t)
	plan := h.seedPlan(t, "hyb", 1_000)
	acc := h.seedAccount(t, 1011, 10_000_000)
	sub := h.seedSubscription(t, acc.ID, 9004, plan.ID, plan.QuotaTokens, domain.BillingHybrid)
	ctx := context.Background()

	// Exhaust the subscription quota first.
	_, err := h.store.Subscriptions().AddUsage(ctx, sub.ID, plan.QuotaTokens)
	require.NoError(t, err)

	dec, err := h.svc.Prehold(ctx, billing.PreholdRequest{
		AccountID: acc.ID, APIKeyID: 1, SubscriptionID: sub.ID, V10HostID: sub.V10HostID,
		Model: "m", GroupRatio: 1, EstimateMicros: 100,
		BillingMode: domain.BillingHybrid, Plan: plan,
	})
	require.NoError(t, err)
	assert.Equal(t, domain.PaidByBalance, dec.Mode,
		"an exhausted subscription must fall through to the balance")
}

func TestFreeModeWhenBillingDisabled(t *testing.T) {
	clk := &testClock{now: fixedTime}
	st := memstore.NewWithClock(clk)
	ce := memcache.NewWithClock(clk, time.Minute)
	eng := billing.NewEngine(billing.EngineConfig{TokenToMicros: 1})
	svc := New(st, ce, memqueue.NewWithClock(clk), eng, clk, metrics.New(), Config{
		EnforceBilling: false, // shadow / kill-switch mode
	})

	dec, err := svc.Prehold(context.Background(), billing.PreholdRequest{
		AccountID: 1, APIKeyID: 1, Model: "m", EstimateMicros: 999_999_999,
		BillingMode: domain.BillingBalance,
	})
	require.NoError(t, err, "billing disabled must never block a request")
	assert.True(t, dec.Free)
	assert.Equal(t, domain.PaidByFree, dec.Mode)
}

// ---------------------------------------------------------------------------
// Spend caps
// ---------------------------------------------------------------------------

func TestKeyDailyCapRejects(t *testing.T) {
	h := newHarness(t)
	plan := h.seedPlan(t, "cap", 0)
	acc := h.seedAccount(t, 1012, 1_000_000_000) // plenty of balance
	ctx := context.Background()

	// Seed the spend counter the way a busy day would.
	key := cache.KeyDailyKey(77, fixedTime.Format("2006-01-02"))
	require.NoError(t, h.cache.Set(ctx, key, []byte("49999999"), 48*time.Hour))

	_, err := h.svc.Prehold(ctx, billing.PreholdRequest{
		AccountID: acc.ID, APIKeyID: 77, Model: "m", GroupRatio: 1,
		EstimateMicros: 5_000_000, BillingMode: domain.BillingBalance, Plan: plan,
	})
	require.Error(t, err, "the daily cap must stop the request before forwarding")
	e := errs.As(err)
	require.NotNil(t, e)
	assert.Equal(t, errs.CodeInsufficientQuota, e.Code)
}

func TestAccountMonthlyCapRejects(t *testing.T) {
	h := newHarness(t)
	plan := h.seedPlan(t, "acc-cap", 0)
	acc := h.seedAccount(t, 1013, 10_000_000_000)
	ctx := context.Background()

	key := cache.AccountMonthlyKey(acc.ID, fixedTime.Format("2006-01"))
	require.NoError(t, h.cache.Set(ctx, key, []byte("499999999"), 40*24*time.Hour))

	_, err := h.svc.Prehold(ctx, billing.PreholdRequest{
		AccountID: acc.ID, APIKeyID: 88, Model: "m", GroupRatio: 1,
		EstimateMicros: 5_000_000, BillingMode: domain.BillingBalance, Plan: plan,
	})
	require.Error(t, err)
	assert.Equal(t, errs.CodeInsufficientQuota, errs.As(err).Code)
}

func TestCapsDisabledDoNotBlock(t *testing.T) {
	clk := &testClock{now: fixedTime}
	st := memstore.NewWithClock(clk)
	ce := memcache.NewWithClock(clk, time.Minute)
	eng := billing.NewEngine(billing.EngineConfig{TokenToMicros: 1})
	svc := New(st, ce, memqueue.NewWithClock(clk), eng, clk, metrics.New(), Config{
		EnforceBilling: true, // caps all zero => unlimited
	})
	acc, err := st.Accounts().Upsert(context.Background(), &domain.Account{
		SiteID: "app-1", V10ClientID: 1, Status: domain.AccountEnabled,
		BalanceCredit: 1_000_000,
	})
	require.NoError(t, err)

	dec, perr := svc.Prehold(context.Background(), billing.PreholdRequest{
		AccountID: acc.ID, APIKeyID: 5, Model: "m", GroupRatio: 1,
		EstimateMicros: 500_000, BillingMode: domain.BillingBalance,
	})
	require.NoError(t, perr)
	assert.Equal(t, domain.PaidByBalance, dec.Mode)
}

// ---------------------------------------------------------------------------
// Balance query
// ---------------------------------------------------------------------------

func TestBalanceReflectsCredits(t *testing.T) {
	h := newHarness(t)
	acc := h.seedAccount(t, 1014, 0)
	ctx := context.Background()

	before, err := h.svc.Balance(ctx, acc.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), before)

	_, err = h.svc.Credit(ctx, billing.CreditRequest{
		SiteID: "app-1", V10ClientID: acc.V10ClientID,
		Kind: domain.LedgerV10Order, AmountMicros: 12_345_678,
		RefType: "v10_order", RefID: "ORD-B",
	})
	require.NoError(t, err)

	after, err := h.svc.Balance(ctx, acc.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(12_345_678), after)
}

// ---------------------------------------------------------------------------
// Usage envelope round-trip
// ---------------------------------------------------------------------------

func TestUsageEnvelopeRoundTrip(t *testing.T) {
	rec := domain.UsageRecord{
		RequestID: "req-1", AccountID: 1, V10HostID: 2, APIKeyID: 3,
		Model: "m", PromptTokens: 10, CompletionTokens: 20, CostMicros: 30,
		BillingMode: domain.PaidBySubscription, StatusCode: 200, CreatedAt: fixedTime,
	}
	raw, err := marshalUsage(rec)
	require.NoError(t, err)

	back, err := UnmarshalUsage(raw)
	require.NoError(t, err)
	assert.Equal(t, rec.RequestID, back.RequestID)
	assert.Equal(t, rec.PromptTokens, back.PromptTokens)
	assert.Equal(t, rec.CostMicros, back.CostMicros)
	assert.True(t, rec.CreatedAt.Equal(back.CreatedAt))
}

func TestUnmarshalUsageRejectsGarbage(t *testing.T) {
	_, err := UnmarshalUsage([]byte("not json"))
	require.Error(t, err)
}
