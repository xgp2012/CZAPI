package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"czapi/internal/pkg/clock"
	"czapi/internal/pkg/errs"
)

// TestInternalHTTPStatusTable pins the whole /internal/v1 code -> HTTP mapping.
func TestInternalHTTPStatusTable(t *testing.T) {
	tests := map[int]int{
		0:       http.StatusOK,
		40000:   http.StatusBadRequest,
		40001:   http.StatusBadRequest,
		40100:   http.StatusUnauthorized,
		40101:   http.StatusUnauthorized,
		40102:   http.StatusUnauthorized,
		40300:   http.StatusForbidden,
		40400:   http.StatusNotFound,
		40200:   http.StatusPaymentRequired,
		40900:   http.StatusConflict,
		42900:   http.StatusTooManyRequests,
		50300:   http.StatusServiceUnavailable,
		50000:   http.StatusInternalServerError,
		9999999: http.StatusInternalServerError,
	}
	for code, want := range tests {
		assert.Equal(t, want, internalHTTPStatus(code), "code %d", code)
	}
}

// errReader fails on the first Read, exercising the readBody error branch.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("broken pipe") }
func (errReader) Close() error             { return nil }

func TestInternalSigMiddleware_BodyReadError(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	called := false
	handler := InternalSigMiddleware(f.v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req, err := http.NewRequest(http.MethodPost, "/internal/v1/credit", nil)
	require.NoError(t, err)
	req.Body = errReader{}
	req.RemoteAddr = testRemoteIPAddr + ":443"
	rec := newRecorder()
	handler.ServeHTTP(rec, req)

	assert.False(t, called)
	assert.Equal(t, http.StatusBadRequest, rec.status)
}

func TestReadBody_NilBody(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "/x", nil)
	require.NoError(t, err)
	req.Body = nil
	body, tooLarge, rerr := readBody(req)
	assert.NoError(t, rerr)
	assert.False(t, tooLarge)
	assert.Nil(t, body)
}

func TestBearerToken_NilRequest(t *testing.T) {
	assert.Equal(t, "", bearerToken(nil))
}

func TestAllowsModel_NilPrincipal(t *testing.T) {
	var p *Principal
	assert.False(t, p.AllowsModel("x"))
	assert.False(t, (&Principal{}).AllowsModel("x"))
}

func TestVerifierWithoutKeyReader(t *testing.T) {
	v := NewVerifier(Deps{}, VerifierConfig{Clock: fixedClock(t)})
	_, e := v.Authenticate(context.Background(), testPlain, "1.2.3.4", "")
	require.NotNil(t, e)
	assert.Equal(t, errs.CodeInternal, e.Code)
	assert.Equal(t, http.StatusInternalServerError, e.Status)
}

func TestHydrateSwallowsPlanErrors(t *testing.T) {
	f := newFixture(t)
	f.plans.err = errors.New("plan table gone")
	p, e := f.v.Authenticate(context.Background(), testPlain, "1.2.3.4", "")
	require.Nil(t, e, "a plan read failure must not block authentication")
	require.NotNil(t, p)
	assert.Nil(t, p.Plan)
	assert.Equal(t, uint64(9), p.SubscriptionID)
}

func TestBackfillSkippedOnCacheErrors(t *testing.T) {
	f := newFixture(t)
	f.cache.setErr = errors.New("cache down")
	_, e := f.v.Authenticate(context.Background(), testPlain, "1.2.3.4", "")
	assert.Nil(t, e, "a cache write failure must not block authentication")
	assert.Zero(t, f.cache.len())
}

func TestLoadFallsBackWhenCacheReadFails(t *testing.T) {
	f := newFixture(t)
	f.cache.getErr = errors.New("cache down")
	_, e := f.v.Authenticate(context.Background(), testPlain, "1.2.3.4", "")
	assert.Nil(t, e, "a cache read failure must fall through to the store")
	assert.Equal(t, 1, f.keys.hashCalls())
}

func TestInvalidateSurvivesCacheErrors(t *testing.T) {
	f := newFixture(t)
	f.cache.getErr = errors.New("cache down")
	// The secondary-index read fails; the primary delete still runs and the error
	// from the failed read is returned to the caller.
	assert.Error(t, f.v.InvalidateByID(context.Background(), 321))
	// Del itself does not consult getErr, so a plain Invalidate succeeds.
	assert.NoError(t, f.v.Invalidate(context.Background(), "abc"))
}

func TestTouchUsedIgnoresErrors(t *testing.T) {
	f := newFixture(t)
	f.keys.touchErr = errors.New("boom")
	assert.NotPanics(t, func() { f.v.TouchUsed(context.Background(), 321) })
}

// newRecorder is a minimal http.ResponseWriter used where httptest is awkward.
type newRecorderWriter struct {
	header http.Header
	status int
	body   []byte
}

func newRecorder() *newRecorderWriter {
	return &newRecorderWriter{header: make(http.Header), status: http.StatusOK}
}

func (n *newRecorderWriter) Header() http.Header { return n.header }

func (n *newRecorderWriter) WriteHeader(status int) { n.status = status }

func (n *newRecorderWriter) Write(p []byte) (int, error) {
	n.body = append(n.body, p...)
	return len(p), nil
}

// fixedClock returns a Fake clock frozen at the shared test instant.
func fixedClock(t *testing.T) *clock.Fake {
	t.Helper()
	now, err := time.Parse(time.RFC3339, testNowStr)
	require.NoError(t, err)
	return clock.NewFake(now)
}

// TestDepsFromStoreWiring documents that a store-backed Deps needs an explicit
// cache: DepsFromStore only fills the readers.
func TestDepsFromStoreWiring(t *testing.T) {
	f := newFixture(t)
	d := DepsFromStore(&fakeStore{
		keys:  fakeKeyRepo{inner: f.keys},
		accs:  fakeAccountRepo{inner: f.accs},
		subs:  fakeSubscriptionRepo{inner: f.subs},
		plans: fakePlanRepo{inner: f.plans},
	})
	d.Cache = f.cache

	v := NewVerifier(d, VerifierConfig{Salt: testSalt, Clock: f.clk})
	p, e := v.Authenticate(context.Background(), testPlain, "1.2.3.4", "")
	require.Nil(t, e)
	require.NotNil(t, p)
	require.NotNil(t, p.Plan)
}
