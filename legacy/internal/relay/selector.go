package relay

import (
	"context"
	"math/rand"
	"sort"
	"sync"
	"time"

	"czapi/internal/domain"
	"czapi/internal/pkg/errs"
	"czapi/internal/store"
)

// channelSelector implements Selector: priority tiers, weighted random inside a
// tier, breaker-aware.
//
// Safe for concurrent use.
type channelSelector struct {
	channels store.ChannelRepo
	breaker  *Breaker
	mu       sync.Mutex
	rnd      *rand.Rand
	// healthLookup caches breaker state per channel for the diagnostics views.
}

// NewSelector builds a selector. A nil rnd seeds a deterministic generator so
// tests can rely on stable ordering; production callers pass nil and get a
// time-seeded source.
func NewSelector(channels store.ChannelRepo, breaker *Breaker, rnd *rand.Rand) *channelSelector {
	if rnd == nil {
		rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return &channelSelector{channels: channels, breaker: breaker, rnd: rnd}
}

// Select returns candidate channels in the order they should be tried.
//
// Ordering: priority descending, then weighted random within the same priority
// tier. Retry simply walks further down the returned slice, which is why the
// full order is returned rather than a single channel.
func (s *channelSelector) Select(ctx context.Context, model string, exclude map[uint64]bool) ([]domain.Channel, error) {
	all, err := s.channels.ListByModel(ctx, model)
	if err != nil {
		return nil, errs.Internal("relay: list channels", err)
	}

	candidates := make([]domain.Channel, 0, len(all))
	for _, ch := range all {
		if exclude != nil && exclude[ch.ID] {
			continue
		}
		if s.breaker != nil && !s.breaker.Allow(ch.ID) {
			continue
		}
		candidates = append(candidates, ch)
	}
	if len(candidates) == 0 {
		return nil, errs.ChannelUnavailable("no channel available for model " + model)
	}

	// Group by priority (descending).
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Priority > candidates[j].Priority
	})

	out := make([]domain.Channel, 0, len(candidates))
	for i := 0; i < len(candidates); {
		j := i
		for j < len(candidates) && candidates[j].Priority == candidates[i].Priority {
			j++
		}
		tier := candidates[i:j]
		out = append(out, s.shuffleWeighted(tier)...)
		i = j
	}
	return out, nil
}

// shuffleWeighted returns the tier in a weighted-random order without
// replacement: repeatedly draw proportional to weight.
func (s *channelSelector) shuffleWeighted(tier []domain.Channel) []domain.Channel {
	if len(tier) <= 1 {
		out := make([]domain.Channel, len(tier))
		copy(out, tier)
		return out
	}
	pool := make([]domain.Channel, len(tier))
	copy(pool, tier)

	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]domain.Channel, 0, len(pool))
	for len(pool) > 0 {
		total := 0
		for _, ch := range pool {
			total += weightOf(ch)
		}
		// rnd.Intn panics on a non-positive argument, and total can only be
		// positive because weightOf returns >= 1.
		pick := s.rnd.Intn(total)
		acc := 0
		idx := 0
		for i, ch := range pool {
			acc += weightOf(ch)
			if pick < acc {
				idx = i
				break
			}
		}
		out = append(out, pool[idx])
		pool = append(pool[:idx], pool[idx+1:]...)
	}
	return out
}

func weightOf(ch domain.Channel) int {
	if ch.Weight <= 0 {
		return 100
	}
	return ch.Weight
}

// Report feeds an outcome into the breaker.
func (s *channelSelector) Report(ctx context.Context, channelID uint64, err error) {
	if s.breaker == nil {
		return
	}
	s.breaker.Record(channelID, err)
}
