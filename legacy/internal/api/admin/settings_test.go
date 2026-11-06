package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"czapi/internal/auth"
	"czapi/internal/domain"
	"czapi/internal/pkg/clock"
	"czapi/internal/runtimecfg"
	"czapi/internal/store/memstore"
)

// newSettingsHarness builds the minimum wiring these handlers need.
func newSettingsHarness(t *testing.T) (*Handlers, *runtimecfg.Store, *domain.AdminUser) {
	t.Helper()

	st := memstore.New()
	clk := clock.NewFake(time.Date(2026, 11, 5, 3, 0, 0, 0, time.UTC))

	hash, err := HashPassword("initial-pass-123")
	require.NoError(t, err)
	adminUser := &domain.AdminUser{
		ID: 1, Username: "admin", PasswordHash: hash, Role: "owner", Status: 1,
	}
	require.NoError(t, st.Admins().Create(context.Background(), adminUser))

	rt := runtimecfg.New(runtimecfg.Settings{Limits: runtimecfg.Limits{
		KeyDailyMicros: 50_000_000, AccountMonthlyMicros: 500_000_000,
		GlobalDailyMicros: 0, RPM: 60, TPM: 100_000, Concurrency: 5,
	}})

	h := New(Deps{Store: st, Clock: clk})
	h.SetRuntimeSettings(rt)
	return h, rt, adminUser
}

// reqWithActor builds a request carrying an authenticated admin identity.
func reqWithActor(method, target string, body any, username string) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	r := httptest.NewRequest(method, target, &buf)
	r.Header.Set("Content-Type", "application/json")
	ctx := auth.WithAdmin(r.Context(), &auth.AdminClaims{
		AdminID: 1, Username: username, Role: "owner",
	})
	return r.WithContext(ctx)
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), "body: %s", rec.Body.String())
	return out
}

// --- limits ------------------------------------------------------------------

func TestRuntimeLimitsPatchAppliesAndReports(t *testing.T) {
	h, rt, _ := newSettingsHarness(t)
	rpm := 240

	rec := httptest.NewRecorder()
	h.RuntimeLimitsUpdate(rec, reqWithActor(http.MethodPatch, "/admin/v1/settings/limits",
		map[string]any{"rpm": rpm}, "admin"))

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, 240, rt.Limits().RPM, "the live value must change")
	// Untouched fields stay put.
	assert.Equal(t, int64(50_000_000), rt.Limits().KeyDailyMicros)
}

func TestRuntimeLimitsRejectsNegativeWith400(t *testing.T) {
	h, rt, _ := newSettingsHarness(t)
	before := rt.Limits()

	rec := httptest.NewRecorder()
	h.RuntimeLimitsUpdate(rec, reqWithActor(http.MethodPatch, "/admin/v1/settings/limits",
		map[string]any{"key_daily_micros": -1}, "admin"))

	assert.Equal(t, http.StatusBadRequest, rec.Code, "a negative cap is client error, not 500")
	assert.Equal(t, before, rt.Limits(), "a rejected update must not change anything")
	assert.Contains(t, rec.Body.String(), "key_daily_micros",
		"the error must name the offending field so the form can highlight it")
}

func TestRuntimeLimitsEmptyPatchIsRejected(t *testing.T) {
	h, _, _ := newSettingsHarness(t)
	rec := httptest.NewRecorder()
	h.RuntimeLimitsUpdate(rec, reqWithActor(http.MethodPatch, "/admin/v1/settings/limits",
		map[string]any{}, "admin"))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRuntimeLimitsRequiresAuth(t *testing.T) {
	h, rt, _ := newSettingsHarness(t)
	before := rt.Limits()

	// No actor in context: this is what an unauthenticated caller looks like.
	r := httptest.NewRequest(http.MethodPatch, "/admin/v1/settings/limits",
		bytes.NewBufferString(`{"rpm":1}`))
	rec := httptest.NewRecorder()
	h.RuntimeLimitsUpdate(rec, r)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, before, rt.Limits(), "unauthenticated writes must not apply")
}

func TestRuntimeLimitsWithoutStoreReturns503(t *testing.T) {
	h, _, _ := newSettingsHarness(t)
	h.SetRuntimeSettings(nil)
	rec := httptest.NewRecorder()
	h.RuntimeLimitsUpdate(rec, reqWithActor(http.MethodPatch, "/admin/v1/settings/limits",
		map[string]any{"rpm": 10}, "admin"))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestSettingsPayloadExposesLimitsAndReadOnlyNotes(t *testing.T) {
	h, _, _ := newSettingsHarness(t)
	rec := httptest.NewRecorder()
	h.Settings(rec, reqWithActor(http.MethodGet, "/admin/v1/settings", nil, "admin"))

	require.Equal(t, http.StatusOK, rec.Code)
	body := decode(t, rec)
	data, ok := body["data"].(map[string]any)
	require.True(t, ok, "response shape: %s", rec.Body.String())

	limits, ok := data["limits"].(map[string]any)
	require.True(t, ok, "limits must be present so the form can render")
	assert.EqualValues(t, 60, limits["rpm"])

	// The console needs to explain why some fields are not editable.
	notes, ok := data["config_notes"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, notes, "key_hash_salt")
}

// --- password ----------------------------------------------------------------

func TestChangePasswordSucceedsAndNewPasswordWorks(t *testing.T) {
	h, _, adminUser := newSettingsHarness(t)

	rec := httptest.NewRecorder()
	h.ChangePassword(rec, reqWithActor(http.MethodPost, "/admin/v1/password", map[string]any{
		"current_password": "initial-pass-123",
		"new_password":     "a-much-better-passphrase",
	}, "admin"))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	reloaded, err := h.store.Admins().GetByUsername(context.Background(), "admin")
	require.NoError(t, err)
	assert.NotEqual(t, adminUser.PasswordHash, reloaded.PasswordHash, "hash must be replaced")
	assert.True(t, VerifyPassword(reloaded.PasswordHash, "a-much-better-passphrase"),
		"the new password must verify")
	assert.False(t, VerifyPassword(reloaded.PasswordHash, "initial-pass-123"),
		"the old password must stop working")
}

func TestChangePasswordRequiresCorrectCurrentPassword(t *testing.T) {
	h, _, _ := newSettingsHarness(t)

	rec := httptest.NewRecorder()
	h.ChangePassword(rec, reqWithActor(http.MethodPost, "/admin/v1/password", map[string]any{
		"current_password": "wrong-password",
		"new_password":     "a-much-better-passphrase",
	}, "admin"))

	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"a stolen token alone must not be enough to take over the account")

	reloaded, err := h.store.Admins().GetByUsername(context.Background(), "admin")
	require.NoError(t, err)
	assert.True(t, VerifyPassword(reloaded.PasswordHash, "initial-pass-123"),
		"password must be unchanged")
}

func TestChangePasswordRejectsWeakPasswords(t *testing.T) {
	h, _, _ := newSettingsHarness(t)

	for _, weak := range []string{"short", "admin", "password", "12345678"} {
		rec := httptest.NewRecorder()
		h.ChangePassword(rec, reqWithActor(http.MethodPost, "/admin/v1/password", map[string]any{
			"current_password": "initial-pass-123",
			"new_password":     weak,
		}, "admin"))
		assert.Equal(t, http.StatusBadRequest, rec.Code, "weak password %q must be refused", weak)
	}

	// The original password must still work after all those refusals.
	reloaded, err := h.store.Admins().GetByUsername(context.Background(), "admin")
	require.NoError(t, err)
	assert.True(t, VerifyPassword(reloaded.PasswordHash, "initial-pass-123"))
}

func TestChangePasswordRejectsReusingTheSamePassword(t *testing.T) {
	h, _, _ := newSettingsHarness(t)
	rec := httptest.NewRecorder()
	h.ChangePassword(rec, reqWithActor(http.MethodPost, "/admin/v1/password", map[string]any{
		"current_password": "initial-pass-123",
		"new_password":     "initial-pass-123",
	}, "admin"))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestChangePasswordRequiresAuth(t *testing.T) {
	h, _, _ := newSettingsHarness(t)
	r := httptest.NewRequest(http.MethodPost, "/admin/v1/password",
		bytes.NewBufferString(`{"current_password":"x","new_password":"yyyyyyyy"}`))
	rec := httptest.NewRecorder()
	h.ChangePassword(rec, r)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestPasswordStrengthRules(t *testing.T) {
	assert.NoError(t, validatePasswordStrength("a-good-passphrase"))
	assert.Error(t, validatePasswordStrength("short"))
	assert.Error(t, validatePasswordStrength("admin"))
	assert.Error(t, validatePasswordStrength(strings.Repeat("x", 200)))
}
