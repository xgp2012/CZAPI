package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"czapi/internal/api/admin"
	"czapi/internal/config"
	"czapi/internal/domain"
	"czapi/internal/pkg/errs"
	"czapi/internal/pkg/log"
	"czapi/internal/store/memstore"
)

var fixedTime = time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)

type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }

// newConfig returns a config with defaults plus the bootstrap settings a fresh
// install would use.
func newConfig() *config.Config {
	cfg := config.Default()
	cfg.Bootstrap = config.Bootstrap{
		AdminUsername: "admin",
		AdminPassword: "admin",
	}
	return cfg
}

func newStore() *memstore.Store {
	return memstore.NewWithClock(&testClock{now: fixedTime})
}

// ---------------------------------------------------------------------------
// Administrator
// ---------------------------------------------------------------------------

func TestRunCreatesDefaultAdmin(t *testing.T) {
	st := newStore()
	ctx := context.Background()

	rep, err := Run(ctx, st, newConfig(), log.New("error", "text"), &testClock{now: fixedTime})
	require.NoError(t, err)
	assert.True(t, rep.AdminCreated)
	assert.Equal(t, "admin", rep.AdminUsername)

	got, err := st.Admins().GetByUsername(ctx, "admin")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "admin", got.Role, "the seeded account must be a superuser")
	assert.Equal(t, int8(1), got.Status)
	assert.NotEqual(t, "admin", got.PasswordHash, "the password must never be stored in clear")
}

func TestSeededPasswordActuallyVerifies(t *testing.T) {
	// The point of seeding an admin is that someone can log in with it. Assert
	// through the same verifier the login handler uses, not just that a row exists.
	st := newStore()
	ctx := context.Background()
	_, err := Run(ctx, st, newConfig(), nil, &testClock{now: fixedTime})
	require.NoError(t, err)

	got, err := st.Admins().GetByUsername(ctx, "admin")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, admin.VerifyPassword(got.PasswordHash, "admin"),
		"admin/admin must authenticate")
	assert.False(t, admin.VerifyPassword(got.PasswordHash, "wrong"))
}

func TestRunNeverResetsAnExistingPassword(t *testing.T) {
	st := newStore()
	ctx := context.Background()
	clk := &testClock{now: fixedTime}

	// First run seeds admin/admin.
	_, err := Run(ctx, st, newConfig(), nil, clk)
	require.NoError(t, err)

	// The operator changes the password.
	got, err := st.Admins().GetByUsername(ctx, "admin")
	require.NoError(t, err)
	hash, err := admin.HashPassword("a-much-better-password")
	require.NoError(t, err)
	got.PasswordHash = hash
	require.NoError(t, st.Admins().Update(ctx, got))

	// A restart must not resurrect the default password: that would be a
	// permanent backdoor for anyone who has read this repository.
	rep, err := Run(ctx, st, newConfig(), nil, clk)
	require.NoError(t, err)
	assert.False(t, rep.AdminCreated, "an existing admin must not be recreated")

	after, err := st.Admins().GetByUsername(ctx, "admin")
	require.NoError(t, err)
	assert.True(t, admin.VerifyPassword(after.PasswordHash, "a-much-better-password"),
		"the operator's password must survive a restart")
	assert.False(t, admin.VerifyPassword(after.PasswordHash, "admin"),
		"the default password must not come back")
}

func TestRunSkipsAdminWhenPasswordBlank(t *testing.T) {
	st := newStore()
	cfg := newConfig()
	cfg.Bootstrap.AdminPassword = ""

	rep, err := Run(context.Background(), st, cfg, nil, &testClock{now: fixedTime})
	require.NoError(t, err)
	assert.False(t, rep.AdminCreated)

	n, err := st.Admins().Count(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(0), n, "an empty password means do not seed an admin")
}

// ---------------------------------------------------------------------------
// Site
// ---------------------------------------------------------------------------

func TestRunCreatesDefaultSiteWithGeneratedSecret(t *testing.T) {
	st := newStore()
	ctx := context.Background()

	rep, err := Run(ctx, st, newConfig(), nil, &testClock{now: fixedTime})
	require.NoError(t, err)
	assert.True(t, rep.SiteCreated)
	assert.Equal(t, "v10", rep.SiteAppID)
	require.NotEmpty(t, rep.SiteSecret, "a generated secret must be reported once")
	assert.GreaterOrEqual(t, len(rep.SiteSecret), 32, "the secret must be long enough to be safe")

	// The stored site must match what was reported, or V10 cannot sign requests.
	site, err := st.Sites().GetByAppID(ctx, "v10")
	require.NoError(t, err)
	require.NotNil(t, site)
	assert.Equal(t, rep.SiteSecret, site.Secret)
	assert.True(t, site.Enabled)
}

func TestRunRegistersConfiguredSites(t *testing.T) {
	// A site listed in internal.sites must reach the store: the signature
	// verifier resolves app ids there, so a config entry alone would do nothing.
	st := newStore()
	cfg := newConfig()
	cfg.Internal.Sites = []config.Site{
		{AppID: "v10-prod", Secret: "s3cret-from-config", Name: "Prod", Enabled: true},
	}

	rep, err := Run(context.Background(), st, cfg, nil, &testClock{now: fixedTime})
	require.NoError(t, err)
	assert.True(t, rep.SiteCreated)
	assert.Equal(t, "v10-prod", rep.SiteAppID)
	assert.Empty(t, rep.SiteSecret, "a configured secret is not echoed back")

	site, err := st.Sites().GetByAppID(context.Background(), "v10-prod")
	require.NoError(t, err)
	require.NotNil(t, site)
	assert.Equal(t, "s3cret-from-config", site.Secret)
}

func TestRunNeverRotatesAnExistingSiteSecret(t *testing.T) {
	st := newStore()
	ctx := context.Background()
	clk := &testClock{now: fixedTime}

	_, err := Run(ctx, st, newConfig(), nil, clk)
	require.NoError(t, err)
	first, err := st.Sites().GetByAppID(ctx, "v10")
	require.NoError(t, err)

	// Rotating on every restart would break every signed V10 call until the V10
	// admin notices, so the second run must leave the secret alone.
	rep, err := Run(ctx, st, newConfig(), nil, clk)
	require.NoError(t, err)
	assert.False(t, rep.SiteCreated)
	assert.Empty(t, rep.SiteSecret)

	second, err := st.Sites().GetByAppID(ctx, "v10")
	require.NoError(t, err)
	assert.Equal(t, first.Secret, second.Secret)
}

// ---------------------------------------------------------------------------
// Demo content
// ---------------------------------------------------------------------------

func TestRunSeedsDemoContentWhenEnabled(t *testing.T) {
	st := newStore()
	ctx := context.Background()
	cfg := newConfig()
	cfg.Bootstrap.DemoContent = true

	rep, err := Run(ctx, st, cfg, nil, &testClock{now: fixedTime})
	require.NoError(t, err)
	assert.True(t, rep.DemoSeeded)

	// A demo plan, model and channel make the console and /v1 non-empty.
	plan, err := st.Plans().GetByCode(ctx, "demo")
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.Positive(t, plan.QuotaTokens)

	model, err := st.Models().GetByName(ctx, "gpt-4o-mini")
	require.NoError(t, err)
	require.NotNil(t, model)
	assert.Equal(t, int8(1), model.Status)

	channels, err := st.Channels().List(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, channels)
}

func TestDemoContentOffByDefault(t *testing.T) {
	st := newStore()
	ctx := context.Background()

	_, err := Run(ctx, st, newConfig(), nil, &testClock{now: fixedTime})
	require.NoError(t, err)

	// A production install must not silently acquire a channel with a fake key.
	channels, err := st.Channels().List(ctx)
	require.NoError(t, err)
	assert.Empty(t, channels)
}

func TestDemoContentDoesNotDuplicateOnRestart(t *testing.T) {
	st := newStore()
	ctx := context.Background()
	cfg := newConfig()
	cfg.Bootstrap.DemoContent = true
	clk := &testClock{now: fixedTime}

	_, err := Run(ctx, st, cfg, nil, clk)
	require.NoError(t, err)
	_, err = Run(ctx, st, cfg, nil, clk)
	require.NoError(t, err)

	channels, err := st.Channels().List(ctx)
	require.NoError(t, err)
	assert.Len(t, channels, 1, "restarting must not add a second demo channel")
}

// ---------------------------------------------------------------------------
// Full sequence
// ---------------------------------------------------------------------------

func TestRunOnFreshStoreIsIdempotent(t *testing.T) {
	st := newStore()
	ctx := context.Background()
	cfg := newConfig()
	cfg.Bootstrap.DemoContent = true
	clk := &testClock{now: fixedTime}

	first, err := Run(ctx, st, cfg, nil, clk)
	require.NoError(t, err)
	assert.True(t, first.AdminCreated)
	assert.True(t, first.SiteCreated)

	second, err := Run(ctx, st, cfg, nil, clk)
	require.NoError(t, err)
	assert.False(t, second.AdminCreated, "the admin already exists")
	assert.False(t, second.SiteCreated, "the site already exists")

	n, err := st.Admins().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "exactly one admin, never two")
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func TestIsNotFoundMatchesStoreError(t *testing.T) {
	assert.True(t, isNotFound(errs.NotFound("nope")))
	assert.False(t, isNotFound(errs.Internal("boom", nil)))
	assert.False(t, isNotFound(nil))
}

func TestNameOrFallsBack(t *testing.T) {
	assert.Equal(t, "fallback", nameOr("", "fallback"))
	assert.Equal(t, "fallback", nameOr("   ", "fallback"))
	assert.Equal(t, "given", nameOr("given", "fallback"))
}

func TestRandomHexIsUniqueAndUrlSafe(t *testing.T) {
	a, err := randomHex(32)
	require.NoError(t, err)
	b, err := randomHex(32)
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
	assert.NotContains(t, a, "+")
	assert.NotContains(t, a, "/")
}

// unused keeps the domain import meaningful for the compiler if assertions change.
var _ = domain.SubActive
