package memstore

import (
	"context"
	"sort"
	"time"

	"czapi/internal/domain"
	"czapi/internal/pkg/errs"
)

// subscriptionRepo implements store.SubscriptionRepo on top of Store.
type subscriptionRepo struct{ s *Store }

// Create stores a subscription. It is idempotent on V10HostID: an existing row
// is returned unchanged with created=false.
func (r subscriptionRepo) Create(ctx context.Context, v *domain.Subscription) (*domain.Subscription, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if v == nil || v.V10HostID == 0 {
		return nil, false, errs.InvalidRequest("subscription v10_host_id is required")
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()

	if id, ok := r.s.subByHost[v.V10HostID]; ok {
		return copySubscription(r.s.subscriptions[id]), false, nil
	}
	now := r.s.now()
	out := copySubscription(v)
	out.ID = r.s.nextID(kindSubscription)
	if out.Status == "" {
		out.Status = domain.SubActive
	}
	if out.BillingMode == "" {
		out.BillingMode = domain.BillingHybrid
	}
	if out.CreatedAt.IsZero() {
		out.CreatedAt = now
	}
	out.UpdatedAt = now
	r.s.subscriptions[out.ID] = out
	r.s.subOrder = append(r.s.subOrder, out.ID)
	r.s.subByHost[out.V10HostID] = out.ID
	return copySubscription(out), true, nil
}

// GetByID returns the subscription with the given id.
func (r subscriptionRepo) GetByID(ctx context.Context, id uint64) (*domain.Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	v, ok := r.s.subscriptions[id]
	if !ok {
		return nil, errs.NotFound("subscription not found")
	}
	return copySubscription(v), nil
}

// GetByV10Host returns the subscription mirroring a V10 host.
func (r subscriptionRepo) GetByV10Host(ctx context.Context, hostID uint64) (*domain.Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	id, ok := r.s.subByHost[hostID]
	if !ok {
		return nil, errs.NotFound("subscription not found")
	}
	return copySubscription(r.s.subscriptions[id]), nil
}

// ListByAccount returns an account's subscriptions, soonest expiry first.
func (r subscriptionRepo) ListByAccount(ctx context.Context, accountID uint64) ([]domain.Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	out := make([]domain.Subscription, 0, 4)
	for _, id := range r.s.subOrder {
		v, ok := r.s.subscriptions[id]
		if !ok || v.AccountID != accountID {
			continue
		}
		out = append(out, *copySubscription(v))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].PeriodEnd.Before(out[j].PeriodEnd)
	})
	return out, nil
}

// Update replaces an existing subscription, matched by id.
func (r subscriptionRepo) Update(ctx context.Context, v *domain.Subscription) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if v == nil || v.ID == 0 {
		return errs.InvalidRequest("subscription id is required")
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()
	cur, ok := r.s.subscriptions[v.ID]
	if !ok {
		return errs.NotFound("subscription not found")
	}
	out := copySubscription(v)
	out.CreatedAt = cur.CreatedAt
	out.UpdatedAt = r.s.now()
	r.s.subscriptions[out.ID] = out
	r.s.subByHost[out.V10HostID] = out.ID
	return nil
}

// AddUsage atomically adds tokens to quota_used and returns the new value.
func (r subscriptionRepo) AddUsage(ctx context.Context, id uint64, tokens int64) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()
	v, ok := r.s.subscriptions[id]
	if !ok {
		return 0, errs.NotFound("subscription not found")
	}
	v.QuotaUsed += tokens
	v.UpdatedAt = r.s.now()
	return v.QuotaUsed, nil
}

// ResetPeriod sets a new period, replaces the quota total and clears usage.
func (r subscriptionRepo) ResetPeriod(ctx context.Context, id uint64, start, end time.Time, quotaTotal int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()
	v, ok := r.s.subscriptions[id]
	if !ok {
		return errs.NotFound("subscription not found")
	}
	v.PeriodStart = start
	v.PeriodEnd = end
	v.QuotaTotal = quotaTotal
	v.QuotaUsed = 0
	v.UpdatedAt = r.s.now()
	return nil
}

// ListExpired returns active subscriptions whose period_end is at or before at.
func (r subscriptionRepo) ListExpired(ctx context.Context, at time.Time) ([]domain.Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	out := make([]domain.Subscription, 0, 4)
	for _, id := range r.s.subOrder {
		v, ok := r.s.subscriptions[id]
		if !ok || v.Status != domain.SubActive {
			continue
		}
		if v.PeriodEnd.IsZero() || v.PeriodEnd.After(at) {
			continue
		}
		out = append(out, *copySubscription(v))
	}
	return out, nil
}

// apiKeyRepo implements store.APIKeyRepo on top of Store.
type apiKeyRepo struct{ s *Store }

// Create stores a new API key and assigns its id.
func (r apiKeyRepo) Create(ctx context.Context, k *domain.APIKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if k == nil || k.KeyHash == "" {
		return errs.InvalidRequest("key hash is required")
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()
	if _, ok := r.s.keyByHash[k.KeyHash]; ok {
		return errs.Conflict("api key already exists")
	}
	now := r.s.now()
	out := copyAPIKey(k)
	out.ID = r.s.nextID(kindAPIKey)
	if out.Status == "" {
		out.Status = domain.KeyActive
	}
	if out.CreatedAt.IsZero() {
		out.CreatedAt = now
	}
	out.UpdatedAt = now
	r.s.keys[out.ID] = out
	r.s.keyOrder = append(r.s.keyOrder, out.ID)
	r.s.keyByHash[out.KeyHash] = out.ID
	k.ID = out.ID
	k.Status = out.Status
	k.CreatedAt = out.CreatedAt
	k.UpdatedAt = out.UpdatedAt
	return nil
}

// GetByID returns the API key with the given id.
func (r apiKeyRepo) GetByID(ctx context.Context, id uint64) (*domain.APIKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	k, ok := r.s.keys[id]
	if !ok {
		return nil, errs.NotFound("api key not found")
	}
	return copyAPIKey(k), nil
}

// GetByHash returns the API key with the given lookup hash.
func (r apiKeyRepo) GetByHash(ctx context.Context, keyHash string) (*domain.APIKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	id, ok := r.s.keyByHash[keyHash]
	if !ok {
		return nil, errs.NotFound("api key not found")
	}
	return copyAPIKey(r.s.keys[id]), nil
}

// Update replaces an existing API key, matched by id.
func (r apiKeyRepo) Update(ctx context.Context, k *domain.APIKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if k == nil || k.ID == 0 {
		return errs.InvalidRequest("api key id is required")
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()
	cur, ok := r.s.keys[k.ID]
	if !ok {
		return errs.NotFound("api key not found")
	}
	out := copyAPIKey(k)
	out.CreatedAt = cur.CreatedAt
	out.UpdatedAt = r.s.now()
	if out.Status == "" {
		out.Status = cur.Status
	}
	delete(r.s.keyByHash, cur.KeyHash)
	r.s.keys[out.ID] = out
	r.s.keyByHash[out.KeyHash] = out.ID
	return nil
}

// ListByAccount returns an account's keys, newest creation first.
func (r apiKeyRepo) ListByAccount(ctx context.Context, accountID uint64) ([]domain.APIKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	out := make([]domain.APIKey, 0, 8)
	for i := len(r.s.keyOrder) - 1; i >= 0; i-- {
		k, ok := r.s.keys[r.s.keyOrder[i]]
		if !ok || k.AccountID != accountID {
			continue
		}
		out = append(out, *copyAPIKey(k))
	}
	return out, nil
}

// ListBySubscription returns the keys bound to a subscription.
func (r apiKeyRepo) ListBySubscription(ctx context.Context, subscriptionID uint64) ([]domain.APIKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	out := make([]domain.APIKey, 0, 8)
	for _, id := range r.s.keyOrder {
		k, ok := r.s.keys[id]
		if !ok || k.SubscriptionID != subscriptionID {
			continue
		}
		out = append(out, *copyAPIKey(k))
	}
	return out, nil
}

// CountBySubscription returns how many non-revoked keys a subscription owns.
func (r apiKeyRepo) CountBySubscription(ctx context.Context, subscriptionID uint64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	n := 0
	for _, k := range r.s.keys {
		if k.SubscriptionID == subscriptionID && k.Status != domain.KeyRevoked {
			n++
		}
	}
	return n, nil
}

// RevokeBySubscription revokes every non-revoked key of a subscription and
// returns the count of keys flipped.
func (r apiKeyRepo) RevokeBySubscription(ctx context.Context, subscriptionID uint64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()
	now := r.s.now()
	n := 0
	for _, id := range r.s.keyOrder {
		k, ok := r.s.keys[id]
		if !ok || k.SubscriptionID != subscriptionID {
			continue
		}
		if k.Status == domain.KeyRevoked {
			continue
		}
		k.Status = domain.KeyRevoked
		k.UpdatedAt = now
		n++
	}
	return n, nil
}

// SetStatusBySubscription flips every non-revoked key of a subscription.
func (r apiKeyRepo) SetStatusBySubscription(ctx context.Context, subscriptionID uint64, status domain.APIKeyStatus) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if status == "" {
		return 0, errs.InvalidRequest("api key status is required")
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()
	now := r.s.now()
	n := 0
	for _, id := range r.s.keyOrder {
		k, ok := r.s.keys[id]
		if !ok || k.SubscriptionID != subscriptionID {
			continue
		}
		if k.Status == domain.KeyRevoked || k.Status == status {
			continue
		}
		k.Status = status
		k.UpdatedAt = now
		n++
	}
	return n, nil
}

// TouchLastUsed records the last usage timestamp (best effort).
func (r apiKeyRepo) TouchLastUsed(ctx context.Context, id uint64, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()
	k, ok := r.s.keys[id]
	if !ok {
		return nil // best effort: a missing key is not an error
	}
	ts := at
	k.LastUsedAt = &ts
	return nil
}

// Delete removes an API key.
func (r apiKeyRepo) Delete(ctx context.Context, id uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()
	k, ok := r.s.keys[id]
	if !ok {
		return errs.NotFound("api key not found")
	}
	delete(r.s.keyByHash, k.KeyHash)
	delete(r.s.keys, id)
	for i, v := range r.s.keyOrder {
		if v == id {
			r.s.keyOrder = append(r.s.keyOrder[:i], r.s.keyOrder[i+1:]...)
			break
		}
	}
	return nil
}
