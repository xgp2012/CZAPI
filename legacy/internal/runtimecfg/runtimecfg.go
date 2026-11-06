// Package runtimecfg holds settings an operator may change while the gateway is
// running, without editing YAML and restarting.
//
// The design rule is narrow on purpose: only values that are *safe to change at
// runtime* belong here. Anything whose change would invalidate stored data or
// break in-flight traffic (key_hash_salt, storage DSN, cache/queue driver,
// listen address) stays in the file and requires a deliberate restart. Exposing
// those through a web form would turn a single mis-click into "every API key
// stops working" or "the gateway loses its database", which is not a trade an
// operator can make from a browser.
//
// All values are read on the request path, so reads must be cheap and lock-free:
// the whole snapshot is stored in one atomic pointer and swapped on write.
package runtimecfg

import (
	"encoding/json"
	"sync/atomic"
)

// Limits are the money and rate guardrails.
//
// Money is micro-yuan (1 yuan = 1_000_000). Zero disables a cap, matching the
// YAML semantics, because operators rely on 0 meaning "no limit".
type Limits struct {
	// KeyDailyMicros caps one API key's daily spend.
	KeyDailyMicros int64 `json:"key_daily_micros"`
	// AccountMonthlyMicros caps one account's monthly spend.
	AccountMonthlyMicros int64 `json:"account_monthly_micros"`
	// GlobalDailyMicros caps all spend per day.
	GlobalDailyMicros int64 `json:"global_daily_micros"`
	// RPM / TPM / Concurrency are per-key request rate limits.
	RPM         int `json:"rpm"`
	TPM         int `json:"tpm"`
	Concurrency int `json:"concurrency"`
}

// Settings is the mutable snapshot.
type Settings struct {
	Limits Limits `json:"limits"`
}

// Validate rejects values that would break the gateway or invert its intent.
//
// Negative caps are refused rather than treated as "unlimited": a negative cap
// would silently make every request exceed the limit and reject all traffic,
// which is a very confusing outage to debug.
func (s Settings) Validate() error {
	for _, f := range []struct {
		name  string
		value int64
	}{
		{"key_daily_micros", s.Limits.KeyDailyMicros},
		{"account_monthly_micros", s.Limits.AccountMonthlyMicros},
		{"global_daily_micros", s.Limits.GlobalDailyMicros},
	} {
		if f.value < 0 {
			return &ValidationError{Field: f.name, Reason: "不得为负数（用 0 表示不限制）"}
		}
	}
	for _, f := range []struct {
		name  string
		value int
	}{
		{"rpm", s.Limits.RPM},
		{"tpm", s.Limits.TPM},
		{"concurrency", s.Limits.Concurrency},
	} {
		if f.value < 0 {
			return &ValidationError{Field: f.name, Reason: "不得为负数（用 0 表示不限制）"}
		}
	}
	return nil
}

// ValidationError reports a rejected field with an operator-facing reason.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Reason }

// Store hands out the current settings and accepts updates.
//
// It is safe for concurrent use. A nil *Store is valid and returns the zero
// snapshot, so callers never need a nil check.
type Store struct {
	snap atomic.Pointer[Settings]
	// onChange is invoked after a successful update, for audit or metrics.
	// It must not block: it runs on the request goroutine.
	onChange func(Settings)
}

// New builds a store seeded with initial values from configuration.
func New(initial Settings) *Store {
	s := &Store{}
	copy := initial
	s.snap.Store(&copy)
	return s
}

// SetOnChange registers an observer called after each successful update.
func (s *Store) SetOnChange(fn func(Settings)) {
	if s == nil {
		return
	}
	s.onChange = fn
}

// Get returns the current snapshot. The returned value is a copy, so callers
// cannot mutate shared state.
func (s *Store) Get() Settings {
	if s == nil {
		return Settings{}
	}
	if p := s.snap.Load(); p != nil {
		return *p
	}
	return Settings{}
}

// Limits returns just the limits, the common read on the hot path.
func (s *Store) Limits() Limits { return s.Get().Limits }

// Caps satisfies quota.CapsSource so the money guardrails can read live values
// without importing this package.
func (s *Store) Caps() (keyDaily, accountMonthly, globalDaily int64) {
	l := s.Limits()
	return l.KeyDailyMicros, l.AccountMonthlyMicros, l.GlobalDailyMicros
}

// Update validates and applies a new snapshot atomically.
//
// Validation happens before the swap, so a rejected update leaves the running
// configuration exactly as it was.
func (s *Store) Update(next Settings) error {
	if err := next.Validate(); err != nil {
		return err
	}
	if s == nil {
		return nil
	}
	s.snap.Store(&next)
	if s.onChange != nil {
		s.onChange(next)
	}
	return nil
}

// Patch applies a partial update: nil fields keep their current value.
//
// Operators edit one field at a time in a form; sending the whole object back
// invites lost updates when two admins have the page open.
func (s *Store) Patch(p Patch) (Settings, error) {
	cur := s.Get()
	next := cur
	if p.KeyDailyMicros != nil {
		next.Limits.KeyDailyMicros = *p.KeyDailyMicros
	}
	if p.AccountMonthlyMicros != nil {
		next.Limits.AccountMonthlyMicros = *p.AccountMonthlyMicros
	}
	if p.GlobalDailyMicros != nil {
		next.Limits.GlobalDailyMicros = *p.GlobalDailyMicros
	}
	if p.RPM != nil {
		next.Limits.RPM = *p.RPM
	}
	if p.TPM != nil {
		next.Limits.TPM = *p.TPM
	}
	if p.Concurrency != nil {
		next.Limits.Concurrency = *p.Concurrency
	}
	if err := s.Update(next); err != nil {
		return cur, err
	}
	return next, nil
}

// Patch carries optional fields; a nil pointer means "leave unchanged".
type Patch struct {
	KeyDailyMicros       *int64 `json:"key_daily_micros"`
	AccountMonthlyMicros *int64 `json:"account_monthly_micros"`
	GlobalDailyMicros    *int64 `json:"global_daily_micros"`
	RPM                  *int   `json:"rpm"`
	TPM                  *int   `json:"tpm"`
	Concurrency          *int   `json:"concurrency"`
}

// Empty reports whether the patch would change nothing.
func (p Patch) Empty() bool {
	return p.KeyDailyMicros == nil && p.AccountMonthlyMicros == nil &&
		p.GlobalDailyMicros == nil && p.RPM == nil && p.TPM == nil &&
		p.Concurrency == nil
}

// MarshalJSON keeps the payload field order stable for tests and clients.
func (s Settings) Marshal() ([]byte, error) { return json.Marshal(s) }
