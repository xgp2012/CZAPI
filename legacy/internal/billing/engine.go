package billing

import (
	"math"
	"unicode/utf8"

	"czapi/internal/domain"
)

// EngineConfig tunes the pricing engine.
type EngineConfig struct {
	// TokenToMicros converts one ratio-weighted standard token into micro-yuan.
	// Values <= 0 fall back to 1.
	TokenToMicros int64
	// DefaultCompletionTokens is the assumed output length when a request omits
	// max_tokens. Used by callers that build pre-holds.
	DefaultCompletionTokens int
}

const (
	defaultTokenToMicros    = int64(1)
	defaultCompletionTokens = 512
	defaultModelRatio       = 1.0
	defaultCompletionRatio  = 1.0
	defaultCachedRatio      = 0.5
	defaultGroupRatio       = 1.0
	// overflowClamp bounds every intermediate magnitude. Anything above this is
	// treated as a pricing configuration error and clamped rather than allowed
	// to wrap around into a negative or absurd charge.
	overflowClamp = float64(math.MaxInt64 / 2)
	// microsPerUnit converts an absolute per-1M-token price into per-token.
	microsPerUnit = 1_000_000.0
)

// ratioEngine is the concrete, dependency-free pricing calculator.
//
// Formula (plan §6.6, new-api compatible):
//
//	cachedDiscount  = cached * (1 - cached_ratio)
//	effectivePrompt = prompt + completion*completion_ratio - cachedDiscount
//	costTokens      = effectivePrompt * model_ratio * group_ratio
//	costMicros      = ceil(costTokens * token_to_micros)
//
// When a model defines absolute prices (InputMicros/OutputMicros, expressed per
// one million tokens) the ratio path is replaced by:
//
//	costMicros = ceil(prompt*input/1e6 + completion*output/1e6
//	                  - cached*input*(1-cached_ratio)/1e6)
//
// Safe for concurrent use: the engine holds no mutable state.
type ratioEngine struct {
	tokenToMicros           int64
	defaultCompletionTokens int
}

// NewEngine builds an Engine from cfg, applying safe defaults.
func NewEngine(cfg EngineConfig) Engine {
	if cfg.TokenToMicros <= 0 {
		cfg.TokenToMicros = defaultTokenToMicros
	}
	if cfg.DefaultCompletionTokens <= 0 {
		cfg.DefaultCompletionTokens = defaultCompletionTokens
	}
	return &ratioEngine{
		tokenToMicros:           cfg.TokenToMicros,
		defaultCompletionTokens: cfg.DefaultCompletionTokens,
	}
}

// DefaultCompletionTokens reports the fallback output length used for pre-holds.
// Callers building an estimate pass this when the request omits max_tokens.
func (e *ratioEngine) DefaultCompletionTokens() int { return e.defaultCompletionTokens }

// Price resolves the multipliers for a (model, plan) pair.
//
// A nil model keeps the neutral multipliers (1.0/1.0/0.5) so an unknown model is
// still billable at list price rather than free. A nil plan means "no group
// discount".
func (e *ratioEngine) Price(model *domain.Model, plan *domain.Plan) Price {
	p := Price{
		ModelRatio:      defaultModelRatio,
		CompletionRatio: defaultCompletionRatio,
		CachedRatio:     defaultCachedRatio,
		GroupRatio:      defaultGroupRatio,
	}
	if model != nil {
		p.ModelRatio = clampRatio(model.ModelRatio, defaultModelRatio)
		p.CompletionRatio = clampRatio(model.CompletionRatio, defaultCompletionRatio)
		// CachedRatio is intentional: 0 is a legal value ("cache is free"), so it
		// must not be treated as "unset". Only negatives/non-finite are clamped.
		p.CachedRatio = clampCached(model.CachedRatio)
		p.InputMicros = model.InputMicros
		p.OutputMicros = model.OutputMicros
		if p.InputMicros < 0 {
			p.InputMicros = 0
		}
		if p.OutputMicros < 0 {
			p.OutputMicros = 0
		}
	}
	if plan != nil {
		p.GroupRatio = clampRatio(plan.GroupRatio, defaultGroupRatio)
	}
	return p
}

// Cost computes the charge for one usage block.
//
// Invariants: the result is never negative and never NaN/Inf; negative token
// counts are clamped to zero; cached tokens are clamped to the prompt length.
func (e *ratioEngine) Cost(p Price, u Usage, tokenToMicros int64) Cost {
	prompt := clampTokens(u.PromptTokens)
	completion := clampTokens(u.CompletionTokens)
	cached := clampTokens(u.CachedTokens)
	if cached > prompt {
		cached = prompt
	}
	if tokenToMicros <= 0 {
		tokenToMicros = e.tokenToMicros
	}
	if tokenToMicros <= 0 {
		tokenToMicros = defaultTokenToMicros
	}

	out := Cost{
		PromptTokens:     prompt,
		CompletionTokens: completion,
		CachedTokens:     cached,
		Estimated:        u.Estimated,
	}

	modelRatio := finiteOr(p.ModelRatio, defaultModelRatio)
	completionRatio := finiteOr(p.CompletionRatio, defaultCompletionRatio)
	cachedRatio := finiteOr(p.CachedRatio, defaultCachedRatio)
	groupRatio := finiteOr(p.GroupRatio, defaultGroupRatio)

	if p.InputMicros != 0 || p.OutputMicros != 0 {
		// Absolute price path (per 1M tokens).
		input := float64(maxI64(p.InputMicros, 0))
		output := float64(maxI64(p.OutputMicros, 0))
		charged := float64(prompt)*input/microsPerUnit +
			float64(completion)*output/microsPerUnit -
			float64(cached)*input*(1-cachedRatio)/microsPerUnit
		out.CostTokens = 0
		out.CostMicros = toMicros(charged)
		return out
	}

	// Ratio path.
	cachedDiscount := float64(cached) * (1 - cachedRatio)
	effectivePrompt := float64(prompt) + float64(completion)*completionRatio - cachedDiscount
	if effectivePrompt < 0 {
		effectivePrompt = 0
	}
	costTokens := effectivePrompt * modelRatio * groupRatio
	if costTokens < 0 || math.IsNaN(costTokens) {
		costTokens = 0
	}
	if costTokens > overflowClamp {
		costTokens = overflowClamp
	}
	out.CostTokens = costTokens
	out.CostMicros = toMicros(costTokens * float64(tokenToMicros))
	return out
}

// EstimateTokens approximates the token count of text without a tokenizer
// dependency. ASCII text averages roughly four characters per token, CJK text
// about 1.5 characters per token, and other multibyte scripts about two.
//
// Invalid UTF-8 bytes are counted as "other" so the function never panics.
func (e *ratioEngine) EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	var ascii, cjk, other int
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			// Invalid encoding: consume one byte, count as "other".
			other++
		case r < 0x80:
			ascii++
		case isCJK(r):
			cjk++
		default:
			other++
		}
		i += size
	}
	// Ceil each class so any non-empty class contributes at least one token.
	tokens := int(math.Ceil(float64(ascii)/4.0)) +
		int(math.Ceil(float64(cjk)/1.5)) +
		int(math.Ceil(float64(other)/2.0))
	if tokens < 1 {
		return 1
	}
	return tokens
}

// EstimateChatTokens estimates a whole conversation: the sum of per-message
// content estimates plus a fixed four-token framing overhead per message, which
// mirrors the chat formatting overhead documented by OpenAI.
func (e *ratioEngine) EstimateChatTokens(messages []string) int {
	if len(messages) == 0 {
		return 0
	}
	total := 0
	for _, m := range messages {
		total += e.EstimateTokens(m) + 4
	}
	return total
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func clampTokens(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

func clampRatio(v, fallback float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return fallback
	}
	if v <= 0 {
		return fallback
	}
	if v > overflowClamp {
		return overflowClamp
	}
	return v
}

// clampCached allows zero (free cache) but rejects negatives and non-finite
// values, and caps the discount ratio at 1.
func clampCached(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return defaultCachedRatio
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func finiteOr(v, fallback float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return fallback
	}
	return v
}

func maxI64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// toMicros applies ceil and clamps into the int64-safe range.
func toMicros(v float64) int64 {
	if math.IsNaN(v) || v <= 0 {
		return 0
	}
	if v >= overflowClamp {
		return int64(overflowClamp)
	}
	return int64(math.Ceil(v))
}

func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF: // CJK Unified Ideographs
		return true
	case r >= 0x3400 && r <= 0x4DBF: // CJK Extension A
		return true
	case r >= 0x3040 && r <= 0x30FF: // Hiragana / Katakana
		return true
	case r >= 0xAC00 && r <= 0xD7AF: // Hangul syllables
		return true
	case r >= 0xF900 && r <= 0xFAFF: // CJK compatibility ideographs
		return true
	case r >= 0x3000 && r <= 0x303F: // CJK punctuation
		return true
	case r >= 0xFF00 && r <= 0xFFEF: // Fullwidth forms
		return true
	}
	return false
}
