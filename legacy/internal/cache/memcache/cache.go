// Package memcache is the in-process implementation of the cache.Cache
// contract: quota pre-holds, fixed-window rate limits, concurrency slots, nonce
// replay protection and a small JSON object cache.
//
// It exists so the gateway can run with cache.driver=memory and so every test
// runs without Redis. A single janitor goroutine sweeps expired entries every
// 30 seconds; Close stops it exactly once and is idempotent, so no goroutine
// leaks between tests.
//
// Concurrency: Safe for concurrent use. All state sits behind one sync.Mutex;
// operations are short and never block on I/O.
//
// Time: the clock is injected (clock.System by default) so tests can travel in
// time instead of sleeping.
package memcache

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"czapi/internal/cache"
	"czapi/internal/pkg/clock"
	"czapi/internal/pkg/errs"
	"czapi/internal/pkg/idgen"
)

// Compile-time proof that the memory driver satisfies the frozen contract.
var _ cache.Cache = (*Cache)(nil)

// DefaultSweepInterval is how often the janitor evicts expired entries.
const DefaultSweepInterval = 30 * time.Second

// entry is one stored value with its absolute expiry.
type entry struct {
	val      []byte
	expireAt time.Time
	hasTTL   bool
}

// expired reports whether e has a TTL that passed at now.
func (e entry) expired(now time.Time) bool {
	return e.hasTTL && !now.Before(e.expireAt)
}

// hold is one outstanding reservation against a scope.
type hold struct {
	amount   int64
	expireAt time.Time
	hasTTL   bool
}

// Cache is an in-memory cache.Cache implementation.
//
// Safe for concurrent use.
type Cache struct {
	clk   clock.Clock
	sweep time.Duration

	mu   sync.Mutex
	data map[string]entry
	// holds maps scope -> holdID -> hold.
	holds map[string]map[string]hold
	// counters holds integer counters used by the fixed-window limiters and
	// the concurrency slots; they expire like plain entries.
	counters map[string]entry

	closeOnce sync.Once
	done      chan struct{}
	stopped   chan struct{}
}

// New returns a cache with the default janitor and the system clock.
func New() *Cache { return NewWithClock(clock.System{}, DefaultSweepInterval) }

// NewWithClock returns a cache whose TTLs are evaluated against c and whose
// janitor sweeps every sweep. A nil clock falls back to clock.System and a
// non-positive sweep falls back to DefaultSweepInterval.
func NewWithClock(c clock.Clock, sweep time.Duration) *Cache {
	if c == nil {
		c = clock.System{}
	}
	if sweep <= 0 {
		sweep = DefaultSweepInterval
	}
	cc := &Cache{
		clk:      c,
		sweep:    sweep,
		data:     make(map[string]entry),
		holds:    make(map[string]map[string]hold),
		counters: make(map[string]entry),
		done:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
	go cc.janitor()
	return cc
}

// janitor evicts expired entries until done is closed.
func (c *Cache) janitor() {
	defer close(c.stopped)
	t := time.NewTicker(c.sweep)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			c.sweepExpired()
		}
	}
}

// sweepExpired removes every entry whose TTL has passed.
func (c *Cache) sweepExpired() {
	now := c.clk.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.data {
		if e.expired(now) {
			delete(c.data, k)
		}
	}
	for k, e := range c.counters {
		if e.expired(now) {
			delete(c.counters, k)
		}
	}
	for scope, hs := range c.holds {
		for id, h := range hs {
			if h.hasTTL && !now.Before(h.expireAt) {
				delete(hs, id)
			}
		}
		if len(hs) == 0 {
			delete(c.holds, scope)
		}
	}
}

// Ping always succeeds: the memory driver needs no backend.
func (c *Cache) Ping(ctx context.Context) error { return ctx.Err() }

// Close stops the janitor goroutine. It is idempotent and safe to call
// concurrently; later calls block until the goroutine has exited.
func (c *Cache) Close() error {
	c.closeOnce.Do(func() { close(c.done) })
	<-c.stopped
	return nil
}

// Get returns the value stored under key.
func (c *Cache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	now := c.clk.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.data[key]
	if !ok {
		return nil, false, nil
	}
	if e.expired(now) {
		delete(c.data, key)
		return nil, false, nil
	}
	return cloneBytes(e.val), true, nil
}

// Set stores val under key with the given TTL; ttl<=0 means no expiry.
func (c *Cache) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = c.makeEntry(val, ttl, c.clk.Now())
	return nil
}

// SetNX stores val only when key is absent or expired.
func (c *Cache) SetNX(ctx context.Context, key string, val []byte, ttl time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	now := c.clk.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.data[key]; ok && !e.expired(now) {
		return false, nil
	}
	c.data[key] = c.makeEntry(val, ttl, now)
	return true, nil
}

// Del removes the given keys. Removing a missing key is not an error.
func (c *Cache) Del(ctx context.Context, keys ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, k := range keys {
		delete(c.data, k)
		delete(c.counters, k)
	}
	return nil
}

// IncrBy adds delta to the integer counter at key and returns the new value.
// The TTL is applied when the counter is created (or re-created after expiry)
// and is left untouched by later increments, which is what makes the
// fixed-window limiters work.
func (c *Cache) IncrBy(ctx context.Context, key string, delta int64, ttl time.Duration) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	now := c.clk.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.counters[key]
	if !ok || e.expired(now) {
		e = c.makeEntry(nil, ttl, now)
	}
	cur := int64(0)
	if len(e.val) > 0 {
		var v int64
		if err := json.Unmarshal(e.val, &v); err != nil {
			// A corrupt counter restarts from zero rather than failing.
			v = 0
		}
		cur = v
	}
	cur += delta
	e.val = encodeInt(cur)
	c.counters[key] = e
	return cur, nil
}

// Hold reserves amount against scope when budget minus the live held total is
// at least amount. A successful hold is identified by a unique holdID used
// later by Release.
func (c *Cache) Hold(ctx context.Context, scope string, amount, budget int64, ttl time.Duration) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if amount < 0 {
		return "", false, errs.InvalidRequest("hold amount must not be negative")
	}
	now := c.clk.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	held := c.heldLocked(scope, now)
	if budget-held < amount {
		return "", false, nil
	}
	id := idgen.RandomHex(8)
	hs, ok := c.holds[scope]
	if !ok {
		hs = make(map[string]hold)
		c.holds[scope] = hs
	}
	h := hold{amount: amount}
	if ttl > 0 {
		h.hasTTL = true
		h.expireAt = now.Add(ttl)
	}
	hs[id] = h
	return id, true, nil
}

// Release settles a hold: the unused part (held amount minus actual) returns to
// the scope. Releasing an unknown or already-expired hold returns 0 and no
// error, so retries are harmless.
func (c *Cache) Release(ctx context.Context, scope, holdID string, actual int64) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	now := c.clk.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	hs, ok := c.holds[scope]
	if !ok {
		return 0, nil
	}
	h, ok := hs[holdID]
	if !ok {
		return 0, nil
	}
	delete(hs, holdID)
	if len(hs) == 0 {
		delete(c.holds, scope)
	}
	if h.hasTTL && !now.Before(h.expireAt) {
		return 0, nil
	}
	refund := h.amount - actual
	if refund < 0 {
		refund = 0
	}
	return refund, nil
}

// Held returns the sum of the live holds of a scope.
func (c *Cache) Held(ctx context.Context, scope string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	now := c.clk.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.heldLocked(scope, now), nil
}

// heldLocked sums the live holds of scope. The caller must hold the lock.
func (c *Cache) heldLocked(scope string, now time.Time) int64 {
	var total int64
	for id, h := range c.holds[scope] {
		if h.hasTTL && !now.Before(h.expireAt) {
			delete(c.holds[scope], id)
			continue
		}
		total += h.amount
	}
	if len(c.holds[scope]) == 0 {
		delete(c.holds, scope)
	}
	return total
}

// AllowRequest consumes one token from a fixed-window request counter and
// reports whether the caller stays within limit for the current window.
// remaining is what is left after this call and resetIn is the time until the
// window resets.
func (c *Cache) AllowRequest(ctx context.Context, scope string, limit int, window time.Duration) (bool, int, time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return false, 0, 0, err
	}
	if limit <= 0 {
		// A non-positive limit disables the limiter rather than blocking all
		// traffic; callers treat 0 as "unlimited".
		return true, 0, 0, nil
	}
	used, resetIn, err := c.bump(ctx, cache.RPMKey(scope), 1, window)
	if err != nil {
		return false, 0, 0, err
	}
	remaining := int64(limit) - used
	if remaining < 0 {
		remaining = 0
	}
	return used <= int64(limit), int(remaining), resetIn, nil
}

// AllowTokens consumes tokens from a fixed-window token counter. The check is
// best effort: tokens are counted even when the call is refused, so a client
// cannot escape the window by splitting requests.
func (c *Cache) AllowTokens(ctx context.Context, scope string, tokens, limit int, window time.Duration) (bool, int, error) {
	if err := ctx.Err(); err != nil {
		return false, 0, err
	}
	if limit <= 0 {
		return true, 0, nil
	}
	used, _, err := c.bump(ctx, cache.TPMKey(scope), int64(tokens), window)
	if err != nil {
		return false, 0, err
	}
	remaining := int64(limit) - used
	if remaining < 0 {
		remaining = 0
	}
	return used <= int64(limit), int(remaining), nil
}

// bump increments the window counter and returns its new value plus the time
// left in the window.
func (c *Cache) bump(ctx context.Context, key string, delta int64, window time.Duration) (int64, time.Duration, error) {
	if window <= 0 {
		window = time.Minute
	}
	now := c.clk.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.counters[key]
	if !ok || e.expired(now) {
		e = c.makeEntry(nil, window, now)
	}
	cur := int64(0)
	if len(e.val) > 0 {
		var v int64
		if err := json.Unmarshal(e.val, &v); err != nil {
			v = 0
		}
		cur = v
	}
	cur += delta
	e.val = encodeInt(cur)
	c.counters[key] = e

	resetIn := time.Duration(0)
	if e.hasTTL {
		resetIn = e.expireAt.Sub(now)
		if resetIn < 0 {
			resetIn = 0
		}
	}
	return cur, resetIn, nil
}

// AcquireConcurrency takes one concurrency slot on scope. ok=false means limit
// slots are already in use. The returned release function decrements exactly
// once, however often it is called; slots also expire after ttl so a crashed
// request cannot leak a slot forever. ctx must not be cancelled inside release:
// the release function always completes.
func (c *Cache) AcquireConcurrency(ctx context.Context, scope string, limit int, ttl time.Duration) (bool, func(), error) {
	if err := ctx.Err(); err != nil {
		return false, nil, err
	}
	if limit <= 0 {
		// Unlimited: hand out a no-op release so callers need no special case.
		return true, func() {}, nil
	}
	key := cache.ConcurrencyKey(scope)
	now := c.clk.Now()

	c.mu.Lock()
	e, ok := c.counters[key]
	if !ok || e.expired(now) {
		e = c.makeEntry(nil, ttl, now)
	}
	cur := int64(0)
	if len(e.val) > 0 {
		var v int64
		if err := json.Unmarshal(e.val, &v); err != nil {
			v = 0
		}
		cur = v
	}
	if cur >= int64(limit) {
		c.counters[key] = e
		c.mu.Unlock()
		return false, func() {}, nil
	}
	e.val = encodeInt(cur + 1)
	c.counters[key] = e
	c.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			cur, ok := c.counters[key]
			if !ok {
				return
			}
			have := int64(0)
			if len(cur.val) > 0 {
				var v int64
				if err := json.Unmarshal(cur.val, &v); err == nil {
					have = v
				}
			}
			if have > 0 {
				have--
			}
			cur.val = encodeInt(have)
			c.counters[key] = cur
		})
	}
	return true, release, nil
}

// CheckNonce reports ok=true the first time (appID, nonce) is seen and false
// for every replay inside ttl.
func (c *Cache) CheckNonce(ctx context.Context, appID, nonce string, ttl time.Duration) (bool, error) {
	if appID == "" || nonce == "" {
		return false, errs.InvalidRequest("app_id and nonce are required")
	}
	return c.SetNX(ctx, cache.NonceKey(appID, nonce), []byte("1"), ttl)
}

// GetJSON decodes the value stored under key into dst.
func (c *Cache) GetJSON(ctx context.Context, key string, dst any) (bool, error) {
	raw, found, err := c.Get(ctx, key)
	if err != nil || !found {
		return found, err
	}
	if dst == nil {
		return true, nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return false, errs.Internal("cache json decode failed", err)
	}
	return true, nil
}

// SetJSON encodes val into the cache under key with the given TTL.
func (c *Cache) SetJSON(ctx context.Context, key string, val any, ttl time.Duration) error {
	raw, err := json.Marshal(val)
	if err != nil {
		return errs.Internal("cache json encode failed", err)
	}
	return c.Set(ctx, key, raw, ttl)
}

// makeEntry builds an entry with an absolute expiry derived from ttl.
func (c *Cache) makeEntry(val []byte, ttl time.Duration, now time.Time) entry {
	e := entry{val: cloneBytes(val)}
	if ttl > 0 {
		e.hasTTL = true
		e.expireAt = now.Add(ttl)
	}
	return e
}

// cloneBytes returns a copy of b; nil stays nil.
func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// encodeInt renders v as ASCII decimal so counters stay JSON-readable.
func encodeInt(v int64) []byte {
	var buf [20]byte
	i := len(buf)
	neg := v < 0
	var u uint64
	if neg {
		u = uint64(-v)
	} else {
		u = uint64(v)
	}
	if u == 0 {
		return []byte{'0'}
	}
	for u > 0 {
		i--
		buf[i] = byte('0' + u%10)
		u /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return cloneBytes(buf[i:])
}
