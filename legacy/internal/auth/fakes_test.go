package auth

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"czapi/internal/cache"
	"czapi/internal/domain"
	"czapi/internal/pkg/errs"
	"czapi/internal/store"
)

// ---------------------------------------------------------------------------
// Narrow reader fakes
// ---------------------------------------------------------------------------

// fakeKeys is an in-memory APIKeyReader.
type fakeKeys struct {
	mu sync.Mutex
	// byHash maps salted key hashes to keys.
	byHash map[string]*domain.APIKey
	// byID maps key ids to keys.
	byID map[uint64]*domain.APIKey

	// notFoundAsError makes GetByHash/GetByID return errs.NotFound instead of a
	// nil record, exercising both driver conventions.
	notFoundAsError bool

	// counters for the cache-hit assertions.
	getByHashCalls int
	touches        []uint64
	touchErr       error
}

func newFakeKeys() *fakeKeys {
	return &fakeKeys{byHash: map[string]*domain.APIKey{}, byID: map[uint64]*domain.APIKey{}}
}

func (f *fakeKeys) add(k *domain.APIKey) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byHash[k.KeyHash] = k
	f.byID[k.ID] = k
}

func (f *fakeKeys) GetByHash(_ context.Context, keyHash string) (*domain.APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getByHashCalls++
	if k, ok := f.byHash[keyHash]; ok {
		return k, nil
	}
	if f.notFoundAsError {
		return nil, errs.NotFound("key not found")
	}
	return nil, nil
}

func (f *fakeKeys) GetByID(_ context.Context, id uint64) (*domain.APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if k, ok := f.byID[id]; ok {
		return k, nil
	}
	if f.notFoundAsError {
		return nil, errs.NotFound("key not found")
	}
	return nil, nil
}

func (f *fakeKeys) TouchLastUsed(_ context.Context, id uint64, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touches = append(f.touches, id)
	return f.touchErr
}

func (f *fakeKeys) hashCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getByHashCalls
}

// fakeAccounts is an in-memory AccountReader.
type fakeAccounts struct {
	byID map[uint64]*domain.Account
	err  error
}

func (f *fakeAccounts) GetByID(_ context.Context, id uint64) (*domain.Account, error) {
	if f.err != nil {
		return nil, f.err
	}
	if a, ok := f.byID[id]; ok {
		return a, nil
	}
	return nil, nil
}

// fakeSubscriptions is an in-memory SubscriptionReader.
type fakeSubscriptions struct {
	byID map[uint64]*domain.Subscription
	err  error
}

func (f *fakeSubscriptions) GetByID(_ context.Context, id uint64) (*domain.Subscription, error) {
	if f.err != nil {
		return nil, f.err
	}
	if s, ok := f.byID[id]; ok {
		return s, nil
	}
	return nil, nil
}

// fakePlans is an in-memory PlanReader.
type fakePlans struct {
	byID map[uint64]*domain.Plan
	err  error
	// calls counts successful lookups, to prove caching avoids repeat loads.
	calls int
}

func (f *fakePlans) GetByID(_ context.Context, id uint64) (*domain.Plan, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if p, ok := f.byID[id]; ok {
		return p, nil
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// Cache fake
// ---------------------------------------------------------------------------

// fakeCache implements the subset of cache.Cache the auth package uses, plus
// inert implementations of the remaining methods so the value satisfies the
// interface. TTLs are not enforced: tests drive invalidation explicitly.
type fakeCache struct {
	mu     sync.Mutex
	kv     map[string][]byte
	nonces map[string]struct{}
	getErr error
	setErr error
}

func newFakeCache() *fakeCache {
	return &fakeCache{kv: map[string][]byte{}, nonces: map[string]struct{}{}}
}

func (f *fakeCache) Ping(context.Context) error { return nil }
func (f *fakeCache) Close() error               { return nil }

func (f *fakeCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, false, f.getErr
	}
	v, ok := f.kv[key]
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), v...), true, nil
}

func (f *fakeCache) Set(_ context.Context, key string, val []byte, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	f.kv[key] = append([]byte(nil), val...)
	return nil
}

func (f *fakeCache) SetNX(_ context.Context, key string, val []byte, _ time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.kv[key]; ok {
		return false, nil
	}
	f.kv[key] = append([]byte(nil), val...)
	return true, nil
}

func (f *fakeCache) Del(_ context.Context, keys ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range keys {
		delete(f.kv, k)
	}
	return nil
}

func (f *fakeCache) IncrBy(_ context.Context, _ string, _ int64, _ time.Duration) (int64, error) {
	return 0, nil
}

func (f *fakeCache) Hold(context.Context, string, int64, int64, time.Duration) (string, bool, error) {
	return "", true, nil
}

func (f *fakeCache) Release(context.Context, string, string, int64) (int64, error) { return 0, nil }

func (f *fakeCache) Held(context.Context, string) (int64, error) { return 0, nil }

func (f *fakeCache) AllowRequest(context.Context, string, int, time.Duration) (bool, int, time.Duration, error) {
	return true, 0, 0, nil
}

func (f *fakeCache) AllowTokens(context.Context, string, int, int, time.Duration) (bool, int, error) {
	return true, 0, nil
}

func (f *fakeCache) AcquireConcurrency(context.Context, string, int, time.Duration) (bool, func(), error) {
	return true, func() {}, nil
}

func (f *fakeCache) CheckNonce(_ context.Context, appID, nonce string, _ time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := cache.NonceKey(appID, nonce)
	if _, seen := f.nonces[k]; seen {
		return false, nil
	}
	f.nonces[k] = struct{}{}
	return true, nil
}

func (f *fakeCache) GetJSON(_ context.Context, key string, dst any) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return false, f.getErr
	}
	raw, ok := f.kv[key]
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return false, err
	}
	return true, nil
}

func (f *fakeCache) SetJSON(_ context.Context, key string, val any, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	raw, err := json.Marshal(val)
	if err != nil {
		return err
	}
	f.kv[key] = raw
	return nil
}

func (f *fakeCache) len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.kv)
}

// ---------------------------------------------------------------------------
// Resolver fake
// ---------------------------------------------------------------------------

// fakeResolver resolves app ids from a static table.
type fakeResolver struct {
	sites map[string]fakeSite
	err   error
}

type fakeSite struct {
	secret     string
	secretPrev string
	enabled    bool
}

func (f *fakeResolver) Lookup(_ context.Context, appID string) (string, string, bool, error) {
	if f.err != nil {
		return "", "", false, f.err
	}
	s, ok := f.sites[appID]
	if !ok {
		return "", "", false, nil
	}
	return s.secret, s.secretPrev, s.enabled, nil
}

// ---------------------------------------------------------------------------
// store.Store fake for DepsFromStore
// ---------------------------------------------------------------------------

// fakeKeyRepo adapts fakeKeys to store.APIKeyRepo. It embeds the interface so the
// many unused methods are satisfied by a nil value, and overrides the three the
// auth package actually calls.
type fakeKeyRepo struct {
	store.APIKeyRepo
	inner *fakeKeys
}

func (r fakeKeyRepo) GetByHash(ctx context.Context, keyHash string) (*domain.APIKey, error) {
	return r.inner.GetByHash(ctx, keyHash)
}

func (r fakeKeyRepo) GetByID(ctx context.Context, id uint64) (*domain.APIKey, error) {
	return r.inner.GetByID(ctx, id)
}

func (r fakeKeyRepo) TouchLastUsed(ctx context.Context, id uint64, at time.Time) error {
	return r.inner.TouchLastUsed(ctx, id, at)
}

// fakeAccountRepo adapts fakeAccounts to store.AccountRepo.
type fakeAccountRepo struct {
	store.AccountRepo
	inner *fakeAccounts
}

func (r fakeAccountRepo) GetByID(ctx context.Context, id uint64) (*domain.Account, error) {
	return r.inner.GetByID(ctx, id)
}

// fakeSubscriptionRepo adapts fakeSubscriptions to store.SubscriptionRepo.
type fakeSubscriptionRepo struct {
	store.SubscriptionRepo
	inner *fakeSubscriptions
}

func (r fakeSubscriptionRepo) GetByID(ctx context.Context, id uint64) (*domain.Subscription, error) {
	return r.inner.GetByID(ctx, id)
}

// fakePlanRepo adapts fakePlans to store.PlanRepo.
type fakePlanRepo struct {
	store.PlanRepo
	inner *fakePlans
}

func (r fakePlanRepo) GetByID(ctx context.Context, id uint64) (*domain.Plan, error) {
	return r.inner.GetByID(ctx, id)
}

// fakeStore is a store.Store whose repositories all delegate to the narrow fakes.
type fakeStore struct {
	store.Store
	keys  store.APIKeyRepo
	accs  store.AccountRepo
	subs  store.SubscriptionRepo
	plans store.PlanRepo
}

func (f *fakeStore) Keys() store.APIKeyRepo                { return f.keys }
func (f *fakeStore) Accounts() store.AccountRepo           { return f.accs }
func (f *fakeStore) Subscriptions() store.SubscriptionRepo { return f.subs }
func (f *fakeStore) Plans() store.PlanRepo                 { return f.plans }
