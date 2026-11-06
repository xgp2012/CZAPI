package admin

import (
	"net/http"
	"strconv"
	"strings"

	"czapi/internal/domain"
	"czapi/internal/pkg/errs"
	"czapi/internal/store"
)

// ---------------------------------------------------------------------------
// Ledger
// ---------------------------------------------------------------------------

// ListLedger implements GET /admin/v1/ledger.
func (h *Handlers) ListLedger(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	limit, offset, page, pageSize := pageWindow(r)
	from, to := parseRange(r, h.clk.Now(), 30)
	f := store.LedgerFilter{From: from, To: to, Offset: offset, Limit: limit}
	if v := r.URL.Query().Get("account_id"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			f.AccountID = n
		}
	}
	if v := strings.TrimSpace(r.URL.Query().Get("type")); v != "" {
		f.Type = domain.LedgerType(v)
	}
	if v := strings.TrimSpace(r.URL.Query().Get("ref_type")); v != "" {
		f.RefType = v
	}

	rows, err := h.store.Ledger().List(r.Context(), f)
	if err != nil {
		failw(w, errs.Internal("list ledger", err))
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		item := map[string]any{
			"entry_id": e.ID, "account_id": e.AccountID, "type": string(e.Type),
			"amount_micros": e.AmountMicros, "balance_after": e.BalanceAfter,
			"ref_type": e.RefType, "ref_id": e.RefID, "remark": e.Remark,
			"created_at": e.CreatedAt.Unix(),
		}
		if acc, aerr := h.store.Accounts().GetByID(r.Context(), e.AccountID); aerr == nil && acc != nil {
			item["v10_client_id"] = acc.V10ClientID
			item["email"] = acc.Email
		}
		items = append(items, item)
	}
	okw(w, map[string]any{
		"total": len(items), "page": page, "page_size": pageSize, "items": items,
	})
}

// ---------------------------------------------------------------------------
// Reconcile
// ---------------------------------------------------------------------------

// Reconcile implements GET /admin/v1/reconcile.
//
// It compares three independent views of today's spend:
//
//	usage rows   — what the metering pipeline recorded
//	rollups      — the pre-aggregated daily table used by the dashboards
//	ledger       — the credits actually deducted from balances
//
// A non-zero diff means one of the three is lagging or a write was lost, which
// is exactly the signal the shadow/alerting safeguard watches (plan §15.3).
func (h *Handlers) Reconcile(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	ctx := r.Context()
	from, to := parseRange(r, h.clk.Now(), 1)

	usageSum, uerr := h.store.Usage().Summary(ctx, store.UsageFilter{From: from, To: to})
	if uerr != nil {
		failw(w, errs.Internal("reconcile: usage", uerr))
		return
	}
	globalCost, _ := h.store.Usage().SumCostGlobalBetween(ctx, from, to)

	rollups, _ := h.store.Aggregates().BucketCost(ctx, store.UsageFilter{From: from, To: to})
	var rollupCost int64
	for _, d := range rollups {
		rollupCost += d.CostMicros
	}

	// Balance deductions since the window start: consume entries are negative.
	ledgerRows, _ := h.store.Ledger().List(ctx, store.LedgerFilter{
		Type: domain.LedgerConsume, From: from, To: to, Limit: 100000,
	})
	var ledgerCost int64
	for _, e := range ledgerRows {
		ledgerCost += -e.AmountMicros
	}

	diff := rollupCost - ledgerCost
	okw(w, map[string]any{
		"window": map[string]any{"from": from.Unix(), "to": to.Unix()},
		"usage": map[string]any{
			"requests": usageSum.Requests, "cost_micros": usageSum.CostMicros,
			"sum_cost_global": globalCost,
		},
		"rollup_cost_micros": rollupCost,
		"ledger_cost_micros": ledgerCost,
		"ledger_entries":     len(ledgerRows),
		"diff_micros":        diff,
		"ok":                 diff == 0,
		"tolerance_micros":   1000,
		"note": "ledger covers balance-paid traffic only; subscription-paid traffic " +
			"appears in usage and rollups but never in the ledger",
	})
	if h.mx != nil {
		h.mx.SetReconcileDiff(diff)
	}
}

// ---------------------------------------------------------------------------
// Flags
// ---------------------------------------------------------------------------

// ListFlags implements GET /admin/v1/flags.
func (h *Handlers) ListFlags(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	if h.flags == nil {
		failw(w, errs.FeatureDisabled("feature flags"))
		return
	}
	stored, err := h.flags.List(r.Context())
	if err != nil {
		failw(w, errs.Internal("list flags", err))
		return
	}
	items := make([]map[string]any, 0, len(stored))
	for _, f := range stored {
		items = append(items, map[string]any{
			"key": f.Key, "value": f.Value, "scope": f.Scope,
			"updated_at": f.UpdatedAt.Unix(),
		})
	}
	okw(w, map[string]any{
		"total": len(items), "items": items,
		"known": []string{
			FlagGatewayEnabled, FlagBillingEnforced,
			FlagModuleAutocreateEnabled, FlagRechargeEnabled,
		},
	})
}

// SetFlag implements PUT /admin/v1/flags/{key}.
func (h *Handlers) SetFlag(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	if h.flags == nil {
		failw(w, errs.FeatureDisabled("feature flags"))
		return
	}
	key := chiParam(r, "key")
	var req struct {
		Value *bool  `json:"value"`
		Scope string `json:"scope"`
	}
	if err := decodeBody(r, &req); err != nil {
		failw(w, errs.InvalidRequest("invalid body: "+err.Error()))
		return
	}
	if req.Value == nil {
		failw(w, errs.InvalidRequest("value is required"))
		return
	}
	if err := h.flags.Set(r.Context(), key, *req.Value, strings.TrimSpace(req.Scope)); err != nil {
		failw(w, errs.Internal("set flag", err))
		return
	}
	h.audit(r, "flag.set", "flag", key, "", mustJSON(map[string]any{
		"value": *req.Value, "scope": req.Scope,
	}))
	okw(w, map[string]any{
		"key": key, "value": *req.Value, "scope": req.Scope,
		"effective": h.flags.Get(r.Context(), key, req.Scope),
	})
}

// ---------------------------------------------------------------------------
// Sites
// ---------------------------------------------------------------------------

// ListSites implements GET /admin/v1/sites. Secrets are never returned.
func (h *Handlers) ListSites(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	sites, err := h.store.Sites().List(r.Context())
	if err != nil {
		failw(w, errs.Internal("list sites", err))
		return
	}
	items := make([]map[string]any, 0, len(sites))
	for _, s := range sites {
		items = append(items, map[string]any{
			"app_id": s.AppID, "name": s.Name, "ip_allow": s.IPAllow,
			"enabled": s.Enabled, "has_secret": s.Secret != "",
			"has_previous_secret": s.SecretPrev != "",
			"created_at":          s.CreatedAt.Unix(),
		})
	}
	okw(w, map[string]any{"total": len(items), "items": items})
}

// CreateSite implements POST /admin/v1/sites. It returns the secret exactly once.
func (h *Handlers) CreateSite(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	var req struct {
		AppID   string `json:"app_id"`
		Name    string `json:"name"`
		IPAllow string `json:"ip_allow"`
		Secret  string `json:"secret"`
	}
	if err := decodeBody(r, &req); err != nil {
		failw(w, errs.InvalidRequest("invalid body: "+err.Error()))
		return
	}
	if strings.TrimSpace(req.AppID) == "" {
		failw(w, errs.InvalidRequest("app_id is required"))
		return
	}
	secret := strings.TrimSpace(req.Secret)
	if secret == "" {
		generated, gerr := randomSecret()
		if gerr != nil {
			failw(w, errs.Internal("generate secret", gerr))
			return
		}
		secret = generated
	}
	site := &domain.Site{
		AppID: strings.TrimSpace(req.AppID), Name: strings.TrimSpace(req.Name),
		Secret: secret, Enabled: true, CreatedAt: h.clk.Now(),
	}
	if v := strings.TrimSpace(req.IPAllow); v != "" {
		for _, part := range strings.Split(v, ",") {
			if p := strings.TrimSpace(part); p != "" {
				site.IPAllow = append(site.IPAllow, p)
			}
		}
	}
	if site.Name == "" {
		site.Name = site.AppID
	}
	if err := h.store.Sites().Create(r.Context(), site); err != nil {
		failw(w, errs.Internal("create site", err))
		return
	}
	h.audit(r, "site.create", "site", site.AppID, "", "")
	okw(w, map[string]any{
		"app_id": site.AppID, "name": site.Name, "secret": secret,
		"note": "store this secret now; it is shown only once",
	})
}

// RotateSecret implements POST /admin/v1/sites/{app_id}/rotate-secret.
//
// The previous secret keeps working until the V10 side is updated, so rotation
// causes no downtime (availability first).
func (h *Handlers) RotateSecret(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	appID := chiParam(r, "app_id")
	site, err := h.store.Sites().GetByAppID(r.Context(), appID)
	if err != nil || site == nil {
		failw(w, errs.NotFound("site not found"))
		return
	}
	fresh, gerr := randomSecret()
	if gerr != nil {
		failw(w, errs.Internal("generate secret", gerr))
		return
	}
	site.SecretPrev = site.Secret
	site.Secret = fresh
	site.UpdatedAt = h.clk.Now()
	if uerr := h.store.Sites().Update(r.Context(), site); uerr != nil {
		failw(w, errs.Internal("rotate secret", uerr))
		return
	}
	h.audit(r, "site.rotate_secret", "site", appID, "", "")
	okw(w, map[string]any{
		"app_id": appID, "secret": fresh,
		"note": "the previous secret remains valid until it is replaced again",
	})
}

// ---------------------------------------------------------------------------
// Settings + audit
// ---------------------------------------------------------------------------

// Settings implements GET /admin/v1/settings.
//
// Only non-secret operational settings are exposed; credentials live in the
// configuration file and are never readable through the API.
func (h *Handlers) Settings(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	okw(w, h.settingsPayload(r))

}

// buildVersion is stamped at build time when available.
var buildVersion = "dev"

// ListAudit implements GET /admin/v1/audit.
func (h *Handlers) ListAudit(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	limit, offset, page, pageSize := pageWindow(r)
	rows, err := h.store.Audit().List(r.Context(), offset, limit)
	if err != nil {
		failw(w, errs.Internal("list audit", err))
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, a := range rows {
		items = append(items, map[string]any{
			"id": a.ID, "actor_type": a.ActorType, "actor_id": a.ActorID,
			"action": a.Action, "object_type": a.ObjectType, "object_id": a.ObjectID,
			"before": a.Before, "after": a.After, "ip": a.IP,
			"created_at": a.CreatedAt.Unix(),
		})
	}
	okw(w, map[string]any{
		"total": len(items), "page": page, "page_size": pageSize, "items": items,
	})
}
