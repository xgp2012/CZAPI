package memcache

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"czapi/internal/cache"
	"czapi/internal/pkg/clock"
	"czapi/internal/pkg/errs"
)

var ctxBg = context.Background()

// newCache returns a cache on a fake clock with a long sweep interval, so tests
// drive expiry explicitly instead of racing a ticker.
func newCache(t *testing.T) (*Cache, *clock.Fake) {
	t.Helper()
	fake := clock.NewFake(time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC))
	c := NewWithClock(fake, time.Hour)
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	return c, fake
}

// --------------------------------------------------------------------------
// KV primitives
// --------------------------------------------------------------------------

func TestGetSetDel(t *testing.T) {
	c, _ := newCache(t)

	_, found, err := c.Get(ctxBg, "missing")
	require.NoError(t, err)
	assert.False(t, found)

	require.NoError(t, c.Set(ctxBg, "k", []byte("v"), time.Minute))
	raw, found, err := c.Get(ctxBg, "k")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, []byte("v"), raw)

	// The stored value is copied in both directions.
	raw[0] = 'x'
	raw2, _, err := c.Get(ctxBg, "k")
	require.NoError(t, err)
	assert.Equal(t, []byte("v"), raw2)

	require.NoError(t, c.Del(ctxBg, "k", "never-existed"))
	_, found, err = c.Get(ctxBg, "k")
	require.NoError(t, err)
	assert.False(t, found)
}

func TestTTLExpiry(t *testing.T) {
	c, fake := newCache(t)
	require.NoError(t, c.Set(ctxBg, "k", []byte("v"), time.Minute))

	fake.Advance(59 * time.Second)
	_, found, err := c.Get(ctxBg, "k")
	require.NoError(t, err)
	assert.True(t, found)

	fake.Advance(2 * time.Second)
	_, found, err = c.Get(ctxBg, "k")
	require.NoError(t, err)
	assert.False(t, found, "expired entry is invisible")

	// ttl<=0 means no expiry.
	require.NoError(t, c.Set(ctxBg, "forever", []byte("v"), 0))
	fake.Advance(24 * time.Hour)
	_, found, err = c.Get(ctxBg, "forever")
	require.NoError(t, err)
	assert.True(t, found)
}

func TestSetNX(t *testing.T) {
	c, fake := newCache(t)

	ok, err := c.SetNX(ctxBg, "k", []byte("1"), time.Minute)
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = c.SetNX(ctxBg, "k", []byte("2"), time.Minute)
	require.NoError(t, err)
	assert.False(t, ok, "second SetNX must not overwrite")

	raw, _, err := c.Get(ctxBg, "k")
	require.NoError(t, err)
	assert.Equal(t, []byte("1"), raw)

	fake.Advance(time.Minute + time.Second)
	ok, err = c.SetNX(ctxBg, "k", []byte("3"), time.Minute)
	require.NoError(t, err)
	assert.True(t, ok, "an expired key may be claimed again")
}

func TestIncrBy(t *testing.T) {
	c, fake := newCache(t)
	key := cache.RPMKey("acct")

	v, err := c.IncrBy(ctxBg, key, 1, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, int64(1), v)

	v, err = c.IncrBy(ctxBg, key, 4, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, int64(5), v)

	v, err = c.IncrBy(ctxBg, key, -6, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, int64(-1), v)

	// The window restarts after the ttl set on first use.
	fake.Advance(time.Minute + time.Second)
	v, err = c.IncrBy(ctxBg, key, 2, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, int64(2), v, "counter restarts after the ttl")
}

func TestJSONRoundTrip(t *testing.T) {
	c, _ := newCache(t)

	type payload struct {
		Name string   `json:"name"`
		N    int      `json:"n"`
		Tags []string `json:"tags"`
	}
	in := payload{Name: "gpt", N: 3, Tags: []string{"a", "b"}}
	require.NoError(t, c.SetJSON(ctxBg, "obj", in, time.Minute))

	var out payload
	found, err := c.GetJSON(ctxBg, "obj", &out)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, in, out)

	var missing payload
	found, err = c.GetJSON(ctxBg, "nope", &missing)
	require.NoError(t, err)
	assert.False(t, found)

	// A corrupt payload surfaces as a domain error, never a panic.
	require.NoError(t, c.Set(ctxBg, "bad", []byte("{not json"), time.Minute))
	_, err = c.GetJSON(ctxBg, "bad", &out)
	require.Error(t, err)
	var e *errs.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, errs.CodeInternal, e.Code)

	// Unencodable values fail cleanly.
	err = c.SetJSON(ctxBg, "chan", make(chan int), time.Minute)
	require.Error(t, err)
}

// --------------------------------------------------------------------------
// Hold / Release
// --------------------------------------------------------------------------

func TestHoldArithmetic(t *testing.T) {
	c, _ := newCache(t)
	scope := cache.HoldBalanceScope(1)

	id1, ok, err := c.Hold(ctxBg, scope, 300, 1_000, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	assert.NotEmpty(t, id1)

	held, err := c.Held(ctxBg, scope)
	require.NoError(t, err)
	assert.Equal(t, int64(300), held)

	id2, ok, err := c.Hold(ctxBg, scope, 400, 1_000, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	assert.NotEqual(t, id1, id2, "hold ids are unique")

	held, err = c.Held(ctxBg, scope)
	require.NoError(t, err)
	assert.Equal(t, int64(700), held)

	// Settle the first hold for 200 of 300: 100 comes back.
	refund, err := c.Release(ctxBg, scope, id1, 200)
	require.NoError(t, err)
	assert.Equal(t, int64(100), refund)

	held, err = c.Held(ctxBg, scope)
	require.NoError(t, err)
	assert.Equal(t, int64(400), held, "only the surviving hold remains")

	// Over-spending the hold never produces a negative refund.
	refund, err = c.Release(ctxBg, scope, id2, 900)
	require.NoError(t, err)
	assert.Equal(t, int64(0), refund)

	held, err = c.Held(ctxBg, scope)
	require.NoError(t, err)
	assert.Zero(t, held)
}

func TestReleaseUnknownHold(t *testing.T) {
	c, _ := newCache(t)
	refund, err := c.Release(ctxBg, "no-such-scope", "no-such-hold", 10)
	require.NoError(t, err)
	assert.Zero(t, refund)

	_, ok, err := c.Hold(ctxBg, "s", 10, 100, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	refund, err = c.Release(ctxBg, "s", "bad-id", 10)
	require.NoError(t, err)
	assert.Zero(t, refund)

	// Double release is a harmless no-op (retry-safe).
	refund, err = c.Release(ctxBg, "s", "", 0)
	require.NoError(t, err)
	assert.Zero(t, refund)
}

func TestHoldBudgetExceeded(t *testing.T) {
	c, _ := newCache(t)
	scope := cache.HoldSubscriptionScope(9)

	_, ok, err := c.Hold(ctxBg, scope, 800, 1_000, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	// 1000 - 800 = 200 < 201.
	_, ok, err = c.Hold(ctxBg, scope, 201, 1_000, time.Minute)
	require.NoError(t, err)
	assert.False(t, ok, "budget is respected")

	// Exactly the remaining budget is allowed.
	id, ok, err := c.Hold(ctxBg, scope, 200, 1_000, time.Minute)
	require.NoError(t, err)
	assert.True(t, ok)

	held, err := c.Held(ctxBg, scope)
	require.NoError(t, err)
	assert.Equal(t, int64(1_000), held)

	// Freeing budget makes room again.
	_, err = c.Release(ctxBg, scope, id, 0)
	require.NoError(t, err)
	_, ok, err = c.Hold(ctxBg, scope, 200, 1_000, time.Minute)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestHoldNegativeAmountRejected(t *testing.T) {
	c, _ := newCache(t)
	_, ok, err := c.Hold(ctxBg, "s", -1, 100, time.Minute)
	require.Error(t, err)
	assert.False(t, ok)
	var e *errs.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, errs.CodeInvalidRequest, e.Code)
}

func TestHoldExpiresWithTTL(t *testing.T) {
	c, fake := newCache(t)
	scope := "bal:7"

	_, ok, err := c.Hold(ctxBg, scope, 500, 1_000, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	// A leaked hold must not block the scope forever.
	fake.Advance(2 * time.Minute)
	held, err := c.Held(ctxBg, scope)
	require.NoError(t, err)
	assert.Zero(t, held)

	_, ok, err = c.Hold(ctxBg, scope, 1_000, 1_000, time.Minute)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestHoldConcurrentNeverOversells(t *testing.T) {
	c, _ := newCache(t)
	scope := "bal:conc"

	const (
		workers = 50
		amount  = 100
		budget  = 1_000
	)
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		granted int64
	)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			_, ok, err := c.Hold(ctxBg, scope, amount, budget, time.Minute)
			if err != nil {
				return
			}
			if ok {
				mu.Lock()
				granted += amount
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int64(budget), granted, "exactly the budget is handed out")

	held, err := c.Held(ctxBg, scope)
	require.NoError(t, err)
	assert.LessOrEqual(t, held, int64(budget), "held total never exceeds the budget")
}

func TestHoldScopesAreIsolated(t *testing.T) {
	c, _ := newCache(t)
	_, ok, err := c.Hold(ctxBg, "bal:1", 100, 100, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	held, err := c.Held(ctxBg, "bal:2")
	require.NoError(t, err)
	assert.Zero(t, held, "scopes do not share budget")
}

// --------------------------------------------------------------------------
// Fixed-window rate limiting
// --------------------------------------------------------------------------

func TestAllowRequestWindow(t *testing.T) {
	c, fake := newCache(t)
	scope := "key:1"
	window := time.Minute
	limit := 3

	for i := 0; i < limit; i++ {
		ok, remaining, resetIn, err := c.AllowRequest(ctxBg, scope, limit, window)
		require.NoError(t, err)
		assert.True(t, ok, "request %d is within the limit", i+1)
		assert.Equal(t, limit-i-1, remaining)
		assert.Greater(t, resetIn, time.Duration(0))
	}

	ok, remaining, _, err := c.AllowRequest(ctxBg, scope, limit, window)
	require.NoError(t, err)
	assert.False(t, ok, "the 4th request in the window is refused")
	assert.Zero(t, remaining)

	// The window resets.
	fake.Advance(window + time.Second)
	ok, remaining, _, err = c.AllowRequest(ctxBg, scope, limit, window)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, limit-1, remaining)
}

func TestAllowRequestZeroLimitIsUnlimited(t *testing.T) {
	c, _ := newCache(t)
	for i := 0; i < 10; i++ {
		ok, _, _, err := c.AllowRequest(ctxBg, "k", 0, time.Minute)
		require.NoError(t, err)
		assert.True(t, ok)
	}
}

func TestAllowTokens(t *testing.T) {
	c, fake := newCache(t)
	scope := "key:2"
	const (
		limit  = 1_000
		window = time.Minute
	)

	ok, remaining, err := c.AllowTokens(ctxBg, scope, 400, limit, window)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, 600, remaining)

	ok, remaining, err = c.AllowTokens(ctxBg, scope, 600, limit, window)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Zero(t, remaining)

	ok, remaining, err = c.AllowTokens(ctxBg, scope, 1, limit, window)
	require.NoError(t, err)
	assert.False(t, ok, "the budget is spent for this window")
	assert.Zero(t, remaining)

	// A single oversized request is refused too.
	fake.Advance(window + time.Second)
	ok, _, err = c.AllowTokens(ctxBg, scope, limit+1, limit, window)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestAllowTokensZeroLimitIsUnlimited(t *testing.T) {
	c, _ := newCache(t)
	ok, _, err := c.AllowTokens(ctxBg, "k", 999_999, 0, time.Minute)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestRateLimitsAreScopedSeparately(t *testing.T) {
	c, _ := newCache(t)
	ok, _, _, err := c.AllowRequest(ctxBg, "a", 1, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	ok, _, _, err = c.AllowRequest(ctxBg, "a", 1, time.Minute)
	require.NoError(t, err)
	assert.False(t, ok)

	ok, _, _, err = c.AllowRequest(ctxBg, "b", 1, time.Minute)
	require.NoError(t, err)
	assert.True(t, ok, "another scope has its own bucket")
}

// --------------------------------------------------------------------------
// Concurrency slots
// --------------------------------------------------------------------------

func TestAcquireConcurrencyLimitAndRelease(t *testing.T) {
	c, _ := newCache(t)
	scope := "channel:1"
	const limit = 2

	ok1, rel1, err := c.AcquireConcurrency(ctxBg, scope, limit, time.Minute)
	require.NoError(t, err)
	require.True(t, ok1)
	require.NotNil(t, rel1)

	ok2, rel2, err := c.AcquireConcurrency(ctxBg, scope, limit, time.Minute)
	require.NoError(t, err)
	require.True(t, ok2)

	ok3, rel3, err := c.AcquireConcurrency(ctxBg, scope, limit, time.Minute)
	require.NoError(t, err)
	assert.False(t, ok3, "the limit is reached")
	assert.NotNil(t, rel3, "a failed acquire still returns a callable release")
	rel3() // must be harmless

	rel1()
	ok4, rel4, err := c.AcquireConcurrency(ctxBg, scope, limit, time.Minute)
	require.NoError(t, err)
	assert.True(t, ok4, "a released slot is reusable")

	// Releasing twice must not free two slots.
	rel1()
	rel1()

	rel2()
	rel4()
	ok5, rel5, err := c.AcquireConcurrency(ctxBg, scope, limit, time.Minute)
	require.NoError(t, err)
	assert.True(t, ok5)
	ok6, _, err := c.AcquireConcurrency(ctxBg, scope, limit, time.Minute)
	require.NoError(t, err)
	assert.True(t, ok6, "both slots were freed exactly once")
	rel5()
}

func TestAcquireConcurrencyExpires(t *testing.T) {
	c, fake := newCache(t)
	scope := "channel:2"

	_, rel, err := c.AcquireConcurrency(ctxBg, scope, 1, time.Minute)
	require.NoError(t, err)

	ok, _, err := c.AcquireConcurrency(ctxBg, scope, 1, time.Minute)
	require.NoError(t, err)
	assert.False(t, ok)

	fake.Advance(2 * time.Minute)
	ok, _, err = c.AcquireConcurrency(ctxBg, scope, 1, time.Minute)
	require.NoError(t, err)
	assert.True(t, ok, "stale slots expire so a crashed request cannot leak one")

	rel() // releasing a stale slot is harmless
}

func TestAcquireConcurrencyZeroLimitIsUnlimited(t *testing.T) {
	c, _ := newCache(t)
	for i := 0; i < 5; i++ {
		ok, rel, err := c.AcquireConcurrency(ctxBg, "s", 0, time.Minute)
		require.NoError(t, err)
		assert.True(t, ok)
		require.NotNil(t, rel)
		rel()
	}
}

func TestAcquireConcurrencyReleaseIsOnce(t *testing.T) {
	c, _ := newCache(t)
	scope := "channel:3"
	const limit = 1

	_, rel, err := c.AcquireConcurrency(ctxBg, scope, limit, time.Minute)
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go func() {
			defer wg.Done()
			rel()
		}()
	}
	wg.Wait()

	// Exactly one slot is free after 10 concurrent releases of the same slot.
	ok1, rel1, err := c.AcquireConcurrency(ctxBg, scope, limit, time.Minute)
	require.NoError(t, err)
	assert.True(t, ok1)

	ok2, _, err := c.AcquireConcurrency(ctxBg, scope, limit, time.Minute)
	require.NoError(t, err)
	assert.False(t, ok2, "the counter must not go negative")
	rel1()
}

// --------------------------------------------------------------------------
// Nonce replay protection
// --------------------------------------------------------------------------

func TestCheckNonceReplay(t *testing.T) {
	c, fake := newCache(t)
	ttl := 5 * time.Minute

	ok, err := c.CheckNonce(ctxBg, "app-1", "nonce-1", ttl)
	require.NoError(t, err)
	assert.True(t, ok, "first sighting is accepted")

	for i := 0; i < 3; i++ {
		ok, err = c.CheckNonce(ctxBg, "app-1", "nonce-1", ttl)
		require.NoError(t, err)
		assert.False(t, ok, "replay #%d must be rejected", i+1)
	}

	// A different app or nonce is independent.
	ok, err = c.CheckNonce(ctxBg, "app-2", "nonce-1", ttl)
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = c.CheckNonce(ctxBg, "app-1", "nonce-2", ttl)
	require.NoError(t, err)
	assert.True(t, ok)

	// After the ttl the nonce may be reused (the signature timestamp window
	// rejects the request long before this happens).
	fake.Advance(ttl + time.Second)
	ok, err = c.CheckNonce(ctxBg, "app-1", "nonce-1", ttl)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestCheckNonceConcurrentReplay(t *testing.T) {
	c, _ := newCache(t)

	const n = 20
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		okCh = 0
	)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			ok, err := c.CheckNonce(ctxBg, "app", "same", time.Minute)
			if err == nil && ok {
				mu.Lock()
				okCh++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, okCh, "exactly one concurrent call wins")
}

func TestCheckNonceValidation(t *testing.T) {
	c, _ := newCache(t)
	_, err := c.CheckNonce(ctxBg, "", "n", time.Minute)
	require.Error(t, err)
	_, err = c.CheckNonce(ctxBg, "app", "", time.Minute)
	require.Error(t, err)
}

// --------------------------------------------------------------------------
// Lifecycle
// --------------------------------------------------------------------------

func TestCloseIsIdempotentAndStopsJanitor(t *testing.T) {
	fake := clock.NewFake(time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC))
	c := NewWithClock(fake, 5*time.Millisecond)

	require.NoError(t, c.Ping(ctxBg))
	require.NoError(t, c.Set(ctxBg, "k", []byte("v"), 5*time.Millisecond))

	// Let the janitor run at least once.
	time.Sleep(20 * time.Millisecond)

	require.NoError(t, c.Close())
	require.NoError(t, c.Close(), "Close is idempotent")

	var wg sync.WaitGroup
	wg.Add(5)
	for i := 0; i < 5; i++ {
		go func() {
			defer wg.Done()
			assert.NoError(t, c.Close())
		}()
	}
	wg.Wait()

	// The goroutine exited: a closed stopped channel receives immediately.
	select {
	case <-c.stopped:
	case <-time.After(time.Second):
		t.Fatal("janitor goroutine still running after Close")
	}
}

func TestJanitorEvictsExpiredEntries(t *testing.T) {
	fake := clock.NewFake(time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC))
	c := NewWithClock(fake, time.Millisecond)
	defer func() { require.NoError(t, c.Close()) }()

	require.NoError(t, c.Set(ctxBg, "short", []byte("v"), time.Second))
	_, err := c.IncrBy(ctxBg, "counter", 1, time.Second)
	require.NoError(t, err)
	_, ok, err := c.Hold(ctxBg, "s", 10, 100, time.Second)
	require.NoError(t, err)
	require.True(t, ok)

	fake.Advance(2 * time.Second)
	time.Sleep(50 * time.Millisecond) // let the janitor sweep

	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Empty(t, c.data, "expired KV entries are evicted")
	assert.Empty(t, c.counters, "expired counters are evicted")
	assert.Empty(t, c.holds, "expired holds are evicted")
}

func TestNewUsesDefaults(t *testing.T) {
	c := New()
	require.NoError(t, c.Ping(ctxBg))
	require.NoError(t, c.Set(ctxBg, "k", []byte("v"), time.Minute))
	raw, found, err := c.Get(ctxBg, "k")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, []byte("v"), raw)
	require.NoError(t, c.Close())

	// A nil clock / non-positive sweep fall back to the defaults.
	c2 := NewWithClock(nil, 0)
	require.NoError(t, c2.Ping(ctxBg))
	require.NoError(t, c2.Close())
}

func TestContextCancellation(t *testing.T) {
	c, _ := newCache(t)
	ctx, cancel := context.WithCancel(ctxBg)
	cancel()

	_, _, err := c.Get(ctx, "k")
	require.Error(t, err)
	require.Error(t, c.Set(ctx, "k", nil, time.Minute))
	_, _, _, err = c.AllowRequest(ctx, "s", 1, time.Minute)
	require.Error(t, err)
	_, _, err = c.Hold(ctx, "s", 1, 10, time.Minute)
	require.Error(t, err)
	_, err = c.Release(ctx, "s", "id", 0)
	require.Error(t, err)
	_, _, err = c.AcquireConcurrency(ctx, "s", 1, time.Minute)
	require.Error(t, err)
}
