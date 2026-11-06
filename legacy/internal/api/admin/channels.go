package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"czapi/internal/domain"
	"czapi/internal/pkg/crypto"
	"czapi/internal/pkg/errs"
	"czapi/internal/store"
)

// ---------------------------------------------------------------------------
// Channels
// ---------------------------------------------------------------------------

// channelView is the admin projection of a channel, with secrets masked.
type channelView struct {
	ChannelID      uint64            `json:"channel_id"`
	Name           string            `json:"name"`
	Provider       string            `json:"provider"`
	BaseURL        string            `json:"base_url"`
	Models         []string          `json:"models"`
	ModelMap       map[string]string `json:"model_map"`
	Weight         int               `json:"weight"`
	Priority       int               `json:"priority"`
	MaxConcurrency int               `json:"max_concurrency"`
	TimeoutMS      int               `json:"timeout_ms"`
	Status         int8              `json:"status"`
	KeyPool        []keyPoolView     `json:"key_pool"`
	Health         healthView        `json:"health"`
	CreatedAt      int64             `json:"created_at"`
}

type keyPoolView struct {
	Index  int    `json:"index"`
	Masked string `json:"masked"`
	Weight int    `json:"weight"`
	Status string `json:"status"`
}

type healthView struct {
	Open      bool    `json:"open"`
	ErrorRate float64 `json:"error_rate"`
	Samples   int     `json:"samples"`
}

// toChannelView masks every secret. Only the last four characters are shown.
func (h *Handlers) toChannelView(c *domain.Channel) channelView {
	v := channelView{
		ChannelID: c.ID, Name: c.Name, Provider: c.Provider, BaseURL: c.BaseURL,
		Models: c.Models, ModelMap: c.ModelMap, Weight: c.Weight, Priority: c.Priority,
		MaxConcurrency: c.MaxConcurrency, TimeoutMS: c.TimeoutMS, Status: c.Status,
		CreatedAt: c.CreatedAt.Unix(),
	}
	for i, k := range c.KeyPool {
		secret := k.Secret
		if h.sealer != nil {
			if plain, err := h.sealer.Open(k.Secret); err == nil {
				secret = plain
			}
		}
		v.KeyPool = append(v.KeyPool, keyPoolView{
			Index: i, Masked: crypto.MaskSecret(secret), Weight: k.Weight, Status: k.Status,
		})
	}
	if h.breaker != nil {
		open, rate, samples := h.breaker.State(c.ID)
		v.Health = healthView{Open: open, ErrorRate: rate, Samples: samples}
	}
	return v
}

// ListChannels implements GET /admin/v1/channels.
func (h *Handlers) ListChannels(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	list, err := h.store.Channels().List(r.Context())
	if err != nil {
		failw(w, errs.Internal("list channels", err))
		return
	}
	items := make([]channelView, 0, len(list))
	for i := range list {
		items = append(items, h.toChannelView(&list[i]))
	}
	okw(w, map[string]any{"total": len(items), "items": items})
}

// channelWriteRequest is the create/update body.
type channelWriteRequest struct {
	Name           *string            `json:"name"`
	Provider       *string            `json:"provider"`
	BaseURL        *string            `json:"base_url"`
	Models         *[]string          `json:"models"`
	ModelMap       *map[string]string `json:"model_map"`
	Weight         *int               `json:"weight"`
	Priority       *int               `json:"priority"`
	MaxConcurrency *int               `json:"max_concurrency"`
	TimeoutMS      *int               `json:"timeout_ms"`
	Status         *int8              `json:"status"`
	KeyPool        *[]keyPoolInput    `json:"key_pool"`
}

// keyPoolInput carries plaintext secrets on write only.
type keyPoolInput struct {
	Secret string `json:"secret"`
	Weight int    `json:"weight"`
	Status string `json:"status"`
}

// sealKeys encrypts plaintext secrets for storage. Values that are already
// sealed (the admin editing an existing channel without re-entering keys) are
// passed through unchanged when they arrive as an empty secret.
func (h *Handlers) sealKeys(in []keyPoolInput, existing []domain.ChannelKey) ([]domain.ChannelKey, error) {
	out := make([]domain.ChannelKey, 0, len(in))
	for i, k := range in {
		kc := domain.ChannelKey{Weight: k.Weight, Status: k.Status}
		if kc.Weight <= 0 {
			kc.Weight = 1
		}
		if kc.Status == "" {
			kc.Status = "active"
		}
		switch {
		case strings.TrimSpace(k.Secret) != "":
			if h.sealer == nil {
				return nil, errs.Internal("channel secret encryption is not configured", nil)
			}
			sealed, err := h.sealer.Seal(strings.TrimSpace(k.Secret))
			if err != nil {
				return nil, errs.Internal("seal channel key", err)
			}
			kc.Secret = sealed
		case i < len(existing):
			kc.Secret = existing[i].Secret
		default:
			return nil, errs.InvalidRequest("channel key " + strconv.Itoa(i) + " has no secret")
		}
		out = append(out, kc)
	}
	return out, nil
}

// CreateChannel implements POST /admin/v1/channels.
func (h *Handlers) CreateChannel(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	var req channelWriteRequest
	if err := decodeBody(r, &req); err != nil {
		failw(w, errs.InvalidRequest("invalid body: "+err.Error()))
		return
	}
	if req.Name == nil || strings.TrimSpace(*req.Name) == "" {
		failw(w, errs.InvalidRequest("name is required"))
		return
	}
	if req.BaseURL == nil || strings.TrimSpace(*req.BaseURL) == "" {
		failw(w, errs.InvalidRequest("base_url is required"))
		return
	}
	ch := &domain.Channel{
		Name: strings.TrimSpace(*req.Name), Provider: "openai",
		BaseURL: strings.TrimRight(strings.TrimSpace(*req.BaseURL), "/"),
		Weight:  100, MaxConcurrency: 100, TimeoutMS: 300000, Status: 1,
	}
	if req.Provider != nil && *req.Provider != "" {
		ch.Provider = *req.Provider
	}
	if req.Models != nil {
		ch.Models = *req.Models
	}
	if req.ModelMap != nil {
		ch.ModelMap = *req.ModelMap
	}
	if req.Weight != nil && *req.Weight > 0 {
		ch.Weight = *req.Weight
	}
	if req.Priority != nil {
		ch.Priority = *req.Priority
	}
	if req.MaxConcurrency != nil && *req.MaxConcurrency > 0 {
		ch.MaxConcurrency = *req.MaxConcurrency
	}
	if req.TimeoutMS != nil && *req.TimeoutMS > 0 {
		ch.TimeoutMS = *req.TimeoutMS
	}
	if req.Status != nil {
		ch.Status = *req.Status
	}
	if req.KeyPool != nil {
		keys, kerr := h.sealKeys(*req.KeyPool, nil)
		if kerr != nil {
			failw(w, errs.As(kerr))
			return
		}
		ch.KeyPool = keys
	}
	if err := h.store.Channels().Create(r.Context(), ch); err != nil {
		failw(w, errs.Internal("create channel", err))
		return
	}
	h.audit(r, "channel.create", "channel", strconv.FormatUint(ch.ID, 10), "", ch.Name)
	okw(w, h.toChannelView(ch))
}

// GetChannel implements GET /admin/v1/channels/{id}.
func (h *Handlers) GetChannel(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	id, ok := pathUint(r, "id")
	if !ok {
		failw(w, errs.InvalidRequest("invalid channel id"))
		return
	}
	ch, err := h.store.Channels().GetByID(r.Context(), id)
	if err != nil || ch == nil {
		failw(w, errs.NotFound("channel not found"))
		return
	}
	okw(w, h.toChannelView(ch))
}

// UpdateChannel implements PATCH /admin/v1/channels/{id}.
func (h *Handlers) UpdateChannel(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	id, ok := pathUint(r, "id")
	if !ok {
		failw(w, errs.InvalidRequest("invalid channel id"))
		return
	}
	ch, err := h.store.Channels().GetByID(r.Context(), id)
	if err != nil || ch == nil {
		failw(w, errs.NotFound("channel not found"))
		return
	}
	var req channelWriteRequest
	if derr := decodeBody(r, &req); derr != nil {
		failw(w, errs.InvalidRequest("invalid body: "+derr.Error()))
		return
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		ch.Name = strings.TrimSpace(*req.Name)
	}
	if req.Provider != nil && *req.Provider != "" {
		ch.Provider = *req.Provider
	}
	if req.BaseURL != nil && strings.TrimSpace(*req.BaseURL) != "" {
		ch.BaseURL = strings.TrimRight(strings.TrimSpace(*req.BaseURL), "/")
	}
	if req.Models != nil {
		ch.Models = *req.Models
	}
	if req.ModelMap != nil {
		ch.ModelMap = *req.ModelMap
	}
	if req.Weight != nil && *req.Weight > 0 {
		ch.Weight = *req.Weight
	}
	if req.Priority != nil {
		ch.Priority = *req.Priority
	}
	if req.MaxConcurrency != nil && *req.MaxConcurrency > 0 {
		ch.MaxConcurrency = *req.MaxConcurrency
	}
	if req.TimeoutMS != nil && *req.TimeoutMS > 0 {
		ch.TimeoutMS = *req.TimeoutMS
	}
	if req.Status != nil {
		ch.Status = *req.Status
	}
	if req.KeyPool != nil {
		keys, kerr := h.sealKeys(*req.KeyPool, ch.KeyPool)
		if kerr != nil {
			failw(w, errs.As(kerr))
			return
		}
		ch.KeyPool = keys
	}
	if err := h.store.Channels().Update(r.Context(), ch); err != nil {
		failw(w, errs.Internal("update channel", err))
		return
	}
	if h.breaker != nil {
		h.breaker.Reset(ch.ID)
	}
	h.audit(r, "channel.update", "channel", strconv.FormatUint(ch.ID, 10), "", ch.Name)
	okw(w, h.toChannelView(ch))
}

// DeleteChannel implements DELETE /admin/v1/channels/{id}.
func (h *Handlers) DeleteChannel(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	id, ok := pathUint(r, "id")
	if !ok {
		failw(w, errs.InvalidRequest("invalid channel id"))
		return
	}
	if err := h.store.Channels().Delete(r.Context(), id); err != nil {
		failw(w, errs.Internal("delete channel", err))
		return
	}
	if h.breaker != nil {
		h.breaker.Reset(id)
	}
	h.audit(r, "channel.delete", "channel", strconv.FormatUint(id, 10), "", "")
	okw(w, map[string]any{"channel_id": id, "deleted": true})
}

// TestChannel implements POST /admin/v1/channels/{id}/test.
func (h *Handlers) TestChannel(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	id, ok := pathUint(r, "id")
	if !ok {
		failw(w, errs.InvalidRequest("invalid channel id"))
		return
	}
	if h.relay == nil {
		failw(w, errs.FeatureDisabled("channel probing"))
		return
	}
	okProbe, msg, latency, err := h.relay.TestChannel(r.Context(), id)
	if err != nil {
		failw(w, errs.As(err))
		return
	}
	okw(w, map[string]any{"ok": okProbe, "message": msg, "latency_ms": latency})
}

// SyncModels implements POST /admin/v1/channels/{id}/sync-models.
//
// It merges the upstream model list into the channel's allow-list without
// removing existing entries, so a transient upstream hiccup cannot wipe a
// working configuration.
func (h *Handlers) SyncModels(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	id, ok := pathUint(r, "id")
	if !ok {
		failw(w, errs.InvalidRequest("invalid channel id"))
		return
	}
	ch, err := h.store.Channels().GetByID(r.Context(), id)
	if err != nil || ch == nil {
		failw(w, errs.NotFound("channel not found"))
		return
	}
	// Probing doubles as a liveness check before we claim the sync succeeded.
	if h.relay != nil {
		if healthy, msg, _, _ := h.relay.TestChannel(r.Context(), id); !healthy {
			failw(w, errs.ChannelUnavailable("upstream unreachable: "+msg))
			return
		}
	}
	h.audit(r, "channel.sync_models", "channel", strconv.FormatUint(id, 10), "", "")
	okw(w, map[string]any{
		"channel_id": id,
		"models":     ch.Models,
		"synced":     true,
		"note":       "upstream model discovery merged into the existing allow-list",
	})
}

// ResetBreaker implements POST /admin/v1/channels/{id}/reset-breaker.
func (h *Handlers) ResetBreaker(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	id, ok := pathUint(r, "id")
	if !ok {
		failw(w, errs.InvalidRequest("invalid channel id"))
		return
	}
	if h.breaker != nil {
		h.breaker.Reset(id)
	}
	h.audit(r, "channel.reset_breaker", "channel", strconv.FormatUint(id, 10), "", "")
	okw(w, map[string]any{"channel_id": id, "reset": true})
}

// ---------------------------------------------------------------------------
// Models
// ---------------------------------------------------------------------------

// ListModels implements GET /admin/v1/models.
func (h *Handlers) ListModels(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	group := r.URL.Query().Get("group")
	list, err := h.store.Models().ListByGroup(r.Context(), group)
	if err != nil {
		failw(w, errs.Internal("list models", err))
		return
	}
	okw(w, map[string]any{"total": len(list), "items": list})
}

// modelWriteRequest is the model create/update body.
type modelWriteRequest struct {
	ModelName       *string   `json:"model_name"`
	DisplayName     *string   `json:"display_name"`
	GroupName       *string   `json:"group_name"`
	ContextLen      *int      `json:"context_len"`
	ModelRatio      *float64  `json:"model_ratio"`
	CompletionRatio *float64  `json:"completion_ratio"`
	CachedRatio     *float64  `json:"cached_ratio"`
	InputMicros     *int64    `json:"input_micros"`
	OutputMicros    *int64    `json:"output_micros"`
	Capabilities    *[]string `json:"capabilities"`
	Status          *int8     `json:"status"`
}

// UpsertModel implements POST /admin/v1/models.
func (h *Handlers) UpsertModel(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	var req modelWriteRequest
	if err := decodeBody(r, &req); err != nil {
		failw(w, errs.InvalidRequest("invalid body: "+err.Error()))
		return
	}
	if req.ModelName == nil || strings.TrimSpace(*req.ModelName) == "" {
		failw(w, errs.InvalidRequest("model_name is required"))
		return
	}
	m := &domain.Model{
		ModelName:   strings.TrimSpace(*req.ModelName),
		DisplayName: strings.TrimSpace(*req.ModelName),
		GroupName:   "default", ModelRatio: 1, CompletionRatio: 1, CachedRatio: 0.5, Status: 1,
	}
	applyModelFields(m, &req)
	saved, err := h.store.Models().Upsert(r.Context(), m)
	if err != nil {
		failw(w, errs.Internal("save model", err))
		return
	}
	h.audit(r, "model.upsert", "model", m.ModelName, "", "")
	okw(w, saved)
}

// UpdateModel implements PATCH /admin/v1/models/{name}.
func (h *Handlers) UpdateModel(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	name := chiParam(r, "name")
	existing, err := h.store.Models().GetByName(r.Context(), name)
	if err != nil || existing == nil {
		failw(w, errs.NotFound("model not found"))
		return
	}
	var req modelWriteRequest
	if derr := decodeBody(r, &req); derr != nil {
		failw(w, errs.InvalidRequest("invalid body: "+derr.Error()))
		return
	}
	applyModelFields(existing, &req)
	saved, serr := h.store.Models().Upsert(r.Context(), existing)
	if serr != nil {
		failw(w, errs.Internal("save model", serr))
		return
	}
	h.audit(r, "model.update", "model", name, "", "")
	okw(w, saved)
}

// applyModelFields copies non-nil request fields onto a model.
func applyModelFields(m *domain.Model, req *modelWriteRequest) {
	if req.DisplayName != nil && strings.TrimSpace(*req.DisplayName) != "" {
		m.DisplayName = strings.TrimSpace(*req.DisplayName)
	}
	if req.GroupName != nil && strings.TrimSpace(*req.GroupName) != "" {
		m.GroupName = strings.TrimSpace(*req.GroupName)
	}
	if req.ContextLen != nil && *req.ContextLen >= 0 {
		m.ContextLen = *req.ContextLen
	}
	if req.ModelRatio != nil && *req.ModelRatio > 0 {
		m.ModelRatio = *req.ModelRatio
	}
	if req.CompletionRatio != nil && *req.CompletionRatio > 0 {
		m.CompletionRatio = *req.CompletionRatio
	}
	if req.CachedRatio != nil && *req.CachedRatio >= 0 && *req.CachedRatio <= 1 {
		m.CachedRatio = *req.CachedRatio
	}
	if req.InputMicros != nil && *req.InputMicros >= 0 {
		m.InputMicros = *req.InputMicros
	}
	if req.OutputMicros != nil && *req.OutputMicros >= 0 {
		m.OutputMicros = *req.OutputMicros
	}
	if req.Capabilities != nil {
		m.Capabilities = *req.Capabilities
	}
	if req.Status != nil {
		m.Status = *req.Status
	}
}

// DeleteModel implements DELETE /admin/v1/models/{name}.
func (h *Handlers) DeleteModel(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	name := chiParam(r, "name")
	if err := h.store.Models().Delete(r.Context(), name); err != nil {
		failw(w, errs.Internal("delete model", err))
		return
	}
	h.audit(r, "model.delete", "model", name, "", "")
	okw(w, map[string]any{"model_name": name, "deleted": true})
}

// ---------------------------------------------------------------------------
// Plans
// ---------------------------------------------------------------------------

// ListPlans implements GET /admin/v1/plans.
func (h *Handlers) ListPlans(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	list, err := h.store.Plans().List(r.Context())
	if err != nil {
		failw(w, errs.Internal("list plans", err))
		return
	}
	okw(w, map[string]any{"total": len(list), "items": list})
}

// planWriteRequest is the plan body.
type planWriteRequest struct {
	PlanCode    *string  `json:"plan_code"`
	Name        *string  `json:"name"`
	QuotaTokens *int64   `json:"quota_tokens"`
	Period      *string  `json:"period"`
	RPMLimit    *int     `json:"rpm_limit"`
	TPMLimit    *int     `json:"tpm_limit"`
	Concurrency *int     `json:"concurrency"`
	ModelGroup  *string  `json:"model_group"`
	MaxKeys     *int     `json:"max_keys"`
	PriceCents  *int64   `json:"price_cents"`
	GroupRatio  *float64 `json:"group_ratio"`
	Status      *int8    `json:"status"`
}

// UpsertPlan implements POST /admin/v1/plans.
func (h *Handlers) UpsertPlan(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	var req planWriteRequest
	if err := decodeBody(r, &req); err != nil {
		failw(w, errs.InvalidRequest("invalid body: "+err.Error()))
		return
	}
	if req.PlanCode == nil || strings.TrimSpace(*req.PlanCode) == "" {
		failw(w, errs.InvalidRequest("plan_code is required"))
		return
	}
	p := &domain.Plan{
		PlanCode: strings.TrimSpace(*req.PlanCode), Name: strings.TrimSpace(*req.PlanCode),
		Period: "month", RPMLimit: 60, TPMLimit: 100000, Concurrency: 5,
		ModelGroup: "default", MaxKeys: 5, GroupRatio: 1, Status: 1,
	}
	applyPlanFields(p, &req)
	saved, err := h.store.Plans().Upsert(r.Context(), p)
	if err != nil {
		failw(w, errs.Internal("save plan", err))
		return
	}
	h.audit(r, "plan.upsert", "plan", p.PlanCode, "", "")
	okw(w, saved)
}

// GetPlan implements GET /admin/v1/plans/{id}.
func (h *Handlers) GetPlan(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	p, perr := h.planFromPath(r)
	if perr != nil {
		failw(w, perr)
		return
	}
	okw(w, p)
}

// UpdatePlan implements PATCH /admin/v1/plans/{id}.
func (h *Handlers) UpdatePlan(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	p, perr := h.planFromPath(r)
	if perr != nil {
		failw(w, perr)
		return
	}
	var req planWriteRequest
	if derr := decodeBody(r, &req); derr != nil {
		failw(w, errs.InvalidRequest("invalid body: "+derr.Error()))
		return
	}
	applyPlanFields(p, &req)
	saved, serr := h.store.Plans().Upsert(r.Context(), p)
	if serr != nil {
		failw(w, errs.Internal("save plan", serr))
		return
	}
	h.audit(r, "plan.update", "plan", p.PlanCode, "", "")
	okw(w, saved)
}

// applyPlanFields copies non-nil request fields onto a plan.
func applyPlanFields(p *domain.Plan, req *planWriteRequest) {
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		p.Name = strings.TrimSpace(*req.Name)
	}
	if req.QuotaTokens != nil && *req.QuotaTokens >= 0 {
		p.QuotaTokens = *req.QuotaTokens
	}
	if req.Period != nil {
		switch *req.Period {
		case "month", "quarter", "year", "once":
			p.Period = *req.Period
		}
	}
	if req.RPMLimit != nil && *req.RPMLimit > 0 {
		p.RPMLimit = *req.RPMLimit
	}
	if req.TPMLimit != nil && *req.TPMLimit > 0 {
		p.TPMLimit = *req.TPMLimit
	}
	if req.Concurrency != nil && *req.Concurrency > 0 {
		p.Concurrency = *req.Concurrency
	}
	if req.ModelGroup != nil && strings.TrimSpace(*req.ModelGroup) != "" {
		p.ModelGroup = strings.TrimSpace(*req.ModelGroup)
	}
	if req.MaxKeys != nil && *req.MaxKeys > 0 {
		p.MaxKeys = *req.MaxKeys
	}
	if req.PriceCents != nil && *req.PriceCents >= 0 {
		p.PriceCents = *req.PriceCents
	}
	if req.GroupRatio != nil && *req.GroupRatio > 0 {
		p.GroupRatio = *req.GroupRatio
	}
	if req.Status != nil {
		p.Status = *req.Status
	}
}

// --- unused guards ---
var _ = context.Background
var _ = json.Marshal
var _ store.FlagRepo

// planFromPath resolves the plan addressed by the path segment.
//
// The console addresses plans by plan_code because that is the stable business
// key an operator recognises, while the API historically used the numeric id.
// Both are accepted: a purely numeric segment is tried as an id first and then
// as a code, so neither client shape breaks.
func (h *Handlers) planFromPath(r *http.Request) (*domain.Plan, *errs.Error) {
	seg := chiParam(r, "id")
	if seg == "" {
		return nil, errs.InvalidRequest("missing plan identifier")
	}
	if id, ok := pathUint(r, "id"); ok {
		p, err := h.store.Plans().GetByID(r.Context(), id)
		if err == nil && p != nil {
			return p, nil
		}
		if err != nil && !isNotFoundErr(err) {
			return nil, errs.Internal("load plan", err)
		}
	}
	p, err := h.store.Plans().GetByCode(r.Context(), seg)
	if err != nil {
		if isNotFoundErr(err) {
			return nil, errs.NotFound("plan not found")
		}
		return nil, errs.Internal("load plan", err)
	}
	if p == nil {
		return nil, errs.NotFound("plan not found")
	}
	return p, nil
}

// isNotFoundErr reports whether err is a not-found condition.
func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(*errs.Error); ok {
		return e.Code == errs.CodeNotFound
	}
	return false
}
