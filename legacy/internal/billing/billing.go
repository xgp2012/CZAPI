// Package billing defines the metering and pricing contract (plan §6.6).
//
// The formula mirrors new-api:
//
//	cost_tokens = (prompt + completion*completion_ratio - cached_discount)
//	              * model_ratio * group_ratio
//	cost_micros = ceil(cost_tokens * token_to_micros)
//
// Cost is always computed in integer micro-yuan to avoid float drift.
package billing

import (
	"context"
	"time"

	"czapi/internal/domain"
)

// Usage is what the relay observed for one request.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	// Estimated marks token counts derived from a local estimator rather than
	// the upstream usage block.
	Estimated bool
	// Partial marks a stream that ended before [DONE] (client disconnect or
	// upstream truncation).
	Partial bool
}

// Price holds the resolved multipliers for a request.
type Price struct {
	ModelRatio      float64
	CompletionRatio float64
	CachedRatio     float64
	GroupRatio      float64
	// InputMicros / OutputMicros override the ratio formula when non-zero,
	// expressing an absolute price per 1M tokens.
	InputMicros  int64
	OutputMicros int64
}

// Cost is the computed charge for a request.
type Cost struct {
	// CostMicros is the final charge in micro-yuan (>= 0).
	CostMicros int64
	// CostTokens is the intermediate ratio-weighted token count (for audit).
	CostTokens float64
	// Breakdown for the console/logs.
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	Estimated        bool
}

// Engine is the pure pricing calculator. It performs no I/O, so it is fully
// unit-testable and also used by the shadow comparison path.
type Engine interface {
	// Price resolves multipliers for (model, plan) — pure function of inputs.
	Price(model *domain.Model, plan *domain.Plan) Price
	// Cost computes the charge for a usage block.
	Cost(p Price, u Usage, tokenToMicros int64) Cost
	// EstimateTokens approximates a prompt's token count when the upstream
	// omits usage. Implementations must never panic on arbitrary input.
	EstimateTokens(text string) int
}

// Decision describes how a request was funded.
type Decision struct {
	// Mode actually used (subscription/balance/free).
	Mode domain.UsageBillingMode
	// SubscriptionID funded the request when Mode==PaidBySubscription.
	SubscriptionID uint64
	// ChargedMicros is the amount debited from the balance bucket.
	ChargedMicros int64
	// ChargedTokens is the amount debited from the subscription quota.
	ChargedTokens int64
	// HeldMicros is the pre-held amount that must be released.
	HeldMicros int64
	HoldID     string
	// Free means nothing was charged (billing disabled / shadow).
	Free bool
	// Exhausted means the pre-hold could not be satisfied.
	Exhausted bool
}

// Ledger is the settlement contract implemented by the quota/billing service.
// It is the only component allowed to move credits.
type Ledger interface {
	// Prehold reserves funds before forwarding. It must not block longer than
	// a cache round trip.
	Prehold(ctx context.Context, req PreholdRequest) (Decision, error)
	// Settle converts a pre-hold into an actual charge and records usage.
	Settle(ctx context.Context, req SettleRequest) (domain.UsageRecord, error)
	// Credit applies an external voucher (V10 order/redeem code/refund/adjust).
	Credit(ctx context.Context, req CreditRequest) (CreditResult, error)
	// Balance returns the current micro-yuan balance of an account.
	Balance(ctx context.Context, accountID uint64) (int64, error)
}

// PreholdRequest is the input for reserving funds.
type PreholdRequest struct {
	AccountID      uint64
	APIKeyID       uint64
	SubscriptionID uint64
	V10HostID      uint64
	Model          string
	GroupRatio     float64
	// Estimated micros for the request (prompt + max_tokens at list price).
	EstimateMicros int64
	BillingMode    domain.BillingMode
	Plan           *domain.Plan
	KeyModels      []string
}

// SettleRequest finalises a request.
type SettleRequest struct {
	Decision      Decision
	Usage         Usage
	Model         string
	ModelPricing  Price
	TokenToMicros int64
	RequestID     string
	APIKeyID      uint64
	AccountID     uint64
	V10HostID     uint64
	ChannelID     uint64
	Stream        bool
	LatencyMS     int
	TTFTMS        int
	StatusCode    int
	ErrorCode     string
	Shadow        bool
	At            time.Time
}

// CreditRequest is an idempotent balance movement driven by a V10 voucher.
type CreditRequest struct {
	SiteID       string
	V10ClientID  uint64
	Kind         domain.LedgerType
	AmountMicros int64
	RefType      string
	RefID        string
	Remark       string
	// AllowNegative permits the balance to go below zero (refund case).
	AllowNegative bool
}

// CreditResult is the outcome of Credit.
type CreditResult struct {
	Applied       bool   `json:"applied"`
	AccountID     uint64 `json:"account_id"`
	BalanceAfter  int64  `json:"balance_after"`
	LedgerEntryID uint64 `json:"ledger_entry_id"`
}
