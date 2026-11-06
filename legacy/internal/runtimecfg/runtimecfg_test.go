package runtimecfg

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func base() Settings {
	return Settings{Limits: Limits{
		KeyDailyMicros:       50_000_000,
		AccountMonthlyMicros: 500_000_000,
		GlobalDailyMicros:    0,
		RPM:                  60,
		TPM:                  100_000,
		Concurrency:          5,
	}}
}

func TestNewSeedsFromInitialValues(t *testing.T) {
	s := New(base())
	assert.Equal(t, int64(50_000_000), s.Limits().KeyDailyMicros)
	assert.Equal(t, 60, s.Limits().RPM)
}

// TestNilStoreIsUsable matters because the admin handler may be constructed
// before the store exists in some test wiring; a nil store must never panic.
func TestNilStoreIsUsable(t *testing.T) {
	var s *Store
	assert.Equal(t, Settings{}, s.Get())
	assert.Equal(t, Limits{}, s.Limits())
	assert.NoError(t, s.Update(base()))
	kd, am, gd := s.Caps()
	assert.Zero(t, kd)
	assert.Zero(t, am)
	assert.Zero(t, gd)
}

func TestCapsMirrorsLimits(t *testing.T) {
	s := New(base())
	kd, am, gd := s.Caps()
	assert.Equal(t, int64(50_000_000), kd)
	assert.Equal(t, int64(500_000_000), am)
	assert.Equal(t, int64(0), gd)
}

func TestPatchChangesOnlyProvidedFields(t *testing.T) {
	s := New(base())
	newRPM := 120
	next, err := s.Patch(Patch{RPM: &newRPM})
	require.NoError(t, err)

	assert.Equal(t, 120, next.Limits.RPM, "the patched field must change")
	// Everything else must survive untouched: this is the lost-update guard.
	assert.Equal(t, int64(50_000_000), next.Limits.KeyDailyMicros)
	assert.Equal(t, int64(500_000_000), next.Limits.AccountMonthlyMicros)
	assert.Equal(t, 100_000, next.Limits.TPM)
	assert.Equal(t, 5, next.Limits.Concurrency)
	assert.Equal(t, next, s.Get(), "the snapshot must be updated")
}

func TestPatchZeroIsMeaningfulNotAbsent(t *testing.T) {
	// 0 means "no cap". A patch carrying 0 must clear the limit rather than be
	// ignored as a zero value.
	s := New(base())
	zero := int64(0)
	next, err := s.Patch(Patch{KeyDailyMicros: &zero})
	require.NoError(t, err)
	assert.Zero(t, next.Limits.KeyDailyMicros)
}

func TestPatchEmptyIsANoop(t *testing.T) {
	s := New(base())
	before := s.Get()
	next, err := s.Patch(Patch{})
	require.NoError(t, err)
	assert.Equal(t, before, next)
}

func TestNegativeValuesAreRejected(t *testing.T) {
	s := New(base())
	before := s.Get()

	neg := int64(-1)
	_, err := s.Patch(Patch{KeyDailyMicros: &neg})
	require.Error(t, err)
	assert.IsType(t, &ValidationError{}, err)
	// A rejected update must leave the running config intact.
	assert.Equal(t, before, s.Get())

	negInt := -5
	_, err = s.Patch(Patch{RPM: &negInt})
	require.Error(t, err)
	assert.Equal(t, before, s.Get())
}

func TestValidationErrorMessageNamesTheField(t *testing.T) {
	neg := int64(-1)
	err := (Settings{Limits: Limits{GlobalDailyMicros: neg}}).Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "global_daily_micros")
}

func TestGetReturnsACopy(t *testing.T) {
	s := New(base())
	got := s.Get()
	got.Limits.RPM = 9999 // mutating the copy must not affect the store
	assert.NotEqual(t, 9999, s.Limits().RPM)
}

// TestConcurrentReadsAndWrites is a smoke test for the atomic swap. Run with
// -race to make it meaningful; the code is written so reads never lock.
func TestConcurrentReadsAndWrites(t *testing.T) {
	s := New(base())
	var wg sync.WaitGroup

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				v := int(n*10 + j)
				_, err := s.Patch(Patch{RPM: &v})
				assert.NoError(t, err)
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 400; j++ {
				l := s.Limits()
				assert.GreaterOrEqual(t, l.RPM, 0)
				_, _, _ = s.Caps()
			}
		}()
	}
	wg.Wait()
}

func TestOnChangeObserverFiresOnSuccessOnly(t *testing.T) {
	s := New(base())
	var seen []int
	s.SetOnChange(func(st Settings) { seen = append(seen, st.Limits.RPM) })

	v := 77
	_, err := s.Patch(Patch{RPM: &v})
	require.NoError(t, err)

	neg := -1
	_, err = s.Patch(Patch{RPM: &neg})
	require.Error(t, err)

	assert.Equal(t, []int{77}, seen, "observer must not fire for a rejected update")
}

func TestSetOnChangeOnNilStoreIsSafe(t *testing.T) {
	var s *Store
	assert.NotPanics(t, func() { s.SetOnChange(func(Settings) {}) })
}
