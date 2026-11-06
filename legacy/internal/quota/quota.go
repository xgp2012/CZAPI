// Package quota implements the credit ledger: pre-hold, settlement and the
// idempotent external credit path (plan §6.4, §6.6, §15.2 #2).
//
// This is the only component allowed to move credits. It is implemented once,
// deliberately, because a defect here costs real money. Two invariants drive
// the design:
//
//  1. Nothing is charged before it is reserved. Prehold reserves against the
//     funding bucket; Settle converts the reservation into a real charge and
//     refunds the difference. A crash between the two loses at most the hold,
//     which expires by TTL.
//  2. External credits are idempotent on (ref_type, ref_id). Replaying a V10
//     voucher any number of times applies the amount exactly once.
//
// Safe for concurrent use.
package quota

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"czapi/internal/billing"
	"czapi/internal/cache"
	"czapi/internal/domain"
	"czapi/internal/metrics"
	"czapi/internal/pkg/clock"
	"czapi/internal/pkg/errs"
	"czapi/internal/pkg/idgen"
	"czapi/internal/queue"
	"czapi/internal/store"
)

// Config tunes the ledger service.
type Config struct {
	// TokenToMicros is the global yuan conversion used for absolute charges.
	TokenToMicros int64
	// EnforceBilling=false meters and records but never deducts (shadow mode).
	EnforceBilling bool
	// ShadowMode additionally writes a shadow usage row for comparison.
	ShadowMode bool
	// HoldTTL bounds how long a reservation may live before it self-expires.
	HoldTTL time.Duration
	// Caps (micro-yuan). Zero disables a cap.
	KeyDailyMicros       int64
	AccountMonthlyMicros int64
	GlobalDailyMicros    int64
	// DefaultRPMLimit etc. are not used here (rate limiting is a middleware
	// concern) but are kept for symmetry with config.Limits.
}

// Service implements billing.Ledger.
type Service struct {
	st     store.Store
	ce     cache.Cache
	q      queue.Queue
	engine billing.Engine
	clk    clock.Clock
	mx     *metrics.Registry
	cfg    Config
	// caps optionally supplies live ceilings that an operator changed through the
	// admin console. When nil, the static Config values are used, so existing
	// deployments and tests behave exactly as before.
	caps CapsSource
}

// CapsSource reports the ceilings in force right now.
//
// Implemented by runtimecfg.Store. Kept as an interface so this package does not
// depend on the admin console wiring.
type CapsSource interface {
	Caps() (keyDaily, accountMonthly, globalDaily int64)
}

// New builds the ledger service.
func New(st store.Store, ce cache.Cache, q queue.Queue, engine billing.Engine, clk clock.Clock, mx *metrics.Registry, cfg Config) *Service {
	if cfg.HoldTTL <= 0 {
		cfg.HoldTTL = 5 * time.Minute
	}
	if cfg.TokenToMicros <= 0 {
		cfg.TokenToMicros = 1
	}
	return &Service{st: st, ce: ce, q: q, engine: engine, clk: clk, mx: mx, cfg: cfg}
}

// ---------------------------------------------------------------------------
// Prehold
// ---------------------------------------------------------------------------

// Prehold reserves funds for a request that is about to be forwarded.
//
// Funding selection follows the key's billing mode:
//   - subscription: reserve ratio-weighted tokens against the period quota
//   - balance:      reserve micro-yuan against the account balance
//   - hybrid:       try the subscription first, fall back to the balance
//
// Daily/monthly caps are evaluated before any reservation is made, so a runaway
// client is stopped even when its balance is healthy (plan §15.2 #2).
func (s *Service) Prehold(ctx context.Context, req billing.PreholdRequest) (billing.Decision, error) {
	d := billing.Decision{}

	// Billing disabled => free, but still allow the request to proceed.
	if !s.cfg.EnforceBilling {
		d.Free = true
		d.Mode = domain.PaidByFree
		return d, nil
	}

	estimate := req.EstimateMicros
	if estimate < 0 {
		estimate = 0
	}

	// --- Caps ---------------------------------------------------------------
	if err := s.checkCaps(ctx, req, estimate); err != nil {
		return d, err
	}

	mode := req.BillingMode
	if !mode.Valid() {
		mode = domain.BillingHybrid
	}

	// A key may be pinned to a subscription; without one it cannot use the
	// subscription bucket at all.
	subID := req.SubscriptionID

	// --- Subscription bucket ------------------------------------------------
	if subID != 0 && (mode == domain.BillingSubscription || mode == domain.BillingHybrid) {
		sub, err := s.st.Subscriptions().GetByID(ctx, subID)
		if err != nil {
			if mode == domain.BillingSubscription {
				return d, errs.InsufficientQuota()
			}
		} else if sub.Status == domain.SubActive {
			budget := sub.Remaining()
			if budget > 0 {
				wantTokens := s.microsToTokens(estimate, req.GroupRatio)
				if wantTokens < 1 {
					wantTokens = 1
				}
				if wantTokens <= budget {
					scope := cache.HoldSubscriptionScope(subID)
					holdID, ok, herr := s.ce.Hold(ctx, scope, wantTokens, budget, s.cfg.HoldTTL)
					if herr != nil {
						return d, errs.Internal("quota: subscription hold", herr)
					}
					if ok {
						d.Mode = domain.PaidBySubscription
						d.SubscriptionID = subID
						d.HoldID = holdID
						d.ChargedTokens = wantTokens
						return d, nil
					}
				}
			}
		}
		if mode == domain.BillingSubscription {
			// Pure subscription keys have no fallback.
			s.observeReject("subscription_exhausted")
			return d, errs.InsufficientQuota()
		}
	}

	// --- Balance bucket ----------------------------------------------------
	if mode == domain.BillingBalance || mode == domain.BillingHybrid {
		acc, err := s.st.Accounts().GetByID(ctx, req.AccountID)
		if err != nil {
			return d, errs.Internal("quota: load account", err)
		}
		budget := acc.BalanceCredit
		if acc.AllowNegative && budget < 0 {
			// A negative-allowed account may still spend; give it a small
			// grace window equal to one estimate rather than unlimited credit.
			budget = estimate
		}
		if budget > 0 {
			scope := cache.HoldBalanceScope(req.AccountID)
			holdID, ok, herr := s.ce.Hold(ctx, scope, estimate, budget, s.cfg.HoldTTL)
			if herr != nil {
				return d, errs.Internal("quota: balance hold", herr)
			}
			if ok {
				d.Mode = domain.PaidByBalance
				d.HoldID = holdID
				d.ChargedMicros = estimate
				d.HeldMicros = estimate
				return d, nil
			}
		}
		s.observeReject("balance_exhausted")
		return d, errs.InsufficientQuota()
	}

	return d, errs.InsufficientQuota()
}

// microsToTokens converts an estimated micro-yuan charge into the ratio-weighted
// token count consumed from a subscription quota.
//
// Because costMicros = ceil(costTokens * tokenToMicros), the inverse is
// approximately costMicros / tokenToMicros. The group ratio is already baked
// into the estimate by the caller, so it is not applied twice.
func (s *Service) microsToTokens(estimateMicros int64, _ float64) int64 {
	if estimateMicros <= 0 {
		return 0
	}
	t := estimateMicros / s.cfg.TokenToMicros
	if t < 1 {
		return 1
	}
	return t
}

// SetCapsSource installs live ceilings. Passing nil restores the static Config
// values. Called once at startup, before the service starts serving traffic.
func (s *Service) SetCapsSource(src CapsSource) {
	if s == nil {
		return
	}
	s.caps = src
}

// resolveCaps resolves the ceilings in force now.
//
// A live source wins; otherwise the configured values apply. A nil source must
// never disable enforcement silently, which is why the fallback is the Config.
func (s *Service) resolveCaps() (keyDaily, accountMonthly, globalDaily int64) {
	if s.caps != nil {
		return s.caps.Caps()
	}
	return s.cfg.KeyDailyMicros, s.cfg.AccountMonthlyMicros, s.cfg.GlobalDailyMicros
}

// checkCaps enforces the per-key daily, per-account monthly and global daily
// spend ceilings. Counters live in the cache and are incremented at settlement.
func (s *Service) checkCaps(ctx context.Context, req billing.PreholdRequest, estimate int64) error {
	now := s.clk.Now()
	day := now.Format("2006-01-02")
	month := now.Format("2006-01")

	keyDaily, accountMonthly, globalDaily := s.resolveCaps()

	if keyDaily > 0 {
		cur, err := s.counterValue(ctx, cache.KeyDailyKey(req.APIKeyID, day))
		if err == nil && cur+estimate > keyDaily {
			s.observeReject("key_daily_cap")
			return errs.InsufficientQuota()
		}
	}
	if accountMonthly > 0 {
		cur, err := s.counterValue(ctx, cache.AccountMonthlyKey(req.AccountID, month))
		if err == nil && cur+estimate > accountMonthly {
			s.observeReject("account_monthly_cap")
			return errs.InsufficientQuota()
		}
	}
	if globalDaily > 0 {
		cur, err := s.counterValue(ctx, cache.GlobalDailyKey(day))
		if err == nil && cur+estimate > globalDaily {
			s.observeReject("global_daily_cap")
			return errs.ChannelUnavailable("service temporarily at capacity")
		}
	}
	return nil
}

// counterValue reads a spend counter. A missing counter reads as zero.
func (s *Service) counterValue(ctx context.Context, key string) (int64, error) {
	raw, found, err := s.ce.Get(ctx, key)
	if err != nil || !found {
		return 0, err
	}
	v, perr := strconv.ParseInt(string(raw), 10, 64)
	if perr != nil {
		return 0, nil
	}
	return v, nil
}

// addSpend increments the spend counters after a settlement.
func (s *Service) addSpend(ctx context.Context, keyID, accountID uint64, amount int64) {
	if amount <= 0 {
		return
	}
	now := s.clk.Now()
	day := now.Format("2006-01-02")
	month := now.Format("2006-01")
	// Keep daily counters for 48h and monthly for 40 days so a late job can
	// still read them without unbounded growth.
	_, _ = s.ce.IncrBy(ctx, cache.KeyDailyKey(keyID, day), amount, 48*time.Hour)
	_, _ = s.ce.IncrBy(ctx, cache.AccountMonthlyKey(accountID, month), amount, 40*24*time.Hour)
	_, _ = s.ce.IncrBy(ctx, cache.GlobalDailyKey(day), amount, 48*time.Hour)
}

// ---------------------------------------------------------------------------
// Settle
// ---------------------------------------------------------------------------

// Settle converts a reservation into an actual charge and records usage.
//
// It is safe to call twice for the same RequestID: the usage insert is
// idempotent on request_id, and releasing an already-released hold is a no-op.
func (s *Service) Settle(ctx context.Context, req billing.SettleRequest) (domain.UsageRecord, error) {
	at := req.At
	if at.IsZero() {
		at = s.clk.Now()
	}
	tokenToMicros := req.TokenToMicros
	if tokenToMicros <= 0 {
		tokenToMicros = s.cfg.TokenToMicros
	}

	cost := s.engine.Cost(req.ModelPricing, req.Usage, tokenToMicros)
	actualMicros := cost.CostMicros
	if !s.cfg.EnforceBilling {
		actualMicros = 0
	}

	rec := domain.UsageRecord{
		RequestID:        req.RequestID,
		AccountID:        req.AccountID,
		SubscriptionID:   req.Decision.SubscriptionID,
		APIKeyID:         req.APIKeyID,
		V10HostID:        req.V10HostID,
		Model:            req.Model,
		ChannelID:        req.ChannelID,
		PromptTokens:     cost.PromptTokens,
		CompletionTokens: cost.CompletionTokens,
		CachedTokens:     cost.CachedTokens,
		CostMicros:       actualMicros,
		BillingMode:      req.Decision.Mode,
		Estimated:        cost.Estimated,
		Partial:          req.Usage.Partial,
		Shadow:           req.Shadow,
		Stream:           req.Stream,
		LatencyMS:        req.LatencyMS,
		TTFTMS:           req.TTFTMS,
		StatusCode:       req.StatusCode,
		ErrorCode:        req.ErrorCode,
		CreatedAt:        at,
	}

	// --- Subscription settlement -------------------------------------------
	if req.Decision.Mode == domain.PaidBySubscription && req.Decision.SubscriptionID != 0 {
		chargedTokens := int64(math.Ceil(cost.CostTokens))
		if chargedTokens < 0 {
			chargedTokens = 0
		}
		if chargedTokens > req.Decision.ChargedTokens {
			// Actual exceeded the reservation. We charge the real amount rather
			// than the reservation, so the period quota may overdraw by at most
			// one request; that is corrected on the next period reset. The
			// difference is reported so operators can see the estimate drift.
			s.observeOverdraw(chargedTokens - req.Decision.ChargedTokens)
		}
		scope := cache.HoldSubscriptionScope(req.Decision.SubscriptionID)
		if _, err := s.ce.Release(ctx, scope, req.Decision.HoldID, chargedTokens); err != nil {
			// A failed release must not lose the charge: fall through and still
			// record usage, then report.
			s.observeReject("release_failed")
		}
		if _, err := s.st.Subscriptions().AddUsage(ctx, req.Decision.SubscriptionID, chargedTokens); err != nil {
			return rec, errs.Internal("quota: add subscription usage", err)
		}
	}

	// --- Balance settlement ------------------------------------------------
	if req.Decision.Mode == domain.PaidByBalance {
		scope := cache.HoldBalanceScope(req.AccountID)
		if _, err := s.ce.Release(ctx, scope, req.Decision.HoldID, actualMicros); err != nil {
			s.observeReject("release_failed")
		}
		if actualMicros > 0 {
			// Debit through the immutable ledger so the balance is auditable and
			// the consumption is replay-safe on request_id.
			entry := &domain.CreditLedgerEntry{
				AccountID:    req.AccountID,
				Type:         domain.LedgerConsume,
				AmountMicros: -actualMicros,
				RefType:      "usage",
				RefID:        req.RequestID,
				Remark:       fmt.Sprintf("usage %s", req.Model),
				CreatedAt:    at,
			}
			if _, _, err := s.st.Ledger().Credit(ctx, entry); err != nil {
				return rec, errs.Internal("quota: debit balance", err)
			}
			s.addSpend(ctx, req.APIKeyID, req.AccountID, actualMicros)
		}
	}

	// Shadow mode writes a comparison row that is never charged.
	if s.cfg.ShadowMode && !req.Shadow {
		shadowRec := rec
		shadowRec.Shadow = true
		shadowRec.RequestID = rec.RequestID + "-shadow"
		shadowRec.CostMicros = cost.CostMicros
		s.enqueueUsage(ctx, shadowRec)
	}

	return rec, nil
}

// Release abandons a reservation without charging (upstream failure before any
// billable work). It is a no-op when the hold was never taken.
func (s *Service) Release(ctx context.Context, d billing.Decision, accountID uint64) {
	if d.HoldID == "" {
		return
	}
	switch d.Mode {
	case domain.PaidBySubscription:
		_, _ = s.ce.Release(ctx, cache.HoldSubscriptionScope(d.SubscriptionID), d.HoldID, 0)
	case domain.PaidByBalance:
		_, _ = s.ce.Release(ctx, cache.HoldBalanceScope(accountID), d.HoldID, 0)
	}
}

// RecordUsage publishes a settled record to the accounting worker, degrading to
// a synchronous insert when the queue is unavailable (plan §6.6).
func (s *Service) RecordUsage(ctx context.Context, rec domain.UsageRecord) error {
	if s.q == nil {
		_, err := s.st.Usage().Insert(ctx, &rec)
		return err
	}
	payload, err := marshalUsage(rec)
	if err != nil {
		_, ierr := s.st.Usage().Insert(ctx, &rec)
		return ierr
	}
	if perr := s.q.Publish(ctx, queue.TopicUsage, payload); perr != nil {
		// Queue down: never lose the charge, write synchronously.
		if _, ierr := s.st.Usage().Insert(ctx, &rec); ierr != nil {
			return errs.Internal("quota: persist usage", ierr)
		}
		s.mx.SetBillingLag(0)
		return nil
	}
	return nil
}

func (s *Service) enqueueUsage(ctx context.Context, rec domain.UsageRecord) {
	if err := s.RecordUsage(ctx, rec); err != nil {
		// Best effort for the shadow row only.
		_ = err
	}
}

// ---------------------------------------------------------------------------
// Credit (external vouchers)
// ---------------------------------------------------------------------------

// Credit applies a V10-driven balance movement.
//
// Hard rule (plan §5.4): every credit must carry ref_type and ref_id, and the
// pair is unique. A request without a voucher is rejected outright so no
// endpoint can accidentally mint credit.
func (s *Service) Credit(ctx context.Context, req billing.CreditRequest) (billing.CreditResult, error) {
	var out billing.CreditResult

	if req.RefType == "" || req.RefID == "" {
		return out, errs.New(400, errs.CodeInvalidRequest, "ref_type and ref_id are required for every credit")
	}
	if req.Kind == "" {
		return out, errs.New(400, errs.CodeInvalidRequest, "kind is required")
	}
	if !req.Kind.Valid() {
		return out, errs.New(400, errs.CodeInvalidRequest, "unknown credit kind: "+string(req.Kind))
	}
	if req.AmountMicros == 0 {
		return out, errs.New(400, errs.CodeInvalidRequest, "amount_micros must be non-zero")
	}
	if req.V10ClientID == 0 {
		return out, errs.New(400, errs.CodeInvalidRequest, "v10_client_id is required")
	}

	acc, err := s.resolveAccount(ctx, req.SiteID, req.V10ClientID)
	if err != nil {
		return out, err
	}
	out.AccountID = acc.ID

	// Refunds and manual deductions may push the balance negative so the debt is
	// visible and offset by the next top-up; top-ups never may.
	allowNegative := req.AllowNegative || req.Kind == domain.LedgerRefund || req.AmountMicros < 0

	remark := req.Remark
	if allowNegative {
		// domain.CreditLedgerEntry carries no per-entry negative flag, so the
		// permission travels on the entry itself. Both drivers honour this
		// marker (see store/memstore and store/mysqlstore), which keeps a
		// refund from being rejected as insufficient quota.
		remark = markAllowNegative(remark)
	}

	entry := &domain.CreditLedgerEntry{
		AccountID:    acc.ID,
		Type:         req.Kind,
		AmountMicros: req.AmountMicros,
		RefType:      req.RefType,
		RefID:        req.RefID,
		Remark:       remark,
		CreatedAt:    s.clk.Now(),
	}

	applied, balanceAfter, cerr := s.st.Ledger().Credit(ctx, entry)
	if cerr != nil {
		if e, ok := cerr.(*errs.Error); ok && e.Code == errs.CodeInsufficientQuota {
			s.mx.ObserveCredit(string(req.Kind), "insufficient")
			return out, cerr
		}
		return out, errs.Internal("quota: apply credit", cerr)
	}
	if applied {
		s.mx.ObserveCredit(string(req.Kind), "applied")
	} else {
		s.mx.ObserveCredit(string(req.Kind), "duplicate")
	}

	out.Applied = applied
	out.BalanceAfter = balanceAfter
	out.LedgerEntryID = entry.ID
	return out, nil
}

// resolveAccount looks up (or creates) the account for a V10 client.
func (s *Service) resolveAccount(ctx context.Context, siteID string, clientID uint64) (*domain.Account, error) {
	acc, err := s.st.Accounts().GetByV10Client(ctx, siteID, clientID)
	if err == nil && acc != nil {
		return acc, nil
	}
	if !isNotFound(err) {
		return nil, errs.Internal("quota: load account", err)
	}
	// Auto-provision on first contact so a V10 order can credit an account that
	// never called the gateway before.
	created, uerr := s.st.Accounts().Upsert(ctx, &domain.Account{
		SiteID:      siteID,
		V10ClientID: clientID,
		Status:      domain.AccountEnabled,
	})
	if uerr != nil {
		return nil, errs.Internal("quota: provision account", uerr)
	}
	return created, nil
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(*errs.Error); ok {
		return e.Code == errs.CodeNotFound
	}
	return false
}

// Balance returns the account's current micro-yuan balance.
func (s *Service) Balance(ctx context.Context, accountID uint64) (int64, error) {
	acc, err := s.st.Accounts().GetByID(ctx, accountID)
	if err != nil {
		return 0, err
	}
	return acc.BalanceCredit, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (s *Service) observeReject(reason string) { s.mx.ObserveQuotaRejected(reason) }

// observeOverdraw records that a settled request consumed more than its
// reservation. A sustained non-zero rate means the estimator is too optimistic
// and the pre-hold should be raised.
func (s *Service) observeOverdraw(tokens int64) {
	if tokens <= 0 {
		return
	}
	if s.mx != nil {
		s.mx.ObserveQuotaRejected("estimate_overdraw")
	}
}

// NewRequestID returns a fresh correlation id for a request.
func NewRequestID() string { return idgen.ULID() }

// AllowNegativeMarker is the token appended to a ledger entry's remark when the
// entry is permitted to push the balance below zero. It is the only entry-level
// signal available because domain.CreditLedgerEntry carries no such flag, and it
// is matched as a substring so the remark stays readable.
const AllowNegativeMarker = "allow_negative"

// markAllowNegative appends the marker to a remark exactly once.
func markAllowNegative(remark string) string {
	if strings.Contains(remark, AllowNegativeMarker) {
		return remark
	}
	if remark == "" {
		return AllowNegativeMarker
	}
	return remark + "|" + AllowNegativeMarker
}
