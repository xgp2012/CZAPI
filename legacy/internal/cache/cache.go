// Package cache defines the low-latency state contract (quota pre-holds, rate
// limiting, nonce replay protection, key validation cache, breaker state).
//
// Two drivers: "memory" (in-process, zero-dependency) and "redis" (production).
// Both must be safe for concurrent use and must implement the semantics
// documented per method.
package cache

import (
	"context"
	"time"
)

// Cache groups the low-latency primitives.
type Cache interface {
	// Ping verifies connectivity.
	Ping(ctx context.Context) error
	Close() error

	// --- Generic KV with TTL ---
	Get(ctx context.Context, key string) (val []byte, found bool, err error)
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	SetNX(ctx context.Context, key string, val []byte, ttl time.Duration) (ok bool, err error)
	Del(ctx context.Context, keys ...string) error
	// IncrBy increments an integer counter and returns the new value, setting
	// the TTL on first creation.
	IncrBy(ctx context.Context, key string, delta int64, ttl time.Duration) (int64, error)

	// --- Quota pre-hold (plan §6.4) ---
	//
	// A hold reserves an amount against a scope and is later settled with the
	// real amount. Scopes used by the gateway:
	//   HoldKeyBalance(accountID)  — micro-yuan, balance bucket
	//   HoldSubscription(subID)    — tokens, subscription period quota
	//
	// Hold atomically reserves amount against scope. budget is the maximum that
	// may be held at once; ok=false means "budget - alreadyHeld < amount".
	Hold(ctx context.Context, scope string, amount, budget int64, ttl time.Duration) (holdID string, ok bool, err error)
	// Release settles a hold: the unused part (amount - actual) is returned to
	// the scope. Returns the refunded amount. Releasing an unknown hold is a
	// no-op returning 0 (safe for retries).
	Release(ctx context.Context, scope, holdID string, actual int64) (refund int64, err error)
	// Held returns the currently held amount for a scope.
	Held(ctx context.Context, scope string) (int64, error)

	// --- Rate limiting (token bucket, plan §6.4) ---
	// AllowRequest consumes one token from the per-minute request bucket.
	AllowRequest(ctx context.Context, scope string, limit int, window time.Duration) (ok bool, remaining int, resetIn time.Duration, err error)
	// AllowTokens consumes tokens from the per-minute token bucket (best effort).
	AllowTokens(ctx context.Context, scope string, tokens, limit int, window time.Duration) (ok bool, remaining int, err error)
	// AcquireConcurrency takes one concurrency slot; the returned release
	// function must always be called. ok=false when the limit is reached.
	AcquireConcurrency(ctx context.Context, scope string, limit int, ttl time.Duration) (ok bool, release func(), err error)

	// --- Replay protection (plan §8.7) ---
	// CheckNonce returns ok=true the first time nonce is seen, false afterwards.
	CheckNonce(ctx context.Context, appID, nonce string, ttl time.Duration) (ok bool, err error)

	// --- JSON helper cache for validated keys / channel health ---
	GetJSON(ctx context.Context, key string, dst any) (found bool, err error)
	SetJSON(ctx context.Context, key string, val any, ttl time.Duration) error
}

// KeyCacheKey builds the cache key for a validated API key lookup hash.
func KeyCacheKey(lookupHash string) string { return "cz:key:" + lookupHash }

// QuotaKey builds the pre-hold scope for a balance bucket.
func HoldBalanceScope(accountID uint64) string { return "bal:" + u64(accountID) }

// HoldSubscriptionScope builds the pre-hold scope for a subscription quota.
func HoldSubscriptionScope(subID uint64) string { return "sub:" + u64(subID) }

// QuotaKey is retained for callers indexing holds by API key.
func QuotaKey(keyID uint64) string { return "cz:quota:hold:" + u64(keyID) }

// RPMKey builds the per-minute request bucket key.
func RPMKey(scope string) string { return "cz:rl:rpm:" + scope }

// TPMKey builds the per-minute token bucket key.
func TPMKey(scope string) string { return "cz:rl:tpm:" + scope }

// ConcurrencyKey builds the concurrency slot key.
func ConcurrencyKey(scope string) string { return "cz:rl:conc:" + scope }

// NonceKey builds the replay-protection key.
func NonceKey(appID, nonce string) string { return "cz:nonce:" + appID + ":" + nonce }

// KeyDailyKey builds the per-key daily spend counter key (plan §15.2 #2).
func KeyDailyKey(keyID uint64, day string) string { return "cz:cap:key:" + u64(keyID) + ":" + day }

// AccountMonthlyKey builds the per-account monthly spend counter key.
func AccountMonthlyKey(accountID uint64, month string) string {
	return "cz:cap:acc:" + u64(accountID) + ":" + month
}

// GlobalDailyKey builds the site-wide daily spend counter key.
func GlobalDailyKey(day string) string { return "cz:cap:global:" + day }

func u64(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
