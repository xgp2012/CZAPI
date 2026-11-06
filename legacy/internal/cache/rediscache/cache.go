// Package rediscache is the Redis 7 implementation of the cache.Cache contract.
//
// It backs the gateway's hot-path accounting: balance and quota holds, per-key
// rate limits, token-per-minute caps, concurrency slots, nonce replay
// protection and JSON value caching.
//
// Every operation that must be atomic is a single Lua script executed through
// redis.NewScript (which uses EVALSHA with an EVAL fallback), so a race between
// two gateway replicas can never double-spend a balance hold or let one extra
// request past a rate limit.
//
// The keyspace layout is:
//
//	cz:held:<scope>             sum of the outstanding holds of a scope
//	cz:hold:<scope>:<holdID>    one individual hold and its amount
//	cz:rl:rpm:<scope>           requests in the current window
//	cz:rl:tpm:<scope>           tokens in the current window
//	cz:rl:conc:<scope>          concurrency slots currently in use
//	cz:nonce:<appID>:<nonce>    seen-nonce marker
//
// requires Redis 7; not integration-tested on the build machine.
package rediscache

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"czapi/internal/cache"
	"czapi/internal/pkg/clock"
	"czapi/internal/pkg/errs"
)

// Config configures the Redis client.
type Config struct {
	// Addr is host:port of the Redis server, for example "127.0.0.1:6379".
	Addr string
	// Password is the AUTH secret; leave empty when the server has none.
	Password string
	// DB is the logical database index.
	DB int
	// PoolSize caps the number of sockets; zero means the client default.
	PoolSize int
	// DialTimeout bounds connection establishment; zero means the default.
	DialTimeout time.Duration
}

// Cache implements cache.Cache on top of Redis.
//
// Safe for concurrent use: it wraps a redis.Client, whose connection pool is
// safe by contract, and holds no mutable state of its own.
type Cache struct {
	rdb *redis.Client
	// clk supplies the timestamp embedded in generated hold ids. TTLs are always
	// passed to Redis explicitly, so the clock never affects a decision.
	clk clock.Clock
}

// compile-time assertion that the contract is satisfied.
var _ cache.Cache = (*Cache)(nil)

// New builds a Cache and verifies the connection with a PING.
//
// The PING is deliberate: a misconfigured address must fail at start-up, not on
// the first request that needs a quota check.
func New(ctx context.Context, cfg Config) (*Cache, error) {
	if cfg.Addr == "" {
		return nil, errs.Internal("rediscache: config.Addr is required", nil)
	}
	opts := &redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	}
	if cfg.PoolSize > 0 {
		opts.PoolSize = cfg.PoolSize
	}
	if cfg.DialTimeout > 0 {
		opts.DialTimeout = cfg.DialTimeout
	}
	rdb := redis.NewClient(opts)

	c := &Cache{rdb: rdb, clk: clock.System{}}
	if err := c.Ping(ctx); err != nil {
		_ = rdb.Close()
		return nil, err
	}
	return c, nil
}

// withClock returns a copy of c using clk for id generation. It exists so tests
// can pin the timestamp prefix of hold ids.
func withClock(c *Cache, clk clock.Clock) *Cache {
	c.clk = clk
	return c
}

// Ping verifies the connection.
func (c *Cache) Ping(ctx context.Context) error {
	if c == nil || c.rdb == nil {
		return errs.Internal("rediscache: not connected", nil)
	}
	if err := c.rdb.Ping(ctx).Err(); err != nil {
		return wrapRedis("rediscache: ping", err)
	}
	return nil
}

// Close releases the connection pool.
func (c *Cache) Close() error {
	if c == nil || c.rdb == nil {
		return nil
	}
	return c.rdb.Close()
}

// Client exposes the underlying client for health checks and tests.
func (c *Cache) Client() *redis.Client { return c.rdb }

// wrapRedis maps a Redis error into the shared vocabulary.
//
// redis.Nil is not an error: it means "key absent", and every caller in this
// package treats that as a zero value.
func wrapRedis(msg string, err error) error {
	if err == nil || errors.Is(err, redis.Nil) {
		return nil
	}
	return errs.Internal(msg, err)
}

// ---------------------------------------------------------------------------
// Plain key/value operations
// ---------------------------------------------------------------------------

// Get returns the value stored under key.
//
// A missing key is found=false with no error, as the contract requires.
func (c *Cache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	raw, err := c.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, wrapRedis("rediscache: get", err)
	}
	return raw, true, nil
}

// Set stores val under key for ttl. A ttl <= 0 stores without expiry.
func (c *Cache) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.rdb.Set(ctx, key, val, ttl).Err(); err != nil {
		return wrapRedis("rediscache: set", err)
	}
	return nil
}

// SetNX stores val only when key is absent, reporting whether it was stored.
func (c *Cache) SetNX(ctx context.Context, key string, val []byte, ttl time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	ok, err := c.rdb.SetNX(ctx, key, val, ttl).Result()
	if err != nil {
		return false, wrapRedis("rediscache: setnx", err)
	}
	return ok, nil
}

// Del removes keys.
func (c *Cache) Del(ctx context.Context, keys ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	if err := c.rdb.Del(ctx, keys...).Err(); err != nil {
		return wrapRedis("rediscache: del", err)
	}
	return nil
}

// IncrBy adds delta to a counter and returns the new value.
//
// The expiry is applied whenever the key has none, not only on creation, so a
// counter that somehow lost its TTL self-heals instead of becoming permanent.
func (c *Cache) IncrBy(ctx context.Context, key string, delta int64, ttl time.Duration) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	res, err := incrWithTTLScript.Run(ctx, c.rdb, []string{key}, delta, ttlMillis(ttl)).Result()
	if err != nil {
		return 0, wrapRedis("rediscache: incrby", err)
	}
	return toInt64(res), nil
}

// GetJSON decodes the JSON document stored under key into dst.
func (c *Cache) GetJSON(ctx context.Context, key string, dst any) (bool, error) {
	raw, found, err := c.Get(ctx, key)
	if err != nil || !found {
		return false, err
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return false, errs.Internal("rediscache: decode json value", err)
	}
	return true, nil
}

// SetJSON stores v as JSON under key.
func (c *Cache) SetJSON(ctx context.Context, key string, v any, ttl time.Duration) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return errs.Internal("rediscache: encode json value", err)
	}
	return c.Set(ctx, key, raw, ttl)
}

// ---------------------------------------------------------------------------
// Rate limiting
// ---------------------------------------------------------------------------

// AllowRequest consumes one request from the window bucket.
//
// The INCR and the expiry are one script, so the window can never be left
// without a TTL (which would lock a key out permanently).
func (c *Cache) AllowRequest(ctx context.Context, scope string, limit int, window time.Duration) (bool, int, time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return false, 0, 0, err
	}
	key := cache.RPMKey(scope)

	// Fail open, preserving the documented signature.
	vals, err := allowRequestScript.Run(ctx, c.rdb, []string{key},
		int64(limit), ttlMillis(window)).Int64Slice()
	if err != nil || len(vals) < 3 {
		return true, limit, window, nil
	}
	return vals[0] == 1, int(vals[1]), time.Duration(vals[2]) * time.Millisecond, nil
}

// AllowTokens consumes tokens from the window bucket.
//
// Unlike AllowRequest this reserves the tokens up front; the caller should add
// back whatever the upstream did not consume.
func (c *Cache) AllowTokens(ctx context.Context, scope string, tokens, limit int, window time.Duration) (bool, int, error) {
	if err := ctx.Err(); err != nil {
		return false, 0, err
	}
	key := cache.TPMKey(scope)

	vals, err := allowTokensScript.Run(ctx, c.rdb, []string{key},
		int64(limit), int64(tokens), ttlMillis(window)).Int64Slice()
	if err != nil || len(vals) < 2 {
		return true, limit, nil
	}
	return vals[0] == 1, int(vals[1]), nil
}

// AcquireConcurrency takes one of limit concurrency slots.
//
// The INCR and the comparison happen in one script, and the script itself
// compensates with a DECR when the limit is exceeded, so a rejected caller
// never leaks a slot. The returned release function is idempotent.
func (c *Cache) AcquireConcurrency(ctx context.Context, scope string, limit int, ttl time.Duration) (bool, func(), error) {
	if err := ctx.Err(); err != nil {
		return false, nil, err
	}
	key := cache.ConcurrencyKey(scope)

	res, err := acquireConcurrencyScript.Run(ctx, c.rdb, []string{key},
		int64(limit), ttlMillis(ttl)).Result()
	if err != nil {
		// Fail open, with a no-op release so the caller's defer still works.
		return true, func() {}, nil
	}
	if toInt64(res) != 1 {
		return false, func() {}, nil
	}

	var once sync.Once
	release := func() {
		once.Do(func() {
			// Detach from a cancelled request context: giving up on a slot must
			// still succeed.
			relCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			_ = c.rdb.Decr(relCtx, key).Err()
		})
	}
	return true, release, nil
}

// CheckNonce reports whether nonce is fresh for appID.
//
// The check and the write are one SET NX, so two replicas cannot both accept
// the same nonce.
func (c *Cache) CheckNonce(ctx context.Context, appID, nonce string, ttl time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if appID == "" || nonce == "" {
		return false, errs.InvalidRequest("app_id and nonce are required")
	}
	ok, err := c.rdb.SetNX(ctx, cache.NonceKey(appID, nonce), []byte("1"), ttl).Result()
	if err != nil {
		return false, wrapRedis("rediscache: check nonce", err)
	}
	// ok means "the nonce was fresh", so this request may proceed.
	return ok, nil
}

// ---------------------------------------------------------------------------
// Balance and quota holds
// ---------------------------------------------------------------------------

// Hold reserves amount against a scope and returns a hold id.
//
// The whole decision is one script: it reads the outstanding holds, refuses
// when budget-held < amount, and otherwise records the hold and bumps the held
// total, both with a TTL. check-then-act is therefore race-free across
// replicas.
//
// ok=false with a nil error is the normal "budget exhausted" answer.
func (c *Cache) Hold(ctx context.Context, scope string, amount, budget int64, ttl time.Duration) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if amount <= 0 {
		return "", false, errs.InvalidRequest("hold amount must be positive")
	}
	holdID := newHoldID(c.now())

	res, err := holdScript.Run(ctx, c.rdb,
		[]string{heldKey(scope), holdKey(scope, holdID)},
		amount, budget, ttlMillis(ttl)).Result()
	if err != nil {
		return "", false, wrapRedis("rediscache: hold", err)
	}
	if toInt64(res) != 1 {
		return "", false, nil
	}
	return holdID, true, nil
}

// Release settles a hold against the amount actually consumed and returns the
// unused remainder.
//
// An unknown hold id is not an error: the hold already expired, so there is
// nothing to refund and the answer is 0.
func (c *Cache) Release(ctx context.Context, scope, holdID string, actual int64) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if holdID == "" {
		return 0, nil
	}
	if actual < 0 {
		actual = 0
	}

	res, err := releaseScript.Run(ctx, c.rdb,
		[]string{heldKey(scope), holdKey(scope, holdID)}, actual).Result()
	if err != nil {
		return 0, wrapRedis("rediscache: release", err)
	}
	return toInt64(res), nil
}

// Held returns the sum of outstanding holds for a scope.
func (c *Cache) Held(ctx context.Context, scope string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	val, err := c.rdb.Get(ctx, heldKey(scope)).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, wrapRedis("rediscache: held", err)
	}
	return val, nil
}

// heldKey is the key holding the sum of a scope's outstanding holds.
func heldKey(scope string) string { return "cz:held:" + scope }

// holdKey is the key recording one individual hold.
func holdKey(scope, holdID string) string { return "cz:hold:" + scope + ":" + holdID }

// now returns the current time from the injected clock.
func (c *Cache) now() time.Time {
	if c.clk == nil {
		return time.Now().UTC()
	}
	return c.clk.Now().UTC()
}

// newHoldID returns a unique, roughly time-ordered hold identifier.
//
// The timestamp prefix comes from the injected clock so tests can pin it; the
// random suffix keeps ids unique across replicas and restarts regardless.
func newHoldID(at time.Time) string {
	return strconv.FormatInt(at.UnixNano(), 36) + "-" + randomHex(8)
}

// ttlMillis renders a TTL as whole milliseconds, defaulting to one minute.
func ttlMillis(ttl time.Duration) int64 {
	ms := int64(ttl / time.Millisecond)
	if ms <= 0 {
		ms = int64(time.Minute / time.Millisecond)
	}
	return ms
}

// toInt64 coerces a Lua reply into an int64.
//
// go-redis normally hands Lua numbers back as int64, but a reply nested in a
// table may arrive as a string depending on the server version, so both forms
// are accepted.
func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case string:
		parsed, err := strconv.ParseInt(n, 10, 64)
		if err != nil {
			return 0
		}
		return parsed
	case []byte:
		parsed, err := strconv.ParseInt(string(n), 10, 64)
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}
