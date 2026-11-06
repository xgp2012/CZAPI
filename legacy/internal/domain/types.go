// Package domain holds the gateway's persisted entities.
//
// These types are the frozen contract between the store drivers (memory /
// mysql), the billing engine, the relay and the API layers. Field names map to
// the cz_* tables in plan §7; JSON tags are used by the admin/internal APIs.
package domain

import "time"

// ---------------------------------------------------------------------------
// Enums
// ---------------------------------------------------------------------------

// AccountStatus is the lifecycle state of a V10 client mirrored in the gateway.
type AccountStatus int8

const (
	AccountEnabled  AccountStatus = 1
	AccountDisabled AccountStatus = 0
)

// SubscriptionStatus mirrors V10 host states.
type SubscriptionStatus string

const (
	SubActive     SubscriptionStatus = "active"
	SubSuspended  SubscriptionStatus = "suspended"
	SubExpired    SubscriptionStatus = "expired"
	SubTerminated SubscriptionStatus = "terminated"
)

// BillingMode selects which credit bucket a key consumes.
type BillingMode string

const (
	// BillingSubscription draws only from the subscription period quota.
	BillingSubscription BillingMode = "subscription"
	// BillingBalance draws only from the account balance ledger.
	BillingBalance BillingMode = "balance"
	// BillingHybrid draws from the subscription first, then the balance.
	BillingHybrid BillingMode = "hybrid"
)

// Valid reports whether m is a known billing mode.
func (m BillingMode) Valid() bool {
	switch m {
	case BillingSubscription, BillingBalance, BillingHybrid:
		return true
	}
	return false
}

// APIKeyStatus is the lifecycle state of an sk- key.
type APIKeyStatus string

const (
	KeyActive    APIKeyStatus = "active"
	KeySuspended APIKeyStatus = "suspended"
	KeyRevoked   APIKeyStatus = "revoked"
)

// LedgerType classifies a balance-ledger entry.
type LedgerType string

const (
	// LedgerV10Order is a top-up driven by a paid V10 order.
	LedgerV10Order LedgerType = "v10_order"
	// LedgerRedeemCode is a top-up driven by a redeemed card code.
	LedgerRedeemCode LedgerType = "redeem_code"
	// LedgerAdminAdjust is a manual adjustment made by an administrator.
	LedgerAdminAdjust LedgerType = "admin_adjust"
	// LedgerRefund is a negative entry driven by a V10 refund.
	LedgerRefund LedgerType = "refund"
	// LedgerConsume is a negative entry produced by inference usage.
	LedgerConsume LedgerType = "consume"
)

// Valid reports whether t is a known ledger type.
func (t LedgerType) Valid() bool {
	switch t {
	case LedgerV10Order, LedgerRedeemCode, LedgerAdminAdjust, LedgerRefund, LedgerConsume:
		return true
	}
	return false
}

// UsageBillingMode records which bucket paid for a request (plan §7
// cz_usage_record.billing_mode: 1 = subscription quota, 2 = balance).
type UsageBillingMode int8

const (
	PaidBySubscription UsageBillingMode = 1
	PaidByBalance      UsageBillingMode = 2
	PaidByFree         UsageBillingMode = 3 // not billed (shadow/disabled)
)

// ---------------------------------------------------------------------------
// Entities
// ---------------------------------------------------------------------------

// Site is a V10 installation allowed to call /internal/v1. app_id is the
// isolation key for client ids (plan §6.8).
type Site struct {
	ID         uint64    `json:"id"`
	AppID      string    `json:"app_id"`
	Name       string    `json:"name"`
	Secret     string    `json:"-"` // never serialised
	SecretPrev string    `json:"-"`
	IPAllow    []string  `json:"ip_allow"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Account is the gateway-side tenant mapped 1:1 to a V10 client.
type Account struct {
	ID            uint64        `json:"id"`
	SiteID        string        `json:"site_id"` // = site.app_id
	V10ClientID   uint64        `json:"v10_client_id"`
	Email         string        `json:"email"`
	Status        AccountStatus `json:"status"`
	BalanceCredit int64         `json:"balance_credit"` // micro-yuan
	AllowNegative bool          `json:"allow_negative"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

// Plan is a sellable package definition maintained by administrators.
type Plan struct {
	ID          uint64    `json:"id"`
	PlanCode    string    `json:"plan_code"`
	Name        string    `json:"name"`
	QuotaTokens int64     `json:"quota_tokens"`
	Period      string    `json:"period"` // month|quarter|year|once
	RPMLimit    int       `json:"rpm_limit"`
	TPMLimit    int       `json:"tpm_limit"`
	Concurrency int       `json:"concurrency"`
	ModelGroup  string    `json:"model_group"`
	MaxKeys     int       `json:"max_keys"`
	PriceCents  int64     `json:"price_cents"`
	GroupRatio  float64   `json:"group_ratio"`
	Status      int8      `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Subscription is the gateway projection of one V10 host (plan §7 cz_subscription).
type Subscription struct {
	ID          uint64             `json:"id"`
	AccountID   uint64             `json:"account_id"`
	V10HostID   uint64             `json:"v10_host_id"`
	PlanID      uint64             `json:"plan_id"`
	PeriodStart time.Time          `json:"period_start"`
	PeriodEnd   time.Time          `json:"period_end"`
	QuotaTotal  int64              `json:"quota_total"`
	QuotaUsed   int64              `json:"quota_used"`
	BillingMode BillingMode        `json:"billing_mode"`
	Status      SubscriptionStatus `json:"status"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
}

// Remaining returns the tokens left in the current period (never negative).
func (s *Subscription) Remaining() int64 {
	if s.QuotaTotal <= s.QuotaUsed {
		return 0
	}
	return s.QuotaTotal - s.QuotaUsed
}

// APIKey is an sk- credential. Only the hash is persisted (plan §6.4).
type APIKey struct {
	ID             uint64       `json:"id"`
	AccountID      uint64       `json:"account_id"`
	SubscriptionID uint64       `json:"subscription_id"`
	KeyHash        string       `json:"-"`
	KeyPrefix      string       `json:"key_prefix"`
	Name           string       `json:"name"`
	Models         []string     `json:"models"`       // nil => follow the subscription plan
	IPWhitelist    []string     `json:"ip_whitelist"` // CIDR list, empty => no restriction
	ExpiresAt      *time.Time   `json:"expires_at"`
	Status         APIKeyStatus `json:"status"`
	LastUsedAt     *time.Time   `json:"last_used_at"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
}

// ChannelKey is one upstream credential inside a channel's key pool.
type ChannelKey struct {
	// Secret is AES-256-GCM sealed at rest; never exposed by any API.
	Secret   string `json:"secret"`
	Weight   int    `json:"weight"`
	Status   string `json:"status"` // active|disabled
	Cooldown int64  `json:"cooldown_until_unix"`
}

// Channel routes traffic to one upstream base URL (plan §6.5).
type Channel struct {
	ID             uint64            `json:"id"`
	Name           string            `json:"name"`
	Provider       string            `json:"provider"` // openai
	BaseURL        string            `json:"base_url"`
	KeyPool        []ChannelKey      `json:"key_pool"`
	Models         []string          `json:"models"`
	ModelMap       map[string]string `json:"model_map"`
	Weight         int               `json:"weight"`
	Priority       int               `json:"priority"`
	MaxConcurrency int               `json:"max_concurrency"`
	TimeoutMS      int               `json:"timeout_ms"`
	Status         int8              `json:"status"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

// ChannelHealth is the breaker state kept alongside a channel (plan §6.5).
type ChannelHealth struct {
	ChannelID uint64    `json:"channel_id"`
	OpenUntil time.Time `json:"open_until"`
	HalfOpen  bool      `json:"half_open"`
	ErrorRate float64   `json:"error_rate"`
	Samples   int       `json:"samples"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Model is the billable model catalogue entry (plan §6.6 ratio engine).
type Model struct {
	ID              uint64    `json:"id"`
	ModelName       string    `json:"model_name"`
	DisplayName     string    `json:"display_name"`
	GroupName       string    `json:"group_name"`
	ContextLen      int       `json:"context_len"`
	ModelRatio      float64   `json:"model_ratio"`
	CompletionRatio float64   `json:"completion_ratio"`
	CachedRatio     float64   `json:"cached_ratio"`
	InputMicros     int64     `json:"input_micros"`  // optional absolute price override per 1M tokens
	OutputMicros    int64     `json:"output_micros"` // optional absolute price override per 1M tokens
	Capabilities    []string  `json:"capabilities"`
	Status          int8      `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// UsageRecord is one metered request (plan §7 cz_usage_record).
type UsageRecord struct {
	ID               uint64           `json:"id"`
	RequestID        string           `json:"request_id"`
	AccountID        uint64           `json:"account_id"`
	SubscriptionID   uint64           `json:"subscription_id"`
	APIKeyID         uint64           `json:"api_key_id"`
	V10HostID        uint64           `json:"v10_host_id"`
	Model            string           `json:"model"`
	ChannelID        uint64           `json:"channel_id"`
	PromptTokens     int              `json:"prompt_tokens"`
	CompletionTokens int              `json:"completion_tokens"`
	CachedTokens     int              `json:"cached_tokens"`
	CostMicros       int64            `json:"cost_micros"`
	BillingMode      UsageBillingMode `json:"billing_mode"`
	Estimated        bool             `json:"estimated"`
	Partial          bool             `json:"partial"`
	Shadow           bool             `json:"shadow"`
	Stream           bool             `json:"stream"`
	LatencyMS        int              `json:"latency_ms"`
	TTFTMS           int              `json:"ttft_ms"`
	StatusCode       int              `json:"status_code"`
	ErrorCode        string           `json:"error_code"`
	CreatedAt        time.Time        `json:"created_at"`
}

// CreditLedgerEntry is an immutable balance movement. Every non-consume entry
// must carry (RefType, RefID); the pair is unique so V10 retries cannot
// double-credit (plan §5.4).
type CreditLedgerEntry struct {
	ID           uint64     `json:"id"`
	AccountID    uint64     `json:"account_id"`
	Type         LedgerType `json:"type"`
	AmountMicros int64      `json:"amount_micros"` // positive credits, negative debits
	BalanceAfter int64      `json:"balance_after"`
	RefType      string     `json:"ref_type"`
	RefID        string     `json:"ref_id"`
	Remark       string     `json:"remark"`
	CreatedAt    time.Time  `json:"created_at"`
}

// AdminUser is a gateway-console operator (independent of V10 admins).
type AdminUser struct {
	ID           uint64    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	Role         string    `json:"role"` // admin|operator|viewer
	Status       int8      `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// AuditLog records an administrative mutation (plan §12 越权).
type AuditLog struct {
	ID         uint64    `json:"id"`
	ActorType  string    `json:"actor_type"` // admin|internal|system
	ActorID    string    `json:"actor_id"`
	Action     string    `json:"action"`
	ObjectType string    `json:"object_type"`
	ObjectID   string    `json:"object_id"`
	Before     string    `json:"before"`
	After      string    `json:"after"`
	IP         string    `json:"ip"`
	CreatedAt  time.Time `json:"created_at"`
}

// FlagValue is a persisted, hot-reloadable feature switch (plan §15.2 #1).
type FlagValue struct {
	Key   string `json:"key"`
	Value bool   `json:"value"`
	// Scope allows per-account overrides: "" = global, "account:12" = that account.
	Scope     string    `json:"scope"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DailyUsage is a pre-aggregated rollup used by the console dashboards.
type DailyUsage struct {
	Day              string `json:"day"` // YYYY-MM-DD
	AccountID        uint64 `json:"account_id"`
	V10HostID        uint64 `json:"v10_host_id"`
	APIKeyID         uint64 `json:"api_key_id"`
	Model            string `json:"model"`
	Requests         int64  `json:"requests"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	CostMicros       int64  `json:"cost_micros"`
}

// UsageSummary is the aggregate returned by /internal/v1/usage/summary.
type UsageSummary struct {
	Requests         int64 `json:"requests"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	CostMicros       int64 `json:"cost_micros"`
}
