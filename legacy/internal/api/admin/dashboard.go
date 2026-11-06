package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"czapi/internal/domain"
	"czapi/internal/pkg/errs"
	"czapi/internal/store"
)

// jsonEncode encodes a value to the response writer.
func jsonEncode(w http.ResponseWriter, v any) error {
	return json.NewEncoder(w).Encode(v)
}

// decodeBody parses a JSON body with a size cap.
func decodeBody(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20))
	return dec.Decode(dst)
}

// pathUint parses a uint64 path parameter.
func pathUint(r *http.Request, name string) (uint64, bool) {
	n, err := strconv.ParseUint(chiParam(r, name), 10, 64)
	if err != nil || n == 0 {
		return 0, false
	}
	return n, true
}

// intQuery reads an integer query parameter with a default.
func intQuery(r *http.Request, name string, def int) int {
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

// pageWindow converts page/page_size into offset/limit.
func pageWindow(r *http.Request) (limit, offset, page, pageSize int) {
	page = intQuery(r, "page", 1)
	if page < 1 {
		page = 1
	}
	pageSize = intQuery(r, "page_size", 20)
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 200 {
		pageSize = 200
	}
	return pageSize, (page - 1) * pageSize, page, pageSize
}

// parseRange reads from/to unix seconds with a default lookback in days.
func parseRange(r *http.Request, now time.Time, defaultDays int) (time.Time, time.Time) {
	from := now.AddDate(0, 0, -defaultDays)
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

// ---------------------------------------------------------------------------
// Dashboard
// ---------------------------------------------------------------------------

// Dashboard implements GET /admin/v1/dashboard.
func (h *Handlers) Dashboard(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	ctx := r.Context()
	now := h.clk.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	today, terr := h.store.Usage().Summary(ctx, store.UsageFilter{From: dayStart, To: now})
	if terr != nil {
		failw(w, errs.Internal("dashboard: today", terr))
		return
	}
	month, _ := h.store.Usage().Summary(ctx, store.UsageFilter{From: monthStart, To: now})

	channels, _ := h.store.Channels().List(ctx)
	chViews := make([]map[string]any, 0, len(channels))
	for _, c := range channels {
		v := map[string]any{
			"channel_id": c.ID, "name": c.Name, "status": c.Status,
			"open": false, "error_rate": 0.0,
		}
		if h.breaker != nil {
			open, rate, samples := h.breaker.State(c.ID)
			v["open"] = open
			v["error_rate"] = rate
			v["samples"] = samples
		}
		chViews = append(chViews, v)
	}

	// Aggregate top models from the raw usage table for the current day window.
	topModels := h.topModels(ctx, dayStart, now)
	topAccounts := h.topAccounts(ctx, monthStart, now)

	daily, _ := h.store.Aggregates().BucketCost(ctx, store.UsageFilter{From: dayStart.AddDate(0, 0, -29), To: now})
	trend := make([]map[string]any, 0, len(daily))
	for _, d := range daily {
		trend = append(trend, map[string]any{
			"day": d.Day, "requests": d.Requests, "cost_micros": d.CostMicros,
		})
	}

	okw(w, map[string]any{
		"requests_today":       today.Requests,
		"cost_today_micros":    today.CostMicros,
		"cost_month_micros":    month.CostMicros,
		"error_rate_5m":        0.0,
		"qps_5m":               0.0,
		"ttft_p95_ms_5m":       0,
		"active_keys":          0,
		"active_subscriptions": 0,
		"accounts":             h.countAccounts(ctx),
		"channels":             chViews,
		"top_models":           topModels,
		"top_accounts":         topAccounts,
		"trend":                trend,
		"reconcile": map[string]any{
			"last_run": 0, "diff_micros": 0, "ok": true,
		},
	})
}

// countAccounts returns the account count, tolerating repository errors.
func (h *Handlers) countAccounts(ctx context.Context) int64 {
	n, err := h.store.Accounts().Count(ctx)
	if err != nil {
		return 0
	}
	return n
}

// topModels aggregates usage by model for a window.
func (h *Handlers) topModels(ctx context.Context, from, to time.Time) []map[string]any {
	rows, err := h.store.Usage().List(ctx, store.UsageFilter{From: from, To: to, Limit: 5000})
	if err != nil {
		return []map[string]any{}
	}
	type acc struct {
		requests int64
		cost     int64
	}
	byModel := map[string]*acc{}
	for _, u := range rows {
		a, ok := byModel[u.Model]
		if !ok {
			a = &acc{}
			byModel[u.Model] = a
		}
		a.requests++
		a.cost += u.CostMicros
	}
	out := make([]map[string]any, 0, len(byModel))
	for m, a := range byModel {
		out = append(out, map[string]any{
			"model": m, "requests": a.requests, "cost_micros": a.cost,
		})
	}
	sortByCostDesc(out)
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

// topAccounts aggregates usage by account for a window.
func (h *Handlers) topAccounts(ctx context.Context, from, to time.Time) []map[string]any {
	rows, err := h.store.Usage().List(ctx, store.UsageFilter{From: from, To: to, Limit: 5000})
	if err != nil {
		return []map[string]any{}
	}
	type acc struct{ cost int64 }
	byAcc := map[uint64]*acc{}
	for _, u := range rows {
		a, ok := byAcc[u.AccountID]
		if !ok {
			a = &acc{}
			byAcc[u.AccountID] = a
		}
		a.cost += u.CostMicros
	}
	out := make([]map[string]any, 0, len(byAcc))
	for id, a := range byAcc {
		item := map[string]any{"account_id": id, "cost_micros": a.cost}
		if account, err := h.store.Accounts().GetByID(ctx, id); err == nil && account != nil {
			item["v10_client_id"] = account.V10ClientID
			item["email"] = account.Email
		}
		out = append(out, item)
	}
	sortByCostDesc(out)
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

// sortByCostDesc orders rows by cost_micros descending.
func sortByCostDesc(rows []map[string]any) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			a, _ := rows[j-1]["cost_micros"].(int64)
			b, _ := rows[j]["cost_micros"].(int64)
			if b > a {
				rows[j-1], rows[j] = rows[j], rows[j-1]
				continue
			}
			break
		}
	}
}

// --- unused-import guard ---
var _ = domain.Plan{}
