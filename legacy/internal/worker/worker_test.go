package worker

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"czapi/internal/domain"
	"czapi/internal/metrics"
	"czapi/internal/pkg/log"
	"czapi/internal/queue"
	"czapi/internal/queue/memqueue"
	"czapi/internal/quota"
	"czapi/internal/store"
	"czapi/internal/store/memstore"
)

var fixedTime = time.Date(2026, 11, 5, 9, 30, 0, 0, time.UTC)

// testClock is a movable clock.
type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }

// harness bundles the consumer with its collaborators.
type harness struct {
	st  *memstore.Store
	q   *memqueue.Queue
	mx  *metrics.Registry
	clk *testClock
	c   *UsageConsumer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	clk := &testClock{now: fixedTime}
	st := memstore.NewWithClock(clk)
	q := memqueue.NewWithClock(clk)
	mx := metrics.New()
	lg := log.New("error", "text")
	c := NewUsageConsumer(st, q, mx, lg, clk, Config{IdleSleep: 5 * time.Millisecond})
	return &harness{st: st, q: q, mx: mx, clk: clk, c: c}
}

// publish puts a usage record on the queue exactly as the API would.
func (h *harness) publish(t *testing.T, rec domain.UsageRecord) {
	t.Helper()
	payload, err := marshalForTest(rec)
	require.NoError(t, err)
	require.NoError(t, h.q.Publish(context.Background(), queue.TopicUsage, payload))
}

// drain consumes until the queue is empty or the deadline passes.
func (h *harness) drain(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		n, err := h.q.Consume(context.Background(), queue.TopicUsage, 50, 10*time.Millisecond, h.c.handleMessage)
		require.NoError(t, err)
		if n == 0 {
			return
		}
	}
	t.Fatal("queue did not drain in time")
}

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

func TestConsumerPersistsUsage(t *testing.T) {
	h := newHarness(t)
	rec := domain.UsageRecord{
		RequestID: "req-1", AccountID: 1, V10HostID: 2, APIKeyID: 3,
		Model: "m", PromptTokens: 10, CompletionTokens: 5, CostMicros: 15,
		BillingMode: domain.PaidBySubscription, StatusCode: 200,
		CreatedAt: fixedTime,
	}
	h.publish(t, rec)
	h.drain(t)

	rows, err := h.st.Usage().List(context.Background(), allUsage())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "req-1", rows[0].RequestID)
	assert.Equal(t, 10, rows[0].PromptTokens)
	assert.Equal(t, int64(15), rows[0].CostMicros)

	handled, failed, _ := h.c.Stats()
	assert.Equal(t, int64(1), handled)
	assert.Equal(t, int64(0), failed)
}

func TestConsumerIsIdempotentOnRequestID(t *testing.T) {
	h := newHarness(t)
	rec := domain.UsageRecord{
		RequestID: "req-dup", AccountID: 1, APIKeyID: 3, Model: "m",
		PromptTokens: 7, CostMicros: 7, StatusCode: 200, CreatedAt: fixedTime,
	}

	// The queue is at-least-once: the same message can arrive twice. The row must
	// not be counted twice, which is what keeps the money correct.
	h.publish(t, rec)
	h.publish(t, rec)
	h.drain(t)

	rows, err := h.st.Usage().List(context.Background(), allUsage())
	require.NoError(t, err)
	assert.Len(t, rows, 1, "a duplicate delivery must not create a second row")

	// The rollup must also reflect one request, not two.
	daily, err := h.st.Aggregates().ListDaily(context.Background(), store.UsageFilter{
		From: fixedTime.Add(-time.Hour), To: fixedTime.Add(time.Hour),
	})
	require.NoError(t, err)
	total := int64(0)
	for _, d := range daily {
		total += d.Requests
	}
	assert.Equal(t, int64(1), total, "the rollup must count the request once")
}

func TestConsumerPreservesEventTime(t *testing.T) {
	// The partitioned usage table keys idempotency on (request_id, created_at),
	// so the original event time must survive the queue round trip. A rewrite to
	// "now" would silently break deduplication.
	h := newHarness(t)
	eventTime := time.Date(2026, 11, 4, 23, 59, 59, 0, time.UTC)
	h.publish(t, domain.UsageRecord{
		RequestID: "req-time", AccountID: 1, APIKeyID: 3, Model: "m",
		PromptTokens: 1, CostMicros: 1, StatusCode: 200, CreatedAt: eventTime,
	})
	h.drain(t)

	rows, err := h.st.Usage().List(context.Background(), allUsage())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.True(t, eventTime.Equal(rows[0].CreatedAt),
		"the event time must not be rewritten to the consumption time")
}

func TestConsumerDropsUndecodablePayload(t *testing.T) {
	h := newHarness(t)

	// A payload that can never succeed must be dropped, not retried forever:
	// retrying would block the topic head indefinitely.
	require.NoError(t, h.q.Publish(context.Background(), queue.TopicUsage, []byte("not json")))

	n, err := h.q.Consume(context.Background(), queue.TopicUsage, 10, 50*time.Millisecond,
		h.c.handleMessage)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "the message must be acknowledged and dropped")

	_, failed, _ := h.c.Stats()
	assert.Equal(t, int64(1), failed, "the drop must be counted as a failure")

	// The topic must be empty afterwards, so the loop can make progress.
	depth, err := h.q.Depth(context.Background(), queue.TopicUsage)
	require.NoError(t, err)
	assert.Equal(t, int64(0), depth)
}

// ---------------------------------------------------------------------------
// Rollups
// ---------------------------------------------------------------------------

func TestConsumerWritesDailyRollup(t *testing.T) {
	h := newHarness(t)
	h.publish(t, domain.UsageRecord{
		RequestID: "req-r", AccountID: 5, V10HostID: 6, APIKeyID: 7, Model: "m",
		PromptTokens: 100, CompletionTokens: 50, CostMicros: 150,
		StatusCode: 200, CreatedAt: fixedTime,
	})
	h.drain(t)

	daily, err := h.st.Aggregates().ListDaily(context.Background(), store.UsageFilter{
		From: fixedTime.Add(-time.Hour), To: fixedTime.Add(time.Hour),
	})
	require.NoError(t, err)
	require.NotEmpty(t, daily, "a rollup row must exist for the day")

	var totalCost, totalPrompt int64
	for _, d := range daily {
		totalCost += d.CostMicros
		totalPrompt += d.PromptTokens
	}
	assert.Equal(t, int64(150), totalCost)
	assert.Equal(t, int64(100), totalPrompt)
}

func TestDailyFromUsageProjectsCorrectly(t *testing.T) {
	u := &domain.UsageRecord{
		AccountID: 1, V10HostID: 2, APIKeyID: 3, Model: "gpt-x",
		PromptTokens: 11, CompletionTokens: 22, CostMicros: 33,
		CreatedAt: fixedTime,
	}
	d := dailyFromUsage(u)
	assert.Equal(t, "2026-11-05", d.Day)
	assert.Equal(t, uint64(1), d.AccountID)
	assert.Equal(t, uint64(2), d.V10HostID)
	assert.Equal(t, uint64(3), d.APIKeyID)
	assert.Equal(t, "gpt-x", d.Model)
	assert.Equal(t, int64(1), d.Requests)
	assert.Equal(t, int64(11), d.PromptTokens)
	assert.Equal(t, int64(22), d.CompletionTokens)
	assert.Equal(t, int64(33), d.CostMicros)
}

// ---------------------------------------------------------------------------
// Expiry sweeper
// ---------------------------------------------------------------------------

func TestSweeperExpiresSubscriptionsAndSuspendsKeys(t *testing.T) {
	clk := &testClock{now: fixedTime}
	st := memstore.NewWithClock(clk)
	ctx := context.Background()

	plan, err := st.Plans().Upsert(ctx, &domain.Plan{
		PlanCode: "p", Name: "P", QuotaTokens: 1000, Period: "month",
		ModelGroup: "default", GroupRatio: 1, Status: 1,
	})
	require.NoError(t, err)
	acc, err := st.Accounts().Upsert(ctx, &domain.Account{
		SiteID: "app-1", V10ClientID: 1, Status: domain.AccountEnabled,
	})
	require.NoError(t, err)

	// This subscription ended yesterday.
	sub, _, err := st.Subscriptions().Create(ctx, &domain.Subscription{
		AccountID: acc.ID, V10HostID: 11, PlanID: plan.ID,
		PeriodStart: fixedTime.AddDate(0, -1, 0), PeriodEnd: fixedTime.Add(-24 * time.Hour),
		QuotaTotal: 1000, BillingMode: domain.BillingSubscription, Status: domain.SubActive,
		CreatedAt: fixedTime, UpdatedAt: fixedTime,
	})
	require.NoError(t, err)
	require.NoError(t, st.Keys().Create(ctx, &domain.APIKey{
		AccountID: acc.ID, SubscriptionID: sub.ID, KeyHash: "h", KeyPrefix: "sk-",
		Status: domain.KeyActive, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}))

	// This one is still running and must be untouched.
	live, _, err := st.Subscriptions().Create(ctx, &domain.Subscription{
		AccountID: acc.ID, V10HostID: 22, PlanID: plan.ID,
		PeriodStart: fixedTime, PeriodEnd: fixedTime.AddDate(0, 1, 0),
		QuotaTotal: 1000, BillingMode: domain.BillingSubscription, Status: domain.SubActive,
		CreatedAt: fixedTime, UpdatedAt: fixedTime,
	})
	require.NoError(t, err)

	sweeper := NewSubscriptionSweeper(st, log.New("error", "text"), clk)
	sweeper.sweepOnce(ctx)

	got, err := st.Subscriptions().GetByID(ctx, sub.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.SubExpired, got.Status)

	// Expiring the subscription is not enough: the keys must stop working.
	keys, err := st.Keys().ListBySubscription(ctx, sub.ID)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.NotEqual(t, domain.KeyActive, keys[0].Status,
		"the expired subscription's keys must not stay active")

	untouched, err := st.Subscriptions().GetByID(ctx, live.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.SubActive, untouched.Status,
		"a subscription still inside its period must not be swept")
}

// ---------------------------------------------------------------------------
// Shadow comparison
// ---------------------------------------------------------------------------

func TestShadowDiffRelativeError(t *testing.T) {
	cases := []struct {
		name      string
		live      int64
		shadow    int64
		want      float64
		exceeds   bool
		tolerance float64
	}{
		{"identical", 1000, 1000, 0, false, 0.01},
		{"shadow higher", 1000, 1100, 0.1, true, 0.01},
		{"shadow lower", 1000, 900, 0.1, true, 0.01},
		{"zero live", 0, 500, 0, false, 0.01},
		{"tiny drift", 10000, 10001, 0.0001, false, 0.01},
		{"within tolerance", 1000, 1005, 0.005, false, 0.01},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := ShadowDiff{LiveMicros: tc.live, ShadowMicros: tc.shadow}
			assert.InDelta(t, tc.want, d.RelativeError(), 1e-9)
			assert.Equal(t, tc.exceeds, d.Exceeds(tc.tolerance))
		})
	}
}

func TestShadowDiffFormat(t *testing.T) {
	d := ShadowDiff{LiveMicros: 1000, ShadowMicros: 1100}
	got := d.FormatDiff()
	assert.Contains(t, got, "live=1000")
	assert.Contains(t, got, "shadow=1100")
	assert.Contains(t, got, "rel_err=0.1000")
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

func TestConsumerRunStopsOnContextCancel(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.c.Run(ctx) }()

	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}

func TestConsumerCloseStopsRun(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- h.c.Run(ctx) }()

	h.c.Close()
	select {
	case err := <-done:
		assert.NoError(t, err, "Close is a clean stop, not a cancellation")
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Close")
	}

	// Close is idempotent so a deferred Close after a signal cannot panic.
	assert.NotPanics(t, h.c.Close)
}

// marshalForTest encodes a record exactly the way the API does, so the consumer
// is always tested against the real wire format.
func marshalForTest(rec domain.UsageRecord) ([]byte, error) {
	return quota.MarshalUsageForTest(rec)
}

// allUsage matches every usage row.
func allUsage() store.UsageFilter {
	return store.UsageFilter{IncludeShadow: true}
}
