package billing

import (
	"math"
	"math/rand"
	"strings"
	"testing"

	"czapi/internal/domain"
)

func model(mr, cr, cached float64) *domain.Model {
	return &domain.Model{
		ModelName:       "test",
		ModelRatio:      mr,
		CompletionRatio: cr,
		CachedRatio:     cached,
	}
}

func TestCost(t *testing.T) {
	e := NewEngine(EngineConfig{TokenToMicros: 1, DefaultCompletionTokens: 512})

	tests := []struct {
		name       string
		price      Price
		usage      Usage
		tokenToMic int64
		want       int64
	}{
		{
			name:  "zero usage is free",
			price: Price{ModelRatio: 1, CompletionRatio: 1, CachedRatio: 0.5, GroupRatio: 1},
			usage: Usage{},
			want:  0,
		},
		{
			name:  "prompt only, neutral ratios",
			price: Price{ModelRatio: 1, CompletionRatio: 1, CachedRatio: 0.5, GroupRatio: 1},
			usage: Usage{PromptTokens: 1000},
			want:  1000,
		},
		{
			name:  "completion only, neutral ratios",
			price: Price{ModelRatio: 1, CompletionRatio: 1, CachedRatio: 0.5, GroupRatio: 1},
			usage: Usage{CompletionTokens: 500},
			want:  500,
		},
		{
			name:  "completion ratio 3",
			price: Price{ModelRatio: 1, CompletionRatio: 3, CachedRatio: 0.5, GroupRatio: 1},
			usage: Usage{PromptTokens: 1000, CompletionTokens: 500},
			want:  1000 + 1500,
		},
		{
			name:  "model ratio 0.5",
			price: Price{ModelRatio: 0.5, CompletionRatio: 1, CachedRatio: 0.5, GroupRatio: 1},
			usage: Usage{PromptTokens: 1000},
			want:  500,
		},
		{
			name:  "group ratio discount 0.8",
			price: Price{ModelRatio: 1, CompletionRatio: 1, CachedRatio: 0.5, GroupRatio: 0.8},
			usage: Usage{PromptTokens: 1000},
			want:  800,
		},
		{
			name:  "combined model 0.5 group 0.8 completion 2",
			price: Price{ModelRatio: 0.5, CompletionRatio: 2, CachedRatio: 0.5, GroupRatio: 0.8},
			usage: Usage{PromptTokens: 1000, CompletionTokens: 1000},
			// (1000 + 2000) * 0.5 * 0.8 = 1200
			want: 1200,
		},
		{
			name:  "cached discount half price",
			price: Price{ModelRatio: 1, CompletionRatio: 1, CachedRatio: 0.5, GroupRatio: 1},
			usage: Usage{PromptTokens: 1000, CachedTokens: 400},
			// 1000 - 400*0.5 = 800
			want: 800,
		},
		{
			name:  "all prompt cached",
			price: Price{ModelRatio: 1, CompletionRatio: 1, CachedRatio: 0.5, GroupRatio: 1},
			usage: Usage{PromptTokens: 1000, CachedTokens: 1000},
			want:  500,
		},
		{
			name:  "cached exceeds prompt is clamped",
			price: Price{ModelRatio: 1, CompletionRatio: 1, CachedRatio: 0.5, GroupRatio: 1},
			usage: Usage{PromptTokens: 100, CachedTokens: 5000},
			want:  50,
		},
		{
			name:  "cached ratio 0 means cache is free",
			price: Price{ModelRatio: 1, CompletionRatio: 1, CachedRatio: 0, GroupRatio: 1},
			usage: Usage{PromptTokens: 1000, CachedTokens: 500},
			// 1000 - 500*(1-0) = 500
			want: 500,
		},
		{
			name:  "absolute price path",
			price: Price{InputMicros: 500_000, OutputMicros: 1_000_000, CachedRatio: 0.5},
			usage: Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000},
			// 1e6*5e5/1e6 + 1e6*1e6/1e6 = 500000 + 1000000
			want: 1_500_000,
		},
		{
			name:  "absolute price with cached discount",
			price: Price{InputMicros: 1_000_000, OutputMicros: 0, CachedRatio: 0.5},
			usage: Usage{PromptTokens: 1_000_000, CachedTokens: 1_000_000},
			// 1e6*1e6/1e6 - 1e6*1e6*0.5/1e6 = 1000000 - 500000
			want: 500_000,
		},
		{
			name:       "token to micros multiplier",
			price:      Price{ModelRatio: 1, CompletionRatio: 1, CachedRatio: 0.5, GroupRatio: 1},
			usage:      Usage{PromptTokens: 100},
			tokenToMic: 10,
			want:       1000,
		},
		{
			name:       "token to micros zero falls back to config",
			price:      Price{ModelRatio: 1, CompletionRatio: 1, CachedRatio: 0.5, GroupRatio: 1},
			usage:      Usage{PromptTokens: 100},
			tokenToMic: 0,
			want:       100,
		},
		{
			name:  "negative tokens clamped to zero",
			price: Price{ModelRatio: 1, CompletionRatio: 1, CachedRatio: 0.5, GroupRatio: 1},
			usage: Usage{PromptTokens: -500, CompletionTokens: -500, CachedTokens: -1},
			want:  0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := e.Cost(tc.price, tc.usage, tc.tokenToMic)
			if got.CostMicros != tc.want {
				t.Fatalf("CostMicros = %d, want %d (costTokens=%.4f)", got.CostMicros, tc.want, got.CostTokens)
			}
			if got.CostMicros < 0 {
				t.Fatalf("negative cost %d", got.CostMicros)
			}
		})
	}
}

func TestCostCeil(t *testing.T) {
	e := NewEngine(EngineConfig{TokenToMicros: 1})
	// 3 tokens * 0.3333 ratio = 0.9999 -> must round UP to 1.
	got := e.Cost(Price{ModelRatio: 0.3333, CompletionRatio: 1, CachedRatio: 0.5, GroupRatio: 1}, Usage{PromptTokens: 3}, 1)
	if got.CostMicros != 1 {
		t.Fatalf("expected ceil to 1, got %d (costTokens=%.6f)", got.CostMicros, got.CostTokens)
	}

	// 1 token * 1.0 = exactly 1 (no up-rounding beyond the value).
	got = e.Cost(Price{ModelRatio: 1, CompletionRatio: 1, CachedRatio: 0.5, GroupRatio: 1}, Usage{PromptTokens: 1}, 1)
	if got.CostMicros != 1 {
		t.Fatalf("expected 1, got %d", got.CostMicros)
	}

	// A tenth of a micro must still round up to 1 (never 0 for real usage).
	got = e.Cost(Price{ModelRatio: 0.0001, CompletionRatio: 1, CachedRatio: 0.5, GroupRatio: 1}, Usage{PromptTokens: 1}, 1)
	if got.CostMicros != 1 {
		t.Fatalf("expected 1, got %d", got.CostMicros)
	}
}

func TestCostHugeValuesClamped(t *testing.T) {
	e := NewEngine(EngineConfig{TokenToMicros: 1_000_000})
	got := e.Cost(
		Price{ModelRatio: 1e9, CompletionRatio: 1e9, CachedRatio: 0.5, GroupRatio: 1e9},
		Usage{PromptTokens: math.MaxInt32, CompletionTokens: math.MaxInt32},
		1_000_000,
	)
	if got.CostMicros < 0 {
		t.Fatalf("overflow produced negative cost: %d", got.CostMicros)
	}
	if got.CostMicros > int64(overflowClamp) {
		t.Fatalf("cost not clamped: %d", got.CostMicros)
	}
}

func TestPriceClamps(t *testing.T) {
	e := NewEngine(EngineConfig{})

	if p := e.Price(nil, nil); p.ModelRatio != 1 || p.CompletionRatio != 1 || p.CachedRatio != 0.5 || p.GroupRatio != 1 {
		t.Fatalf("nil defaults wrong: %+v", p)
	}

	neg := &domain.Model{ModelRatio: -5, CompletionRatio: -2, CachedRatio: -1}
	if p := e.Price(neg, nil); p.ModelRatio <= 0 || p.CompletionRatio <= 0 || p.CachedRatio < 0 {
		t.Fatalf("negative ratios not clamped: %+v", p)
	}

	zeroCached := &domain.Model{ModelRatio: 1, CompletionRatio: 1, CachedRatio: 0}
	if p := e.Price(zeroCached, nil); p.CachedRatio != 0 {
		t.Fatalf("CachedRatio=0 must be preserved (free cache), got %f", p.CachedRatio)
	}

	over := &domain.Model{ModelRatio: 1, CompletionRatio: 1, CachedRatio: 5}
	if p := e.Price(over, nil); p.CachedRatio > 1 {
		t.Fatalf("CachedRatio > 1 not clamped: %f", p.CachedRatio)
	}

	plan := &domain.Plan{GroupRatio: 0.8}
	if p := e.Price(model(1, 1, 0.5), plan); p.GroupRatio != 0.8 {
		t.Fatalf("group ratio not applied: %f", p.GroupRatio)
	}

	noGroup := &domain.Plan{GroupRatio: 0}
	if p := e.Price(model(1, 1, 0.5), noGroup); p.GroupRatio != 1 {
		t.Fatalf("zero group ratio must default to 1, got %f", p.GroupRatio)
	}
}

func TestPricePassesAbsolutePrices(t *testing.T) {
	e := NewEngine(EngineConfig{})
	m := &domain.Model{ModelRatio: 1, CompletionRatio: 1, CachedRatio: 0.5, InputMicros: 123, OutputMicros: 456}
	p := e.Price(m, nil)
	if p.InputMicros != 123 || p.OutputMicros != 456 {
		t.Fatalf("absolute prices dropped: %+v", p)
	}
	neg := &domain.Model{InputMicros: -1, OutputMicros: -1}
	p = e.Price(neg, nil)
	if p.InputMicros < 0 || p.OutputMicros < 0 {
		t.Fatalf("negative absolute prices not clamped: %+v", p)
	}
}

func TestEstimateTokens(t *testing.T) {
	e := NewEngine(EngineConfig{})

	cases := []struct {
		name string
		in   string
		min  int
		max  int
	}{
		{"empty", "", 0, 0},
		{"short ascii", "hello", 1, 3},
		{"ascii word batch", strings.Repeat("a", 400), 100, 100},
		{"cjk", strings.Repeat("你好世界", 10), 20, 30},
		{"mixed", "hello 世界 ok", 3, 12},
		{"single space", " ", 1, 1},
		{"emoji", "😀😀😀😀", 1, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := e.EstimateTokens(tc.in)
			if got < tc.min || got > tc.max {
				t.Fatalf("EstimateTokens(%q) = %d, want within [%d,%d]", tc.in, got, tc.min, tc.max)
			}
		})
	}

	// Invalid UTF-8 must not panic and must return >= 1.
	invalid := string([]byte{0xff, 0xfe, 0x41, 0x42, 0x43})
	if got := e.EstimateTokens(invalid); got < 1 {
		t.Fatalf("invalid utf8 -> %d, want >= 1", got)
	}
}

func TestEstimateChatTokens(t *testing.T) {
	e := NewEngine(EngineConfig{}).(*ratioEngine)
	if got := e.EstimateChatTokens(nil); got != 0 {
		t.Fatalf("nil messages -> %d, want 0", got)
	}
	one := e.EstimateChatTokens([]string{"hello"})
	if one < 5 {
		t.Fatalf("one message -> %d, want >= 5 (content + 4 overhead)", one)
	}
	two := e.EstimateChatTokens([]string{"hello", "hello"})
	if two <= one {
		t.Fatalf("two messages (%d) must exceed one (%d)", two, one)
	}
}

func TestDefaultCompletionTokens(t *testing.T) {
	e := NewEngine(EngineConfig{}).(*ratioEngine)
	if e.DefaultCompletionTokens() != 512 {
		t.Fatalf("default = %d, want 512", e.DefaultCompletionTokens())
	}
	e = NewEngine(EngineConfig{DefaultCompletionTokens: 100}).(*ratioEngine)
	if e.DefaultCompletionTokens() != 100 {
		t.Fatalf("configured = %d, want 100", e.DefaultCompletionTokens())
	}
}

// TestCostInvariants fuzzes the engine: results must never be negative or NaN,
// and must be monotonically non-decreasing in prompt tokens.
func TestCostInvariants(t *testing.T) {
	e := NewEngine(EngineConfig{TokenToMicros: 3})
	rng := rand.New(rand.NewSource(42))
	base := Price{ModelRatio: 0.7, CompletionRatio: 2.5, CachedRatio: 0.4, GroupRatio: 0.9}

	prev := int64(-1)
	for i := 0; i < 1000; i++ {
		prompt := rng.Intn(100000)
		u := Usage{
			PromptTokens:     prompt,
			CompletionTokens: rng.Intn(10000),
			CachedTokens:     rng.Intn(prompt + 1),
		}
		c := e.Cost(base, u, 3)
		if c.CostMicros < 0 {
			t.Fatalf("negative cost %d at %+v", c.CostMicros, u)
		}
		if math.IsNaN(c.CostTokens) || math.IsInf(c.CostTokens, 0) {
			t.Fatalf("non-finite costTokens at %+v", u)
		}
		// Monotonic in prompt with the same completion/cached share.
		u2 := Usage{PromptTokens: 200000, CompletionTokens: u.CompletionTokens}
		c2 := e.Cost(base, u2, 3)
		if c2.CostMicros < c.CostMicros {
			t.Fatalf("not monotonic: prompt %d -> %d, prompt 200000 -> %d", prompt, c.CostMicros, c2.CostMicros)
		}
		if prev > c.CostMicros && prompt > 0 {
			// prev tracking is only for debugging; ignore
		}
		prev = c.CostMicros
	}
}

func BenchmarkCost(b *testing.B) {
	e := NewEngine(EngineConfig{TokenToMicros: 1})
	p := Price{ModelRatio: 0.7, CompletionRatio: 2.5, CachedRatio: 0.4, GroupRatio: 0.9}
	u := Usage{PromptTokens: 4096, CompletionTokens: 1024, CachedTokens: 512}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = e.Cost(p, u, 1)
	}
}
