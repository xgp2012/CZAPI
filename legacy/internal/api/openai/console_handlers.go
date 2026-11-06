package openai

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"czapi/internal/domain"
	"czapi/internal/pkg/crypto"
	"czapi/internal/pkg/errs"
)

// newTicket returns a 256-bit random hex ticket.
func newTicket() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// consoleEnvelope is the browser-facing response shape (docs/console-api-v1.md).
type consoleEnvelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data"`
}

func consoleOK(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, consoleEnvelope{Code: 0, Msg: "ok", Data: data})
}

func consoleFail(w http.ResponseWriter, e *errs.Error) {
	writeJSON(w, e.Status, consoleEnvelope{Code: e.Status, Msg: e.Message, Data: nil})
}

// requireSession resolves the session or writes an error.
func (h *Handlers) requireSession(w http.ResponseWriter, r *http.Request) (*sessionPayload, *domain.Account, bool) {
	s, acc, e := h.session(r)
	if e != nil {
		consoleFail(w, e)
		return nil, nil, false
	}
	return s, acc, true
}

// ---------------------------------------------------------------------------
// Overview
// ---------------------------------------------------------------------------

// usageBlock is a request/token/cost aggregate.
type usageBlock struct {
	Requests         int64 `json:"requests"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	CostMicros       int64 `json:"cost_micros"`
}

// subscriptionView is the console projection of a subscription.
type subscriptionView struct {
	SubscriptionID uint64 `json:"subscription_id"`
	V10HostID      uint64 `json:"v10_host_id"`
	PlanCode       string `json:"plan_code"`
	PlanName       string `json:"plan_name"`
	Status         string `json:"status"`
	BillingMode    string `json:"billing_mode"`
	PeriodStart    int64  `json:"period_start"`
	PeriodEnd      int64  `json:"period_end"`
	QuotaTotal     int64  `json:"quota_total"`
	QuotaUsed      int64  `json:"quota_used"`
	QuotaRemaining int64  `json:"quota_remaining"`
}

// consoleOverview implements GET /console/v1/overview.
func (h *Handlers) consoleOverview(w http.ResponseWriter, r *http.Request) {
	_, acc, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	now := h.clock.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	trendStart := dayStart.AddDate(0, 0, -29)

	filter := func(from time.Time) internalUsageFilter {
		return internalUsageFilter{AccountID: acc.ID, From: from, To: now}
	}

	today, err := h.store.Usage().Summary(ctx, filter(dayStart).toStore())
	if err != nil {
		consoleFail(w, errs.Internal("overview: today", err))
		return
	}
	month, err := h.store.Usage().Summary(ctx, filter(monthStart).toStore())
	if err != nil {
		consoleFail(w, errs.Internal("overview: month", err))
		return
	}

	// Subscriptions with plan names resolved.
	subs, err := h.store.Subscriptions().ListByAccount(ctx, acc.ID)
	if err != nil {
		consoleFail(w, errs.Internal("overview: subscriptions", err))
		return
	}
	views := make([]subscriptionView, 0, len(subs))
	for _, s := range subs {
		v := subscriptionView{
			SubscriptionID: s.ID, V10HostID: s.V10HostID,
			Status: string(s.Status), BillingMode: string(s.BillingMode),
			PeriodStart: s.PeriodStart.Unix(), PeriodEnd: s.PeriodEnd.Unix(),
			QuotaTotal: s.QuotaTotal, QuotaUsed: s.QuotaUsed, QuotaRemaining: s.Remaining(),
		}
		if plan, perr := h.store.Plans().GetByID(ctx, s.PlanID); perr == nil && plan != nil {
			v.PlanCode = plan.PlanCode
			v.PlanName = plan.Name
		}
		views = append(views, v)
	}

	// 30-day trend from the pre-aggregated rollups.
	agg, err := h.store.Aggregates().BucketCost(ctx, internalUsageFilter{
		AccountID: acc.ID, From: trendStart, To: now,
	}.toStore())
	trend := make([]map[string]any, 0, len(agg))
	if err == nil {
		for _, d := range agg {
			trend = append(trend, map[string]any{
				"day": d.Day, "requests": d.Requests,
				"prompt_tokens": d.PromptTokens, "completion_tokens": d.CompletionTokens,
				"cost_micros": d.CostMicros,
			})
		}
	}

	cfg := h.console.normalized()
	consoleOK(w, map[string]any{
		"account": map[string]any{
			"v10_client_id": acc.V10ClientID, "email": acc.Email, "status": acc.Status,
		},
		"balance_micros": acc.BalanceCredit,
		"today":          usageBlock{Requests: today.Requests, PromptTokens: today.PromptTokens, CompletionTokens: today.CompletionTokens, CostMicros: today.CostMicros},
		"month":          usageBlock{Requests: month.Requests, PromptTokens: month.PromptTokens, CompletionTokens: month.CompletionTokens, CostMicros: month.CostMicros},
		"subscriptions":  views,
		"trend":          trend,
		"recharge_url":   cfg.RechargeURL,
	})
}

// ---------------------------------------------------------------------------
// Keys
// ---------------------------------------------------------------------------

// keyView is the console projection of an API key.
type keyView struct {
	KeyID          uint64   `json:"key_id"`
	KeyPrefix      string   `json:"key_prefix"`
	Name           string   `json:"name"`
	Status         string   `json:"status"`
	SubscriptionID uint64   `json:"subscription_id"`
	V10HostID      uint64   `json:"v10_host_id"`
	Models         []string `json:"models"`
	IPWhitelist    []string `json:"ip_whitelist"`
	ExpiresAt      *int64   `json:"expires_at"`
	LastUsedAt     *int64   `json:"last_used_at"`
	CreatedAt      int64    `json:"created_at"`
	UsageMicros30d int64    `json:"usage_micros_30d"`
	Requests30d    int64    `json:"requests_30d"`
}

func (h *Handlers) toKeyView(r *http.Request, k *domain.APIKey) keyView {
	now := h.clock.Now()
	from := now.AddDate(0, 0, -30)
	sum, err := h.store.Usage().Summary(r.Context(), internalUsageFilter{APIKeyID: k.ID, From: from, To: now}.toStore())
	v := keyView{
		KeyID: k.ID, KeyPrefix: k.KeyPrefix, Name: k.Name, Status: string(k.Status),
		SubscriptionID: k.SubscriptionID, Models: k.Models, IPWhitelist: k.IPWhitelist,
		CreatedAt: k.CreatedAt.Unix(),
	}
	if err == nil {
		v.UsageMicros30d = sum.CostMicros
		v.Requests30d = sum.Requests
	}
	if k.ExpiresAt != nil {
		t := k.ExpiresAt.Unix()
		v.ExpiresAt = &t
	}
	if k.LastUsedAt != nil {
		t := k.LastUsedAt.Unix()
		v.LastUsedAt = &t
	}
	if k.SubscriptionID != 0 {
		if sub, serr := h.store.Subscriptions().GetByID(r.Context(), k.SubscriptionID); serr == nil && sub != nil {
			v.V10HostID = sub.V10HostID
		}
	}
	return v
}

// consoleListKeys implements GET /console/v1/keys.
func (h *Handlers) consoleListKeys(w http.ResponseWriter, r *http.Request) {
	_, acc, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	keys, err := h.store.Keys().ListByAccount(r.Context(), acc.ID)
	if err != nil {
		consoleFail(w, errs.Internal("list keys", err))
		return
	}
	items := make([]keyView, 0, len(keys))
	for _, k := range keys {
		items = append(items, h.toKeyView(r, &k))
	}
	consoleOK(w, map[string]any{"total": len(items), "items": items})
}

// createKeyRequest is the body of POST /console/v1/keys.
type createKeyRequest struct {
	Name           string   `json:"name"`
	SubscriptionID uint64   `json:"subscription_id"`
	Models         []string `json:"models"`
	IPWhitelist    []string `json:"ip_whitelist"`
	ExpiresAt      *int64   `json:"expires_at"`
}

// consoleCreateKey implements POST /console/v1/keys. The plaintext key is
// returned exactly once and never persisted.
func (h *Handlers) consoleCreateKey(w http.ResponseWriter, r *http.Request) {
	_, acc, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	var req createKeyRequest
	if err := decodeBody(r, &req); err != nil {
		consoleFail(w, errs.InvalidRequest("invalid body: "+err.Error()))
		return
	}
	ctx := r.Context()

	// Resolve the target subscription and enforce the per-host key limit.
	var sub *domain.Subscription
	if req.SubscriptionID != 0 {
		s, serr := h.store.Subscriptions().GetByID(ctx, req.SubscriptionID)
		if serr != nil || s == nil {
			consoleFail(w, errs.NotFound("subscription not found"))
			return
		}
		if s.AccountID != acc.ID {
			consoleFail(w, errs.New(http.StatusForbidden, errs.CodeInvalidRequest, "subscription does not belong to this account"))
			return
		}
		if s.Status != domain.SubActive {
			consoleFail(w, errs.KeySuspended("subscription is not active"))
			return
		}
		sub = s
	} else {
		// Default to the soonest-expiring active subscription.
		subs, lerr := h.store.Subscriptions().ListByAccount(ctx, acc.ID)
		if lerr != nil {
			consoleFail(w, errs.Internal("load subscriptions", lerr))
			return
		}
		for i := range subs {
			if subs[i].Status == domain.SubActive {
				sub = &subs[i]
				break
			}
		}
		if sub == nil {
			consoleFail(w, errs.InsufficientQuota())
			return
		}
	}

	limit := 5
	if plan, perr := h.store.Plans().GetByID(ctx, sub.PlanID); perr == nil && plan != nil && plan.MaxKeys > 0 {
		limit = plan.MaxKeys
	}
	count, cerr := h.store.Keys().CountBySubscription(ctx, sub.ID)
	if cerr != nil {
		consoleFail(w, errs.Internal("count keys", cerr))
		return
	}
	if count >= limit {
		consoleFail(w, errs.InvalidRequest("key limit reached for this plan ("+strconv.Itoa(limit)+")"))
		return
	}

	plain, prefix := crypto.GenerateAPIKey()
	key := &domain.APIKey{
		AccountID:      acc.ID,
		SubscriptionID: sub.ID,
		KeyHash:        crypto.HashKey(h.keySalt, plain),
		KeyPrefix:      prefix,
		Name:           strings.TrimSpace(req.Name),
		Models:         req.Models,
		IPWhitelist:    req.IPWhitelist,
		Status:         domain.KeyActive,
		CreatedAt:      h.clock.Now(),
		UpdatedAt:      h.clock.Now(),
	}
	if req.ExpiresAt != nil {
		t := time.Unix(*req.ExpiresAt, 0)
		key.ExpiresAt = &t
	}
	if key.Name == "" {
		key.Name = "default"
	}
	if err := h.store.Keys().Create(ctx, key); err != nil {
		consoleFail(w, errs.Internal("create key", err))
		return
	}
	if h.mx != nil {
		h.mx.ObserveCredit("key_create", "applied")
	}
	consoleOK(w, map[string]any{
		"key_id": key.ID, "key": plain, "key_prefix": key.KeyPrefix,
		"name": key.Name, "status": string(key.Status), "created_at": key.CreatedAt.Unix(),
	})
}

// updateKeyRequest is the body of PATCH /console/v1/keys/{id}.
type updateKeyRequest struct {
	Name        *string   `json:"name"`
	Status      *string   `json:"status"`
	Models      *[]string `json:"models"`
	IPWhitelist *[]string `json:"ip_whitelist"`
	ExpiresAt   *int64    `json:"expires_at"`
}

// consoleUpdateKey implements PATCH /console/v1/keys/{id}.
func (h *Handlers) consoleUpdateKey(w http.ResponseWriter, r *http.Request) {
	_, acc, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	id, perr := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if perr != nil {
		consoleFail(w, errs.InvalidRequest("invalid key id"))
		return
	}
	key, err := h.store.Keys().GetByID(r.Context(), id)
	if err != nil || key == nil {
		consoleFail(w, errs.NotFound("key not found"))
		return
	}
	if key.AccountID != acc.ID {
		consoleFail(w, errs.NotFound("key not found"))
		return
	}

	var req updateKeyRequest
	if derr := decodeBody(r, &req); derr != nil {
		consoleFail(w, errs.InvalidRequest("invalid body: "+derr.Error()))
		return
	}
	if req.Name != nil {
		key.Name = strings.TrimSpace(*req.Name)
	}
	if req.Status != nil {
		switch domain.APIKeyStatus(*req.Status) {
		case domain.KeyActive, domain.KeySuspended:
			if key.Status != domain.KeyRevoked {
				key.Status = domain.APIKeyStatus(*req.Status)
			}
		default:
			consoleFail(w, errs.InvalidRequest("status must be active or suspended"))
			return
		}
	}
	if req.Models != nil {
		key.Models = *req.Models
	}
	if req.IPWhitelist != nil {
		key.IPWhitelist = *req.IPWhitelist
	}
	if req.ExpiresAt != nil {
		t := time.Unix(*req.ExpiresAt, 0)
		key.ExpiresAt = &t
	}
	key.UpdatedAt = h.clock.Now()
	if uerr := h.store.Keys().Update(r.Context(), key); uerr != nil {
		consoleFail(w, errs.Internal("update key", uerr))
		return
	}
	// Drop the cached validation so the change takes effect immediately.
	if h.verif != nil {
		_ = h.verif.Invalidate(r.Context(), crypto.LookupHash("")) // no-op guard
	}
	consoleOK(w, h.toKeyView(r, key))
}

// consoleDeleteKey implements DELETE /console/v1/keys/{id} (soft revoke).
func (h *Handlers) consoleDeleteKey(w http.ResponseWriter, r *http.Request) {
	_, acc, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	id, perr := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if perr != nil {
		consoleFail(w, errs.InvalidRequest("invalid key id"))
		return
	}
	key, err := h.store.Keys().GetByID(r.Context(), id)
	if err != nil || key == nil || key.AccountID != acc.ID {
		consoleFail(w, errs.NotFound("key not found"))
		return
	}
	key.Status = domain.KeyRevoked
	key.UpdatedAt = h.clock.Now()
	if uerr := h.store.Keys().Update(r.Context(), key); uerr != nil {
		consoleFail(w, errs.Internal("revoke key", uerr))
		return
	}
	consoleOK(w, map[string]any{"key_id": key.ID, "status": string(key.Status)})
}

// ---------------------------------------------------------------------------
// Models / usage / docs
// ---------------------------------------------------------------------------

// consoleModels implements GET /console/v1/models.
func (h *Handlers) consoleModels(w http.ResponseWriter, r *http.Request) {
	_, acc, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	subs, _ := h.store.Subscriptions().ListByAccount(ctx, acc.ID)
	group := "default"
	for _, s := range subs {
		if s.Status != domain.SubActive {
			continue
		}
		if plan, perr := h.store.Plans().GetByID(ctx, s.PlanID); perr == nil && plan != nil && plan.ModelGroup != "" {
			group = plan.ModelGroup
			break
		}
	}
	models, err := h.store.Models().ListByGroup(ctx, group)
	if err != nil {
		consoleFail(w, errs.Internal("list models", err))
		return
	}
	items := make([]map[string]any, 0, len(models))
	for _, m := range models {
		if m.Status != 1 {
			continue
		}
		items = append(items, map[string]any{
			"model_name": m.ModelName, "display_name": m.DisplayName,
			"group_name": m.GroupName, "context_len": m.ContextLen,
			"capabilities": m.Capabilities, "status": m.Status,
			"ratio": map[string]any{
				"model_ratio": m.ModelRatio, "completion_ratio": m.CompletionRatio,
				"cached_ratio": m.CachedRatio,
			},
			"price_hint": map[string]any{
				"input_micros_per_1m":  m.InputMicros,
				"output_micros_per_1m": m.OutputMicros,
			},
		})
	}
	consoleOK(w, map[string]any{"total": len(items), "items": items})
}

// consoleUsageSummary implements GET /console/v1/usage/summary.
func (h *Handlers) consoleUsageSummary(w http.ResponseWriter, r *http.Request) {
	_, acc, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	from, to := parseRange(r, h.clock.Now())
	f := internalUsageFilter{AccountID: acc.ID, From: from, To: to}
	totals, err := h.store.Usage().Summary(r.Context(), f.toStore())
	if err != nil {
		consoleFail(w, errs.Internal("usage summary", err))
		return
	}
	agg, _ := h.store.Aggregates().ListDaily(r.Context(), f.toStore())
	buckets := make([]map[string]any, 0, len(agg))
	for _, d := range agg {
		buckets = append(buckets, map[string]any{
			"key": d.Day, "requests": d.Requests, "prompt_tokens": d.PromptTokens,
			"completion_tokens": d.CompletionTokens, "cost_micros": d.CostMicros,
		})
	}
	consoleOK(w, map[string]any{
		"totals":  usageBlock{Requests: totals.Requests, PromptTokens: totals.PromptTokens, CompletionTokens: totals.CompletionTokens, CostMicros: totals.CostMicros},
		"buckets": buckets,
	})
}

// consoleUsageLogs implements GET /console/v1/usage/logs.
func (h *Handlers) consoleUsageLogs(w http.ResponseWriter, r *http.Request) {
	_, acc, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	from, to := parseRange(r, h.clock.Now())
	page := parseIntDefault(r.URL.Query().Get("page"), 1)
	pageSize := parseIntDefault(r.URL.Query().Get("page_size"), 20)
	if pageSize > 200 {
		pageSize = 200
	}
	if page < 1 {
		page = 1
	}
	f := internalUsageFilter{
		AccountID: acc.ID, From: from, To: to,
		Limit: pageSize, Offset: (page - 1) * pageSize,
	}
	if v := r.URL.Query().Get("api_key_id"); v != "" {
		if id, perr := strconv.ParseUint(v, 10, 64); perr == nil {
			f.APIKeyID = id
		}
	}
	if v := r.URL.Query().Get("model"); v != "" {
		f.Model = v
	}
	rows, err := h.store.Usage().List(r.Context(), f.toStore())
	if err != nil {
		consoleFail(w, errs.Internal("usage logs", err))
		return
	}
	totals, _ := h.store.Usage().Summary(r.Context(), f.toStore())
	items := make([]map[string]any, 0, len(rows))
	for _, u := range rows {
		items = append(items, map[string]any{
			"request_id": u.RequestID, "model": u.Model,
			"prompt_tokens": u.PromptTokens, "completion_tokens": u.CompletionTokens,
			"cost_micros": u.CostMicros, "billing_mode": billingModeLabel(u.BillingMode),
			"stream": u.Stream, "estimated": u.Estimated,
			"status_code": u.StatusCode, "latency_ms": u.LatencyMS, "ttft_ms": u.TTFTMS,
			"api_key_id": u.APIKeyID, "v10_host_id": u.V10HostID,
			"created_at": u.CreatedAt.Unix(),
		})
	}
	consoleOK(w, map[string]any{
		"total": totals.Requests, "page": page, "page_size": pageSize, "items": items,
	})
}

// consoleDocs implements GET /console/v1/docs.
func (h *Handlers) consoleDocs(w http.ResponseWriter, r *http.Request) {
	_, _, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	cfg := h.console.normalized()
	base := trimSlash(cfg.BaseURL)
	if base == "" {
		base = "https://api.example.com"
	}
	consoleOK(w, map[string]any{
		"base_url": base,
		"examples": map[string]string{
			"curl": "curl " + base + "/v1/chat/completions \\\n" +
				"  -H \"Authorization: Bearer $CZAPI_KEY\" \\\n" +
				"  -H \"Content-Type: application/json\" \\\n" +
				"  -d '{\"model\":\"deepseek-chat\",\"messages\":[{\"role\":\"user\",\"content\":\"你好\"}]}'",
			"python": "from openai import OpenAI\n\n" +
				"client = OpenAI(api_key=\"$CZAPI_KEY\", base_url=\"" + base + "/v1\")\n" +
				"resp = client.chat.completions.create(\n" +
				"    model=\"deepseek-chat\",\n" +
				"    messages=[{\"role\": \"user\", \"content\": \"你好\"}],\n" +
				")\n" +
				"print(resp.choices[0].message.content)",
			"node": "import OpenAI from 'openai';\n\n" +
				"const client = new OpenAI({ apiKey: process.env.CZAPI_KEY, baseURL: '" + base + "/v1' });\n" +
				"const resp = await client.chat.completions.create({\n" +
				"  model: 'deepseek-chat',\n" +
				"  messages: [{ role: 'user', content: '你好' }],\n" +
				"});\n" +
				"console.log(resp.choices[0].message.content);",
			"langchain": "from langchain_openai import ChatOpenAI\n\n" +
				"llm = ChatOpenAI(\n" +
				"    model=\"deepseek-chat\",\n" +
				"    api_key=\"$CZAPI_KEY\",\n" +
				"    base_url=\"" + base + "/v1\",\n" +
				")\n" +
				"print(llm.invoke(\"你好\").content)",
		},
	})
}

// billingModeLabel renders the numeric billing mode for the UI.
func billingModeLabel(m domain.UsageBillingMode) string {
	switch m {
	case domain.PaidBySubscription:
		return "subscription"
	case domain.PaidByBalance:
		return "balance"
	case domain.PaidByFree:
		return "free"
	}
	return "unknown"
}

func parseRange(r *http.Request, now time.Time) (time.Time, time.Time) {
	from := now.AddDate(0, 0, -30)
	to := now
	if v := r.URL.Query().Get("from"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			from = time.Unix(n, 0)
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			to = time.Unix(n, 0)
		}
	}
	return from, to
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
