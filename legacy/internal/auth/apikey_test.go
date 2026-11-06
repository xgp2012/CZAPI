package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"czapi/internal/cache"
	"czapi/internal/domain"
	"czapi/internal/pkg/clock"
	"czapi/internal/pkg/crypto"
	"czapi/internal/pkg/errs"
)

const (
	testSalt   = "test-salt"
	testPlain  = "sk-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"
	testNowStr = "2026-11-05T12:00:00Z"
)

// fixture bundles everything a verifier test needs.
type fixture struct {
	keys  *fakeKeys
	accs  *fakeAccounts
	subs  *fakeSubscriptions
	plans *fakePlans
	cache *fakeCache
	clk   *clock.Fake
	now   time.Time
	deps  Deps
	v     *Verifier
}

// newFixture builds a fully-populated, healthy key chain.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	now, err := time.Parse(time.RFC3339, testNowStr)
	require.NoError(t, err)

	f := &fixture{
		keys:  newFakeKeys(),
		accs:  &fakeAccounts{byID: map[uint64]*domain.Account{}},
		subs:  &fakeSubscriptions{byID: map[uint64]*domain.Subscription{}},
		plans: &fakePlans{byID: map[uint64]*domain.Plan{}},
		cache: newFakeCache(),
		now:   now,
	}
	f.clk = clock.NewFake(now)
	f.accs.byID[7] = &domain.Account{ID: 7, V10ClientID: 1234, Status: domain.AccountEnabled}
	f.plans.byID[3] = &domain.Plan{ID: 3, PlanCode: "pro", ModelGroup: "default"}
	f.subs.byID[9] = &domain.Subscription{
		ID: 9, AccountID: 7, V10HostID: 555, PlanID: 3,
		Status: domain.SubActive,
		// Period ends 30 days after the frozen test clock.
		PeriodStart: now.Add(-24 * time.Hour),
		PeriodEnd:   now.Add(30 * 24 * time.Hour),
	}
	f.accs.byID[8] = &domain.Account{ID: 8, V10ClientID: 999, Status: domain.AccountEnabled}
	f.keys.add(&domain.APIKey{
		ID:             321,
		AccountID:      7,
		SubscriptionID: 9,
		KeyHash:        crypto.HashKey(testSalt, testPlain),
		KeyPrefix:      crypto.Prefix(testPlain),
		Name:           "prod",
		Status:         domain.KeyActive,
		CreatedAt:      now.Add(-time.Hour),
	})
	f.deps = Deps{Keys: f.keys, Accounts: f.accs, Subscriptions: f.subs, Plans: f.plans, Cache: f.cache}
	f.v = NewVerifier(f.deps, VerifierConfig{Salt: testSalt, Clock: f.clk})
	return f
}

func TestNewVerifierDefaults(t *testing.T) {
	v := NewVerifier(Deps{}, VerifierConfig{})
	assert.Equal(t, DefaultKeyCacheTTL, v.CacheTTL())
	// A nil clock must not panic when Authenticate is called.
	_, e := v.Authenticate(context.Background(), testPlain, "1.2.3.4", "")
	require.NotNil(t, e)
	assert.Equal(t, http.StatusInternalServerError, e.Status)
}

func TestAuthenticate_Success(t *testing.T) {
	f := newFixture(t)
	p, e := f.v.Authenticate(context.Background(), testPlain, "1.2.3.4", "deepseek-chat")
	require.Nil(t, e)
	require.NotNil(t, p)
	assert.Equal(t, uint64(321), p.KeyID)
	assert.Equal(t, uint64(7), p.AccountID)
	assert.Equal(t, uint64(9), p.SubscriptionID)
	assert.Equal(t, uint64(555), p.V10HostID)
	assert.False(t, p.FromCache)
	require.NotNil(t, p.Account)
	assert.Equal(t, uint64(1234), p.Account.V10ClientID)
	require.NotNil(t, p.Key)
	require.NotNil(t, p.Subscription)
	require.NotNil(t, p.Plan)
	assert.Equal(t, "pro", p.Plan.PlanCode)
	assert.Equal(t, crypto.LookupHash(testPlain), p.LookupHash)
}

func TestAuthenticate_Failures(t *testing.T) {
	tests := []struct {
		name        string
		plainKey    string
		mutate      func(f *fixture)
		remoteIP    string
		model       string
		wantCode    errs.Code
		wantHTTP    int
		wantMessage string
	}{
		{
			name:     "malformed key shape",
			plainKey: "not-a-key",
			wantCode: errs.CodeInvalidAPIKey,
			wantHTTP: http.StatusUnauthorized,
		},
		{
			name:     "key not found",
			plainKey: "sk-unknownkeyunknownkey1234567890",
			wantCode: errs.CodeInvalidAPIKey,
			wantHTTP: http.StatusUnauthorized,
		},
		{
			name:     "revoked key",
			mutate:   func(f *fixture) { f.keys.byHash[crypto.HashKey(testSalt, testPlain)].Status = domain.KeyRevoked },
			wantCode: errs.CodeInvalidAPIKey,
			wantHTTP: http.StatusUnauthorized,
		},
		{
			name:     "suspended key",
			mutate:   func(f *fixture) { f.keys.byHash[crypto.HashKey(testSalt, testPlain)].Status = domain.KeySuspended },
			wantCode: errs.CodeKeySuspended,
			wantHTTP: http.StatusForbidden,
		},
		{
			name: "expired key",
			mutate: func(f *fixture) {
				past := f.now.Add(-time.Second)
				f.keys.byHash[crypto.HashKey(testSalt, testPlain)].ExpiresAt = &past
			},
			wantCode:    errs.CodeKeySuspended,
			wantHTTP:    http.StatusForbidden,
			wantMessage: "API key has expired.",
		},
		{
			name: "missing account",
			mutate: func(f *fixture) {
				delete(f.accs.byID, 7)
			},
			wantCode:    errs.CodeKeySuspended,
			wantHTTP:    http.StatusForbidden,
			wantMessage: "Account is disabled.",
		},
		{
			name: "disabled account",
			mutate: func(f *fixture) {
				f.accs.byID[7].Status = domain.AccountDisabled
			},
			wantCode:    errs.CodeKeySuspended,
			wantHTTP:    http.StatusForbidden,
			wantMessage: "Account is disabled.",
		},
		{
			name: "ip not allowed",
			mutate: func(f *fixture) {
				f.keys.byHash[crypto.HashKey(testSalt, testPlain)].IPWhitelist = []string{"10.0.0.0/8", "192.168.1.5"}
			},
			remoteIP: "203.0.113.9",
			wantCode: errs.CodeIPNotAllowed,
			wantHTTP: http.StatusForbidden,
		},
		{
			name: "model not allowed",
			mutate: func(f *fixture) {
				f.keys.byHash[crypto.HashKey(testSalt, testPlain)].Models = []string{"deepseek-chat"}
			},
			model:    "gpt-4o",
			wantCode: errs.CodeModelNotAllowed,
			wantHTTP: http.StatusForbidden,
		},
		{
			name: "subscription suspended",
			mutate: func(f *fixture) {
				f.subs.byID[9].Status = domain.SubSuspended
			},
			wantCode:    errs.CodeKeySuspended,
			wantHTTP:    http.StatusForbidden,
			wantMessage: "Subscription is suspended or expired.",
		},
		{
			name: "subscription expired",
			mutate: func(f *fixture) {
				f.subs.byID[9].Status = domain.SubExpired
			},
			wantCode:    errs.CodeKeySuspended,
			wantHTTP:    http.StatusForbidden,
			wantMessage: "Subscription is suspended or expired.",
		},
		{
			name: "subscription period ended",
			mutate: func(f *fixture) {
				f.subs.byID[9].PeriodEnd = f.now.Add(-time.Minute)
			},
			wantCode:    errs.CodeKeySuspended,
			wantHTTP:    http.StatusForbidden,
			wantMessage: "Subscription is suspended or expired.",
		},
		{
			name: "subscription row missing",
			mutate: func(f *fixture) {
				delete(f.subs.byID, 9)
			},
			wantCode:    errs.CodeKeySuspended,
			wantHTTP:    http.StatusForbidden,
			wantMessage: "Subscription is suspended or expired.",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if tc.mutate != nil {
				tc.mutate(f)
			}
			plain := tc.plainKey
			if plain == "" {
				plain = testPlain
			}
			ip := tc.remoteIP
			if ip == "" {
				ip = "1.2.3.4"
			}
			p, e := f.v.Authenticate(context.Background(), plain, ip, tc.model)
			require.NotNil(t, e, "expected failure")
			assert.Nil(t, p)
			assert.Equal(t, tc.wantCode, e.Code)
			assert.Equal(t, tc.wantHTTP, e.Status)
			if tc.wantMessage != "" {
				assert.Equal(t, tc.wantMessage, e.Message)
			}
		})
	}
}

func TestAuthenticate_IPWhitelistMatching(t *testing.T) {
	tests := []struct {
		name   string
		list   []string
		ip     string
		expect bool
	}{
		{"exact ipv4", []string{"1.2.3.4"}, "1.2.3.4", true},
		{"exact ipv4 miss", []string{"1.2.3.4"}, "1.2.3.5", false},
		{"cidr ipv4", []string{"1.2.3.0/24"}, "1.2.3.99", true},
		{"cidr ipv4 miss", []string{"1.2.3.0/24"}, "1.2.4.1", false},
		{"ipv6 cidr", []string{"2001:db8::/32"}, "2001:db8::1", true},
		{"ipv4-mapped form", []string{"1.2.3.0/24"}, "::ffff:1.2.3.4", true},
		{"empty list entry ignored", []string{"", "1.2.3.4"}, "1.2.3.4", true},
		{"garbage entry ignored", []string{"nonsense"}, "1.2.3.4", false},
		{"unparseable remote", []string{"0.0.0.0/0"}, "not-an-ip", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expect, IPAllowed(tc.list, tc.ip))
		})
	}
}

func TestAuthenticate_CacheHitAndBackfill(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	first, e := f.v.Authenticate(ctx, testPlain, "1.2.3.4", "")
	require.Nil(t, e)
	require.False(t, first.FromCache)
	require.Equal(t, 1, f.keys.hashCalls())
	require.Greater(t, f.cache.len(), 0, "record must be cached")

	second, e := f.v.Authenticate(ctx, testPlain, "1.2.3.4", "")
	require.Nil(t, e)
	assert.True(t, second.FromCache, "second call must be served from the cache")
	assert.Equal(t, 1, f.keys.hashCalls(), "cache hit must not hit the store")
	assert.Equal(t, first.KeyID, second.KeyID)
	assert.Equal(t, first.LookupHash, second.LookupHash)
	assert.Equal(t, first.V10HostID, second.V10HostID)
	require.NotNil(t, second.Plan)
	assert.Equal(t, uint64(3), second.Plan.ID)

	// A throttled plan read on the second call proves the plan came from cache.
	assert.Equal(t, 1, f.plans.calls)
}

func TestAuthenticate_CacheEntryStillEnforcesLiveChecks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// The key expires one minute from "now"; the record still caches normally.
	exp := f.now.Add(time.Minute)
	f.keys.byHash[crypto.HashKey(testSalt, testPlain)].ExpiresAt = &exp
	_, e := f.v.Authenticate(ctx, testPlain, "1.2.3.4", "")
	require.Nil(t, e)

	// A second call inside the window is served from the cache and passes.
	p, e := f.v.Authenticate(ctx, testPlain, "1.2.3.4", "")
	require.Nil(t, e)
	require.True(t, p.FromCache)

	// Expiry is evaluated per request against the clock, so advancing past the
	// expiry must reject even though the record is still cached.
	f.clk.Advance(2 * time.Minute)
	_, e = f.v.Authenticate(ctx, testPlain, "1.2.3.4", "")
	require.NotNil(t, e)
	assert.Equal(t, errs.CodeKeySuspended, e.Code)
	assert.Equal(t, "API key has expired.", e.Message)
	assert.Equal(t, 1, f.keys.hashCalls(), "the rejection must come from the cached record")
}

func TestInvalidate_ForcesStoreRead(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	lookup := crypto.LookupHash(testPlain)

	_, e := f.v.Authenticate(ctx, testPlain, "1.2.3.4", "")
	require.Nil(t, e)
	_, e = f.v.Authenticate(ctx, testPlain, "1.2.3.4", "")
	require.Nil(t, e)
	assert.Equal(t, 1, f.keys.hashCalls())

	require.NoError(t, f.v.Invalidate(ctx, lookup))

	// Mutate after invalidation: the next call must observe the new state.
	f.keys.byHash[crypto.HashKey(testSalt, testPlain)].Status = domain.KeyRevoked

	_, e = f.v.Authenticate(ctx, testPlain, "1.2.3.4", "")
	require.NotNil(t, e)
	assert.Equal(t, errs.CodeInvalidAPIKey, e.Code)
	assert.Equal(t, 2, f.keys.hashCalls(), "invalidate must force a store read")
}

func TestInvalidateByID(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	_, e := f.v.Authenticate(ctx, testPlain, "1.2.3.4", "")
	require.Nil(t, e)
	_, e = f.v.Authenticate(ctx, testPlain, "1.2.3.4", "")
	require.Nil(t, e)
	require.Equal(t, 1, f.keys.hashCalls())

	require.NoError(t, f.v.InvalidateByID(ctx, 321))
	f.keys.byHash[crypto.HashKey(testSalt, testPlain)].Status = domain.KeyRevoked

	_, e = f.v.Authenticate(ctx, testPlain, "1.2.3.4", "")
	require.NotNil(t, e)
	assert.Equal(t, errs.CodeInvalidAPIKey, e.Code)
	assert.Equal(t, 2, f.keys.hashCalls())

	// InvalidateByID without a cached entry is a silent no-op.
	require.NoError(t, f.v.InvalidateByID(ctx, 999999))
}

func TestInvalidate_NoCacheIsNoOp(t *testing.T) {
	f := newFixture(t)
	f.deps.Cache = nil
	v := NewVerifier(f.deps, VerifierConfig{Salt: testSalt, Clock: f.clk})
	assert.NoError(t, v.Invalidate(context.Background(), crypto.LookupHash(testPlain)))
	assert.NoError(t, v.InvalidateByID(context.Background(), 321))
	_, e := v.Authenticate(context.Background(), testPlain, "1.2.3.4", "")
	assert.Nil(t, e, "auth must work without a cache")
}

func TestAuthenticate_DriverNotFoundError(t *testing.T) {
	f := newFixture(t)
	f.keys.notFoundAsError = true
	_, e := f.v.Authenticate(context.Background(), "sk-unknownkeyunknownkey1234567890", "1.2.3.4", "")
	require.NotNil(t, e)
	assert.Equal(t, errs.CodeInvalidAPIKey, e.Code)
	assert.Equal(t, http.StatusUnauthorized, e.Status)
}

func TestAuthenticate_StoreErrorsBecome500(t *testing.T) {
	f := newFixture(t)
	f.keys.notFoundAsError = false
	boom := errors.New("db down")
	f.accs.err = boom
	_, e := f.v.Authenticate(context.Background(), testPlain, "1.2.3.4", "")
	require.NotNil(t, e)
	assert.Equal(t, http.StatusInternalServerError, e.Status)
	assert.ErrorIs(t, e, boom)
}

func TestAuthenticate_NotCachedWhenIncomplete(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	delete(f.accs.byID, 7) // account gone => auth fails at step 5
	_, e := f.v.Authenticate(ctx, testPlain, "1.2.3.4", "")
	require.NotNil(t, e)
	assert.Zero(t, f.cache.len(), "an unauthenticatable record must not be cached")
}

func TestAuthenticate_NoSubscriptionKey(t *testing.T) {
	f := newFixture(t)
	k := f.keys.byHash[crypto.HashKey(testSalt, testPlain)]
	k.SubscriptionID = 0
	k.AccountID = 8

	p, e := f.v.Authenticate(context.Background(), testPlain, "1.2.3.4", "")
	require.Nil(t, e)
	require.NotNil(t, p)
	assert.Zero(t, p.SubscriptionID)
	assert.Nil(t, p.Subscription)
	assert.Equal(t, uint64(8), p.AccountID)
}

func TestCheckModel(t *testing.T) {
	p := &Principal{Key: &domain.APIKey{Models: []string{"deepseek-chat", "gpt-4o-mini"}}}
	assert.Nil(t, (&Verifier{}).CheckModel(p, "deepseek-chat"), "listed model allowed")
	assert.Nil(t, (&Verifier{}).CheckModel(p, ""), "empty model always passes")

	e := (&Verifier{}).CheckModel(p, "gpt-4o")
	require.NotNil(t, e)
	assert.Equal(t, errs.CodeModelNotAllowed, e.Code)
	assert.Equal(t, http.StatusForbidden, e.Status)

	open := &Principal{Key: &domain.APIKey{}}
	assert.Nil(t, (&Verifier{}).CheckModel(open, "anything"), "empty whitelist allows every model")

	wildcard := &Principal{Key: &domain.APIKey{Models: []string{"*"}}}
	assert.Nil(t, (&Verifier{}).CheckModel(wildcard, "anything"))

	require.NotNil(t, (&Verifier{}).CheckModel(nil, "x"))
	assert.Equal(t, errs.CodeInvalidAPIKey, (&Verifier{}).CheckModel(nil, "x").Code)
}

func TestTouchUsed(t *testing.T) {
	f := newFixture(t)
	f.v.TouchUsed(context.Background(), 321)
	f.keys.mu.Lock()
	touches := append([]uint64(nil), f.keys.touches...)
	f.keys.mu.Unlock()
	assert.Equal(t, []uint64{321}, touches)

	// Failures must be swallowed.
	f.keys.mu.Lock()
	f.keys.touchErr = errors.New("boom")
	f.keys.mu.Unlock()
	f.v.TouchUsed(context.Background(), 321)
	f.keys.mu.Lock()
	touches = append([]uint64(nil), f.keys.touches...)
	f.keys.mu.Unlock()
	assert.Len(t, touches, 2)

	// Zero id and nil verifier are no-ops.
	var nilVerifier *Verifier
	nilVerifier.TouchUsed(context.Background(), 321)
	f.v.TouchUsed(context.Background(), 0)
}

func TestDepsFromStore(t *testing.T) {
	f := newFixture(t)
	s := &fakeStore{
		keys:  fakeKeyRepo{inner: f.keys},
		accs:  fakeAccountRepo{inner: f.accs},
		subs:  fakeSubscriptionRepo{inner: f.subs},
		plans: fakePlanRepo{inner: f.plans},
	}
	d := DepsFromStore(s)
	require.NotNil(t, d.Keys)
	require.NotNil(t, d.Accounts)
	require.NotNil(t, d.Subscriptions)
	require.NotNil(t, d.Plans)
	assert.Nil(t, d.Cache)

	_, err := d.Keys.GetByHash(context.Background(), crypto.HashKey(testSalt, testPlain))
	require.NoError(t, err)

	v := NewVerifier(d, VerifierConfig{Salt: testSalt, Clock: f.clk})
	p, e := v.Authenticate(context.Background(), testPlain, "1.2.3.4", "")
	require.Nil(t, e)
	assert.Equal(t, uint64(321), p.KeyID)

	// A nil store yields zero-value deps that fail closed, not panic.
	assert.Equal(t, Deps{}, DepsFromStore(nil))
}

func TestKeyCacheEntryPrincipal(t *testing.T) {
	entry := keyCacheEntry{
		LookupHash: "abc",
		Key:        &domain.APIKey{ID: 5, AccountID: 6, SubscriptionID: 7},
		Account:    &domain.Account{ID: 6},
		Subscription: &domain.Subscription{
			ID: 7, V10HostID: 42,
		},
	}
	p := entry.principal("ignored")
	assert.Equal(t, uint64(5), p.KeyID)
	assert.Equal(t, uint64(6), p.AccountID)
	assert.Equal(t, uint64(7), p.SubscriptionID)
	assert.Equal(t, uint64(42), p.V10HostID)
	assert.Equal(t, "abc", p.LookupHash)
	assert.True(t, p.FromCache)

	// An entry without a stored hash falls back to the requested lookup hash.
	assert.Equal(t, "fallback", keyCacheEntry{}.principal("fallback").LookupHash)
}

func TestCacheKeyUsesPackageHelper(t *testing.T) {
	assert.Equal(t, "cz:key:abc", cache.KeyCacheKey("abc"))
	assert.Equal(t, "cz:keyid:321", keyIDIndexKey(321))
	assert.Equal(t, "cz:keyid:0", keyIDIndexKey(0))
}
