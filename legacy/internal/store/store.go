// Package store defines the persistence contract used by every layer.
//
// Two drivers implement it: "memory" (zero-dependency, used for local dev and
// tests) and "mysql" (production). Both must satisfy exactly the same
// semantics, most importantly:
//
//   - Credit is idempotent on (RefType, RefID): replaying the same V10 voucher
//     must apply the amount exactly once and report Applied=false the second time.
//   - InsertUsage is idempotent on RequestID.
//   - CreateSubscription is idempotent on V10HostID.
//
// All methods must be safe for concurrent use.
package store

import (
	"context"
	"time"

	"czapi/internal/domain"
)

// Store groups the repositories.
type Store interface {
	Sites() SiteRepo
	Accounts() AccountRepo
	Plans() PlanRepo
	Subscriptions() SubscriptionRepo
	Keys() APIKeyRepo
	Channels() ChannelRepo
	Models() ModelRepo
	Usage() UsageRepo
	Ledger() LedgerRepo
	Admins() AdminRepo
	Audit() AuditRepo
	Flags() FlagRepo
	Aggregates() AggregateRepo
	// Ping verifies the backend is reachable.
	Ping(ctx context.Context) error
	// Close releases resources.
	Close() error
}

// SiteRepo manages V10 installation registrations.
type SiteRepo interface {
	Create(ctx context.Context, s *domain.Site) error
	Update(ctx context.Context, s *domain.Site) error
	GetByAppID(ctx context.Context, appID string) (*domain.Site, error)
	List(ctx context.Context) ([]domain.Site, error)
}

// AccountRepo manages tenants.
type AccountRepo interface {
	// Upsert creates or updates the account identified by (site_id, v10_client_id).
	Upsert(ctx context.Context, a *domain.Account) (*domain.Account, error)
	GetByID(ctx context.Context, id uint64) (*domain.Account, error)
	GetByV10Client(ctx context.Context, siteID string, clientID uint64) (*domain.Account, error)
	Update(ctx context.Context, a *domain.Account) error
	List(ctx context.Context, offset, limit int) ([]domain.Account, error)
	Count(ctx context.Context) (int64, error)
}

// PlanRepo manages package definitions.
type PlanRepo interface {
	Upsert(ctx context.Context, p *domain.Plan) (*domain.Plan, error)
	GetByID(ctx context.Context, id uint64) (*domain.Plan, error)
	GetByCode(ctx context.Context, code string) (*domain.Plan, error)
	List(ctx context.Context) ([]domain.Plan, error)
}

// SubscriptionRepo manages the V10 host projections.
type SubscriptionRepo interface {
	// Create is idempotent on V10HostID: an existing row is returned unchanged
	// with created=false.
	Create(ctx context.Context, s *domain.Subscription) (out *domain.Subscription, created bool, err error)
	GetByID(ctx context.Context, id uint64) (*domain.Subscription, error)
	GetByV10Host(ctx context.Context, hostID uint64) (*domain.Subscription, error)
	// ListByAccount returns subscriptions for an account, soonest expiry first.
	ListByAccount(ctx context.Context, accountID uint64) ([]domain.Subscription, error)
	Update(ctx context.Context, s *domain.Subscription) error
	// AddUsage atomically adds tokens to quota_used and returns the new value.
	AddUsage(ctx context.Context, id uint64, tokens int64) (used int64, err error)
	// ResetPeriod sets a new period and clears quota_used (renewal).
	ResetPeriod(ctx context.Context, id uint64, start, end time.Time, quotaTotal int64) error
	// ListExpired returns active subscriptions whose period_end <= at.
	ListExpired(ctx context.Context, at time.Time) ([]domain.Subscription, error)
}

// APIKeyRepo manages sk- credentials.
type APIKeyRepo interface {
	Create(ctx context.Context, k *domain.APIKey) error
	GetByID(ctx context.Context, id uint64) (*domain.APIKey, error)
	GetByHash(ctx context.Context, keyHash string) (*domain.APIKey, error)
	Update(ctx context.Context, k *domain.APIKey) error
	// ListByAccount returns keys for an account ordered by creation desc.
	ListByAccount(ctx context.Context, accountID uint64) ([]domain.APIKey, error)
	// ListBySubscription returns keys bound to a subscription.
	ListBySubscription(ctx context.Context, subscriptionID uint64) ([]domain.APIKey, error)
	// CountBySubscription returns how many non-revoked keys a subscription owns.
	CountBySubscription(ctx context.Context, subscriptionID uint64) (int, error)
	// RevokeBySubscription revokes every key of a subscription and returns the count.
	RevokeBySubscription(ctx context.Context, subscriptionID uint64) (int, error)
	// SetStatusBySubscription flips every non-revoked key of a subscription.
	SetStatusBySubscription(ctx context.Context, subscriptionID uint64, status domain.APIKeyStatus) (int, error)
	// TouchLastUsed records the last usage timestamp (best effort).
	TouchLastUsed(ctx context.Context, id uint64, at time.Time) error
	Delete(ctx context.Context, id uint64) error
}

// ChannelRepo manages upstream channels and their breaker state.
type ChannelRepo interface {
	Create(ctx context.Context, c *domain.Channel) error
	Update(ctx context.Context, c *domain.Channel) error
	GetByID(ctx context.Context, id uint64) (*domain.Channel, error)
	List(ctx context.Context) ([]domain.Channel, error)
	// ListByModel returns enabled channels that serve the given external model.
	ListByModel(ctx context.Context, model string) ([]domain.Channel, error)
	Delete(ctx context.Context, id uint64) error

	GetHealth(ctx context.Context, channelID uint64) (*domain.ChannelHealth, error)
	SaveHealth(ctx context.Context, h *domain.ChannelHealth) error
}

// ModelRepo manages the billable model catalogue.
type ModelRepo interface {
	Upsert(ctx context.Context, m *domain.Model) (*domain.Model, error)
	GetByName(ctx context.Context, name string) (*domain.Model, error)
	List(ctx context.Context) ([]domain.Model, error)
	// ListByGroup returns models in one group ("" or "default" => all).
	ListByGroup(ctx context.Context, group string) ([]domain.Model, error)
	Delete(ctx context.Context, name string) error
}

// UsageRepo manages metered request records.
type UsageRepo interface {
	// Insert is idempotent on RequestID.
	Insert(ctx context.Context, r *domain.UsageRecord) (created bool, err error)
	List(ctx context.Context, f UsageFilter) ([]domain.UsageRecord, error)
	Summary(ctx context.Context, f UsageFilter) (domain.UsageSummary, error)
	// SumCostByKeyBetween returns consumed micros for a key in [from, to).
	SumCostByKeyBetween(ctx context.Context, apiKeyID uint64, from, to time.Time) (int64, error)
	// SumCostByAccountBetween returns consumed micros for an account in [from, to).
	SumCostByAccountBetween(ctx context.Context, accountID uint64, from, to time.Time) (int64, error)
	// SumCostGlobalBetween returns site-wide consumed micros in [from, to).
	SumCostGlobalBetween(ctx context.Context, from, to time.Time) (int64, error)
}

// UsageFilter selects usage rows. Zero values mean "no constraint".
type UsageFilter struct {
	SiteID        string
	AccountID     uint64
	V10HostID     uint64
	APIKeyID      uint64
	Model         string
	From          time.Time
	To            time.Time
	Offset        int
	Limit         int
	IncludeShadow bool
}

// LedgerRepo is the immutable balance book.
type LedgerRepo interface {
	// Credit applies amount (may be negative) to the account balance and
	// appends a ledger entry, atomically and idempotently on (ref_type, ref_id).
	// applied=false with no error means the voucher was already processed.
	Credit(ctx context.Context, e *domain.CreditLedgerEntry) (applied bool, balanceAfter int64, err error)
	List(ctx context.Context, f LedgerFilter) ([]domain.CreditLedgerEntry, error)
	// SumByTypeBetween returns the signed sum of amounts for an account.
	SumByTypeBetween(ctx context.Context, accountID uint64, t domain.LedgerType, from, to time.Time) (int64, error)
}

// LedgerFilter selects ledger entries.
type LedgerFilter struct {
	AccountID uint64
	Type      domain.LedgerType
	RefType   string
	From      time.Time
	To        time.Time
	Offset    int
	Limit     int
}

// AdminRepo manages console operators.
type AdminRepo interface {
	Create(ctx context.Context, a *domain.AdminUser) error
	Update(ctx context.Context, a *domain.AdminUser) error
	GetByID(ctx context.Context, id uint64) (*domain.AdminUser, error)
	GetByUsername(ctx context.Context, username string) (*domain.AdminUser, error)
	List(ctx context.Context) ([]domain.AdminUser, error)
	Count(ctx context.Context) (int64, error)
}

// AuditRepo records administrative mutations.
type AuditRepo interface {
	Append(ctx context.Context, l *domain.AuditLog) error
	List(ctx context.Context, offset, limit int) ([]domain.AuditLog, error)
}

// FlagRepo stores feature switches.
type FlagRepo interface {
	Set(ctx context.Context, f *domain.FlagValue) error
	Get(ctx context.Context, key, scope string) (*domain.FlagValue, error)
	List(ctx context.Context) ([]domain.FlagValue, error)
}

// AggregateRepo stores pre-aggregated daily rollups.
type AggregateRepo interface {
	UpsertDaily(ctx context.Context, d *domain.DailyUsage) error
	ListDaily(ctx context.Context, f UsageFilter) ([]domain.DailyUsage, error)
	// RebuildDaily recomputes rollups for a day from raw usage (reconciliation).
	RebuildDaily(ctx context.Context, day time.Time) error
	// BucketCost returns cost per day for the given range, for trend charts.
	BucketCost(ctx context.Context, f UsageFilter) ([]domain.DailyUsage, error)
}
