package memstore

import (
	"context"
	"sort"
	"strings"
	"time"

	"czapi/internal/domain"
	"czapi/internal/pkg/errs"
	"czapi/internal/store"
)

// usageRepo implements store.UsageRepo on top of Store.
type usageRepo struct{ s *Store }

// Insert stores a metering record. It is idempotent on RequestID: a duplicate
// insert returns created=false and leaves stored state untouched.
func (r usageRepo) Insert(ctx context.Context, u *domain.UsageRecord) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if u == nil || u.RequestID == "" {
		return false, errs.InvalidRequest("request_id is required")
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()
	if _, ok := r.s.usageByRequest[u.RequestID]; ok {
		return false, nil
	}
	out := copyUsage(u)
	out.ID = r.s.nextID(kindUsage)
	if out.CreatedAt.IsZero() {
		out.CreatedAt = r.s.now()
	}
	r.s.usage = append(r.s.usage, out)
	r.s.usageByRequest[out.RequestID] = struct{}{}
	u.ID = out.ID
	u.CreatedAt = out.CreatedAt
	return true, nil
}

// List returns usage rows matching f, oldest first.
//
// Filter semantics:
//   - zero values mean "no constraint";
//   - SiteID resolves through the owning account, since a usage row carries no
//     site column;
//   - From is inclusive, To is exclusive;
//   - IncludeShadow=false hides shadow rows;
//   - Limit==0 means no limit, and Limit<0 is treated as 0.
func (r usageRepo) List(ctx context.Context, f store.UsageFilter) ([]domain.UsageRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	matched := r.s.filterUsageLocked(f)
	out := make([]domain.UsageRecord, 0, len(matched))
	for i, u := range matched {
		if i < f.Offset {
			continue
		}
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
		out = append(out, *copyUsage(u))
	}
	return out, nil
}

// Summary aggregates the rows selected by f.
func (r usageRepo) Summary(ctx context.Context, f store.UsageFilter) (domain.UsageSummary, error) {
	if err := ctx.Err(); err != nil {
		return domain.UsageSummary{}, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	var sum domain.UsageSummary
	for _, u := range r.s.filterUsageLocked(f) {
		sum.Requests++
		sum.PromptTokens += int64(u.PromptTokens)
		sum.CompletionTokens += int64(u.CompletionTokens)
		sum.CostMicros += u.CostMicros
	}
	return sum, nil
}

// SumCostByKeyBetween returns consumed micros for a key in [from, to).
func (r usageRepo) SumCostByKeyBetween(ctx context.Context, apiKeyID uint64, from, to time.Time) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	var total int64
	for _, u := range r.s.usage {
		if u.Shadow || u.APIKeyID != apiKeyID {
			continue
		}
		if inRange(u.CreatedAt, from, to) {
			total += u.CostMicros
		}
	}
	return total, nil
}

// SumCostByAccountBetween returns consumed micros for an account in [from, to).
func (r usageRepo) SumCostByAccountBetween(ctx context.Context, accountID uint64, from, to time.Time) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	var total int64
	for _, u := range r.s.usage {
		if u.Shadow || u.AccountID != accountID {
			continue
		}
		if inRange(u.CreatedAt, from, to) {
			total += u.CostMicros
		}
	}
	return total, nil
}

// SumCostGlobalBetween returns site-wide consumed micros in [from, to).
func (r usageRepo) SumCostGlobalBetween(ctx context.Context, from, to time.Time) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	var total int64
	for _, u := range r.s.usage {
		if u.Shadow {
			continue
		}
		if inRange(u.CreatedAt, from, to) {
			total += u.CostMicros
		}
	}
	return total, nil
}

// filterUsageLocked returns the stored rows matching f. The caller must hold at
// least the read lock. Rows are returned in insertion (oldest first) order as
// private pointers; callers must not retain them.
func (s *Store) filterUsageLocked(f store.UsageFilter) []*domain.UsageRecord {
	out := make([]*domain.UsageRecord, 0, len(s.usage))
	for _, u := range s.usage {
		if !f.IncludeShadow && u.Shadow {
			continue
		}
		if f.AccountID != 0 && u.AccountID != f.AccountID {
			continue
		}
		if f.V10HostID != 0 && u.V10HostID != f.V10HostID {
			continue
		}
		if f.APIKeyID != 0 && u.APIKeyID != f.APIKeyID {
			continue
		}
		if f.Model != "" && u.Model != f.Model {
			continue
		}
		if f.SiteID != "" {
			a, ok := s.accounts[u.AccountID]
			if !ok || a.SiteID != f.SiteID {
				continue
			}
		}
		if !inRange(u.CreatedAt, f.From, f.To) {
			continue
		}
		out = append(out, u)
	}
	return out
}

// inRange reports whether t falls in [from, to). A zero bound is treated as
// unbounded on that side.
func inRange(t, from, to time.Time) bool {
	if !from.IsZero() && t.Before(from) {
		return false
	}
	if !to.IsZero() && !t.Before(to) {
		return false
	}
	return true
}

// ledgerRepo implements store.LedgerRepo on top of Store.
type ledgerRepo struct{ s *Store }

// ledgerRefKey builds the idempotency key for (ref_type, ref_id).
func ledgerRefKey(refType, refID string) string { return refType + "\x00" + refID }

// Credit applies e.AmountMicros to the account balance and appends a ledger
// entry, atomically and idempotently on (RefType, RefID).
//
// Both RefType and RefID are mandatory (errs.InvalidRequest otherwise).
// Replaying a known (RefType, RefID) returns applied=false with the current
// balance and leaves state untouched. If the resulting balance would be
// negative and neither the entry nor the account permits it, the call fails
// with errs.InsufficientQuota() and changes nothing.
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
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()

	key := ledgerRefKey(e.RefType, e.RefID)
	if _, ok := r.s.ledgerByRef[key]; ok {
		// Replay: report the current balance, apply nothing.
		var balance int64
		if a, ok := r.s.accounts[e.AccountID]; ok {
			balance = a.BalanceCredit
		}
		return false, balance, nil
	}
	if e.AccountID == 0 {
		return false, 0, errs.InvalidRequest("ledger entry requires account_id")
	}
	a, ok := r.s.accounts[e.AccountID]
	if !ok {
		return false, 0, errs.NotFound("account not found")
	}

	next := a.BalanceCredit + e.AmountMicros
	if next < 0 && !a.AllowNegative && !allowsNegative(e) {
		return false, a.BalanceCredit, errs.InsufficientQuota()
	}

	now := r.s.now()
	a.BalanceCredit = next
	a.UpdatedAt = now

	out := copyLedgerEntry(e)
	out.ID = r.s.nextID(kindLedger)
	out.BalanceAfter = next
	if out.CreatedAt.IsZero() {
		out.CreatedAt = now
	}
	r.s.ledger = append(r.s.ledger, out)
	r.s.ledgerByRef[key] = struct{}{}

	e.ID = out.ID
	e.BalanceAfter = out.BalanceAfter
	e.CreatedAt = out.CreatedAt
	return true, next, nil
}

// allowsNegative reports whether a debit entry is explicitly permitted to push
// the balance below zero.
//
// The frozen domain.CreditLedgerEntry has no such flag of its own, so the only
// entry-level signal available is the Remark convention "allow_negative". This
// keeps the account flag authoritative while letting a single settlement entry
// opt in (plan 鎼?.3: "actual overspend may push the balance negative").
func allowsNegative(e *domain.CreditLedgerEntry) bool {
	return strings.Contains(e.Remark, AllowNegativeMarker)
}

// AllowNegativeMarker is the token a ledger entry carries when it is permitted
// to push the balance below zero. It is matched as a substring so the entry can
// still keep a human-readable remark alongside it, e.g.
// "chargeback |allow_negative".
const AllowNegativeMarker = "allow_negative"

// List returns ledger entries matching f, oldest first. Limit==0 means no
// limit; Limit<0 is treated as 0.
func (r ledgerRepo) List(ctx context.Context, f store.LedgerFilter) ([]domain.CreditLedgerEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	out := make([]domain.CreditLedgerEntry, 0, len(r.s.ledger))
	skipped := 0
	for _, e := range r.s.ledger {
		if f.AccountID != 0 && e.AccountID != f.AccountID {
			continue
		}
		if f.Type != "" && e.Type != f.Type {
			continue
		}
		if f.RefType != "" && e.RefType != f.RefType {
			continue
		}
		if !inRange(e.CreatedAt, f.From, f.To) {
			continue
		}
		if skipped < f.Offset {
			skipped++
			continue
		}
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
		out = append(out, *copyLedgerEntry(e))
	}
	return out, nil
}

// SumByTypeBetween returns the signed sum of amounts for an account's entries of
// type t in [from, to).
func (r ledgerRepo) SumByTypeBetween(ctx context.Context, accountID uint64, t domain.LedgerType, from, to time.Time) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	var total int64
	for _, e := range r.s.ledger {
		if e.AccountID != accountID {
			continue
		}
		if t != "" && e.Type != t {
			continue
		}
		if inRange(e.CreatedAt, from, to) {
			total += e.AmountMicros
		}
	}
	return total, nil
}

// aggregateRepo implements store.AggregateRepo on top of Store.
type aggregateRepo struct{ s *Store }

// aggregateID builds the rollup key for (day, account, host, key, model).
func aggregateID(d *domain.DailyUsage) string {
	return d.Day + "\x00" + u64str(d.AccountID) + "\x00" + u64str(d.V10HostID) +
		"\x00" + u64str(d.APIKeyID) + "\x00" + d.Model
}

// UpsertDaily accumulates d into the rollup identified by
// (day, account, host, key, model).
func (r aggregateRepo) UpsertDaily(ctx context.Context, d *domain.DailyUsage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d == nil || d.Day == "" {
		return errs.InvalidRequest("daily usage day is required")
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()
	id := aggregateID(d)
	if cur, ok := r.s.aggregates[id]; ok {
		cur.Requests += d.Requests
		cur.PromptTokens += d.PromptTokens
		cur.CompletionTokens += d.CompletionTokens
		cur.CostMicros += d.CostMicros
		return nil
	}
	r.s.aggregates[id] = copyDaily(d)
	return nil
}

// ListDaily returns rollups matching f, cheapest-to-sort order: day, then the
// grouping columns.
func (r aggregateRepo) ListDaily(ctx context.Context, f store.UsageFilter) ([]domain.DailyUsage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	out := r.s.filterDailyLocked(f)
	if f.Offset > 0 {
		if f.Offset >= len(out) {
			return nil, nil
		}
		out = out[f.Offset:]
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// BucketCost returns one row per day inside f's range, summing the rollups that
// match the other filters.
func (r aggregateRepo) BucketCost(ctx context.Context, f store.UsageFilter) ([]domain.DailyUsage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()

	byDay := make(map[string]*domain.DailyUsage)
	for _, d := range r.s.filterDailyLocked(f) {
		row, ok := byDay[d.Day]
		if !ok {
			row = &domain.DailyUsage{Day: d.Day}
			byDay[d.Day] = row
		}
		row.Requests += d.Requests
		row.PromptTokens += d.PromptTokens
		row.CompletionTokens += d.CompletionTokens
		row.CostMicros += d.CostMicros
	}
	out := make([]domain.DailyUsage, 0, len(byDay))
	for _, row := range byDay {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out, nil
}

// RebuildDaily recomputes the rollups for one UTC day from raw usage rows,
// replacing whatever was stored for that day. Shadow rows never contribute.
func (r aggregateRepo) RebuildDaily(ctx context.Context, day time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	want := dayOf(day)
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()

	for id, d := range r.s.aggregates {
		if d.Day == want {
			delete(r.s.aggregates, id)
		}
	}
	for _, u := range r.s.usage {
		if u.Shadow || dayOf(u.CreatedAt) != want {
			continue
		}
		d := &domain.DailyUsage{
			Day:              want,
			AccountID:        u.AccountID,
			V10HostID:        u.V10HostID,
			APIKeyID:         u.APIKeyID,
			Model:            u.Model,
			Requests:         1,
			PromptTokens:     int64(u.PromptTokens),
			CompletionTokens: int64(u.CompletionTokens),
			CostMicros:       u.CostMicros,
		}
		id := aggregateID(d)
		if cur, ok := r.s.aggregates[id]; ok {
			cur.Requests++
			cur.PromptTokens += d.PromptTokens
			cur.CompletionTokens += d.CompletionTokens
			cur.CostMicros += d.CostMicros
			continue
		}
		r.s.aggregates[id] = d
	}
	return nil
}

// filterDailyLocked returns copies of the rollups matching f, sorted by day then
// grouping columns. The caller must hold at least the read lock.
func (s *Store) filterDailyLocked(f store.UsageFilter) []domain.DailyUsage {
	out := make([]domain.DailyUsage, 0, len(s.aggregates))
	for _, d := range s.aggregates {
		if f.AccountID != 0 && d.AccountID != f.AccountID {
			continue
		}
		if f.V10HostID != 0 && d.V10HostID != f.V10HostID {
			continue
		}
		if f.APIKeyID != 0 && d.APIKeyID != f.APIKeyID {
			continue
		}
		if f.Model != "" && d.Model != f.Model {
			continue
		}
		if f.SiteID != "" {
			a, ok := s.accounts[d.AccountID]
			if !ok || a.SiteID != f.SiteID {
				continue
			}
		}
		if !inDayRange(d.Day, f.From, f.To) {
			continue
		}
		out = append(out, *copyDaily(d))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Day != out[j].Day {
			return out[i].Day < out[j].Day
		}
		if out[i].AccountID != out[j].AccountID {
			return out[i].AccountID < out[j].AccountID
		}
		if out[i].V10HostID != out[j].V10HostID {
			return out[i].V10HostID < out[j].V10HostID
		}
		if out[i].APIKeyID != out[j].APIKeyID {
			return out[i].APIKeyID < out[j].APIKeyID
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// inDayRange reports whether a YYYY-MM-DD day overlaps [from, to). Days are
// compared as dates in UTC, matching the format used for the rollup key.
func inDayRange(day string, from, to time.Time) bool {
	if from.IsZero() && to.IsZero() {
		return true
	}
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		return false
	}
	if !from.IsZero() && d.Before(from.UTC().Truncate(24*time.Hour)) {
		return false
	}
	if !to.IsZero() && !d.Before(to.UTC()) {
		return false
	}
	return true
}
