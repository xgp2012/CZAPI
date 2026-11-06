package internalapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"czapi/internal/domain"
	"czapi/internal/pkg/errs"
	"czapi/internal/store"
)

// contextWithTimeout is a small helper mirroring context.WithTimeout without
// importing context in every file.
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

// storeUsageFilter builds a usage filter for one key.
func storeUsageFilter(keyID uint64, from, to time.Time) store.UsageFilter {
	return store.UsageFilter{APIKeyID: keyID, From: from, To: to}
}

// ---------------------------------------------------------------------------
// Models
// ---------------------------------------------------------------------------

// Models implements GET /internal/v1/models.
func (h *Handlers) Models(w http.ResponseWriter, r *http.Request) {
	group := r.URL.Query().Get("group")
	models, err := h.store.Models().ListByGroup(r.Context(), group)
	if err != nil {
		failw(w, errs.Internal("list models", err))
		return
	}
	items := make([]map[string]any, 0, len(models))
	for _, m := range models {
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
	okw(w, map[string]any{"total": len(items), "items": items})
}

// ---------------------------------------------------------------------------
// Usage
// ---------------------------------------------------------------------------

// UsageSummary implements GET /internal/v1/usage/summary.
func (h *Handlers) UsageSummary(w http.ResponseWriter, r *http.Request) {
	clientID, ok := parseClientID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	acc, err := h.store.Accounts().GetByV10Client(ctx, siteOf(r), clientID)
	if err != nil || acc == nil {
		failw(w, errs.NotFound("account not found"))
		return
	}
	from, to := parseTimeRange(r, h.clock.Now())
	f := store.UsageFilter{AccountID: acc.ID, From: from, To: to}
	if v := r.URL.Query().Get("v10_host_id"); v != "" {
		if n, perr := strconv.ParseUint(v, 10, 64); perr == nil {
			f.V10HostID = n
		}
	}
	if v := r.URL.Query().Get("api_key_id"); v != "" {
		if n, perr := strconv.ParseUint(v, 10, 64); perr == nil {
			f.APIKeyID = n
		}
	}

	totals, serr := h.store.Usage().Summary(ctx, f)
	if serr != nil {
		failw(w, errs.Internal("usage summary", serr))
		return
	}

	groupBy := r.URL.Query().Get("group_by")
	var buckets []map[string]any
	switch groupBy {
	case "", "day":
		daily, aerr := h.store.Aggregates().ListDaily(ctx, f)
		if aerr == nil {
			buckets = make([]map[string]any, 0, len(daily))
			for _, d := range daily {
				buckets = append(buckets, map[string]any{
					"key": d.Day, "requests": d.Requests,
					"prompt_tokens": d.PromptTokens, "completion_tokens": d.CompletionTokens,
					"cost_micros": d.CostMicros,
				})
			}
		}
	}

	okw(w, map[string]any{
		"totals": map[string]any{
			"requests": totals.Requests, "prompt_tokens": totals.PromptTokens,
			"completion_tokens": totals.CompletionTokens, "cost_micros": totals.CostMicros,
		},
		"buckets": buckets,
	})
}

// UsageLogs implements GET /internal/v1/usage/logs. Prompt content is never
// returned (compliance, plan §12).
func (h *Handlers) UsageLogs(w http.ResponseWriter, r *http.Request) {
	clientID, ok := parseClientID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	acc, err := h.store.Accounts().GetByV10Client(ctx, siteOf(r), clientID)
	if err != nil || acc == nil {
		failw(w, errs.NotFound("account not found"))
		return
	}
	from, to := parseTimeRange(r, h.clock.Now())
	f := store.UsageFilter{AccountID: acc.ID, From: from, To: to}
	page := intParam(r, "page", 1)
	pageSize := intParam(r, "page_size", 20)
	if pageSize > 200 {
		pageSize = 200
	}
	if page < 1 {
		page = 1
	}
	f.Limit = pageSize
	f.Offset = (page - 1) * pageSize
	if v := r.URL.Query().Get("api_key_id"); v != "" {
		if n, perr := strconv.ParseUint(v, 10, 64); perr == nil {
			f.APIKeyID = n
		}
	}
	if v := r.URL.Query().Get("model"); v != "" {
		f.Model = v
	}

	rows, lerr := h.store.Usage().List(ctx, f)
	if lerr != nil {
		failw(w, errs.Internal("usage logs", lerr))
		return
	}
	totals, _ := h.store.Usage().Summary(ctx, f)
	items := make([]map[string]any, 0, len(rows))
	for _, u := range rows {
		items = append(items, map[string]any{
			"request_id": u.RequestID, "model": u.Model,
			"prompt_tokens": u.PromptTokens, "completion_tokens": u.CompletionTokens,
			"cost_micros": u.CostMicros, "billing_mode": billingLabel(u.BillingMode),
			"stream": u.Stream, "estimated": u.Estimated,
			"status_code": u.StatusCode, "latency_ms": u.LatencyMS, "ttft_ms": u.TTFTMS,
			"api_key_id": u.APIKeyID, "v10_host_id": u.V10HostID,
			"created_at": u.CreatedAt.Unix(),
		})
	}
	okw(w, map[string]any{
		"total": totals.Requests, "page": page, "page_size": pageSize, "items": items,
	})
}

func billingLabel(m domain.UsageBillingMode) string {
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

// ---------------------------------------------------------------------------
// Subscriptions
// ---------------------------------------------------------------------------

// createSubscriptionRequest mirrors docs/internal-api-v1.md §2.12.
type createSubscriptionRequest struct {
	V10ClientID uint64 `json:"v10_client_id"`
	V10HostID   uint64 `json:"v10_host_id"`
	PlanCode    string `json:"plan_code"`
	Period      string `json:"period"`
	QuotaTokens *int64 `json:"quota_tokens"`
	BillingMode string `json:"billing_mode"`
	PeriodStart *int64 `json:"period_start"`
	PeriodEnd   *int64 `json:"period_end"`
	MaxKeys     *int   `json:"max_keys"`
}

// CreateSubscription implements POST /internal/v1/subscriptions.
//
// One endpoint serves both provisioning and renewal because they share the same
// identity (v10_host_id). Semantics:
//   - new host                       -> created=true
//   - same host, different periodEnd -> renewal: reset the period and quota
//   - same host, same periodEnd      -> idempotent no-op
func (h *Handlers) CreateSubscription(w http.ResponseWriter, r *http.Request) {
	var req createSubscriptionRequest
	if err := decodeBody(r, &req); err != nil {
		failw(w, errs.InvalidRequest("invalid body: "+err.Error()))
		return
	}
	if req.V10ClientID == 0 || req.V10HostID == 0 {
		failw(w, errs.InvalidRequest("v10_client_id and v10_host_id are required"))
		return
	}
	if strings.TrimSpace(req.PlanCode) == "" {
		failw(w, errs.InvalidRequest("plan_code is required"))
		return
	}
	ctx := r.Context()

	acc, err := h.store.Accounts().GetByV10Client(ctx, siteOf(r), req.V10ClientID)
	if err != nil || acc == nil {
		// Provision on the fly so a V10 order does not need a separate sync call.
		acc, err = h.store.Accounts().Upsert(ctx, &domain.Account{
			SiteID: siteOf(r), V10ClientID: req.V10ClientID, Status: domain.AccountEnabled,
		})
		if err != nil {
			failw(w, errs.Internal("provision account", err))
			return
		}
	}

	plan, perr := h.store.Plans().GetByCode(ctx, req.PlanCode)
	if perr != nil || plan == nil {
		failw(w, errs.NotFound("unknown plan_code: "+req.PlanCode))
		return
	}

	now := h.clock.Now()
	start := now
	end := periodEndFor(now, pickPeriod(req.Period, plan.Period))
	if req.PeriodStart != nil {
		start = time.Unix(*req.PeriodStart, 0)
	}
	if req.PeriodEnd != nil {
		end = time.Unix(*req.PeriodEnd, 0)
	}
	quota := plan.QuotaTokens
	if req.QuotaTokens != nil && *req.QuotaTokens > 0 {
		quota = *req.QuotaTokens
	}
	mode := domain.BillingMode(req.BillingMode)
	if !mode.Valid() {
		mode = domain.BillingHybrid
	}

	sub := &domain.Subscription{
		AccountID:   acc.ID,
		V10HostID:   req.V10HostID,
		PlanID:      plan.ID,
		PeriodStart: start,
		PeriodEnd:   end,
		QuotaTotal:  quota,
		BillingMode: mode,
		Status:      domain.SubActive,
	}
	created, wasCreated, cerr := h.store.Subscriptions().Create(ctx, sub)
	if cerr != nil {
		failw(w, errs.Internal("create subscription", cerr))
		return
	}

	renewed := false
	if !wasCreated {
		// Same period end means the call is a replay of the current period.
		if created.PeriodEnd.Equal(end) {
			// Idempotent no-op: reactivate if it had been suspended/expired so a
			// retried provisioning call can still fix a bad state.
			if created.Status != domain.SubActive {
				created.Status = domain.SubActive
				if uerr := h.store.Subscriptions().Update(ctx, created); uerr != nil {
					failw(w, errs.Internal("reactivate subscription", uerr))
					return
				}
			}
		} else {
			// A new period end means renewal: extend and reset usage.
			if rerr := h.store.Subscriptions().ResetPeriod(ctx, created.ID, start, end, quota); rerr != nil {
				failw(w, errs.Internal("renew subscription", rerr))
				return
			}
			created.PeriodStart = start
			created.PeriodEnd = end
			created.QuotaTotal = quota
			created.QuotaUsed = 0
			created.PlanID = plan.ID
			created.BillingMode = mode
			created.Status = domain.SubActive
			if uerr := h.store.Subscriptions().Update(ctx, created); uerr != nil {
				failw(w, errs.Internal("update subscription", uerr))
				return
			}
			renewed = true
		}
	}

	// Provisioning must leave the host's keys usable.
	if created.Status == domain.SubActive {
		if _, serr := h.store.Keys().SetStatusBySubscription(ctx, created.ID, domain.KeyActive); serr != nil {
			// Non-fatal: the subscription itself is what matters.
			if h.log != nil {
				h.log.Warn("activate keys failed", "subscription_id", created.ID, "err", serr)
			}
		}
	}

	okw(w, map[string]any{
		"subscription_id": created.ID, "v10_host_id": created.V10HostID,
		"created": wasCreated, "renewed": renewed,
		"status": string(created.Status), "plan_code": plan.PlanCode,
		"period_start": created.PeriodStart.Unix(), "period_end": created.PeriodEnd.Unix(),
		"quota_total": created.QuotaTotal, "quota_used": created.QuotaUsed,
		"quota_remaining": created.Remaining(),
	})
}

// GetSubscription implements GET /internal/v1/subscriptions/{id}.
func (h *Handlers) GetSubscription(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	s, err := h.store.Subscriptions().GetByID(r.Context(), id)
	if err != nil || s == nil {
		failw(w, errs.NotFound("subscription not found"))
		return
	}
	okw(w, subscriptionView(h, r, s))
}

// SuspendSubscription implements POST /internal/v1/subscriptions/{id}/suspend.
func (h *Handlers) SuspendSubscription(w http.ResponseWriter, r *http.Request) {
	h.setSubscriptionStatus(w, r, domain.SubSuspended)
}

// ResumeSubscription implements POST /internal/v1/subscriptions/{id}/resume.
func (h *Handlers) ResumeSubscription(w http.ResponseWriter, r *http.Request) {
	h.setSubscriptionStatus(w, r, domain.SubActive)
}

// setSubscriptionStatus flips a subscription and cascades to its keys.
func (h *Handlers) setSubscriptionStatus(w http.ResponseWriter, r *http.Request, status domain.SubscriptionStatus) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	ctx := r.Context()
	s, err := h.store.Subscriptions().GetByID(ctx, id)
	if err != nil || s == nil {
		failw(w, errs.NotFound("subscription not found"))
		return
	}
	s.Status = status
	s.UpdatedAt = h.clock.Now()
	if uerr := h.store.Subscriptions().Update(ctx, s); uerr != nil {
		failw(w, errs.Internal("update subscription", uerr))
		return
	}
	keyStatus := domain.KeySuspended
	if status == domain.SubActive {
		keyStatus = domain.KeyActive
	}
	affected, kerr := h.store.Keys().SetStatusBySubscription(ctx, s.ID, keyStatus)
	if kerr != nil {
		failw(w, errs.Internal("update keys", kerr))
		return
	}
	okw(w, map[string]any{
		"subscription_id": s.ID, "status": string(s.Status), "keys_affected": affected,
	})
}

// DeleteSubscription implements DELETE /internal/v1/subscriptions/{id}.
func (h *Handlers) DeleteSubscription(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	ctx := r.Context()
	s, err := h.store.Subscriptions().GetByID(ctx, id)
	if err != nil || s == nil {
		failw(w, errs.NotFound("subscription not found"))
		return
	}
	s.Status = domain.SubTerminated
	s.UpdatedAt = h.clock.Now()
	if uerr := h.store.Subscriptions().Update(ctx, s); uerr != nil {
		failw(w, errs.Internal("terminate subscription", uerr))
		return
	}
	revoked, kerr := h.store.Keys().RevokeBySubscription(ctx, s.ID)
	if kerr != nil {
		failw(w, errs.Internal("revoke keys", kerr))
		return
	}
	okw(w, map[string]any{
		"subscription_id": s.ID, "status": string(s.Status), "keys_revoked": revoked,
	})
}

// subscriptionView renders a subscription with its plan code.
func subscriptionView(h *Handlers, r *http.Request, s *domain.Subscription) map[string]any {
	out := map[string]any{
		"subscription_id": s.ID, "v10_host_id": s.V10HostID,
		"status": string(s.Status), "billing_mode": string(s.BillingMode),
		"period_start": s.PeriodStart.Unix(), "period_end": s.PeriodEnd.Unix(),
		"quota_total": s.QuotaTotal, "quota_used": s.QuotaUsed,
		"quota_remaining": s.Remaining(),
	}
	if plan, perr := h.store.Plans().GetByID(r.Context(), s.PlanID); perr == nil && plan != nil {
		out["plan_code"] = plan.PlanCode
	}
	return out
}

// HostUsage implements GET /internal/v1/billing/host-usage, the figure V10 uses
// for metered billing (the get_host_flow hook).
func (h *Handlers) HostUsage(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query().Get("v10_host_id")
	hostID, err := strconv.ParseUint(v, 10, 64)
	if err != nil || hostID == 0 {
		failw(w, errs.InvalidRequest("v10_host_id is required"))
		return
	}
	from, to := parseTimeRange(r, h.clock.Now())
	sum, serr := h.store.Usage().Summary(r.Context(), store.UsageFilter{
		V10HostID: hostID, From: from, To: to,
	})
	if serr != nil {
		failw(w, errs.Internal("host usage", serr))
		return
	}
	okw(w, map[string]any{
		"v10_host_id": hostID,
		"from":        from.Unix(), "to": to.Unix(),
		"requests": sum.Requests, "prompt_tokens": sum.PromptTokens,
		"completion_tokens": sum.CompletionTokens, "cost_micros": sum.CostMicros,
		"currency": "CNY", "amount": microsToYuanString(sum.CostMicros),
	})
}

// SSOTicket implements POST /internal/v1/console/sso-ticket.
func (h *Handlers) SSOTicket(w http.ResponseWriter, r *http.Request) {
	var req struct {
		V10ClientID uint64 `json:"v10_client_id"`
		Email       string `json:"email"`
	}
	if err := decodeBody(r, &req); err != nil {
		failw(w, errs.InvalidRequest("invalid body: "+err.Error()))
		return
	}
	if req.V10ClientID == 0 {
		failw(w, errs.InvalidRequest("v10_client_id is required"))
		return
	}
	if h.console == nil {
		failw(w, errs.New(http.StatusServiceUnavailable, errs.CodeFeatureDisabled, "console is not configured"))
		return
	}
	// Ensure the account exists so the console has something to show.
	if _, err := h.store.Accounts().Upsert(r.Context(), &domain.Account{
		SiteID: siteOf(r), V10ClientID: req.V10ClientID, Email: req.Email,
		Status: domain.AccountEnabled,
	}); err != nil {
		failw(w, errs.Internal("provision account", err))
		return
	}
	ticket, ssoURL, expiresIn, err := h.console.IssueTicket(r.Context(), siteOf(r), req.V10ClientID, req.Email)
	if err != nil {
		failw(w, errs.Internal("issue ticket", err))
		return
	}
	okw(w, map[string]any{
		"ticket": ticket, "expires_in": expiresIn, "sso_url": ssoURL,
	})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// parseTimeRange reads from/to unix seconds, defaulting to the last 30 days.
func parseTimeRange(r *http.Request, now time.Time) (time.Time, time.Time) {
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

func intParam(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// pickPeriod returns the explicit period or the plan's default.
func pickPeriod(explicit, planDefault string) string {
	if explicit != "" {
		return explicit
	}
	if planDefault != "" {
		return planDefault
	}
	return "month"
}

// periodEndFor computes the end of a billing period.
//
// "once" is treated as a non-expiring year so the subscription does not silently
// expire while a customer still holds an entitlement.
func periodEndFor(start time.Time, period string) time.Time {
	switch period {
	case "month":
		return start.AddDate(0, 1, 0)
	case "quarter":
		return start.AddDate(0, 3, 0)
	case "year":
		return start.AddDate(1, 0, 0)
	case "once":
		return start.AddDate(10, 0, 0)
	default:
		return start.AddDate(0, 1, 0)
	}
}

// microsToYuanString renders micro-yuan as a 6-decimal yuan string, which is what
// V10 expects for invoice amounts (no floating point in the payload).
func microsToYuanString(micros int64) string {
	neg := micros < 0
	if neg {
		micros = -micros
	}
	whole := micros / 1_000_000
	frac := micros % 1_000_000
	s := strconv.FormatInt(whole, 10) + "." + padLeft(strconv.FormatInt(frac, 10), 6)
	if neg {
		return "-" + s
	}
	return s
}

func padLeft(s string, n int) string {
	for len(s) < n {
		s = "0" + s
	}
	return s
}
