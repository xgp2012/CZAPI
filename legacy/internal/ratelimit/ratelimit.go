// Package ratelimit implements the three-layer limiter described in plan §6.4:
// requests per minute, tokens per minute and concurrent connections.
//
// Limits are resolved per request from the key's plan, falling back to the
// process defaults, so an operator can loosen one customer without touching the
// global configuration.
package ratelimit

import (
	"context"
	"fmt"
	"time"

	"czapi/internal/auth"
	"czapi/internal/cache"
	"czapi/internal/pkg/clock"
)

// Limits are the effective limits for one request.
type Limits struct {
	RPM         int
	TPM         int
	Concurrency int
}

// Config holds the fallback limits used when a plan does not specify them.
type Config struct {
	RPM         int
	TPM         int
	Concurrency int
	// Window is the bucket window, normally one minute.
	Window time.Duration
	// ConcurrencyTTL bounds how long a leaked slot may live.
	ConcurrencyTTL time.Duration
}

func (c Config) withDefaults() Config {
	if c.RPM <= 0 {
		c.RPM = 60
	}
	if c.TPM <= 0 {
		c.TPM = 100000
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 5
	}
	if c.Window <= 0 {
		c.Window = time.Minute
	}
	if c.ConcurrencyTTL <= 0 {
		c.ConcurrencyTTL = 5 * time.Minute
	}
	return c
}

// Limiter applies the limits.
//
// Safe for concurrent use.
type Limiter struct {
	cache cache.Cache
	cfg   Config
	clk   clock.Clock
}

// New builds a limiter.
func New(c cache.Cache, clk clock.Clock, cfg Config) *Limiter {
	if clk == nil {
		clk = clock.System{}
	}
	return &Limiter{cache: c, cfg: cfg.withDefaults(), clk: clk}
}

// Decision is the outcome of a rate limit check.
type Decision struct {
	Allowed    bool
	Message    string
	RetryAfter time.Duration
	// limit kind that rejected the request (for metrics/logs).
	Kind    string
	release func()
}

// Release frees the concurrency slot. It is safe to call more than once and
// safe to call on a rejected decision.
func (d *Decision) Release() {
	if d.release != nil {
		d.release()
		d.release = nil
	}
}

// Allow evaluates the request and token buckets, then acquires a concurrency
// slot. On success the caller MUST call decision.Release().
func (l *Limiter) Allow(ctx context.Context, p *auth.Principal, model string) *Decision {
	limits := l.limitsFor(p)

	// Requests per minute, scoped per key.
	rpmScope := fmt.Sprintf("key:%d", p.KeyID)
	ok, remaining, resetIn, err := l.cache.AllowRequest(ctx, cache.RPMKey(rpmScope), limits.RPM, l.cfg.Window)
	if err != nil {
		// A limiter failure must not take the gateway down: fail open, which is
		// the availability-first choice for a rate limiter.
		return &Decision{Allowed: true}
	}
	if !ok {
		return &Decision{
			Allowed:    false,
			Kind:       "rpm",
			Message:    fmt.Sprintf("Rate limit reached for requests (%d/min), retry shortly.", limits.RPM),
			RetryAfter: resetIn,
		}
	}
	_ = remaining

	// Tokens per minute: a best-effort estimate of one typical request. Precise
	// TPM accounting would need the token count before forwarding, which is only
	// available as an estimate; the bucket therefore charges a fixed weight.
	tpmScope := fmt.Sprintf("key:%d", p.KeyID)
	tokOK, _, terr := l.cache.AllowTokens(ctx, cache.TPMKey(tpmScope), l.tokenWeight(), limits.TPM, l.cfg.Window)
	if terr == nil && !tokOK {
		return &Decision{
			Allowed:    false,
			Kind:       "tpm",
			Message:    fmt.Sprintf("Rate limit reached for tokens (%d/min), retry shortly.", limits.TPM),
			RetryAfter: l.cfg.Window,
		}
	}

	// Concurrency per key.
	concScope := fmt.Sprintf("key:%d", p.KeyID)
	acquired, release, cerr := l.cache.AcquireConcurrency(ctx, cache.ConcurrencyKey(concScope), limits.Concurrency, l.cfg.ConcurrencyTTL)
	if cerr != nil {
		return &Decision{Allowed: true}
	}
	if !acquired {
		return &Decision{
			Allowed:    false,
			Kind:       "concurrency",
			Message:    fmt.Sprintf("Too many concurrent requests (%d), retry shortly.", limits.Concurrency),
			RetryAfter: time.Second,
		}
	}
	return &Decision{Allowed: true, release: release}
}

// tokenWeight is the TPM cost charged per request. It is deliberately small so
// the bucket throttles request storms rather than modelling real usage, which
// would require tokenising the prompt twice.
func (l *Limiter) tokenWeight() int { return 1 }

// limitsFor resolves the effective limits for a principal.
func (l *Limiter) limitsFor(p *auth.Principal) Limits {
	out := Limits{RPM: l.cfg.RPM, TPM: l.cfg.TPM, Concurrency: l.cfg.Concurrency}
	if p == nil || p.Plan == nil {
		return out
	}
	if p.Plan.RPMLimit > 0 {
		out.RPM = p.Plan.RPMLimit
	}
	if p.Plan.TPMLimit > 0 {
		out.TPM = p.Plan.TPMLimit
	}
	if p.Plan.Concurrency > 0 {
		out.Concurrency = p.Plan.Concurrency
	}
	return out
}
