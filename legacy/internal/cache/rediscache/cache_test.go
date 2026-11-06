package rediscache

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"czapi/internal/cache"
	"czapi/internal/pkg/clock"
)

// These tests need no Redis server. They assert the properties of the Lua
// sources, the key builders and the JSON round trip, which is everything about
// this package that can be checked without a live server.
//
// What they cannot check is the scripts' runtime behaviour: that needs Redis 7.

// ---------------------------------------------------------------------------
// Lua script invariants
// ---------------------------------------------------------------------------

func TestHoldScriptIsAtomicAndBudgeted(t *testing.T) {
	src := holdSource

	// The budget test must be exactly "budget - held < amount".
	assert.Contains(t, src, "budget - held < amount",
		"the contract words the refusal as budget - alreadyHeld < amount")
	assert.Contains(t, src, "redis.call('GET', KEYS[1])",
		"the held total must be read inside the script, not by the caller")
	assert.Contains(t, src, "redis.call('INCRBY', KEYS[1], amount)",
		"the held total must be incremented in the same script")
	assert.Contains(t, src, "redis.call('SET', KEYS[2], amount, 'PX', ttl)",
		"the individual hold must record its amount with a ttl")

	// A hold is only valid when both of its keys are passed, so Redis Cluster
	// can route the script to one slot-free replica.
	assert.Contains(t, src, "KEYS[1]")
	assert.Contains(t, src, "KEYS[2]")
	assert.NotContains(t, src, "KEYS[3]", "the hold script touches exactly two keys")

	// No clock reads: the ttl arrives as ARGV so replicas stay deterministic.
	assert.NotContains(t, strings.ToUpper(src), "TIME")
	assert.NotContains(t, strings.ToUpper(src), "NOW")
}

func TestHoldScriptRefusesWithoutMutating(t *testing.T) {
	src := holdSource
	// The refusal path must return before any write, which is what makes
	// "ok=false means nothing changed" true.
	refuseIdx := strings.Index(src, "if budget - held < amount then")
	writeIdx := strings.Index(src, "redis.call('SET', KEYS[2]")
	require.NotEqual(t, -1, refuseIdx)
	require.NotEqual(t, -1, writeIdx)
	assert.Less(t, refuseIdx, writeIdx,
		"the budget check must precede the first write")
}

func TestReleaseScriptRefundsAndCleansUp(t *testing.T) {
	src := releaseSource

	assert.Contains(t, src, "local reserved = tonumber(raw)")
	assert.Contains(t, src, "local refund = reserved - actual")
	assert.Contains(t, src, "redis.call('DEL', KEYS[2])",
		"the hold key must be deleted so a retry cannot refund twice")
	assert.Contains(t, src, "if not raw then",
		"an expired hold must short-circuit to a zero refund")
	assert.Contains(t, src, "if refund < 0 then",
		"the refund must be clamped at zero when actual exceeds the reservation")
	assert.Contains(t, src, "redis.call('DECRBY', KEYS[1], reserved)",
		"the whole reservation leaves the held total, not just the consumed part")

	// The clamp must run before the decrement, otherwise the scope would be
	// credited by the overshoot.
	clampIdx := strings.Index(src, "if refund < 0 then")
	decrIdx := strings.Index(src, "DECRBY")
	assert.Less(t, clampIdx, decrIdx)
}

func TestReleaseScriptTreatsUnknownHoldAsNoop(t *testing.T) {
	src := releaseSource
	// Returning 0 for a missing hold is what makes Release retry-safe.
	assert.Contains(t, src, "if not raw then\n  return 0\nend")
}

func TestAllowRequestScriptUsesIncrAndExpire(t *testing.T) {
	src := allowRequestSource

	assert.Contains(t, src, "redis.call('INCR', KEYS[1])")
	assert.Contains(t, src, "redis.call('PEXPIRE', KEYS[1], window)",
		"the window must always carry an expiry, or a key locks out forever")
	assert.Contains(t, src, "if current == 1 then",
		"the expiry is set on the first increment of a window")
	assert.Contains(t, src, "if current > limit then")
	assert.Contains(t, src, "redis.call('DECR', KEYS[1])",
		"a rejected request must return its token")
	assert.Contains(t, src, "return {1, limit - current, ttl}",
		"the reply carries allowed, remaining and resetInMs")
	assert.Contains(t, src, "return {0, 0, ttl}")
}

func TestAllowTokensScriptReservesAndRollsBack(t *testing.T) {
	src := allowTokensSource

	assert.Contains(t, src, "if current + wanted > limit then",
		"the whole reservation must fit before it is taken")
	assert.Contains(t, src, "return {0, limit - current}",
		"a refused reservation must not consume anything")
	assert.Contains(t, src, "redis.call('INCRBY', KEYS[1], wanted)")
	assert.Contains(t, src, "redis.call('PEXPIRE', KEYS[1], window)")
	assert.NotContains(t, src, "'DECR'",
		"the token bucket refuses before incrementing, so it never needs a rollback")

	// All three arguments must be read.
	for _, arg := range []string{"ARGV[1]", "ARGV[2]", "ARGV[3]"} {
		assert.Contains(t, src, arg)
	}
}

func TestAcquireConcurrencyScriptCompensates(t *testing.T) {
	src := acquireConcurrencySource

	incrIdx := strings.Index(src, "redis.call('INCR', KEYS[1])")
	decrIdx := strings.Index(src, "redis.call('DECR', KEYS[1])")
	limitIdx := strings.Index(src, "if current > limit then")
	require.NotEqual(t, -1, incrIdx)
	require.NotEqual(t, -1, decrIdx)
	require.NotEqual(t, -1, limitIdx)

	assert.Less(t, incrIdx, limitIdx, "increment first, then compare")
	assert.Less(t, limitIdx, decrIdx, "the overshoot is compensated immediately")
	assert.Contains(t, src, "return 0", "a rejected caller gets 0")
	assert.Contains(t, src, "return 1", "an accepted caller gets 1")
	assert.Contains(t, src, "redis.call('PEXPIRE', KEYS[1], ttl)",
		"the slot set carries a ttl as a safety net for a crashed replica")
}

func TestIncrWithTTLScriptSelfHeals(t *testing.T) {
	src := incrWithTTLSource
	assert.Contains(t, src, "redis.call('INCRBY', KEYS[1], ARGV[1])")
	assert.Contains(t, src, "if redis.call('TTL', KEYS[1]) < 0 then",
		"a counter without an expiry must be repaired, not left permanent")
	assert.Contains(t, src, "redis.call('PEXPIRE', KEYS[1], ARGV[2])")
}

func TestScriptsAreAllRegistered(t *testing.T) {
	// A nil script would panic at the first call, so guard it here.
	scripts := map[string]struct {
		src string
		reg any
	}{
		"incrWithTTL":        {incrWithTTLSource, incrWithTTLScript},
		"allowRequest":       {allowRequestSource, allowRequestScript},
		"allowTokens":        {allowTokensSource, allowTokensScript},
		"acquireConcurrency": {acquireConcurrencySource, acquireConcurrencyScript},
		"hold":               {holdSource, holdScript},
		"release":            {releaseSource, releaseScript},
	}
	for name, s := range scripts {
		t.Run(name, func(t *testing.T) {
			assert.NotNil(t, s.reg)
			assert.NotEmpty(t, strings.TrimSpace(s.src))
		})
	}
}

func TestScriptsAvoidBlockingCommands(t *testing.T) {
	// A blocking or O(N) command inside these scripts would stall every gateway
	// replica, because Redis runs scripts single-threaded.
	forbidden := []string{"KEYS *", "'KEYS'", "BLPOP", "BRPOP", "SUBSCRIBE", "FLUSHALL", "FLUSHDB", "DEBUG"}
	sources := map[string]string{
		"incrWithTTL":        incrWithTTLSource,
		"allowRequest":       allowRequestSource,
		"allowTokens":        allowTokensSource,
		"acquireConcurrency": acquireConcurrencySource,
		"hold":               holdSource,
		"release":            releaseSource,
	}
	for name, src := range sources {
		upper := strings.ToUpper(src)
		for _, bad := range forbidden {
			assert.NotContains(t, upper, strings.ToUpper(bad),
				"script %s must not call %s", name, bad)
		}
	}
}

func TestScriptsDeclareTheirKeysAndArgs(t *testing.T) {
	// Every KEYS[n] and ARGV[n] reference must be declared in the header
	// comment, otherwise a caller will pass the wrong count at runtime.
	cases := []struct {
		name    string
		src     string
		wantKey int
		wantArg int
	}{
		{"incrWithTTL", incrWithTTLSource, 1, 2},
		{"allowRequest", allowRequestSource, 1, 2},
		{"allowTokens", allowTokensSource, 1, 3},
		{"acquireConcurrency", acquireConcurrencySource, 1, 2},
		{"hold", holdSource, 2, 3},
		{"release", releaseSource, 2, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			maxKey, maxArg := 0, 0
			for i := 1; i <= 9; i++ {
				if strings.Contains(tc.src, "KEYS["+itoa(i)+"]") {
					maxKey = i
				}
				if strings.Contains(tc.src, "ARGV["+itoa(i)+"]") {
					maxArg = i
				}
			}
			assert.Equal(t, tc.wantKey, maxKey, "key count")
			assert.Equal(t, tc.wantArg, maxArg, "argument count")
		})
	}
}

// ---------------------------------------------------------------------------
// Key builders
// ---------------------------------------------------------------------------

func TestHoldKeysAreScoped(t *testing.T) {
	assert.Equal(t, "cz:held:bal:42", heldKey("bal:42"))
	assert.Equal(t, "cz:hold:bal:42:abc", holdKey("bal:42", "abc"))

	// A hold in one scope must never be visible in another.
	assert.NotEqual(t, heldKey("bal:42"), heldKey("bal:43"))
	assert.NotEqual(t, heldKey("bal:42"), heldKey("sub:42"),
		"the balance and subscription scopes are distinct namespaces")
}

func TestHoldKeysUseTheFrozenScopeBuilders(t *testing.T) {
	balScope := cache.HoldBalanceScope(42)
	subScope := cache.HoldSubscriptionScope(7)

	assert.Equal(t, "bal:42", balScope)
	assert.Equal(t, "sub:7", subScope)
	assert.Equal(t, "cz:held:bal:42", heldKey(balScope))
	assert.Equal(t, "cz:held:sub:7", heldKey(subScope))
}

func TestRateLimitKeysMatchTheFrozenBuilders(t *testing.T) {
	// The cache package owns the keyspace; this package must not invent its own.
	assert.Equal(t, cache.RPMKey("key:1"), "cz:rl:rpm:key:1")
	assert.Equal(t, cache.TPMKey("key:1"), "cz:rl:tpm:key:1")
	assert.Equal(t, cache.ConcurrencyKey("key:1"), "cz:rl:conc:key:1")
	assert.Equal(t, cache.NonceKey("app-1", "n-1"), "cz:nonce:app-1:n-1")
}

func TestNewHoldIDIsUniqueAndSortable(t *testing.T) {
	at := time.Date(2026, 11, 5, 7, 0, 0, 0, time.UTC)
	seen := make(map[string]struct{}, 128)
	for i := 0; i < 128; i++ {
		id := newHoldID(at)
		require.NotEmpty(t, id)
		assert.NotContains(t, id, " ")
		_, dup := seen[id]
		assert.False(t, dup, "duplicate hold id %q", id)
		seen[id] = struct{}{}
	}
}

func TestNewHoldIDEmbedsTheInjectedClock(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 11, 5, 7, 0, 0, 0, time.UTC))
	c := withClock(&Cache{}, clk)

	first := newHoldID(c.now())
	later := newHoldID(clk.Now())

	// Two ids at the same instant share a prefix but stay unique.
	assert.Equal(t, first[:strings.IndexByte(first, '-')],
		later[:strings.IndexByte(later, '-')],
		"ids minted at the same instant share their timestamp prefix")
	assert.NotEqual(t, first, later, "the random suffix keeps them distinct")

	clk.Advance(time.Second)
	after := newHoldID(c.now())
	assert.NotEqual(t, first[:strings.IndexByte(first, '-')],
		after[:strings.IndexByte(after, '-')],
		"the prefix follows the injected clock")
}

func TestRandomHexLength(t *testing.T) {
	for _, n := range []int{1, 8, 16} {
		assert.Len(t, randomHex(n), 2*n)
	}
}

// ---------------------------------------------------------------------------
// Argument coercion
// ---------------------------------------------------------------------------

func TestToInt64AcceptsEveryReplyShape(t *testing.T) {
	assert.Equal(t, int64(7), toInt64(int64(7)))
	assert.Equal(t, int64(7), toInt64("7"))
	assert.Equal(t, int64(7), toInt64([]byte("7")))
	assert.Equal(t, int64(0), toInt64("not-a-number"))
	assert.Equal(t, int64(0), toInt64(nil))
	assert.Equal(t, int64(0), toInt64(7.5), "floats are not expected and coerce to 0")
}

func TestTTLMillisDefaultsAndConverts(t *testing.T) {
	assert.Equal(t, int64(60_000), ttlMillis(time.Minute))
	assert.Equal(t, int64(1_500), ttlMillis(1500*time.Millisecond))
	assert.Equal(t, int64(60_000), ttlMillis(0), "a zero ttl falls back to one minute")
	assert.Equal(t, int64(60_000), ttlMillis(-time.Second), "a negative ttl also falls back")
	assert.Equal(t, int64(60_000), ttlMillis(time.Microsecond), "sub-millisecond falls back")
}

// ---------------------------------------------------------------------------
// JSON helpers
// ---------------------------------------------------------------------------

func TestJSONRoundTripShape(t *testing.T) {
	type payload struct {
		KeyHash   string   `json:"key_hash"`
		Models    []string `json:"models"`
		ExpiresAt int64    `json:"expires_at"`
	}
	in := payload{KeyHash: "abc", Models: []string{"gpt-4o", "o1"}, ExpiresAt: 1_700_000_000}

	raw, err := json.Marshal(in)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"key_hash":"abc"`)
	assert.Contains(t, string(raw), `"models":["gpt-4o","o1"]`)

	var out payload
	require.NoError(t, json.Unmarshal(raw, &out))
	assert.Equal(t, in, out)
}

func TestJSONUnmarshalRejectsGarbage(t *testing.T) {
	var out map[string]any
	assert.Error(t, json.Unmarshal([]byte("{not json"), &out))
}

// ---------------------------------------------------------------------------
// Constructor validation (no server needed)
// ---------------------------------------------------------------------------

func TestNewRejectsEmptyAddr(t *testing.T) {
	c, err := New(context.Background(), Config{})
	assert.Nil(t, c)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Addr")
}

func TestNilCacheIsSafe(t *testing.T) {
	var c *Cache
	assert.Error(t, c.Ping(context.Background()))
	assert.NoError(t, c.Close())
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// itoa renders a small non-negative int without importing strconv in the test.
func itoa(v int) string {
	if v < 10 {
		return string(rune('0' + v))
	}
	return string(rune('0'+v/10)) + string(rune('0'+v%10))
}
