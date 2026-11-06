package internalapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"czapi/internal/auth"
	"czapi/internal/billing"
	"czapi/internal/domain"
	"czapi/internal/pkg/crypto"
	"czapi/internal/pkg/errs"
)

// jsonEncode is a tiny indirection so helpers can encode without importing
// encoding/json twice.
func jsonEncode(w http.ResponseWriter, v any) error {
	return json.NewEncoder(w).Encode(v)
}

// siteOf returns the app id resolved by the signature middleware.
//
// It reads the value the auth package stored, so the tenant key always matches
// the app that signed the request. A missing app id is a programming error (the
// route is inside the signed group), so it degrades to an empty site rather than
// panicking.
func siteOf(r *http.Request) string {
	if call, ok := auth.InternalCallFrom(r.Context()); ok && call != nil {
		return call.AppID
	}
	return ""
}

// decodeBody parses the request body with a size cap.
func decodeBody(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20))
	return dec.Decode(dst)
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

// Health implements GET /internal/v1/health. It is reachable without a
// signature so the V10 module's testConnect() can probe connectivity.
func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 2*time.Second)
	defer cancel()

	db := "ok"
	if err := h.store.Ping(ctx); err != nil {
		db = "down"
	}
	ce := "ok"
	if h.cache != nil {
		if err := h.cache.Ping(ctx); err != nil {
			ce = "down"
		}
	} else {
		ce = "absent"
	}
	okw(w, map[string]any{
		"status":  "ok",
		"version": h.version,
		"time":    h.clock.Now().Unix(),
		"db":      db,
		"cache":   ce,
		"queue":   "ok",
	})
}

// ---------------------------------------------------------------------------
// Accounts
// ---------------------------------------------------------------------------

// SyncAccount implements POST /internal/v1/accounts/sync.
type syncAccountRequest struct {
	V10ClientID uint64 `json:"v10_client_id"`
	Email       string `json:"email"`
	Status      *int8  `json:"status"`
}

// SyncAccount upserts the account for a V10 client.
func (h *Handlers) SyncAccount(w http.ResponseWriter, r *http.Request) {
	var req syncAccountRequest
	if err := decodeBody(r, &req); err != nil {
		failw(w, errs.InvalidRequest("invalid body: "+err.Error()))
		return
	}
	if req.V10ClientID == 0 {
		failw(w, errs.InvalidRequest("v10_client_id is required"))
		return
	}
	status := domain.AccountEnabled
	if req.Status != nil {
		status = domain.AccountStatus(*req.Status)
	}
	acc, err := h.store.Accounts().Upsert(r.Context(), &domain.Account{
		SiteID:      siteOf(r),
		V10ClientID: req.V10ClientID,
		Email:       strings.TrimSpace(req.Email),
		Status:      status,
	})
	if err != nil {
		failw(w, errs.Internal("sync account", err))
		return
	}
	okw(w, map[string]any{
		"account_id":     acc.ID,
		"v10_client_id":  acc.V10ClientID,
		"status":         acc.Status,
		"balance_micros": acc.BalanceCredit,
	})
}

// Quota implements GET /internal/v1/accounts/{client_id}/quota.
func (h *Handlers) Quota(w http.ResponseWriter, r *http.Request) {
	clientID, ok := parseClientID(w, r)
	if !ok {
		return
	}
	acc, err := h.store.Accounts().GetByV10Client(r.Context(), siteOf(r), clientID)
	if err != nil || acc == nil {
		failw(w, errs.NotFound("account not found"))
		return
	}
	subs, serr := h.store.Subscriptions().ListByAccount(r.Context(), acc.ID)
	if serr != nil {
		failw(w, errs.Internal("list subscriptions", serr))
		return
	}
	items := make([]map[string]any, 0, len(subs))
	for _, s := range subs {
		item := map[string]any{
			"subscription_id": s.ID, "v10_host_id": s.V10HostID,
			"status": string(s.Status), "billing_mode": string(s.BillingMode),
			"period_start": s.PeriodStart.Unix(), "period_end": s.PeriodEnd.Unix(),
			"quota_total": s.QuotaTotal, "quota_used": s.QuotaUsed,
			"quota_remaining": s.Remaining(),
		}
		if plan, perr := h.store.Plans().GetByID(r.Context(), s.PlanID); perr == nil && plan != nil {
			item["plan_code"] = plan.PlanCode
		}
		items = append(items, item)
	}
	okw(w, map[string]any{
		"account_id":     acc.ID,
		"v10_client_id":  acc.V10ClientID,
		"balance_micros": acc.BalanceCredit,
		"status":         acc.Status,
		"subscriptions":  items,
	})
}

// ---------------------------------------------------------------------------
// Credit
// ---------------------------------------------------------------------------

// creditRequest mirrors docs/internal-api-v1.md §2.3.
type creditRequest struct {
	V10ClientID   uint64 `json:"v10_client_id"`
	Kind          string `json:"kind"`
	AmountMicros  int64  `json:"amount_micros"`
	RefType       string `json:"ref_type"`
	RefID         string `json:"ref_id"`
	Remark        string `json:"remark"`
	AllowNegative *bool  `json:"allow_negative"`
}

// Credit implements POST /internal/v1/credit — the money-critical endpoint.
//
// Hard rules enforced here (plan §5.4):
//   - ref_type and ref_id are mandatory (40001 when missing)
//   - (ref_type, ref_id) is unique, so a V10 retry never double-credits
func (h *Handlers) Credit(w http.ResponseWriter, r *http.Request) {
	var req creditRequest
	if err := decodeBody(r, &req); err != nil {
		failw(w, errs.InvalidRequest("invalid body: "+err.Error()))
		return
	}
	if strings.TrimSpace(req.RefType) == "" || strings.TrimSpace(req.RefID) == "" {
		// The documented code for a missing voucher is 40001, distinct from a
		// generic parameter error so the plugin can alert instead of retrying.
		writeJSON(w, http.StatusBadRequest, envelope{
			Code: 40001,
			Msg:  "ref_type and ref_id are required for every credit",
		})
		return
	}
	allowNeg := false
	if req.AllowNegative != nil {
		allowNeg = *req.AllowNegative
	}
	res, err := h.quota.Credit(r.Context(), billing.CreditRequest{
		SiteID:        siteOf(r),
		V10ClientID:   req.V10ClientID,
		Kind:          domain.LedgerType(req.Kind),
		AmountMicros:  req.AmountMicros,
		RefType:       req.RefType,
		RefID:         req.RefID,
		Remark:        req.Remark,
		AllowNegative: allowNeg,
	})
	if err != nil {
		failw(w, errs.As(err))
		return
	}
	result := "applied"
	if !res.Applied {
		result = "duplicate"
	}
	if h.mx != nil {
		h.mx.ObserveCredit(req.Kind, result)
	}
	okw(w, map[string]any{
		"applied":         res.Applied,
		"account_id":      res.AccountID,
		"balance_micros":  res.BalanceAfter,
		"ledger_entry_id": res.LedgerEntryID,
	})
}

// ---------------------------------------------------------------------------
// Keys
// ---------------------------------------------------------------------------

// createKeyRequest mirrors docs/internal-api-v1.md §2.4.
type createKeyRequest struct {
	V10ClientID    uint64   `json:"v10_client_id"`
	SubscriptionID uint64   `json:"subscription_id"`
	Name           string   `json:"name"`
	Models         []string `json:"models"`
	IPWhitelist    []string `json:"ip_whitelist"`
	ExpiresAt      *int64   `json:"expires_at"`
	RequestID      string   `json:"request_id"`
}

// CreateKey implements POST /internal/v1/keys. The plaintext is returned once.
func (h *Handlers) CreateKey(w http.ResponseWriter, r *http.Request) {
	var req createKeyRequest
	if err := decodeBody(r, &req); err != nil {
		failw(w, errs.InvalidRequest("invalid body: "+err.Error()))
		return
	}
	if req.V10ClientID == 0 {
		failw(w, errs.InvalidRequest("v10_client_id is required"))
		return
	}
	ctx := r.Context()
	acc, err := h.store.Accounts().GetByV10Client(ctx, siteOf(r), req.V10ClientID)
	if err != nil || acc == nil {
		failw(w, errs.NotFound("account not found; call /accounts/sync first"))
		return
	}

	var sub *domain.Subscription
	if req.SubscriptionID != 0 {
		s, serr := h.store.Subscriptions().GetByID(ctx, req.SubscriptionID)
		if serr != nil || s == nil {
			failw(w, errs.NotFound("subscription not found"))
			return
		}
		if s.AccountID != acc.ID {
			failw(w, errs.New(403, errs.CodeInvalidRequest, "subscription does not belong to this account"))
			return
		}
		sub = s
	} else {
		subs, lerr := h.store.Subscriptions().ListByAccount(ctx, acc.ID)
		if lerr != nil {
			failw(w, errs.Internal("list subscriptions", lerr))
			return
		}
		for i := range subs {
			if subs[i].Status == domain.SubActive {
				sub = &subs[i]
				break
			}
		}
		if sub == nil {
			failw(w, errs.InvalidRequest("account has no active subscription"))
			return
		}
	}

	// Enforce the plan's per-subscription key cap.
	limit := 5
	if plan, perr := h.store.Plans().GetByID(ctx, sub.PlanID); perr == nil && plan != nil && plan.MaxKeys > 0 {
		limit = plan.MaxKeys
	}
	count, cerr := h.store.Keys().CountBySubscription(ctx, sub.ID)
	if cerr != nil {
		failw(w, errs.Internal("count keys", cerr))
		return
	}
	if count >= limit {
		failw(w, errs.InvalidRequest("key limit reached ("+strconv.Itoa(limit)+") for this host"))
		return
	}

	plain, prefix := crypto.GenerateAPIKey()
	key := &domain.APIKey{
		AccountID:      acc.ID,
		SubscriptionID: sub.ID,
		KeyHash:        crypto.HashKey(h.salt, plain),
		KeyPrefix:      prefix,
		Name:           strings.TrimSpace(req.Name),
		Models:         req.Models,
		IPWhitelist:    req.IPWhitelist,
		Status:         domain.KeyActive,
		CreatedAt:      h.clock.Now(),
		UpdatedAt:      h.clock.Now(),
	}
	if key.Name == "" {
		key.Name = "default"
	}
	if req.ExpiresAt != nil {
		t := time.Unix(*req.ExpiresAt, 0)
		key.ExpiresAt = &t
	}
	if err := h.store.Keys().Create(ctx, key); err != nil {
		failw(w, errs.Internal("create key", err))
		return
	}
	okw(w, map[string]any{
		"key_id": key.ID, "key": plain, "key_prefix": key.KeyPrefix,
		"name": key.Name, "status": string(key.Status),
		"created_at": key.CreatedAt.Unix(),
	})
}

// ListKeys implements GET /internal/v1/keys?v10_client_id=..
func (h *Handlers) ListKeys(w http.ResponseWriter, r *http.Request) {
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
	var subFilter uint64
	if v := r.URL.Query().Get("subscription_id"); v != "" {
		if n, perr := strconv.ParseUint(v, 10, 64); perr == nil {
			subFilter = n
		}
	}

	keys, kerr := h.store.Keys().ListByAccount(ctx, acc.ID)
	if kerr != nil {
		failw(w, errs.Internal("list keys", kerr))
		return
	}
	now := h.clock.Now()
	from := now.AddDate(0, 0, -30)
	items := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		if subFilter != 0 && k.SubscriptionID != subFilter {
			continue
		}
		item := map[string]any{
			"key_id": k.ID, "key_prefix": k.KeyPrefix, "name": k.Name,
			"status": string(k.Status), "subscription_id": k.SubscriptionID,
			"models": k.Models, "ip_whitelist": k.IPWhitelist,
			"expires_at": unixPtr(k.ExpiresAt), "last_used_at": unixPtr(k.LastUsedAt),
			"created_at": k.CreatedAt.Unix(),
		}
		if k.SubscriptionID != 0 {
			if s, serr := h.store.Subscriptions().GetByID(ctx, k.SubscriptionID); serr == nil && s != nil {
				item["v10_host_id"] = s.V10HostID
			}
		}
		if sum, uerr := h.store.Usage().Summary(ctx, storeUsageFilter(k.ID, from, now)); uerr == nil {
			item["usage_micros_30d"] = sum.CostMicros
			item["requests_30d"] = sum.Requests
		}
		items = append(items, item)
	}
	okw(w, map[string]any{"total": len(items), "items": items})
}

// updateKeyRequest mirrors the PATCH body.
type updateKeyRequest struct {
	Name        *string   `json:"name"`
	Status      *string   `json:"status"`
	Models      *[]string `json:"models"`
	IPWhitelist *[]string `json:"ip_whitelist"`
	ExpiresAt   *int64    `json:"expires_at"`
}

// UpdateKey implements PATCH /internal/v1/keys/{id}.
func (h *Handlers) UpdateKey(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	key, err := h.store.Keys().GetByID(r.Context(), id)
	if err != nil || key == nil {
		failw(w, errs.NotFound("key not found"))
		return
	}
	var req updateKeyRequest
	if derr := decodeBody(r, &req); derr != nil {
		failw(w, errs.InvalidRequest("invalid body: "+derr.Error()))
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
		case domain.KeyRevoked:
			key.Status = domain.KeyRevoked
		default:
			failw(w, errs.InvalidRequest("status must be active, suspended or revoked"))
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
		failw(w, errs.Internal("update key", uerr))
		return
	}
	okw(w, map[string]any{
		"key_id": key.ID, "key_prefix": key.KeyPrefix, "name": key.Name,
		"status": string(key.Status), "subscription_id": key.SubscriptionID,
		"models": key.Models, "ip_whitelist": key.IPWhitelist,
		"expires_at": unixPtr(key.ExpiresAt), "last_used_at": unixPtr(key.LastUsedAt),
		"created_at": key.CreatedAt.Unix(),
	})
}

// DeleteKey implements DELETE /internal/v1/keys/{id}. Idempotent: revoking a
// revoked key still returns 200.
func (h *Handlers) DeleteKey(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	key, err := h.store.Keys().GetByID(r.Context(), id)
	if err != nil || key == nil {
		failw(w, errs.NotFound("key not found"))
		return
	}
	key.Status = domain.KeyRevoked
	key.UpdatedAt = h.clock.Now()
	if uerr := h.store.Keys().Update(r.Context(), key); uerr != nil {
		failw(w, errs.Internal("revoke key", uerr))
		return
	}
	okw(w, map[string]any{"key_id": key.ID, "status": string(key.Status)})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func parseClientID(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	v := chi.URLParam(r, "client_id")
	if v == "" {
		v = r.URL.Query().Get("v10_client_id")
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil || n == 0 {
		failw(w, errs.InvalidRequest("v10_client_id is required"))
		return 0, false
	}
	return n, true
}

func parseUintParam(w http.ResponseWriter, r *http.Request, name string) (uint64, bool) {
	n, err := strconv.ParseUint(chi.URLParam(r, name), 10, 64)
	if err != nil || n == 0 {
		failw(w, errs.InvalidRequest("invalid "+name))
		return 0, false
	}
	return n, true
}

func unixPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Unix()
}
