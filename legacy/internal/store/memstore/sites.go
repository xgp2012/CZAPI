package memstore

import (
	"context"
	"time"

	"czapi/internal/domain"
	"czapi/internal/pkg/errs"
)

// siteRepo implements store.SiteRepo on top of Store.
type siteRepo struct{ s *Store }

// Create stores a new site and assigns its id.
func (r siteRepo) Create(ctx context.Context, v *domain.Site) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if v == nil || v.AppID == "" {
		return errs.InvalidRequest("site app_id is required")
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()
	if _, ok := r.s.sites[v.AppID]; ok {
		return errs.Conflict("site " + v.AppID + " already exists")
	}
	now := r.s.now()
	out := copySite(v)
	out.ID = r.s.nextID(kindSite)
	if out.CreatedAt.IsZero() {
		out.CreatedAt = now
	}
	out.UpdatedAt = now
	r.s.sites[out.AppID] = out
	r.s.siteOrder = append(r.s.siteOrder, out.AppID)
	v.ID = out.ID
	v.CreatedAt = out.CreatedAt
	v.UpdatedAt = out.UpdatedAt
	return nil
}

// Update replaces an existing site, matched by id.
func (r siteRepo) Update(ctx context.Context, v *domain.Site) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if v == nil || v.AppID == "" {
		return errs.InvalidRequest("site app_id is required")
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()
	cur, ok := r.s.sites[v.AppID]
	if !ok || (v.ID != 0 && cur.ID != v.ID) {
		return errs.NotFound("site " + v.AppID)
	}
	out := copySite(v)
	out.ID = cur.ID
	out.CreatedAt = cur.CreatedAt
	out.UpdatedAt = r.s.now()
	r.s.sites[out.AppID] = out
	return nil
}

// GetByAppID returns the site registered under appID.
func (r siteRepo) GetByAppID(ctx context.Context, appID string) (*domain.Site, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	v, ok := r.s.sites[appID]
	if !ok {
		return nil, errs.NotFound("site " + appID)
	}
	return copySite(v), nil
}

// List returns every registered site in creation order.
func (r siteRepo) List(ctx context.Context) ([]domain.Site, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	out := make([]domain.Site, 0, len(r.s.siteOrder))
	for _, appID := range r.s.siteOrder {
		if v, ok := r.s.sites[appID]; ok {
			out = append(out, *copySite(v))
		}
	}
	return out, nil
}

// accountRepo implements store.AccountRepo on top of Store.
type accountRepo struct{ s *Store }

// clientKey builds the (site, client) lookup key.
func clientKey(siteID string, clientID uint64) string {
	return siteID + "\x00" + u64str(clientID)
}

// Upsert creates or updates the account identified by (site_id, v10_client_id).
func (r accountRepo) Upsert(ctx context.Context, a *domain.Account) (*domain.Account, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a == nil || a.SiteID == "" {
		return nil, errs.InvalidRequest("account site_id is required")
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()

	now := r.s.now()
	key := clientKey(a.SiteID, a.V10ClientID)
	if id, ok := r.s.accountByClient[key]; ok {
		cur := r.s.accounts[id]
		out := copyAccount(a)
		out.ID = cur.ID
		out.CreatedAt = cur.CreatedAt
		out.UpdatedAt = now
		r.s.accounts[id] = out
		return copyAccount(out), nil
	}
	out := copyAccount(a)
	out.ID = r.s.nextID(kindAccount)
	out.CreatedAt = now
	out.UpdatedAt = now
	r.s.accounts[out.ID] = out
	r.s.accountOrder = append(r.s.accountOrder, out.ID)
	r.s.accountByClient[key] = out.ID
	return copyAccount(out), nil
}

// GetByID returns the account with the given id.
func (r accountRepo) GetByID(ctx context.Context, id uint64) (*domain.Account, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	a, ok := r.s.accounts[id]
	if !ok {
		return nil, errs.NotFound("account not found")
	}
	return copyAccount(a), nil
}

// GetByV10Client returns the account mirroring a V10 client.
func (r accountRepo) GetByV10Client(ctx context.Context, siteID string, clientID uint64) (*domain.Account, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	id, ok := r.s.accountByClient[clientKey(siteID, clientID)]
	if !ok {
		return nil, errs.NotFound("account not found")
	}
	return copyAccount(r.s.accounts[id]), nil
}

// Update replaces an existing account, matched by id.
func (r accountRepo) Update(ctx context.Context, a *domain.Account) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a == nil || a.ID == 0 {
		return errs.InvalidRequest("account id is required")
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()
	cur, ok := r.s.accounts[a.ID]
	if !ok {
		return errs.NotFound("account not found")
	}
	out := copyAccount(a)
	out.CreatedAt = cur.CreatedAt
	out.UpdatedAt = r.s.now()
	r.s.accounts[out.ID] = out
	r.s.accountByClient[clientKey(out.SiteID, out.V10ClientID)] = out.ID
	return nil
}

// List returns accounts in creation order, paginated. A non-positive limit
// returns every remaining row.
func (r accountRepo) List(ctx context.Context, offset, limit int) ([]domain.Account, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	out := make([]domain.Account, 0, len(r.s.accountOrder))
	for i, id := range r.s.accountOrder {
		if i < offset {
			continue
		}
		if limit > 0 && len(out) >= limit {
			break
		}
		if a, ok := r.s.accounts[id]; ok {
			out = append(out, *copyAccount(a))
		}
	}
	return out, nil
}

// Count returns the number of accounts.
func (r accountRepo) Count(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	return int64(len(r.s.accounts)), nil
}

// planRepo implements store.PlanRepo on top of Store.
type planRepo struct{ s *Store }

// Upsert creates or updates a plan, matched by id when set and by plan_code
// otherwise.
func (r planRepo) Upsert(ctx context.Context, p *domain.Plan) (*domain.Plan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil || p.PlanCode == "" {
		return nil, errs.InvalidRequest("plan_code is required")
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()

	now := r.s.now()
	out := copyPlan(p)
	if p.ID != 0 {
		if cur, ok := r.s.plans[p.ID]; ok {
			out.CreatedAt = cur.CreatedAt
			out.UpdatedAt = now
			r.s.plans[out.ID] = out
			r.s.planByCode[out.PlanCode] = out.ID
			return copyPlan(out), nil
		}
	}
	if id, ok := r.s.planByCode[p.PlanCode]; ok {
		cur := r.s.plans[id]
		out.ID = cur.ID
		out.CreatedAt = cur.CreatedAt
		out.UpdatedAt = now
		r.s.plans[out.ID] = out
		return copyPlan(out), nil
	}
	out.ID = r.s.nextID(kindPlan)
	out.CreatedAt = now
	out.UpdatedAt = now
	r.s.plans[out.ID] = out
	r.s.planOrder = append(r.s.planOrder, out.ID)
	r.s.planByCode[out.PlanCode] = out.ID
	return copyPlan(out), nil
}

// GetByID returns the plan with the given id.
func (r planRepo) GetByID(ctx context.Context, id uint64) (*domain.Plan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	p, ok := r.s.plans[id]
	if !ok {
		return nil, errs.NotFound("plan not found")
	}
	return copyPlan(p), nil
}

// GetByCode returns the plan with the given plan_code.
func (r planRepo) GetByCode(ctx context.Context, code string) (*domain.Plan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	id, ok := r.s.planByCode[code]
	if !ok {
		return nil, errs.NotFound("plan " + code)
	}
	return copyPlan(r.s.plans[id]), nil
}

// List returns every plan in creation order.
func (r planRepo) List(ctx context.Context) ([]domain.Plan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	out := make([]domain.Plan, 0, len(r.s.planOrder))
	for _, id := range r.s.planOrder {
		if p, ok := r.s.plans[id]; ok {
			out = append(out, *copyPlan(p))
		}
	}
	return out, nil
}

// u64str renders v in decimal without importing strconv at call sites.
func u64str(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// dayOf renders t as YYYY-MM-DD in t's own location; the day boundaries used by
// the daily rollups follow whatever location the caller passes in.
func dayOf(t time.Time) string { return t.Format("2006-01-02") }
