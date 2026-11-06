package mysqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"czapi/internal/domain"
	"czapi/internal/pkg/errs"
	"czapi/internal/store"
)

// ledgerRepo implements store.LedgerRepo against MySQL.
type ledgerRepo struct{ s *Store }

// Credit applies e.AmountMicros to the account balance and appends a ledger
// entry, atomically and idempotently on (RefType, RefID).
//
// The whole operation is one transaction:
//
//  1. INSERT the ledger row; uk_ref (ref_type, ref_id) makes a replay fail with
//     errno 1062.
//  2. On 1062: roll back, re-read the account balance and return applied=false.
//  3. Otherwise lock the account row (SELECT ... FOR UPDATE), enforce the
//     negative-balance rule, then move the balance with a relative UPDATE so no
//     concurrent writer can lose an update.
//  4. Write the resulting balance back onto the ledger row and commit.
//
// The account lock is always taken *after* the ledger insert, so two concurrent
// replays of the same voucher serialise on uk_ref (one wins, one sees 1062) and
// two concurrent distinct vouchers serialise on the account row.
func (r ledgerRepo) Credit(ctx context.Context, e *domain.CreditLedgerEntry) (bool, int64, error) {
	if err := ctx.Err(); err != nil {
		return false, 0, err
	}
	if e == nil {
		return false, 0, errs.InvalidRequest("ledger entry is required")
	}
	if e.RefType == "" || e.RefID == "" {
		return false, 0, errs.InvalidRequest("ledger entry requires ref_type and ref_id")
	}
	if e.AccountID == 0 {
		return false, 0, errs.InvalidRequest("ledger entry requires account_id")
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = r.s.now()
	}

	tx, err := r.s.begin(ctx)
	if err != nil {
		return false, 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// 1. Insert the ledger row. uk_ref rejects a replayed voucher.
	insertSQL, insertArgs := creditInsertSQL(e)
	res, err := tx.ExecContext(ctx, insertSQL, insertArgs...)
	if err != nil {
		if isDuplicateKey(err) {
			// Replay: nothing was applied. Report the current balance.
			_ = tx.Rollback()
			committed = true // nothing more to do; the deferred rollback is a no-op
			balance, bErr := r.s.accountBalance(ctx, e.AccountID)
			if bErr != nil {
				return false, 0, bErr
			}
			return false, balance, nil
		}
		return false, 0, wrapDB("mysqlstore: insert ledger entry", err)
	}
	ledgerID, err := res.LastInsertId()
	if err != nil {
		return false, 0, wrapDB("mysqlstore: ledger id", err)
	}

	// 2. Lock the account row and read the fields the balance rule needs.
	lockSQL := creditLockAccountSQL()
	var (
		balance       int64
		allowNegative bool
	)
	err = tx.QueryRowContext(ctx, lockSQL, e.AccountID).Scan(&balance, &allowNegative)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, 0, errs.NotFound("account not found")
	case err != nil:
		return false, 0, wrapDB("mysqlstore: lock account", err)
	}

	// 3. Enforce the negative-balance rule before touching anything.
	next := balance + e.AmountMicros
	if next < 0 && !allowNegative && !ledgerAllowsNegative(e) {
		return false, balance, errs.InsufficientQuota()
	}

	addSQL := creditAddBalanceSQL()
	if _, err := tx.ExecContext(ctx, addSQL, e.AmountMicros, e.AccountID); err != nil {
		return false, 0, wrapDB("mysqlstore: update account balance", err)
	}

	// 4. Patch the ledger row with the balance the account now holds.
	if _, err := tx.ExecContext(ctx, creditSetBalanceAfterSQL(), next, ledgerID); err != nil {
		return false, 0, wrapDB("mysqlstore: update ledger balance_after", err)
	}

	if err := tx.Commit(); err != nil {
		return false, 0, wrapDB("mysqlstore: commit credit", err)
	}
	committed = true

	e.ID = uint64(ledgerID)
	e.BalanceAfter = next
	return true, next, nil
}

// ledgerAllowsNegative mirrors the memstore convention: a single settlement
// entry may explicitly opt into a negative balance through its Remark, because
// the frozen domain type has no dedicated flag and plan §5.3 allows the
// overspend case to push the balance below zero.
func ledgerAllowsNegative(e *domain.CreditLedgerEntry) bool {
	return strings.Contains(e.Remark, allowNegativeMarker)
}

// allowNegativeMarker is the token a ledger entry carries when it may push the
// balance below zero (see quota.Credit). It is matched as a substring so the
// entry keeps its human-readable remark next to the marker.
const allowNegativeMarker = "allow_negative"

// List returns ledger entries matching f, newest first.
func (r ledgerRepo) List(ctx context.Context, f store.LedgerFilter) ([]domain.CreditLedgerEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q, args := ledgerListSQL(f)
	return scanRows(ctx, r.s.db, q, args, func(rows *sql.Rows) (domain.CreditLedgerEntry, error) {
		var (
			e    domain.CreditLedgerEntry
			kind string
		)
		err := rows.Scan(
			&e.ID, &e.AccountID, &kind, &e.AmountMicros, &e.BalanceAfter,
			&e.RefType, &e.RefID, &e.Remark, &e.CreatedAt,
		)
		e.Type = domain.LedgerType(kind)
		return e, err
	})
}

// SumByTypeBetween returns the signed sum of amounts for an account's entries of
// type t in [from, to).
func (r ledgerRepo) SumByTypeBetween(ctx context.Context, accountID uint64, t domain.LedgerType, from, to time.Time) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	q, args := ledgerSumSQL(accountID, t, from.UTC(), to.UTC())
	var sum int64
	if err := r.s.db.QueryRowContext(ctx, q, args...).Scan(&sum); err != nil {
		return 0, wrapDB("mysqlstore: sum ledger", err)
	}
	return sum, nil
}

// usageRepo implements store.UsageRepo against MySQL.
type usageRepo struct{ s *Store }

// Insert stores a metering record, idempotently on request_id.
//
// RowsAffected is 1 for a real insert and 0 when ON DUPLICATE KEY UPDATE fired,
// which is exactly the created flag the contract asks for.
func (r usageRepo) Insert(ctx context.Context, u *domain.UsageRecord) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if u == nil || u.RequestID == "" {
		return false, errs.InvalidRequest("request_id is required")
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = r.s.now()
	}
	q, args := usageInsertSQL(u)
	res, err := r.s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return false, wrapDB("mysqlstore: insert usage", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, wrapDB("mysqlstore: usage rows affected", err)
	}
	return affected > 0, nil
}

// List returns usage rows matching f, newest first.
func (r usageRepo) List(ctx context.Context, f store.UsageFilter) ([]domain.UsageRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q, args := usageListSQL(f)
	return scanRows(ctx, r.s.db, q, args, scanUsage)
}

// scanUsage reads one cz_usage_record row in usageColumns order.
func scanUsage(rows *sql.Rows) (domain.UsageRecord, error) {
	var (
		u                 domain.UsageRecord
		estimated         bool
		partial           bool
		shadow            bool
		stream            bool
		billingModeNumber int8
	)
	err := rows.Scan(
		&u.ID, &u.RequestID, &u.AccountID, &u.SubscriptionID, &u.APIKeyID,
		&u.V10HostID, &u.Model, &u.ChannelID, &u.PromptTokens, &u.CompletionTokens,
		&u.CachedTokens, &u.CostMicros, &billingModeNumber, &estimated, &partial,
		&shadow, &stream, &u.LatencyMS, &u.TTFTMS, &u.StatusCode, &u.ErrorCode,
		&u.CreatedAt,
	)
	u.BillingMode = domain.UsageBillingMode(billingModeNumber)
	u.Estimated = estimated
	u.Partial = partial
	u.Shadow = shadow
	u.Stream = stream
	return u, err
}

// Summary aggregates the rows selected by f.
func (r usageRepo) Summary(ctx context.Context, f store.UsageFilter) (domain.UsageSummary, error) {
	if err := ctx.Err(); err != nil {
		return domain.UsageSummary{}, err
	}
	q, args := usageSummarySQL(f)
	var sum domain.UsageSummary
	err := r.s.db.QueryRowContext(ctx, q, args...).Scan(
		&sum.Requests, &sum.PromptTokens, &sum.CompletionTokens, &sum.CostMicros,
	)
	if err != nil {
		return domain.UsageSummary{}, wrapDB("mysqlstore: usage summary", err)
	}
	return sum, nil
}

// SumCostByKeyBetween returns consumed micros for a key in [from, to).
func (r usageRepo) SumCostByKeyBetween(ctx context.Context, apiKeyID uint64, from, to time.Time) (int64, error) {
	return r.sumCost(ctx, "api_key_id", apiKeyID, from, to)
}

// SumCostByAccountBetween returns consumed micros for an account in [from, to).
func (r usageRepo) SumCostByAccountBetween(ctx context.Context, accountID uint64, from, to time.Time) (int64, error) {
	return r.sumCost(ctx, "account_id", accountID, from, to)
}

// SumCostGlobalBetween returns site-wide consumed micros in [from, to).
func (r usageRepo) SumCostGlobalBetween(ctx context.Context, from, to time.Time) (int64, error) {
	return r.sumCost(ctx, "", 0, from, to)
}

// sumCost runs the shared spend-cap query for one optional dimension.
func (r usageRepo) sumCost(ctx context.Context, column string, id uint64, from, to time.Time) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	where, args := usageWindowWhere(column, id)
	// Window bounds come first so they line up with "created_at >= ? AND < ?"
	// which usageWindowWhere always emits before the dimension predicate.
	args = append([]any{from.UTC(), to.UTC()}, args...)
	q, qArgs := usageCostSumSQL(where, args)
	var total int64
	if err := r.s.db.QueryRowContext(ctx, q, qArgs...).Scan(&total); err != nil {
		return 0, wrapDB("mysqlstore: sum usage cost", err)
	}
	return total, nil
}

// subscriptionRepo implements store.SubscriptionRepo against MySQL.
type subscriptionRepo struct{ s *Store }

// Create stores a subscription, idempotently on v10_host_id.
//
// ON DUPLICATE KEY UPDATE id = id keeps uk_host authoritative: RowsAffected == 0
// means the host already exists, so the stored row is re-read and returned with
// created=false.
func (r subscriptionRepo) Create(ctx context.Context, v *domain.Subscription) (*domain.Subscription, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if v == nil || v.V10HostID == 0 {
		return nil, false, errs.InvalidRequest("subscription v10_host_id is required")
	}
	now := r.s.now()
	in := *v
	if in.Status == "" {
		in.Status = domain.SubActive
	}
	if in.BillingMode == "" {
		in.BillingMode = domain.BillingHybrid
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	in.UpdatedAt = now

	q, args := subscriptionInsertSQL(&in)
	res, err := r.s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return nil, false, wrapDB("mysqlstore: insert subscription", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, false, wrapDB("mysqlstore: subscription rows affected", err)
	}

	// Always return the stored row: it is authoritative for both paths, and on
	// the insert path it carries the generated id.
	created := affected > 0
	stored, err := r.getByHost(ctx, v.V10HostID)
	if err != nil {
		return nil, false, err
	}
	if created {
		v.ID = stored.ID
		v.CreatedAt = stored.CreatedAt
		v.UpdatedAt = stored.UpdatedAt
		v.Status = stored.Status
		v.BillingMode = stored.BillingMode
	}
	return stored, created, nil
}

// GetByID returns the subscription with the given id.
func (r subscriptionRepo) GetByID(ctx context.Context, id uint64) (*domain.Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return queryOne(ctx, r.s.db, subscriptionByIDSQL(), []any{id}, scanSubscription,
		"mysqlstore: get subscription")
}

// GetByV10Host returns the subscription mirroring a V10 host.
func (r subscriptionRepo) GetByV10Host(ctx context.Context, hostID uint64) (*domain.Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.getByHost(ctx, hostID)
}

// getByHost is GetByV10Host without the context pre-check.
func (r subscriptionRepo) getByHost(ctx context.Context, hostID uint64) (*domain.Subscription, error) {
	return queryOne(ctx, r.s.db, subscriptionByHostSQL(), []any{hostID}, scanSubscription,
		"mysqlstore: get subscription by host")
}

// scanSubscription reads one cz_subscription row in subscriptionColumns order.
func scanSubscription(rows *sql.Rows) (domain.Subscription, error) {
	var (
		v           domain.Subscription
		billingMode string
		status      string
	)
	err := rows.Scan(
		&v.ID, &v.AccountID, &v.V10HostID, &v.PlanID, &v.PeriodStart, &v.PeriodEnd,
		&v.QuotaTotal, &v.QuotaUsed, &billingMode, &status, &v.CreatedAt, &v.UpdatedAt,
	)
	v.BillingMode = domain.BillingMode(billingMode)
	v.Status = domain.SubscriptionStatus(status)
	return v, err
}

// ListByAccount returns an account's subscriptions, soonest expiry first.
func (r subscriptionRepo) ListByAccount(ctx context.Context, accountID uint64) ([]domain.Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return scanRows(ctx, r.s.db, subscriptionListByAccountSQL(), []any{accountID}, scanSubscription)
}

// Update replaces an existing subscription, matched by id.
func (r subscriptionRepo) Update(ctx context.Context, v *domain.Subscription) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if v == nil || v.ID == 0 {
		return errs.InvalidRequest("subscription id is required")
	}
	if _, err := r.s.db.ExecContext(ctx, subscriptionUpdateSQL(), subscriptionUpdateArgs(v)...); err != nil {
		return wrapDB("mysqlstore: update subscription", err)
	}
	return nil
}

// AddUsage atomically adds tokens to quota_used and returns the new value.
func (r subscriptionRepo) AddUsage(ctx context.Context, id uint64, tokens int64) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	// The relative UPDATE is atomic under InnoDB's row lock; the follow-up
	// SELECT reads the value this transaction committed.
	if _, err := r.s.db.ExecContext(ctx, subscriptionAddUsageSQL(), tokens, r.s.now(), id); err != nil {
		return 0, wrapDB("mysqlstore: add subscription usage", err)
	}
	// The follow-up SELECT is unconditional: RowsAffected cannot distinguish
	// "no such row" from "the value did not change" (tokens == 0), so the
	// existence check has to come from reading the row.
	var used int64
	err := r.s.db.QueryRowContext(ctx, subscriptionQuotaUsedSQL(), id).Scan(&used)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errs.NotFound("subscription not found")
	}
	if err != nil {
		return 0, wrapDB("mysqlstore: read subscription usage", err)
	}
	return used, nil
}

// ResetPeriod sets a new period, replaces the quota total and clears usage.
func (r subscriptionRepo) ResetPeriod(ctx context.Context, id uint64, start, end time.Time, quotaTotal int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := r.s.db.ExecContext(ctx, subscriptionResetPeriodSQL(),
		start, end, quotaTotal, r.s.now(), id); err != nil {
		return wrapDB("mysqlstore: reset subscription period", err)
	}
	return nil
}

// ListExpired returns active subscriptions whose period_end is at or before at.
func (r subscriptionRepo) ListExpired(ctx context.Context, at time.Time) ([]domain.Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return scanRows(ctx, r.s.db, subscriptionExpiredSQL(), []any{at}, scanSubscription)
}

// apiKeyRepo implements store.APIKeyRepo against MySQL.
type apiKeyRepo struct{ s *Store }

// Create stores a new API key.
func (r apiKeyRepo) Create(ctx context.Context, k *domain.APIKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if k == nil || k.KeyHash == "" {
		return errs.InvalidRequest("key hash is required")
	}
	now := r.s.now()
	in := *k
	if in.Status == "" {
		in.Status = domain.KeyActive
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	in.UpdatedAt = now

	args, err := keyInsertArgs(&in)
	if err != nil {
		return err
	}
	res, err := r.s.db.ExecContext(ctx, keyInsertSQL(), args...)
	if err != nil {
		return wrapDB("mysqlstore: insert api key", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return wrapDB("mysqlstore: api key id", err)
	}
	k.ID = uint64(id)
	k.Status = in.Status
	k.CreatedAt = in.CreatedAt
	k.UpdatedAt = in.UpdatedAt
	return nil
}

// GetByID returns the API key with the given id.
func (r apiKeyRepo) GetByID(ctx context.Context, id uint64) (*domain.APIKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return queryOne(ctx, r.s.db, keyByIDSQL(), []any{id}, scanAPIKey, "mysqlstore: get api key")
}

// GetByHash returns the API key with the given lookup hash.
func (r apiKeyRepo) GetByHash(ctx context.Context, keyHash string) (*domain.APIKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return queryOne(ctx, r.s.db, keyByHashSQL(), []any{keyHash}, scanAPIKey,
		"mysqlstore: get api key by hash")
}

// scanAPIKey reads one cz_api_key row in keyColumns order.
func scanAPIKey(rows *sql.Rows) (domain.APIKey, error) {
	var (
		k        domain.APIKey
		models   jsonColumn
		ips      jsonColumn
		expires  sql.NullTime
		status   string
		lastUsed sql.NullTime
	)
	err := rows.Scan(
		&k.ID, &k.AccountID, &k.SubscriptionID, &k.KeyHash, &k.KeyPrefix, &k.Name,
		&models, &ips, &expires, &status, &lastUsed, &k.CreatedAt, &k.UpdatedAt,
	)
	if err != nil {
		return domain.APIKey{}, err
	}
	k.Status = domain.APIKeyStatus(status)
	k.ExpiresAt = fromNullTime(expires)
	k.LastUsedAt = fromNullTime(lastUsed)
	if k.Models, err = models.decodeSlice(); err != nil {
		return domain.APIKey{}, err
	}
	if k.IPWhitelist, err = ips.decodeSlice(); err != nil {
		return domain.APIKey{}, err
	}
	return k, nil
}

// Update replaces an existing API key, matched by id.
func (r apiKeyRepo) Update(ctx context.Context, k *domain.APIKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if k == nil || k.ID == 0 {
		return errs.InvalidRequest("api key id is required")
	}
	args, err := keyUpdateArgs(k)
	if err != nil {
		return err
	}
	if _, err := r.s.db.ExecContext(ctx, keyUpdateSQL(), args...); err != nil {
		return wrapDB("mysqlstore: update api key", err)
	}
	return nil
}

// ListByAccount returns an account's keys, newest creation first.
func (r apiKeyRepo) ListByAccount(ctx context.Context, accountID uint64) ([]domain.APIKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return scanRows(ctx, r.s.db, keyListByAccountSQL(), []any{accountID}, scanAPIKey)
}

// ListBySubscription returns the keys bound to a subscription.
func (r apiKeyRepo) ListBySubscription(ctx context.Context, subscriptionID uint64) ([]domain.APIKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return scanRows(ctx, r.s.db, keyListBySubscriptionSQL(), []any{subscriptionID}, scanAPIKey)
}

// CountBySubscription returns how many non-revoked keys a subscription owns.
func (r apiKeyRepo) CountBySubscription(ctx context.Context, subscriptionID uint64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	n, err := countQuery(ctx, r.s.db, keyCountBySubscriptionSQL(), subscriptionID)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// RevokeBySubscription revokes every non-revoked key of a subscription and
// returns the number of keys flipped.
func (r apiKeyRepo) RevokeBySubscription(ctx context.Context, subscriptionID uint64) (int, error) {
	return r.setStatusBySubscription(ctx, subscriptionID, domain.KeyRevoked)
}

// SetStatusBySubscription flips every non-revoked key of a subscription.
func (r apiKeyRepo) SetStatusBySubscription(ctx context.Context, subscriptionID uint64, status domain.APIKeyStatus) (int, error) {
	if status == "" {
		return 0, errs.InvalidRequest("api key status is required")
	}
	return r.setStatusBySubscription(ctx, subscriptionID, status)
}

// setStatusBySubscription is the shared bulk status update.
//
// One statement does the whole flip: the "status <> 'revoked'" guard means a
// revoked key is never resurrected, and RowsAffected is therefore exactly the
// number of keys the caller changed.
func (r apiKeyRepo) setStatusBySubscription(ctx context.Context, subscriptionID uint64, status domain.APIKeyStatus) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	res, err := r.s.db.ExecContext(ctx, keySetStatusBySubscriptionSQL(),
		string(status), r.s.now(), subscriptionID)
	if err != nil {
		return 0, wrapDB("mysqlstore: set key status by subscription", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, wrapDB("mysqlstore: key rows affected", err)
	}
	return int(affected), nil
}

// TouchLastUsed records the last usage timestamp (best effort).
func (r apiKeyRepo) TouchLastUsed(ctx context.Context, id uint64, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := r.s.db.ExecContext(ctx, keyTouchLastUsedSQL(), at.UTC(), id); err != nil {
		// Best effort: the contract says a failure here must not fail a request.
		return nil
	}
	return nil
}

// Delete removes an API key.
func (r apiKeyRepo) Delete(ctx context.Context, id uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	res, err := r.s.db.ExecContext(ctx, keyDeleteSQL(), id)
	if err != nil {
		return wrapDB("mysqlstore: delete api key", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return wrapDB("mysqlstore: delete rows affected", err)
	}
	if affected == 0 {
		return errs.NotFound("api key not found")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Shared scan helpers
// ---------------------------------------------------------------------------

// queryOne runs a single-row query and maps sql.ErrNoRows to errs.NotFound.
func queryOne[T any](ctx context.Context, db *sql.DB, query string, args []any,
	scan func(*sql.Rows) (T, error), msg string) (*T, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapDB(msg, err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, wrapDB(msg, err)
		}
		return nil, errs.NotFound(msg + ": no rows")
	}
	v, err := scan(rows)
	if err != nil {
		return nil, wrapDB(msg+": scan", err)
	}
	return &v, nil
}
