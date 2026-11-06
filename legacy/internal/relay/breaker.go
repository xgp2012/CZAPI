package relay

import (
	"sync"
	"time"

	"czapi/internal/pkg/clock"
)

// BreakerConfig tunes the circuit breaker (plan §6.5).
type BreakerConfig struct {
	// Window is the sliding window used to compute the error rate.
	Window time.Duration
	// ErrorRate is the failure ratio that opens the breaker.
	ErrorRate float64
	// MinSamples is the minimum window size before the breaker may open.
	MinSamples int
	// OpenDuration is how long the breaker stays open before probing.
	OpenDuration time.Duration
}

func (c BreakerConfig) withDefaults() BreakerConfig {
	if c.Window <= 0 {
		c.Window = 5 * time.Minute
	}
	if c.ErrorRate <= 0 || c.ErrorRate > 1 {
		c.ErrorRate = 0.5
	}
	if c.MinSamples <= 0 {
		c.MinSamples = 20
	}
	if c.OpenDuration <= 0 {
		c.OpenDuration = 60 * time.Second
	}
	return c
}

// sample is one recorded outcome.
type sample struct {
	at  time.Time
	err bool
}

// channelState is the per-channel breaker bookkeeping.
type channelState struct {
	samples   []sample
	openUntil time.Time
	// probing guards the half-open state so exactly one probe is allowed.
	probing bool
}

// Breaker is a sliding-window circuit breaker with single-probe half-open
// recovery.
//
// Safe for concurrent use.
type Breaker struct {
	cfg   BreakerConfig
	clk   clock.Clock
	mu    sync.Mutex
	state map[uint64]*channelState
}

// NewBreaker builds a breaker, applying defaults for zero values.
func NewBreaker(cfg BreakerConfig, clk clock.Clock) *Breaker {
	if clk == nil {
		clk = clock.System{}
	}
	return &Breaker{
		cfg:   cfg.withDefaults(),
		clk:   clk,
		state: make(map[uint64]*channelState),
	}
}

// stateLocked returns (creating if needed) the state for a channel.
// Callers must hold b.mu.
func (b *Breaker) stateLocked(channelID uint64) *channelState {
	s, ok := b.state[channelID]
	if !ok {
		s = &channelState{}
		b.state[channelID] = s
	}
	return s
}

// pruneLocked drops samples older than the window.
func (b *Breaker) pruneLocked(s *channelState, now time.Time) {
	cut := now.Add(-b.cfg.Window)
	i := 0
	for i < len(s.samples) && !s.samples[i].at.After(cut) {
		i++
	}
	if i > 0 {
		s.samples = append(s.samples[:0], s.samples[i:]...)
	}
}

// Allow reports whether a request may be routed to the channel.
//
// When the breaker is open and the open period has elapsed the channel becomes
// half-open: exactly one caller is admitted as a probe, everyone else is
// rejected until the probe reports back.
func (b *Breaker) Allow(channelID uint64) bool {
	now := b.clk.Now()
	b.mu.Lock()
	defer b.mu.Unlock()

	s := b.stateLocked(channelID)
	if s.openUntil.IsZero() {
		return true
	}
	if now.Before(s.openUntil) {
		return false
	}
	// Open period elapsed: allow a single probe.
	if s.probing {
		return false
	}
	s.probing = true
	return true
}

// Record feeds an outcome back into the breaker.
//
// A nil error closes the breaker (and releases the probe); an error counts as a
// failure and may open it.
func (b *Breaker) Record(channelID uint64, err error) {
	now := b.clk.Now()
	b.mu.Lock()
	defer b.mu.Unlock()

	s := b.stateLocked(channelID)
	wasProbing := s.probing
	s.probing = false

	failed := err != nil
	s.samples = append(s.samples, sample{at: now, err: failed})
	b.pruneLocked(s, now)

	// A successful probe always closes the breaker.
	if wasProbing && !failed {
		s.openUntil = time.Time{}
		return
	}
	// A failed probe re-opens immediately.
	if wasProbing && failed {
		s.openUntil = now.Add(b.cfg.OpenDuration)
		return
	}

	if len(s.samples) < b.cfg.MinSamples {
		return
	}
	fails := 0
	for _, sm := range s.samples {
		if sm.err {
			fails++
		}
	}
	rate := float64(fails) / float64(len(s.samples))
	if rate > b.cfg.ErrorRate {
		s.openUntil = now.Add(b.cfg.OpenDuration)
	}
}

// State reports the breaker state and the window statistics.
func (b *Breaker) State(channelID uint64) (open bool, errorRate float64, samples int) {
	now := b.clk.Now()
	b.mu.Lock()
	defer b.mu.Unlock()

	s, ok := b.state[channelID]
	if !ok {
		return false, 0, 0
	}
	b.pruneLocked(s, now)
	fails := 0
	for _, sm := range s.samples {
		if sm.err {
			fails++
		}
	}
	if len(s.samples) > 0 {
		errorRate = float64(fails) / float64(len(s.samples))
	}
	return !s.openUntil.IsZero() && now.Before(s.openUntil), errorRate, len(s.samples)
}

// Reset clears all state for a channel (admin action).
func (b *Breaker) Reset(channelID uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.state, channelID)
}

// OpenChannels lists the channels whose breaker is currently open.
func (b *Breaker) OpenChannels() []uint64 {
	now := b.clk.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []uint64
	for id, s := range b.state {
		if !s.openUntil.IsZero() && now.Before(s.openUntil) {
			out = append(out, id)
		}
	}
	return out
}
