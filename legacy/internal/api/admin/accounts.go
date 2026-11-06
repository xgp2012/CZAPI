package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"czapi/internal/billing"
	"czapi/internal/domain"
	"czapi/internal/pkg/errs"
)

// jsonMarshal is the audit-trace encoder.
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// mustJSON renders a value for the audit trail.
func mustJSON(v any) string {
	b, err := jsonMarshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// Accounts
// ---------------------------------------------------------------------------

// accountView is the admin projection of an account.
type accountView struct {
	AccountID     uint64 `json:"account_id"`
	V10ClientID   uint64 `json:"v10_client_id"`
	SiteID        string `json:"site_id"`
	Email         string `json:"email"`
	Status        int8   `json:"status"`
	BalanceMicros int64  `json:"balance_micros"`
	AllowNegative bool   `json:"allow_negative"`
	CreatedAt     int64  `json:"created_at"`
}

func toAccountView(a *domain.Account) accountView {
	return accountView{
		AccountID: a.ID, V10ClientID: a.V10ClientID, SiteID: a.SiteID,
		Email: a.Email, Status: int8(a.Status), BalanceMicros: a.BalanceCredit,
		AllowNegative: a.AllowNegative, CreatedAt: a.CreatedAt.Unix(),
	}
}

// ListAccounts implements GET /admin/v1/accounts.
func (h *Handlers) ListAccounts(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	limit, offset, page, pageSize := pageWindow(r)
	list, err := h.store.Accounts().List(r.Context(), offset, limit)
	if err != nil {
		failw(w, errs.Internal("list accounts", err))
		return
	}
	statusFilter, hasStatus := statusQuery(r)
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))

	items := make([]accountView, 0, len(list))
	for i := range list {
		a := &list[i]
		if hasStatus && int8(a.Status) != statusFilter {
			continue
		}
		if query != "" && !accountMatches(a, query) {
			continue
		}
		items = append(items, toAccountView(a))
	}
	total, _ := h.store.Accounts().Count(r.Context())
	okw(w, map[string]any{
		"total": total, "page": page, "page_size": pageSize, "items": items,
	})
}

// statusQuery reads the optional numeric status filter.
func statusQuery(r *http.Request) (int8, bool) {
	v := r.URL.Query().Get("status")
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return int8(n), true
}

// accountMatches does a case-insensitive substring match on email and client id.
func accountMatches(a *domain.Account, q string) bool {
	if strings.Contains(strings.ToLower(a.Email), q) {
		return true
	}
	if strings.Contains(a.SiteID, q) {
		return true
	}
	return strings.Contains(strconv.FormatUint(a.V10ClientID, 10), q)
}

// GetAccount implements GET /admin/v1/accounts/{id}.
func (h *Handlers) GetAccount(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	id, ok := pathUint(r, "id")
	if !ok {
		failw(w, errs.InvalidRequest("invalid account id"))
		return
	}
	ctx := r.Context()
	acc, err := h.store.Accounts().GetByID(ctx, id)
	if err != nil || acc == nil {
		failw(w, errs.NotFound("account not found"))
		return
	}
	subs, _ := h.store.Subscriptions().ListByAccount(ctx, acc.ID)
	keys, _ := h.store.Keys().ListByAccount(ctx, acc.ID)

	subViews := make([]map[string]any, 0, len(subs))
	for _, s := range subs {
		v := map[string]any{
			"subscription_id": s.ID, "v10_host_id": s.V10HostID,
			"status": string(s.Status), "billing_mode": string(s.BillingMode),
			"period_start": s.PeriodStart.Unix(), "period_end": s.PeriodEnd.Unix(),
			"quota_total": s.QuotaTotal, "quota_used": s.QuotaUsed,
			"quota_remaining": s.Remaining(),
		}
		if plan, perr := h.store.Plans().GetByID(ctx, s.PlanID); perr == nil && plan != nil {
			v["plan_code"] = plan.PlanCode
		}
		subViews = append(subViews, v)
	}
	keyViews := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		keyViews = append(keyViews, map[string]any{
			"key_id": k.ID, "key_prefix": k.KeyPrefix, "name": k.Name,
			"status": string(k.Status), "subscription_id": k.SubscriptionID,
			"last_used_at": k.LastUsedAt, "created_at": k.CreatedAt.Unix(),
		})
	}
	okw(w, map[string]any{
		"account": toAccountView(acc), "subscriptions": subViews, "keys": keyViews,
	})
}

// accountUpdateRequest is the PATCH body.
type accountUpdateRequest struct {
	Status        *int8   `json:"status"`
	AllowNegative *bool   `json:"allow_negative"`
	Email         *string `json:"email"`
}

// UpdateAccount implements PATCH /admin/v1/accounts/{id}.
func (h *Handlers) UpdateAccount(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	id, ok := pathUint(r, "id")
	if !ok {
		failw(w, errs.InvalidRequest("invalid account id"))
		return
	}
	acc, err := h.store.Accounts().GetByID(r.Context(), id)
	if err != nil || acc == nil {
		failw(w, errs.NotFound("account not found"))
		return
	}
	var req accountUpdateRequest
	if derr := decodeBody(r, &req); derr != nil {
		failw(w, errs.InvalidRequest("invalid body: "+derr.Error()))
		return
	}
	before := toAccountView(acc)
	if req.Status != nil {
		switch domain.AccountStatus(*req.Status) {
		case domain.AccountEnabled, domain.AccountDisabled:
			acc.Status = domain.AccountStatus(*req.Status)
		default:
			failw(w, errs.InvalidRequest("status must be 1 (enabled) or 0 (disabled)"))
			return
		}
	}
	if req.AllowNegative != nil {
		acc.AllowNegative = *req.AllowNegative
	}
	if req.Email != nil {
		acc.Email = strings.TrimSpace(*req.Email)
	}
	acc.UpdatedAt = h.clk.Now()
	if uerr := h.store.Accounts().Update(r.Context(), acc); uerr != nil {
		failw(w, errs.Internal("update account", uerr))
		return
	}
	h.audit(r, "account.update", "account", strconv.FormatUint(acc.ID, 10),
		mustJSON(before), mustJSON(toAccountView(acc)))
	okw(w, toAccountView(acc))
}

// adjustRequest is the manual balance adjustment body.
type adjustRequest struct {
	AmountMicros int64  `json:"amount_micros"`
	Remark       string `json:"remark"`
	// RequestID makes the adjustment idempotent; the console sends a UUID.
	RequestID string `json:"request_id"`
	// RefType/RefID default to "adjust" / RequestID.
	RefType string `json:"ref_type"`
	RefID   string `json:"ref_id"`
}

// AdjustBalance implements POST /admin/v1/accounts/{id}/adjust.
//
// Manual adjustments move real money, so they require an idempotency key and are
// written to the ledger with ref_type="adjust" (plan §10.5).
func (h *Handlers) AdjustBalance(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	id, ok := pathUint(r, "id")
	if !ok {
		failw(w, errs.InvalidRequest("invalid account id"))
		return
	}
	var req adjustRequest
	if err := decodeBody(r, &req); err != nil {
		failw(w, errs.InvalidRequest("invalid body: "+err.Error()))
		return
	}
	if req.AmountMicros == 0 {
		failw(w, errs.InvalidRequest("amount_micros must not be zero"))
		return
	}
	refID := strings.TrimSpace(req.RefID)
	if refID == "" {
		refID = strings.TrimSpace(req.RequestID)
	}
	if refID == "" {
		failw(w, errs.InvalidRequest("request_id is required for idempotency"))
		return
	}
	refType := strings.TrimSpace(req.RefType)
	if refType == "" {
		refType = "adjust"
	}
	if h.credit == nil {
		failw(w, errs.FeatureDisabled("balance adjustment"))
		return
	}
	kind := domain.LedgerAdminAdjust
	if req.AmountMicros < 0 {
		kind = domain.LedgerRefund
	}
	remark := strings.TrimSpace(req.Remark)
	if c := actor(r); c != nil {
		remark = strings.TrimSpace(remark + " by=" + c.Username)
	}

	// Credit addresses the tenant by (site_id, v10_client_id), so resolve the
	// account row first and reject an unknown id before touching money.
	acc, aerr := h.store.Accounts().GetByID(r.Context(), id)
	if aerr != nil || acc == nil {
		failw(w, errs.NotFound("account not found"))
		return
	}
	res, cerr := h.credit.Credit(r.Context(), billing.CreditRequest{
		SiteID:       acc.SiteID,
		V10ClientID:  acc.V10ClientID,
		Kind:         kind,
		AmountMicros: req.AmountMicros,
		RefType:      refType,
		RefID:        refID,
		Remark:       remark,
	})
	if cerr != nil {
		failw(w, errs.As(cerr))
		return
	}
	h.audit(r, "account.adjust", "account", strconv.FormatUint(id, 10),
		"", mustJSON(map[string]any{"amount_micros": req.AmountMicros, "ref_id": refID}))
	okw(w, map[string]any{
		"applied": res.Applied, "balance_micros": res.BalanceAfter,
		"ledger_entry_id": res.LedgerEntryID,
	})
}

// ---------------------------------------------------------------------------
// Keys (cross-account)
// ---------------------------------------------------------------------------

// maxScanAccounts bounds the fan-out used by the cross-account listings so one
// admin page can never trigger an unbounded scan.
const maxScanAccounts = 500

// scanAccounts walks accounts page by page, stopping at maxScanAccounts.
func (h *Handlers) scanAccounts(ctx context.Context) []domain.Account {
	const batch = 100
	out := make([]domain.Account, 0, batch)
	for offset := 0; offset < maxScanAccounts; offset += batch {
		page, err := h.store.Accounts().List(ctx, offset, batch)
		if err != nil || len(page) == 0 {
			break
		}
		out = append(out, page...)
		if len(page) < batch {
			break
		}
	}
	return out
}

// ListKeysAcrossAccounts implements GET /admin/v1/keys.
func (h *Handlers) ListKeysAcrossAccounts(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	ctx := r.Context()
	_, _, page, pageSize := pageWindow(r)
	var accountFilter uint64
	if v := r.URL.Query().Get("account_id"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			accountFilter = n
		}
	}
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))

	accounts := h.scanAccounts(ctx)
	if accountFilter != 0 {
		filtered := accounts[:0]
		for _, a := range accounts {
			if a.ID == accountFilter {
				filtered = append(filtered, a)
			}
		}
		accounts = filtered
	}

	rows := make([]map[string]any, 0, pageSize)
	matched := 0
	for _, a := range accounts {
		keys, kerr := h.store.Keys().ListByAccount(ctx, a.ID)
		if kerr != nil {
			continue
		}
		for i := range keys {
			k := &keys[i]
			if statusFilter != "" && string(k.Status) != statusFilter {
				continue
			}
			if query != "" &&
				!strings.Contains(strings.ToLower(k.Name), query) &&
				!strings.Contains(strings.ToLower(k.KeyPrefix), query) {
				continue
			}
			matched++
			if matched <= (page-1)*pageSize || len(rows) >= pageSize {
				continue
			}
			rows = append(rows, map[string]any{
				"key_id": k.ID, "key_prefix": k.KeyPrefix, "name": k.Name,
				"status": string(k.Status), "account_id": k.AccountID,
				"v10_client_id": a.V10ClientID, "email": a.Email,
				"subscription_id": k.SubscriptionID, "models": k.Models,
				"last_used_at": k.LastUsedAt, "created_at": k.CreatedAt.Unix(),
			})
		}
	}
	okw(w, map[string]any{
		"total": matched, "page": page, "page_size": pageSize, "items": rows,
		"scanned_accounts": len(accounts),
	})
}

// UpdateKeyStatus implements PATCH /admin/v1/keys/{id}.
func (h *Handlers) UpdateKeyStatus(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	id, ok := pathUint(r, "id")
	if !ok {
		failw(w, errs.InvalidRequest("invalid key id"))
		return
	}
	key, err := h.store.Keys().GetByID(r.Context(), id)
	if err != nil || key == nil {
		failw(w, errs.NotFound("key not found"))
		return
	}
	var req struct {
		Status *string   `json:"status"`
		Models *[]string `json:"models"`
	}
	if derr := decodeBody(r, &req); derr != nil {
		failw(w, errs.InvalidRequest("invalid body: "+derr.Error()))
		return
	}
	before := string(key.Status)
	if req.Status != nil {
		switch domain.APIKeyStatus(*req.Status) {
		case domain.KeyActive, domain.KeySuspended, domain.KeyRevoked:
			key.Status = domain.APIKeyStatus(*req.Status)
		default:
			failw(w, errs.InvalidRequest("status must be active, suspended or revoked"))
			return
		}
	}
	if req.Models != nil {
		key.Models = *req.Models
	}
	key.UpdatedAt = h.clk.Now()
	if uerr := h.store.Keys().Update(r.Context(), key); uerr != nil {
		failw(w, errs.Internal("update key", uerr))
		return
	}
	// Drop the cached credential so a revoke takes effect on the next request.
	if h.invalidator != nil {
		_ = h.invalidator.InvalidateByID(r.Context(), key.ID)
	}
	h.audit(r, "key.update", "key", strconv.FormatUint(key.ID, 10), before, string(key.Status))
	okw(w, map[string]any{
		"key_id": key.ID, "status": string(key.Status), "models": key.Models,
	})
}

// ---------------------------------------------------------------------------
// Subscriptions (admin)
// ---------------------------------------------------------------------------

// ListSubscriptions implements GET /admin/v1/subscriptions.
func (h *Handlers) ListSubscriptions(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	ctx := r.Context()
	_, _, page, pageSize := pageWindow(r)
	var accountFilter uint64
	if v := r.URL.Query().Get("account_id"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			accountFilter = n
		}
	}
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))

	accounts := h.scanAccounts(ctx)
	if accountFilter != 0 {
		filtered := accounts[:0]
		for _, a := range accounts {
			if a.ID == accountFilter {
				filtered = append(filtered, a)
			}
		}
		accounts = filtered
	}

	rows := make([]map[string]any, 0, pageSize)
	matched := 0
	for _, a := range accounts {
		subs, serr := h.store.Subscriptions().ListByAccount(ctx, a.ID)
		if serr != nil {
			continue
		}
		for i := range subs {
			s := &subs[i]
			if statusFilter != "" && string(s.Status) != statusFilter {
				continue
			}
			matched++
			if matched <= (page-1)*pageSize || len(rows) >= pageSize {
				continue
			}
			v := map[string]any{
				"subscription_id": s.ID, "v10_host_id": s.V10HostID,
				"account_id": s.AccountID, "status": string(s.Status),
				"billing_mode": string(s.BillingMode),
				"period_start": s.PeriodStart.Unix(), "period_end": s.PeriodEnd.Unix(),
				"quota_total": s.QuotaTotal, "quota_used": s.QuotaUsed,
				"quota_remaining": s.Remaining(),
				"v10_client_id":   a.V10ClientID, "email": a.Email,
			}
			if plan, perr := h.store.Plans().GetByID(ctx, s.PlanID); perr == nil && plan != nil {
				v["plan_code"] = plan.PlanCode
			}
			rows = append(rows, v)
		}
	}
	okw(w, map[string]any{
		"total": matched, "page": page, "page_size": pageSize, "items": rows,
		"scanned_accounts": len(accounts),
	})
}

// GetSubscription implements GET /admin/v1/subscriptions/{id}.
func (h *Handlers) GetSubscription(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	id, ok := pathUint(r, "id")
	if !ok {
		failw(w, errs.InvalidRequest("invalid subscription id"))
		return
	}
	s, err := h.store.Subscriptions().GetByID(r.Context(), id)
	if err != nil || s == nil {
		failw(w, errs.NotFound("subscription not found"))
		return
	}
	v := map[string]any{
		"subscription_id": s.ID, "v10_host_id": s.V10HostID, "account_id": s.AccountID,
		"status": string(s.Status), "billing_mode": string(s.BillingMode),
		"period_start": s.PeriodStart.Unix(), "period_end": s.PeriodEnd.Unix(),
		"quota_total": s.QuotaTotal, "quota_used": s.QuotaUsed,
		"quota_remaining": s.Remaining(),
	}
	if plan, perr := h.store.Plans().GetByID(r.Context(), s.PlanID); perr == nil && plan != nil {
		v["plan_code"] = plan.PlanCode
	}
	okw(w, v)
}

// SuspendSubscription implements POST /admin/v1/subscriptions/{id}/suspend.
func (h *Handlers) SuspendSubscription(w http.ResponseWriter, r *http.Request) {
	h.setSubStatus(w, r, domain.SubSuspended)
}

// ResumeSubscription implements POST /admin/v1/subscriptions/{id}/resume.
func (h *Handlers) ResumeSubscription(w http.ResponseWriter, r *http.Request) {
	h.setSubStatus(w, r, domain.SubActive)
}

func (h *Handlers) setSubStatus(w http.ResponseWriter, r *http.Request, status domain.SubscriptionStatus) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	id, ok := pathUint(r, "id")
	if !ok {
		failw(w, errs.InvalidRequest("invalid subscription id"))
		return
	}
	s, err := h.store.Subscriptions().GetByID(r.Context(), id)
	if err != nil || s == nil {
		failw(w, errs.NotFound("subscription not found"))
		return
	}
	before := string(s.Status)
	s.Status = status
	s.UpdatedAt = h.clk.Now()
	if uerr := h.store.Subscriptions().Update(r.Context(), s); uerr != nil {
		failw(w, errs.Internal("update subscription", uerr))
		return
	}
	keyStatus := domain.KeySuspended
	if status == domain.SubActive {
		keyStatus = domain.KeyActive
	}
	affected, _ := h.store.Keys().SetStatusBySubscription(r.Context(), s.ID, keyStatus)
	h.audit(r, "subscription."+strings.ToLower(string(status)), "subscription",
		strconv.FormatUint(s.ID, 10), before, string(status))
	okw(w, map[string]any{
		"subscription_id": s.ID, "status": string(s.Status), "keys_affected": affected,
	})
}

// ResetQuota implements POST /admin/v1/subscriptions/{id}/reset-quota.
//
// It zeroes (or sets) the used counter without touching the period, which is
// what an operator needs after a billing dispute.
func (h *Handlers) ResetQuota(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	id, ok := pathUint(r, "id")
	if !ok {
		failw(w, errs.InvalidRequest("invalid subscription id"))
		return
	}
	s, err := h.store.Subscriptions().GetByID(r.Context(), id)
	if err != nil || s == nil {
		failw(w, errs.NotFound("subscription not found"))
		return
	}
	var req struct {
		QuotaUsed *int64 `json:"quota_used"`
	}
	if derr := decodeBody(r, &req); derr != nil {
		failw(w, errs.InvalidRequest("invalid body: "+derr.Error()))
		return
	}
	before := s.QuotaUsed
	if req.QuotaUsed != nil && *req.QuotaUsed >= 0 {
		s.QuotaUsed = *req.QuotaUsed
	} else {
		s.QuotaUsed = 0
	}
	s.UpdatedAt = h.clk.Now()
	if uerr := h.store.Subscriptions().Update(r.Context(), s); uerr != nil {
		failw(w, errs.Internal("reset quota", uerr))
		return
	}
	h.audit(r, "subscription.reset_quota", "subscription", strconv.FormatUint(s.ID, 10),
		strconv.FormatInt(before, 10), strconv.FormatInt(s.QuotaUsed, 10))
	okw(w, map[string]any{
		"subscription_id": s.ID, "quota_used": s.QuotaUsed,
		"quota_total": s.QuotaTotal, "quota_remaining": s.Remaining(),
	})
}
