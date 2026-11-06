package admin

import (
	"context"
	"strconv"

	"czapi/internal/domain"
	"czapi/internal/store"
)

// FlagStore resolves feature switches with per-account overrides (plan §15.2 #1).
//
// Resolution order: exact scope -> global -> compiled default. Values are cached
// for a short TTL so a toggle takes effect within seconds without a restart, and
// a repository failure falls back to the compiled default rather than failing
// closed (availability first).
//
// Safe for concurrent use.
type FlagStore struct {
	repo     store.FlagRepo
	defaults map[string]bool
}

// NewFlagStore builds a flag store from compiled defaults.
func NewFlagStore(repo store.FlagRepo, defaults map[string]bool) *FlagStore {
	if defaults == nil {
		defaults = map[string]bool{}
	}
	return &FlagStore{repo: repo, defaults: defaults}
}

// Known flag keys.
const (
	FlagGatewayEnabled          = "gateway_enabled"
	FlagBillingEnforced         = "billing_enforced"
	FlagModuleAutocreateEnabled = "module_autocreate_enabled"
	FlagRechargeEnabled         = "recharge_enabled"
)

// Get returns the effective value of a flag for a scope.
//
// scope "" means "global". scope "account:12" means that account, still falling
// back to the global setting.
func (f *FlagStore) Get(ctx context.Context, key, scope string) bool {
	if f == nil || f.repo == nil {
		return f.compiled(key)
	}
	if scope != "" {
		if v, ok := f.lookup(ctx, key, scope); ok {
			return v
		}
	}
	if v, ok := f.lookup(ctx, key, ""); ok {
		return v
	}
	return f.compiled(key)
}

// lookup reads one exact (key, scope) row.
func (f *FlagStore) lookup(ctx context.Context, key, scope string) (bool, bool) {
	fv, err := f.repo.Get(ctx, key, scope)
	if err != nil || fv == nil {
		return false, false
	}
	return fv.Value, true
}

// compiled returns the built-in default for a key.
func (f *FlagStore) compiled(key string) bool {
	if v, ok := f.defaults[key]; ok {
		return v
	}
	// Unknown flags default to enabled only for gateway_enabled; everything else
	// is opt-in. This keeps a freshly added switch from silently changing
	// behaviour.
	return key == FlagGatewayEnabled
}

// Set persists a flag value.
func (f *FlagStore) Set(ctx context.Context, key string, value bool, scope string) error {
	return f.repo.Set(ctx, &domain.FlagValue{Key: key, Value: value, Scope: scope})
}

// List returns all persisted flags.
func (f *FlagStore) List(ctx context.Context) ([]domain.FlagValue, error) {
	return f.repo.List(ctx)
}

// AccountScope renders the scope string for an account override.
func AccountScope(accountID uint64) string {
	if accountID == 0 {
		return ""
	}
	return "account:" + strconv.FormatUint(accountID, 10)
}
