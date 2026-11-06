package rediscache

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"github.com/redis/go-redis/v9"
)

// This file holds every Lua script as an exported-by-package constant so the
// exact source can be asserted by unit tests. The scripts are registered with
// redis.NewScript, which sends EVALSHA and falls back to EVAL on NOSCRIPT.
//
// Lua rules followed here:
//   - KEYS[] holds every key the script touches, so Redis Cluster can route it;
//   - no clock reads, no randomness: ttl and ids come in as ARGV so a replica
//     re-running the script stays deterministic;
//   - every reply is a number or a flat array of numbers.

// incrWithTTLSource increments a counter and attaches an expiry when the key
// has none. It returns the new value.
//
// A plain INCR plus EXPIRE is two round trips and can lose the expiry if the
// connection drops in between; doing both in one script makes the counter
// self-healing instead of permanent.
const incrWithTTLSource = `
local n = redis.call('INCRBY', KEYS[1], ARGV[1])
if redis.call('TTL', KEYS[1]) < 0 then
  redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return n
`

// allowRequestSource implements the request-rate window.
//
// KEYS[1] counter, ARGV[1] limit, ARGV[2] window in ms.
// Returns {allowed, remaining, resetInMs}.
//
// The counter is incremented first so that a burst cannot all pass; the expiry
// is set on the first increment of a window. When the limit is exceeded the
// counter is rolled back with DECR so the rejected request does not consume
// capacity that a later retry could have used.
const allowRequestSource = `
local limit = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local current = redis.call('INCR', KEYS[1])
if current == 1 then
  redis.call('PEXPIRE', KEYS[1], window)
end
local ttl = redis.call('PTTL', KEYS[1])
if ttl < 0 then
  redis.call('PEXPIRE', KEYS[1], window)
  ttl = window
end
if current > limit then
  redis.call('DECR', KEYS[1])
  return {0, 0, ttl}
end
return {1, limit - current, ttl}
`

// allowTokensSource implements the token-per-minute window.
//
// KEYS[1] counter, ARGV[1] limit, ARGV[2] tokens, ARGV[3] window in ms.
// Returns {allowed, remaining}.
//
// Tokens are reserved up front and rolled back when the reservation would
// exceed the limit, so a huge request cannot permanently poison the bucket.
const allowTokensSource = `
local limit = tonumber(ARGV[1])
local wanted = tonumber(ARGV[2])
local window = tonumber(ARGV[3])
local current = tonumber(redis.call('GET', KEYS[1]) or '0')
if current + wanted > limit then
  return {0, limit - current}
end
local next = redis.call('INCRBY', KEYS[1], wanted)
if next == wanted then
  redis.call('PEXPIRE', KEYS[1], window)
elseif redis.call('TTL', KEYS[1]) < 0 then
  redis.call('PEXPIRE', KEYS[1], window)
end
return {1, limit - next}
`

// acquireConcurrencySource takes one concurrency slot.
//
// KEYS[1] counter, ARGV[1] limit, ARGV[2] slot ttl in ms.
// Returns 1 when the slot was taken and 0 when the limit is reached.
//
// When the limit is exceeded the script compensates with DECR inside the same
// atomic step, so no slot can leak. The TTL is a safety net for a replica that
// dies holding a slot.
const acquireConcurrencySource = `
local limit = tonumber(ARGV[1])
local ttl = tonumber(ARGV[2])
local current = redis.call('INCR', KEYS[1])
if current == 1 then
  redis.call('PEXPIRE', KEYS[1], ttl)
end
if current > limit then
  redis.call('DECR', KEYS[1])
  return 0
end
if redis.call('TTL', KEYS[1]) < 0 then
  redis.call('PEXPIRE', KEYS[1], ttl)
end
return 1
`

// holdSource reserves an amount against a scope.
//
// KEYS[1] held total, KEYS[2] this hold's own key.
// ARGV[1] amount, ARGV[2] budget, ARGV[3] ttl in ms.
// Returns 1 when the hold was taken and 0 when the budget is insufficient.
//
// The budget test is "budget - alreadyHeld < amount", exactly as the contract
// words it, and both writes carry the TTL so a crashed request cannot strand
// capacity forever. The hold id lives in KEYS[2] rather than an argument, so the
// script needs no id of its own and stays fully deterministic.
const holdSource = `
local amount = tonumber(ARGV[1])
local budget = tonumber(ARGV[2])
local ttl = tonumber(ARGV[3])
local held = tonumber(redis.call('GET', KEYS[1]) or '0')
if budget - held < amount then
  return 0
end
redis.call('SET', KEYS[2], amount, 'PX', ttl)
local next = redis.call('INCRBY', KEYS[1], amount)
if next == amount then
  redis.call('PEXPIRE', KEYS[1], ttl)
elseif redis.call('TTL', KEYS[1]) < 0 then
  redis.call('PEXPIRE', KEYS[1], ttl)
end
return 1
`

// releaseSource settles a hold and refunds the unused remainder.
//
// KEYS[1] held total, KEYS[2] this hold's own key.
// ARGV[1] the amount actually consumed.
// Returns the refunded amount.
//
// A missing hold key means the hold already expired, so nothing is refunded and
// the answer is 0. This makes Release safe to retry, which the contract
// requires. The refund is clamped at zero because the settlement amount may
// exceed the reservation (an underestimated request) and the scope must never
// be credited by accident.
const releaseSource = `
local actual = tonumber(ARGV[1])
local raw = redis.call('GET', KEYS[2])
if not raw then
  return 0
end
local reserved = tonumber(raw)
redis.call('DEL', KEYS[2])
local refund = reserved - actual
if refund < 0 then
  refund = 0
end
if reserved > 0 then
  local left = redis.call('DECRBY', KEYS[1], reserved)
  if left < 0 then
    redis.call('SET', KEYS[1], 0)
    if redis.call('TTL', KEYS[1]) < 0 then
      redis.call('DEL', KEYS[1])
    end
  end
end
return refund
`

// Registered scripts. One var per source keeps the call sites readable and
// guarantees the source is compiled once per process.
var (
	incrWithTTLScript        = redis.NewScript(incrWithTTLSource)
	allowRequestScript       = redis.NewScript(allowRequestSource)
	allowTokensScript        = redis.NewScript(allowTokensSource)
	acquireConcurrencyScript = redis.NewScript(acquireConcurrencySource)
	holdScript               = redis.NewScript(holdSource)
	releaseScript            = redis.NewScript(releaseSource)
)

// randomHex returns n random bytes rendered as hex.
//
// crypto/rand never fails on the platforms this gateway targets; a failure
// still yields a usable id because the caller prefixes a nanosecond timestamp.
func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return strings.Repeat("0", 2*n)
	}
	return hex.EncodeToString(buf)
}
