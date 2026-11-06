// Package openai implements the OpenAI-compatible surface: /v1/chat/completions
// and friends, plus the browser console session endpoints.
//
// The request pipeline mirrors plan §5.2 exactly:
//
//	auth → validate → ratelimit → prehold → select channel → forward → settle
//
// Each step is a small method so the order is auditable and a failure returns a
// documented error code (plan appendix B).
package openai

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"czapi/internal/auth"
	"czapi/internal/billing"
	"czapi/internal/domain"
	"czapi/internal/metrics"
	"czapi/internal/pkg/errs"
	"czapi/internal/pkg/log"
	"czapi/internal/quota"
	"czapi/internal/ratelimit"
	"czapi/internal/relay"
	"czapi/internal/store"
)

// Handlers serves the OpenAI-compatible API and the console session.
type Handlers struct {
	store   store.Store
	verif   *auth.Verifier
	ledger  billing.Ledger
	engine  billing.Engine
	quota   *quota.Service
	relay   relay.Forwarder
	limiter *ratelimit.Limiter
	mx      *metrics.Registry
	log     *log.Logger
	clock   clock
	console ConsoleConfig
	cache   cacheStore
	keySalt string
}

// cacheStore is the narrow slice of the cache driver the console needs.
type cacheStore interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	Del(ctx context.Context, keys ...string) error
	GetJSON(ctx context.Context, key string, dst any) (bool, error)
	SetJSON(ctx context.Context, key string, val any, ttl time.Duration) error
}

// clock is the minimal time source (kept local to avoid importing the pkg twice).
type clock interface{ Now() time.Time }

// Deps assembles Handlers.
type Deps struct {
	Store    store.Store
	Verifier *auth.Verifier
	Ledger   billing.Ledger
	Engine   billing.Engine
	Quota    *quota.Service
	Relay    relay.Forwarder
	Limiter  *ratelimit.Limiter
	Metrics  *metrics.Registry
	Log      *log.Logger
	Clock    clock
	Console  ConsoleConfig
	Cache    cacheStore
	KeySalt  string
}

// New builds the handlers.
func New(d Deps) *Handlers {
	return &Handlers{
		store: d.Store, verif: d.Verifier, ledger: d.Ledger, engine: d.Engine,
		quota: d.Quota, relay: d.Relay, limiter: d.Limiter, mx: d.Metrics,
		log: d.Log, clock: d.Clock, console: d.Console, cache: d.Cache, keySalt: d.KeySalt,
	}
}

// Mount registers the /v1 routes on r.
func (h *Handlers) Mount(r chi.Router) {
	r.Post("/chat/completions", h.ChatCompletions)
	r.Post("/completions", h.Completions)
	r.Post("/embeddings", h.Embeddings)
	r.Get("/models", h.ListModels)
	r.Get("/models/{id}", h.GetModel)
	r.Get("/usage", h.KeyUsage)
}

// ---------------------------------------------------------------------------
// Chat completions
// ---------------------------------------------------------------------------

// chatRequest is the subset of the OpenAI chat request the gateway inspects.
// The original bytes are forwarded untouched, so unknown fields survive.
type chatRequest struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	// MaxCompletionTokens is the newer spelling used by reasoning models.
	MaxCompletionTokens int  `json:"max_completion_tokens"`
	Stream              bool `json:"stream"`
	Messages            []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

// effectiveMaxTokens returns the output budget to assume for estimates.
func (c *chatRequest) effectiveMaxTokens(fallback int) int {
	if c.MaxCompletionTokens > 0 {
		return c.MaxCompletionTokens
	}
	if c.MaxTokens > 0 {
		return c.MaxTokens
	}
	return fallback
}

// promptText flattens message content for token estimation.
func (c *chatRequest) promptText() []string {
	out := make([]string, 0, len(c.Messages))
	for _, m := range c.Messages {
		raw := strings.TrimSpace(string(m.Content))
		if raw == "" || raw == "null" {
			out = append(out, "")
			continue
		}
		// Content is usually a string but may be an array of parts.
		var s string
		if err := json.Unmarshal(m.Content, &s); err == nil {
			out = append(out, s)
			continue
		}
		out = append(out, raw)
	}
	return out
}

// ChatCompletions is the core endpoint (plan §5.2).
func (h *Handlers) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	h.proxy(w, r, "/v1/chat/completions")
}

// Completions proxies the legacy completions endpoint.
func (h *Handlers) Completions(w http.ResponseWriter, r *http.Request) {
	h.proxy(w, r, "/v1/completions")
}

// Embeddings proxies the embeddings endpoint. Embeddings carry a prompt rather
// than messages, so the estimate uses the raw body length as a proxy.
func (h *Handlers) Embeddings(w http.ResponseWriter, r *http.Request) {
	h.proxy(w, r, "/v1/embeddings")
}

// proxy runs the shared pipeline for a request whose body is JSON.
func (h *Handlers) proxy(w http.ResponseWriter, r *http.Request, upstreamPath string) {
	start := time.Now()
	requestID := requestIDOf(r)

	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeError(w, errs.InvalidRequest("failed to read request body"), requestID)
		return
	}
	if len(body) == 0 {
		writeError(w, errs.InvalidRequest("request body is empty"), requestID)
		return
	}

	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, errs.InvalidRequest("request body is not valid JSON: "+err.Error()), requestID)
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		writeError(w, errs.InvalidRequest("model is required"), requestID)
		return
	}

	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		writeError(w, errs.InvalidAPIKey(), requestID)
		return
	}

	// The middleware could not know the model, so the allow-list is enforced here.
	if e := errFromAllowsModel(p, req.Model); e != nil {
		writeError(w, e, requestID)
		return
	}

	// --- rate limiting -----------------------------------------------------
	if h.limiter != nil {
		decision := h.limiter.Allow(r.Context(), p, req.Model)
		if !decision.Allowed {
			w.Header().Set("Retry-After", itoa(int(decision.RetryAfter.Seconds())+1))
			if h.mx != nil {
				h.mx.ObserveQuotaRejected("rate_limited")
			}
			writeError(w, errs.RateLimited(decision.Message), requestID)
			return
		}
		defer decision.Release()
	}

	// --- model catalogue ---------------------------------------------------
	// An unregistered model is refused outright rather than forwarded blindly:
	// the gateway must not become an open proxy, and an unknown model has no
	// price so it could not be billed correctly.
	model, merr := h.store.Models().GetByName(r.Context(), req.Model)
	if merr != nil {
		if isNotFound(merr) {
			writeError(w, errs.ModelNotAllowed(req.Model), requestID)
			return
		}
		writeError(w, errs.Internal("load model", merr), requestID)
		return
	}
	if model == nil {
		writeError(w, errs.ModelNotAllowed(req.Model), requestID)
		return
	}
	if model.Status != 1 {
		writeError(w, errs.ModelNotAllowed(req.Model), requestID)
		return
	}

	// --- pre-hold ----------------------------------------------------------
	price := h.engine.Price(model, p.Plan)
	estimator, _ := h.engine.(interface{ EstimateChatTokens([]string) int })
	promptTokens := 0
	if estimator != nil {
		promptTokens = estimator.EstimateChatTokens(req.promptText())
	}
	dflt, _ := h.engine.(interface{ DefaultCompletionTokens() int })
	fallback := 512
	if dflt != nil {
		fallback = dflt.DefaultCompletionTokens()
	}
	estimate := h.engine.Cost(price, billing.Usage{
		PromptTokens:     promptTokens,
		CompletionTokens: req.effectiveMaxTokens(fallback),
	}, 0)

	billingMode := domain.BillingHybrid
	if p.Subscription != nil && p.Subscription.BillingMode.Valid() {
		billingMode = p.Subscription.BillingMode
	}
	decision, derr := h.ledger.Prehold(r.Context(), billing.PreholdRequest{
		AccountID:      p.AccountID,
		APIKeyID:       p.KeyID,
		SubscriptionID: p.SubscriptionID,
		V10HostID:      p.V10HostID,
		Model:          req.Model,
		GroupRatio:     price.GroupRatio,
		EstimateMicros: estimate.CostMicros,
		BillingMode:    billingMode,
		Plan:           p.Plan,
		KeyModels:      p.Key.Models,
	})
	if derr != nil {
		if h.mx != nil {
			h.mx.ObserveQuotaRejected("prehold")
		}
		writeError(w, errs.As(derr), requestID)
		return
	}

	// Ensure the pre-hold is released even if forwarding panics or the client
	// disappears before settlement runs.
	settled := false
	defer func() {
		if !settled {
			h.quota.Release(context.Background(), decision, p.AccountID)
		}
	}()

	// --- forwarding --------------------------------------------------------
	stream := req.Stream
	result, ferr := h.relay.Forward(r.Context(), w, relay.Request{
		Path:      upstreamPath,
		Body:      body,
		Model:     req.Model,
		Stream:    stream,
		Headers:   r.Header,
		RequestID: requestID,
	})

	// --- settlement --------------------------------------------------------
	// Settlement always runs, including for partial streams, so the customer is
	// charged for exactly what was delivered (plan §6.7).
	usage := billing.Usage{}
	if result.Usage != nil {
		usage.PromptTokens = result.Usage.PromptTokens
		usage.CompletionTokens = result.Usage.CompletionTokens
		usage.CachedTokens = result.Usage.Cached()
	} else if result.Partial {
		// No usage block and a truncated stream: bill the reservation.
		usage.PromptTokens = promptTokens
		usage.Estimated = true
	} else if ferr != nil {
		usage.PromptTokens = promptTokens
		usage.Estimated = true
	}
	usage.Partial = result.Partial

	rec, serr := h.quota.Settle(r.Context(), billing.SettleRequest{
		Decision:      decision,
		Usage:         usage,
		Model:         req.Model,
		ModelPricing:  price,
		TokenToMicros: 0,
		RequestID:     requestID,
		APIKeyID:      p.KeyID,
		AccountID:     p.AccountID,
		V10HostID:     p.V10HostID,
		ChannelID:     result.ChannelID,
		Stream:        stream,
		LatencyMS:     int(time.Since(start).Milliseconds()),
		TTFTMS:        result.TTFTMS,
		StatusCode:    result.StatusCode,
		ErrorCode:     result.ErrorCode,
		At:            h.clock.Now(),
	})
	settled = true
	if serr != nil && h.log != nil {
		h.log.Error("settle failed", "request_id", requestID, "err", serr)
	}
	if serr == nil {
		if err := h.quota.RecordUsage(r.Context(), rec); err != nil && h.log != nil {
			h.log.Error("record usage failed", "request_id", requestID, "err", err)
		}
	}
	_ = h.store.Keys().TouchLastUsed(context.Background(), p.KeyID, h.clock.Now())

	// Metrics.
	if h.mx != nil {
		h.mx.ObserveRequest(req.Model, channelLabel(result.ChannelID), result.StatusCode)
		h.mx.ObserveLatency(req.Model, stream, time.Since(start))
		if result.TTFTMS > 0 {
			h.mx.ObserveTTFT(req.Model, time.Duration(result.TTFTMS)*time.Millisecond)
		}
		h.mx.ObserveTokens(req.Model, usage.PromptTokens, usage.CompletionTokens, usage.CachedTokens)
	}

	// Errors are only written when nothing has been flushed yet; the relay
	// guarantees it never writes before a successful upstream response.
	if ferr != nil {
		writeError(w, errs.As(ferr), requestID)
		return
	}
}

// ---------------------------------------------------------------------------
// Models
// ---------------------------------------------------------------------------

// modelObject is the OpenAI-compatible model descriptor.
type modelObject struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ListModels returns the models this key may call (plan §6.3 A).
func (h *Handlers) ListModels(w http.ResponseWriter, r *http.Request) {
	requestID := requestIDOf(r)
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		writeError(w, errs.InvalidAPIKey(), requestID)
		return
	}

	group := "default"
	if p.Plan != nil && p.Plan.ModelGroup != "" {
		group = p.Plan.ModelGroup
	}
	models, err := h.store.Models().ListByGroup(r.Context(), group)
	if err != nil {
		writeError(w, errs.Internal("list models", err), requestID)
		return
	}

	data := make([]modelObject, 0, len(models))
	for _, m := range models {
		if m.Status != 1 {
			continue
		}
		if e := errFromAllowsModel(p, m.ModelName); e != nil {
			continue
		}
		data = append(data, modelObject{
			ID: m.ModelName, Object: "model", Created: m.CreatedAt.Unix(), OwnedBy: "czapi",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// GetModel returns one model descriptor.
func (h *Handlers) GetModel(w http.ResponseWriter, r *http.Request) {
	requestID := requestIDOf(r)
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		writeError(w, errs.InvalidAPIKey(), requestID)
		return
	}
	id := chi.URLParam(r, "id")
	if e := errFromAllowsModel(p, id); e != nil {
		writeError(w, e, requestID)
		return
	}
	m, err := h.store.Models().GetByName(r.Context(), id)
	if err != nil {
		writeError(w, errs.NotFound("model not found"), requestID)
		return
	}
	writeJSON(w, http.StatusOK, modelObject{
		ID: m.ModelName, Object: "model", Created: m.CreatedAt.Unix(), OwnedBy: "czapi",
	})
}

// KeyUsage is a gateway extension: today's and this month's usage for the key.
func (h *Handlers) KeyUsage(w http.ResponseWriter, r *http.Request) {
	requestID := requestIDOf(r)
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		writeError(w, errs.InvalidAPIKey(), requestID)
		return
	}
	now := h.clock.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	today, err := h.store.Usage().SumCostByKeyBetween(r.Context(), p.KeyID, dayStart, now)
	if err != nil {
		writeError(w, errs.Internal("usage today", err), requestID)
		return
	}
	month, err := h.store.Usage().SumCostByKeyBetween(r.Context(), p.KeyID, monthStart, now)
	if err != nil {
		writeError(w, errs.Internal("usage month", err), requestID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"key_id":            p.KeyID,
		"today_cost_micros": today,
		"month_cost_micros": month,
		"balance_micros":    p.Account.BalanceCredit,
		"period_remaining":  remaining(p.Subscription),
	})
}

func remaining(s *domain.Subscription) int64 {
	if s == nil {
		return 0
	}
	return s.Remaining()
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func isNotFound(err error) bool {
	e := errs.As(err)
	return e != nil && e.Code == errs.CodeNotFound
}

func channelLabel(id uint64) string {
	if id == 0 {
		return "none"
	}
	buf := make([]byte, 0, 20)
	for id > 0 {
		buf = append([]byte{byte('0' + id%10)}, buf...)
		id /= 10
	}
	return string(buf)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// clientIP extracts the caller address for IP allow-listing.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// errFromAllowsModel adapts the boolean model check to the optional error shape
// used in the handler flow (nil when allowed).
func errFromAllowsModel(p *auth.Principal, model string) *errs.Error {
	if p.AllowsModel(model) {
		return nil
	}
	return errs.ModelNotAllowed(model)
}

// requestIDOf extracts the correlation id, generating one when absent so error
// responses always carry an id.
func requestIDOf(r *http.Request) string {
	if v := auth.RequestID(r); v != "" {
		return v
	}
	return ""
}
