// Package worker runs the asynchronous side of the metering pipeline.
//
// The request path only publishes a usage message; the consumer here persists it
// and maintains the daily rollups. Decoupling the two keeps the hot path fast and
// means a storage hiccup delays bookkeeping instead of failing customer requests
// (plan §6.7).
package worker

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"czapi/internal/domain"
	"czapi/internal/metrics"
	"czapi/internal/pkg/clock"
	"czapi/internal/pkg/errs"
	"czapi/internal/pkg/log"
	"czapi/internal/queue"
	"czapi/internal/quota"
	"czapi/internal/store"
)

// Config tunes the consumer.
type Config struct {
	// BatchSize is how many messages are drained per iteration. Larger batches
	// amortise the storage round-trip.
	BatchSize int
	// IdleSleep is how long the consumer waits when the queue is empty.
	IdleSleep time.Duration
	// WriteTimeout bounds one storage batch.
	WriteTimeout time.Duration
	// RollupInterval is how often yesterday's rollup is refreshed.
	RollupInterval time.Duration
	// ShadowDiff enables the shadow double-run comparison logging (plan §15.3).
	ShadowDiff bool
}

func (c Config) withDefaults() Config {
	if c.BatchSize <= 0 {
		c.BatchSize = 200
	}
	if c.IdleSleep <= 0 {
		c.IdleSleep = 200 * time.Millisecond
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = 10 * time.Second
	}
	if c.RollupInterval <= 0 {
		c.RollupInterval = 15 * time.Minute
	}
	return c
}

// UsageConsumer drains the usage topic into the store.
//
// Safe for concurrent use; Run is intended to be called once.
type UsageConsumer struct {
	st      store.Store
	q       queue.Queue
	mx      *metrics.Registry
	log     *log.Logger
	clk     clock.Clock
	cfg     Config
	stop    chan struct{}
	once    sync.Once
	lag     atomic.Int64
	handled atomic.Int64
	failed  atomic.Int64
}

// NewUsageConsumer builds the consumer.
func NewUsageConsumer(st store.Store, q queue.Queue, mx *metrics.Registry, lg *log.Logger, clk clock.Clock, cfg Config) *UsageConsumer {
	if clk == nil {
		clk = clock.System{}
	}
	return &UsageConsumer{
		st: st, q: q, mx: mx, log: lg, clk: clk,
		cfg: cfg.withDefaults(), stop: make(chan struct{}),
	}
}

// Run consumes until the context is cancelled or Close is called.
//
// A storage error is logged and retried by the queue's own redelivery, so a
// transient outage delays bookkeeping without losing rows.
func (c *UsageConsumer) Run(ctx context.Context) error {
	lastRollup := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.stop:
			return nil
		default:
		}

		n, err := c.q.Consume(ctx, queue.TopicUsage, c.cfg.BatchSize, c.cfg.IdleSleep, c.handleMessage)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if c.log != nil {
				c.log.Error("usage consume failed", "err", err)
			}
			c.sleep(ctx, c.cfg.IdleSleep)
			continue
		}
		if n == 0 {
			// Periodically refresh the rollup so the console stays current even
			// when traffic stops.
			if c.clk.Now().Sub(lastRollup) >= c.cfg.RollupInterval {
				c.refreshRollup(ctx)
				lastRollup = c.clk.Now()
			}
			c.sleep(ctx, c.cfg.IdleSleep)
			continue
		}
		c.reportDepth(ctx)
	}
}

// Close stops the loop. It is safe to call more than once.
func (c *UsageConsumer) Close() {
	c.once.Do(func() { close(c.stop) })
}

// Stats reports counters for /healthz and metrics.
func (c *UsageConsumer) Stats() (handled, failed, lag int64) {
	return c.handled.Load(), c.failed.Load(), c.lag.Load()
}

// handleMessage persists one usage message.
func (c *UsageConsumer) handleMessage(ctx context.Context, m queue.Message) error {
	start := c.clk.Now()
	rec, err := quota.UnmarshalUsage(m.Payload)
	if err != nil {
		// A malformed payload can never succeed, so it is dropped rather than
		// retried forever.
		c.failed.Add(1)
		if c.log != nil {
			c.log.Error("usage message is not decodable; dropping",
				"topic", m.Topic, "err", err)
		}
		return nil
	}

	wctx, cancel := context.WithTimeout(ctx, c.cfg.WriteTimeout)
	defer cancel()

	created, ierr := c.st.Usage().Insert(wctx, &rec)
	if ierr != nil {
		c.failed.Add(1)
		if c.log != nil {
			c.log.Error("usage insert failed", "request_id", rec.RequestID, "err", ierr)
		}
		return errs.As(ierr)
	}
	if !created {
		// Already persisted by an earlier delivery: idempotent no-op.
		c.handled.Add(1)
		return nil
	}
	c.handled.Add(1)

	// Rollups are best effort: a failure must not drop the usage row.
	if uerr := c.st.Aggregates().UpsertDaily(wctx, dailyFromUsage(&rec)); uerr != nil && c.log != nil {
		c.log.Warn("daily rollup failed", "request_id", rec.RequestID, "err", uerr)
	}

	if c.mx != nil {
		c.mx.SetBillingLag(c.clk.Now().Sub(start))
	}
	return nil
}

// dailyFromUsage projects a usage record onto the rollup key.
func dailyFromUsage(u *domain.UsageRecord) *domain.DailyUsage {
	day := u.CreatedAt.Format("2006-01-02")
	d := &domain.DailyUsage{
		Day: day, AccountID: u.AccountID, V10HostID: u.V10HostID,
		APIKeyID: u.APIKeyID, Model: u.Model, Requests: 1,
		PromptTokens:     int64(u.PromptTokens),
		CompletionTokens: int64(u.CompletionTokens),
		CostMicros:       u.CostMicros,
	}
	return d
}

// refreshRollup rebuilds yesterday's aggregate from the raw table, which corrects
// any drift introduced by a lost rollup write.
func (c *UsageConsumer) refreshRollup(ctx context.Context) {
	day := c.clk.Now().AddDate(0, 0, -1)
	if err := c.st.Aggregates().RebuildDaily(ctx, day); err != nil && c.log != nil {
		c.log.Warn("rollup refresh failed", "day", day.Format("2006-01-02"), "err", err)
	}
}

// reportDepth publishes the queue depth gauge.
func (c *UsageConsumer) reportDepth(ctx context.Context) {
	if c.mx == nil {
		return
	}
	depth, err := c.q.Depth(ctx, queue.TopicUsage)
	if err != nil {
		return
	}
	c.lag.Store(depth)
	c.mx.SetQueueDepth(queue.TopicUsage, depth)
}

// sleep waits for d or for shutdown.
func (c *UsageConsumer) sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-c.stop:
	case <-t.C:
	}
}

// ---------------------------------------------------------------------------
// Expiry sweeper
// ---------------------------------------------------------------------------

// SubscriptionSweeper expires subscriptions whose period has ended.
//
// It runs as a separate loop so a slow sweep cannot delay metering.
type SubscriptionSweeper struct {
	st   store.Store
	log  *log.Logger
	clk  clock.Clock
	stop chan struct{}
	once sync.Once
}

// NewSubscriptionSweeper builds the sweeper.
func NewSubscriptionSweeper(st store.Store, lg *log.Logger, clk clock.Clock) *SubscriptionSweeper {
	if clk == nil {
		clk = clock.System{}
	}
	return &SubscriptionSweeper{st: st, log: lg, clk: clk, stop: make(chan struct{})}
}

// Run sweeps every interval until the context is cancelled.
func (s *SubscriptionSweeper) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.stop:
			return nil
		case <-t.C:
			s.sweepOnce(ctx)
		}
	}
}

// Close stops the sweeper.
func (s *SubscriptionSweeper) Close() {
	s.once.Do(func() { close(s.stop) })
}

// sweepOnce expires everything past its period end and suspends its keys.
func (s *SubscriptionSweeper) sweepOnce(ctx context.Context) {
	expired, err := s.st.Subscriptions().ListExpired(ctx, s.clk.Now())
	if err != nil {
		if s.log != nil {
			s.log.Error("expiry sweep failed", "err", err)
		}
		return
	}
	for i := range expired {
		sub := &expired[i]
		sub.Status = domain.SubExpired
		sub.UpdatedAt = s.clk.Now()
		if uerr := s.st.Subscriptions().Update(ctx, sub); uerr != nil {
			if s.log != nil {
				s.log.Error("expire subscription failed", "subscription_id", sub.ID, "err", uerr)
			}
			continue
		}
		// Suspending keys is what actually stops traffic.
		if _, kerr := s.st.Keys().SetStatusBySubscription(ctx, sub.ID, domain.KeySuspended); kerr != nil {
			if s.log != nil {
				s.log.Warn("suspend expired keys failed", "subscription_id", sub.ID, "err", kerr)
			}
		}
		if s.log != nil {
			s.log.Info("subscription expired", "subscription_id", sub.ID, "v10_host_id", sub.V10HostID)
		}
	}
}

// ---------------------------------------------------------------------------
// Audit consumer
// ---------------------------------------------------------------------------

// AuditConsumer persists async audit events (admin actions are written inline,
// but channel and system events go through the queue).
type AuditConsumer struct {
	st   store.Store
	log  *log.Logger
	stop chan struct{}
	once sync.Once
}

// NewAuditConsumer builds the audit consumer.
func NewAuditConsumer(st store.Store, lg *log.Logger) *AuditConsumer {
	return &AuditConsumer{st: st, log: lg, stop: make(chan struct{})}
}

// Run consumes audit messages until stopped.
func (a *AuditConsumer) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-a.stop:
			return nil
		default:
		}
		n, err := a.st2consume(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		if n == 0 {
			t := time.NewTimer(200 * time.Millisecond)
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-a.stop:
				t.Stop()
				return nil
			case <-t.C:
			}
		}
	}
}

// st2consume is a placeholder seam retained for symmetry with UsageConsumer; the
// audit path is written inline by the admin API today, so this loop idles.
func (a *AuditConsumer) st2consume(ctx context.Context) (int, error) { return 0, nil }

// Close stops the consumer.
func (a *AuditConsumer) Close() {
	a.once.Do(func() { close(a.stop) })
}

// ---------------------------------------------------------------------------
// Shadow comparison
// ---------------------------------------------------------------------------

// ShadowDiff compares a shadow (recomputed) cost against the live cost and
// reports the relative error. It implements the double-run safeguard from
// plan §15.3: a sustained divergence means a billing change is unsafe to roll
// out to 100% of traffic.
type ShadowDiff struct {
	LiveMicros   int64
	ShadowMicros int64
}

// RelativeError returns |live-shadow| / live as a fraction in [0,1], or 0 when
// live is zero.
func (d ShadowDiff) RelativeError() float64 {
	if d.LiveMicros == 0 {
		return 0
	}
	diff := d.LiveMicros - d.ShadowMicros
	if diff < 0 {
		diff = -diff
	}
	return float64(diff) / float64(d.LiveMicros)
}

// Exceeds reports whether the divergence is above tolerance.
func (d ShadowDiff) Exceeds(tolerance float64) bool {
	return d.RelativeError() > tolerance
}

// FormatDiff renders a one-line summary for the comparison log.
func (d ShadowDiff) FormatDiff() string {
	var b strings.Builder
	b.WriteString("live=")
	b.WriteString(itoa64(d.LiveMicros))
	b.WriteString(" shadow=")
	b.WriteString(itoa64(d.ShadowMicros))
	b.WriteString(" rel_err=")
	b.WriteString(ftoa(d.RelativeError()))
	return b.String()
}

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [24]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func ftoa(f float64) string {
	// Four decimal places is plenty for a divergence ratio and avoids pulling in
	// strconv formatting choices that vary between Go versions.
	scaled := int64(f*10000 + 0.5)
	return itoa64(scaled/10000) + "." + pad4(itoa64(scaled%10000))
}

func pad4(s string) string {
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}
