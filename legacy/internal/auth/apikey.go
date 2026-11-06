// Package auth implements the gateway's three authentication schemes:
//
//   - API-key authentication for the OpenAI-compatible /v1 surface
//     (apikey.go, middleware.go),
//   - admin JWT issue/verify for the console /admin/v1 surface
//     (adminjwt.go),
//   - HMAC request signing with nonce replay protection for /internal/v1
//     (internalsig.go), see docs/internal-api-v1.md §1.2.
//
// The package deliberately depends on *narrow reader interfaces* declared here
// (APIKeyReader, AccountReader, SubscriptionReader, PlanReader) instead of the
// full store.Store, and on the cache.Cache contract, so it can be wired against
// any driver without importing a concrete implementation. DepsFromStore adapts a
// store.Store for production and for tests that already have a store.
//
// All exported types are immutable after construction and delegate mutable state
// to the injected store/cache implementations, which are themselves documented
// as concurrency-safe. Safe for concurrent use.
package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"czapi/internal/cache"
	"czapi/internal/domain"
	"czapi/internal/pkg/clock"
	"czapi/internal/pkg/crypto"
	"czapi/internal/pkg/errs"
	"czapi/internal/store"
)

// DefaultKeyCacheTTL is applied when VerifierConfig.CacheTTL is not positive.
const DefaultKeyCacheTTL = 60 * time.Second

// keyIDIndexPrefix starts the secondary cache entry mapping an API-key id to its
// lookup hash. The salted key hash (crypto.HashKey) is one-way, so a key id
// alone cannot locate the primary cache entry without this tiny index.
const keyIDIndexPrefix = "cz:keyid:"

// APIKeyReader is the subset of the API-key repository the verifier needs.
type APIKeyReader interface {
	// GetByHash returns the key whose salted hash matches, or a nil key when the
	// row does not exist (drivers may instead return an errs.NotFound error).
	GetByHash(ctx context.Context, keyHash string) (*domain.APIKey, error)
	// GetByID returns a key by primary key.
	GetByID(ctx context.Context, id uint64) (*domain.APIKey, error)
	// TouchLastUsed records the last usage timestamp (best effort).
	TouchLastUsed(ctx context.Context, id uint64, at time.Time) error
}

// AccountReader is the subset of the account repository the verifier needs.
type AccountReader interface {
	GetByID(ctx context.Context, id uint64) (*domain.Account, error)
}

// SubscriptionReader is the subset of the subscription repository the verifier needs.
type SubscriptionReader interface {
	GetByID(ctx context.Context, id uint64) (*domain.Subscription, error)
}

// PlanReader is the subset of the plan repository the verifier needs.
type PlanReader interface {
	GetByID(ctx context.Context, id uint64) (*domain.Plan, error)
}

// Deps is the dependency set the verifier and the internal-signature verifier
// need. Cache may be nil: the verifier then always reads through to the store.
type Deps struct {
	Keys          APIKeyReader
	Accounts      AccountReader
	Subscriptions SubscriptionReader
	Plans         PlanReader
	Cache         cache.Cache
}

// DepsFromStore adapts a store.Store to Deps. The returned Deps has a nil Cache;
// production wiring sets it afterwards (Deps{Cache: c}) or uses the helper
// provided by the composition root.
func DepsFromStore(s store.Store) Deps {
	if s == nil {
		return Deps{}
	}
	return Deps{
		Keys:          s.Keys(),
		Accounts:      s.Accounts(),
		Subscriptions: s.Subscriptions(),
		Plans:         s.Plans(),
	}
}

// Principal is an authenticated API key together with everything the request
// path needs: the tenant account, the optional subscription/plan projection and
// the lookup hash (cache identity of the presented key).
//
// The entity pointers are shared with the store (DB path) or with the decoded
// cache document (cache path). They are shared, read-only snapshots: callers must
// never mutate them, because a store driver may hand out its own internal value.
type Principal struct {
	KeyID          uint64
	AccountID      uint64
	SubscriptionID uint64
	V10HostID      uint64

	Account      *domain.Account
	Key          *domain.APIKey
	Subscription *domain.Subscription
	Plan         *domain.Plan

	// LookupHash is crypto.LookupHash of the presented plaintext key. It is the
	// cache identity and what Invalidate takes.
	LookupHash string
	// FromCache reports whether the record was served from the cache driver.
	FromCache bool
}

// AllowsModel reports whether the key may call model. An empty model (the caller
// does not know it yet) and an empty whitelist always pass; "*" is a wildcard.
func (p *Principal) AllowsModel(model string) bool {
	if p == nil || p.Key == nil {
		return false
	}
	if model == "" || len(p.Key.Models) == 0 {
		return true
	}
	for _, m := range p.Key.Models {
		if m == "*" || m == model {
			return true
		}
	}
	return false
}

// VerifierConfig configures the API-key verifier.
type VerifierConfig struct {
	// Salt is the deployment-wide pepper prepended before hashing (config.Auth.KeyHashSalt).
	Salt string
	// CacheTTL is how long a resolved key record stays cached. ≤0 means
	// DefaultKeyCacheTTL.
	CacheTTL time.Duration
	// Clock supplies "now" for expiry checks. nil means clock.System{}.
	Clock clock.Clock
}

// Verifier validates sk- API keys against the store and the cache.
// Safe for concurrent use.
type Verifier struct {
	deps Deps
	cfg  VerifierConfig
}

// NewVerifier builds a Verifier, applying defaults for zero-valued options.
func NewVerifier(d Deps, cfg VerifierConfig) *Verifier {
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = DefaultKeyCacheTTL
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.System{}
	}
	return &Verifier{deps: d, cfg: cfg}
}

// CacheTTL returns the effective cache TTL.
func (v *Verifier) CacheTTL() time.Duration { return v.cfg.CacheTTL }

// Authenticate runs the full key validation chain and returns the first failure:
//
//  1. key shape (crypto.LooksLikeAPIKey),
//  2. cache hit, else store lookup by salted hash (cache backfilled),
//  3. key status (revoked / suspended),
//  4. key expiry,
//  5. account presence and status,
//  6. IP whitelist,
//  7. model whitelist (skipped when model == ""),
//  8. subscription status and period end.
//
// model may be empty when the caller has not parsed the request body yet; the
// handler must call CheckModel once the model is known.
func (v *Verifier) Authenticate(ctx context.Context, plainKey, remoteIP, model string) (*Principal, *errs.Error) {
	if !crypto.LooksLikeAPIKey(plainKey) {
		return nil, errs.InvalidAPIKey()
	}
	lookup := crypto.LookupHash(plainKey)

	p, fromCache, e := v.load(ctx, plainKey, lookup)
	if e != nil {
		return nil, e
	}
	p.FromCache = fromCache
	now := v.cfg.Clock.Now()

	// 3. Key status.
	switch p.Key.Status {
	case domain.KeyRevoked:
		return nil, errs.InvalidAPIKey()
	case domain.KeySuspended:
		return nil, errs.KeySuspended("")
	}

	// 4. Key expiry.
	if p.Key.ExpiresAt != nil && !p.Key.ExpiresAt.After(now) {
		return nil, errs.KeySuspended("API key has expired.")
	}

	// 5. Account state. A missing account is indistinguishable from a disabled
	// one from the caller's point of view (both are a suspended tenant).
	if p.Account == nil || p.Account.Status == domain.AccountDisabled {
		return nil, errs.KeySuspended("Account is disabled.")
	}

	// 6. IP whitelist.
	if len(p.Key.IPWhitelist) > 0 && !IPAllowed(p.Key.IPWhitelist, remoteIP) {
		return nil, errs.IPNotAllowed()
	}

	// 7. Model whitelist.
	if e := v.CheckModel(p, model); e != nil {
		return nil, e
	}

	// 8. Subscription window. A zero PeriodEnd counts as expired.
	if p.Key.SubscriptionID != 0 {
		if p.Subscription == nil || p.Subscription.Status != domain.SubActive || !p.Subscription.PeriodEnd.After(now) {
			return nil, errs.KeySuspended("Subscription is suspended or expired.")
		}
	}
	return p, nil
}

// load resolves the key record from the cache or the store. The cache only holds
// store state (key, account, subscription, plan); every request-dependent check
// above still runs on each call.
func (v *Verifier) load(ctx context.Context, plain, lookup string) (*Principal, bool, *errs.Error) {
	if v == nil || v.deps.Keys == nil {
		return nil, false, errs.Internal("auth: key reader is not configured", nil)
	}
	if v.deps.Cache != nil {
		var entry keyCacheEntry
		if found, err := v.deps.Cache.GetJSON(ctx, cache.KeyCacheKey(lookup), &entry); err == nil && found && entry.Key != nil {
			return entry.principal(lookup), true, nil
		}
	}
	key, err := v.deps.Keys.GetByHash(ctx, crypto.HashKey(v.cfg.Salt, plain))
	if err != nil && !isNotFound(err) {
		return nil, false, errs.Internal("auth: key lookup failed", err)
	}
	if key == nil {
		return nil, false, errs.InvalidAPIKey()
	}
	p, e := v.hydrate(ctx, key, lookup)
	return p, false, e
}

// hydrate loads the account and the optional subscription/plan for key and
// backfills the cache when the record is complete.
func (v *Verifier) hydrate(ctx context.Context, key *domain.APIKey, lookup string) (*Principal, *errs.Error) {
	p := &Principal{KeyID: key.ID, AccountID: key.AccountID, Key: key, LookupHash: lookup}
	if v.deps.Accounts != nil && key.AccountID != 0 {
		acc, err := v.deps.Accounts.GetByID(ctx, key.AccountID)
		if err != nil && !isNotFound(err) {
			return nil, errs.Internal("auth: account lookup failed", err)
		}
		p.Account = acc
	}
	if key.SubscriptionID != 0 {
		p.SubscriptionID = key.SubscriptionID
		if v.deps.Subscriptions != nil {
			sub, err := v.deps.Subscriptions.GetByID(ctx, key.SubscriptionID)
			if err != nil && !isNotFound(err) {
				return nil, errs.Internal("auth: subscription lookup failed", err)
			}
			p.Subscription = sub
			if sub != nil {
				p.V10HostID = sub.V10HostID
				// The plan is only needed for limits and ratios: a failed load
				// must not break authentication, so errors are swallowed.
				if v.deps.Plans != nil && sub.PlanID != 0 {
					if plan, err := v.deps.Plans.GetByID(ctx, sub.PlanID); err == nil {
						p.Plan = plan
					}
				}
			}
		}
	}
	v.backfill(ctx, lookup, p)
	return p, nil
}

// backfill caches a fully resolved record. Incomplete records (missing account or
// missing subscription) are not cached so a transient inconsistency cannot be
// pinned for a whole TTL.
func (v *Verifier) backfill(ctx context.Context, lookup string, p *Principal) {
	if v.deps.Cache == nil || v.cfg.CacheTTL <= 0 || p.Key == nil {
		return
	}
	if p.Account == nil || (p.Key.SubscriptionID != 0 && p.Subscription == nil) {
		return
	}
	entry := keyCacheEntry{
		LookupHash:   lookup,
		Key:          p.Key,
		Account:      p.Account,
		Subscription: p.Subscription,
		Plan:         p.Plan,
	}
	if err := v.deps.Cache.SetJSON(ctx, cache.KeyCacheKey(lookup), entry, v.cfg.CacheTTL); err != nil {
		return
	}
	if p.KeyID != 0 {
		_ = v.deps.Cache.Set(ctx, keyIDIndexKey(p.KeyID), []byte(lookup), v.cfg.CacheTTL)
	}
}

// CheckModel enforces the key's model whitelist. An empty model always passes (the
// caller does not know it yet) and an empty whitelist means "follow the plan". A
// nil principal fails closed with invalid_api_key because it signals a handler
// wired outside APIKeyMiddleware.
func (v *Verifier) CheckModel(p *Principal, model string) *errs.Error {
	if p == nil || p.Key == nil {
		return errs.InvalidAPIKey()
	}
	if p.AllowsModel(model) {
		return nil
	}
	return errs.ModelNotAllowed(model)
}

// TouchUsed records last_used_at for the key, best effort: failures are ignored
// because they must never fail a request.
func (v *Verifier) TouchUsed(ctx context.Context, keyID uint64) {
	if v == nil || v.deps.Keys == nil || keyID == 0 {
		return
	}
	_ = v.deps.Keys.TouchLastUsed(ctx, keyID, v.cfg.Clock.Now())
}

// Invalidate drops the cached record for lookupHash (crypto.LookupHash of the
// plaintext key). Call it whenever a key, its account, its subscription or its
// plan is mutated; a nil cache makes it a no-op.
func (v *Verifier) Invalidate(ctx context.Context, lookupHash string) error {
	if v == nil || v.deps.Cache == nil || lookupHash == "" {
		return nil
	}
	keys := []string{cache.KeyCacheKey(lookupHash)}
	var entry keyCacheEntry
	if found, err := v.deps.Cache.GetJSON(ctx, cache.KeyCacheKey(lookupHash), &entry); err == nil && found && entry.Key != nil && entry.Key.ID != 0 {
		keys = append(keys, keyIDIndexKey(entry.Key.ID))
	}
	return v.deps.Cache.Del(ctx, keys...)
}

// InvalidateByID drops the cached record for a key id.
//
// The salted key hash (crypto.HashKey) is one-way, so the lookup hash cannot be
// recovered from the stored row: this method therefore relies on the secondary
// index entry written on every cache backfill (cz:keyid:<id> -> lookup hash). If
// that index entry is absent (nothing cached, or the TTL already elapsed) the
// call is a silent no-op returning nil; use Invalidate with the lookup hash when
// the caller already has it.
func (v *Verifier) InvalidateByID(ctx context.Context, keyID uint64) error {
	if v == nil || v.deps.Cache == nil || keyID == 0 {
		return nil
	}
	raw, found, err := v.deps.Cache.Get(ctx, keyIDIndexKey(keyID))
	if err != nil || !found {
		return err
	}
	lookup := strings.TrimSpace(string(raw))
	if lookup == "" {
		return v.deps.Cache.Del(ctx, keyIDIndexKey(keyID))
	}
	return v.Invalidate(ctx, lookup)
}

// keyCacheEntry is the JSON document stored per validated key. LookupHash is kept
// inside the value so the secondary index (and diagnostics) can recover it.
type keyCacheEntry struct {
	LookupHash   string               `json:"lookup_hash"`
	Key          *domain.APIKey       `json:"key"`
	Account      *domain.Account      `json:"account"`
	Subscription *domain.Subscription `json:"subscription,omitempty"`
	Plan         *domain.Plan         `json:"plan,omitempty"`
}

// principal rebuilds a Principal from a cached entry.
func (e keyCacheEntry) principal(lookup string) *Principal {
	p := &Principal{
		Account:      e.Account,
		Key:          e.Key,
		Subscription: e.Subscription,
		Plan:         e.Plan,
		LookupHash:   lookup,
		FromCache:    true,
	}
	if e.LookupHash != "" {
		p.LookupHash = e.LookupHash
	}
	if e.Key != nil {
		p.KeyID = e.Key.ID
		p.AccountID = e.Key.AccountID
		p.SubscriptionID = e.Key.SubscriptionID
	}
	if e.Subscription != nil {
		p.V10HostID = e.Subscription.V10HostID
	}
	return p
}

// keyIDIndexKey is the secondary cache key mapping a key id to its lookup hash.
func keyIDIndexKey(keyID uint64) string {
	return keyIDIndexPrefix + u64str(keyID)
}

// IPAllowed reports whether remoteIP matches any entry of list. Entries may be
// CIDR blocks ("10.0.0.0/8", "2001:db8::/32") or exact addresses. An
// unparseable remote IP never matches.
func IPAllowed(list []string, remoteIP string) bool {
	ip := net.ParseIP(strings.TrimSpace(remoteIP))
	if ip == nil {
		return false
	}
	for _, raw := range list {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			if _, network, err := net.ParseCIDR(entry); err == nil && network.Contains(ip) {
				return true
			}
			continue
		}
		if host := net.ParseIP(entry); host != nil && host.Equal(ip) {
			return true
		}
	}
	return false
}

// isNotFound reports whether err signals a missing row. Drivers are expected to
// return errs.NotFound; a nil result with a nil error is handled by callers.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var e *errs.Error
	if errors.As(err, &e) {
		return e.Status == http.StatusNotFound
	}
	return false
}

// u64str renders v as decimal without importing strconv.
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
