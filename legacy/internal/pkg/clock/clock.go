// Package clock abstracts time so billing tests can travel in time.
package clock

import (
	"sync"
	"time"
)

// Clock is the minimal time interface the gateway depends on.
type Clock interface {
	Now() time.Time
}

// System is the production clock.
type System struct{}

// Now implements Clock.
func (System) Now() time.Time { return time.Now() }

// Fake is a manually advanced clock for tests.
type Fake struct {
	mu sync.Mutex
	t  time.Time
}

// NewFake returns a Fake clock frozen at t.
func NewFake(t time.Time) *Fake { return &Fake{t: t} }

// Now implements Clock.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

// Advance moves the fake clock forward by d.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

// Set sets the fake clock to t.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = t
}
