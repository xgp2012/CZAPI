// Package memstore is the in-process implementation of the store.Store
// contract.
//
// It exists so the gateway can boot, and the whole test suite can run, on a
// machine without MySQL. State lives in Go maps guarded by one sync.RWMutex;
// every read and every write deep-copies slices and maps so a caller can never
// mutate internal state through a returned pointer.
//
// Idempotency follows docs/workstreams.md §5.1:
//
//   - Ledger.Credit is unique on (RefType, RefID): a replay returns
//     applied=false and leaves the balance untouched, even for ten concurrent
//     identical calls.
//   - Usage.Insert is unique on RequestID.
//   - Subscription.Create is unique on V10HostID and returns created=false for
//     an existing row.
//
// Concurrency: Safe for concurrent use. All methods of every repository
// returned by the Store take the store-wide lock for the duration of the call,
// so a single operation is atomic across entities (Credit updates the ledger
// entry and the account balance in one critical section).
//
// Time: timestamps are stamped from the injected clock.Clock (clock.System by
// default) and never from time.Now directly.
package memstore

import (
	"context"
	"sync"
	"time"

	"czapi/internal/domain"
	"czapi/internal/pkg/clock"
	"czapi/internal/store"
)

// Compile-time proof that the memory driver satisfies the frozen contract.
var _ store.Store = (*Store)(nil)

// Entity kinds used for the auto-increment sequences.
const (
	kindSite         = "site"
	kindAccount      = "account"
	kindPlan         = "plan"
	kindSubscription = "subscription"
	kindAPIKey       = "api_key"
	kindChannel      = "channel"
	kindModel        = "model"
	kindUsage        = "usage"
	kindLedger       = "ledger"
	kindAdmin        = "admin"
	kindAudit        = "audit"
)

// Store is an in-memory store.Store implementation.
//
// Safe for concurrent use.
type Store struct {
	clk clock.Clock

	mu sync.RWMutex

	// seq holds per-entity auto-increment counters.
	seq map[string]uint64

	sites     map[string]*domain.Site // keyed by AppID
	siteOrder []string

	accounts        map[uint64]*domain.Account
	accountOrder    []uint64
	accountByClient map[string]uint64 // siteID + "\x00" + v10ClientID -> account id

	plans      map[uint64]*domain.Plan
	planOrder  []uint64
	planByCode map[string]uint64

	subscriptions map[uint64]*domain.Subscription
	subOrder      []uint64
	subByHost     map[uint64]uint64 // v10_host_id -> subscription id

	keys      map[uint64]*domain.APIKey
	keyOrder  []uint64
	keyByHash map[string]uint64

	channels     map[uint64]*domain.Channel
	channelOrder []uint64
	health       map[uint64]*domain.ChannelHealth

	models     map[string]*domain.Model // keyed by ModelName
	modelOrder []string

	usage          []*domain.UsageRecord
	usageByRequest map[string]struct{}

	ledger      []*domain.CreditLedgerEntry
	ledgerByRef map[string]struct{}

	admins     map[uint64]*domain.AdminUser
	adminOrder []uint64

	audit []*domain.AuditLog

	flags map[string]*domain.FlagValue // keyed by flagKey(key, scope)

	aggregates map[string]*domain.DailyUsage // keyed by aggregateID
}

// New returns an empty in-memory store ready for use.
func New() *Store { return NewWithClock(clock.System{}) }

// NewWithClock returns an empty in-memory store that stamps entity timestamps
// with c. A nil clock falls back to clock.System.
func NewWithClock(c clock.Clock) *Store {
	if c == nil {
		c = clock.System{}
	}
	return &Store{
		clk: c,

		seq: make(map[string]uint64),

		sites:     make(map[string]*domain.Site),
		siteOrder: make([]string, 0, 8),

		accounts:        make(map[uint64]*domain.Account),
		accountOrder:    make([]uint64, 0, 16),
		accountByClient: make(map[string]uint64),

		plans:      make(map[uint64]*domain.Plan),
		planOrder:  make([]uint64, 0, 8),
		planByCode: make(map[string]uint64),

		subscriptions: make(map[uint64]*domain.Subscription),
		subOrder:      make([]uint64, 0, 16),
		subByHost:     make(map[uint64]uint64),

		keys:      make(map[uint64]*domain.APIKey),
		keyOrder:  make([]uint64, 0, 32),
		keyByHash: make(map[string]uint64),

		channels:     make(map[uint64]*domain.Channel),
		channelOrder: make([]uint64, 0, 8),
		health:       make(map[uint64]*domain.ChannelHealth),

		models:     make(map[string]*domain.Model),
		modelOrder: make([]string, 0, 32),

		usage:          make([]*domain.UsageRecord, 0, 64),
		usageByRequest: make(map[string]struct{}),

		ledger:      make([]*domain.CreditLedgerEntry, 0, 64),
		ledgerByRef: make(map[string]struct{}),

		admins:     make(map[uint64]*domain.AdminUser),
		adminOrder: make([]uint64, 0, 8),

		audit: make([]*domain.AuditLog, 0, 64),

		flags: make(map[string]*domain.FlagValue),

		aggregates: make(map[string]*domain.DailyUsage),
	}
}

// now returns the current time from the injected clock. A zero-valued Store
// degrades to the system clock instead of panicking.
func (s *Store) now() time.Time {
	if s.clk == nil {
		return time.Now()
	}
	return s.clk.Now()
}

// initMapsLocked lazily creates the maps a write path needs.
//
// New always initialises them, so in normal use this is a no-op; it keeps a
// zero-valued Store from panicking on a nil-map write. The caller must hold the
// write lock.
func (s *Store) initMapsLocked() {
	if s.seq == nil {
		s.seq = make(map[string]uint64)
	}
	if s.sites == nil {
		s.sites = make(map[string]*domain.Site)
	}
	if s.accounts == nil {
		s.accounts = make(map[uint64]*domain.Account)
	}
	if s.accountByClient == nil {
		s.accountByClient = make(map[string]uint64)
	}
	if s.plans == nil {
		s.plans = make(map[uint64]*domain.Plan)
	}
	if s.planByCode == nil {
		s.planByCode = make(map[string]uint64)
	}
	if s.subscriptions == nil {
		s.subscriptions = make(map[uint64]*domain.Subscription)
	}
	if s.subByHost == nil {
		s.subByHost = make(map[uint64]uint64)
	}
	if s.keys == nil {
		s.keys = make(map[uint64]*domain.APIKey)
	}
	if s.keyByHash == nil {
		s.keyByHash = make(map[string]uint64)
	}
	if s.channels == nil {
		s.channels = make(map[uint64]*domain.Channel)
	}
	if s.health == nil {
		s.health = make(map[uint64]*domain.ChannelHealth)
	}
	if s.models == nil {
		s.models = make(map[string]*domain.Model)
	}
	if s.usageByRequest == nil {
		s.usageByRequest = make(map[string]struct{})
	}
	if s.ledgerByRef == nil {
		s.ledgerByRef = make(map[string]struct{})
	}
	if s.admins == nil {
		s.admins = make(map[uint64]*domain.AdminUser)
	}
	if s.flags == nil {
		s.flags = make(map[string]*domain.FlagValue)
	}
	if s.aggregates == nil {
		s.aggregates = make(map[string]*domain.DailyUsage)
	}
}

// nextID returns the next auto-increment identifier for an entity kind.
// The caller must hold the write lock.
func (s *Store) nextID(kind string) uint64 {
	s.seq[kind]++
	return s.seq[kind]
}

// Sites returns the V10 installation repository.
func (s *Store) Sites() store.SiteRepo { return siteRepo{s} }

// Accounts returns the tenant repository.
func (s *Store) Accounts() store.AccountRepo { return accountRepo{s} }

// Plans returns the package repository.
func (s *Store) Plans() store.PlanRepo { return planRepo{s} }

// Subscriptions returns the V10 host projection repository.
func (s *Store) Subscriptions() store.SubscriptionRepo { return subscriptionRepo{s} }

// Keys returns the API key repository.
func (s *Store) Keys() store.APIKeyRepo { return apiKeyRepo{s} }

// Channels returns the upstream channel repository.
func (s *Store) Channels() store.ChannelRepo { return channelRepo{s} }

// Models returns the model catalogue repository.
func (s *Store) Models() store.ModelRepo { return modelRepo{s} }

// Usage returns the metering repository.
func (s *Store) Usage() store.UsageRepo { return usageRepo{s} }

// Ledger returns the balance book repository.
func (s *Store) Ledger() store.LedgerRepo { return ledgerRepo{s} }

// Admins returns the console operator repository.
func (s *Store) Admins() store.AdminRepo { return adminRepo{s} }

// Audit returns the administrative audit repository.
func (s *Store) Audit() store.AuditRepo { return auditRepo{s} }

// Flags returns the feature switch repository.
func (s *Store) Flags() store.FlagRepo { return flagRepo{s} }

// Aggregates returns the daily rollup repository.
func (s *Store) Aggregates() store.AggregateRepo { return aggregateRepo{s} }

// Ping always succeeds: the memory driver needs no backend.
func (s *Store) Ping(ctx context.Context) error { return ctx.Err() }

// Close releases resources. The memory driver holds none, so Close is an
// idempotent no-op and the store stays usable afterwards.
func (s *Store) Close() error { return nil }
