package auth

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"czapi/internal/pkg/clock"
	"czapi/internal/pkg/errs"
	"czapi/internal/pkg/idgen"
	"czapi/internal/pkg/sign"
)

const (
	testAppID        = "v10-site-a"
	testSecret       = "app-secret-current"
	testSecretPrev   = "app-secret-previous"
	testDisabledApp  = "v10-site-disabled"
	testBodyPlain    = `{"v10_client_id":1234,"ref_type":"v10_order","ref_id":"ORD1"}`
	testMethod       = http.MethodPost
	testPath         = "/internal/v1/credit"
	testRemoteIPAddr = "203.0.113.7"
)

// sigFixture is a verifier bound to a couple of fake sites.
type sigFixture struct {
	resolver *fakeResolver
	cache    *fakeCache
	clk      *clock.Fake
	now      time.Time
	v        *SigVerifier
	cfg      SigConfig
}

func newSigFixture(t *testing.T, cfg SigConfig) *sigFixture {
	t.Helper()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	clk := clock.NewFake(now)
	resolver := &fakeResolver{sites: map[string]fakeSite{
		testAppID:       {secret: testSecret, secretPrev: testSecretPrev, enabled: true},
		testDisabledApp: {secret: "disabled-secret", enabled: false},
	}}
	c := newFakeCache()
	cfg.Clock = clk
	v := NewSigVerifier(resolver, c, cfg)
	return &sigFixture{resolver: resolver, cache: c, clk: clk, now: now, v: v, cfg: cfg}
}

// signedHeaders builds a fully valid header set for secret over body.
func (f *sigFixture) signedHeaders(appID, secret, nonce string, body []byte) http.Header {
	ts := f.now.Unix()
	h := make(http.Header)
	h.Set(HeaderAppID, appID)
	h.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	h.Set(HeaderNonce, nonce)
	toSign := sign.StringToSign(testMethod, testPath, "", body, ts, nonce)
	h.Set(HeaderSignature, sign.Sign(secret, toSign))
	return h
}

func newNonce() string { return idgen.RandomHex(16) }

func TestNewSigVerifierDefaults(t *testing.T) {
	v := NewSigVerifier(nil, nil, SigConfig{})
	assert.Equal(t, DefaultClockSkew, v.ClockSkew())
	assert.Equal(t, DefaultNonceTTL, v.NonceTTL())
}

func TestSigVerify_Valid(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	body := []byte(testBodyPlain)
	nonce := newNonce()
	h := f.signedHeaders(testAppID, testSecret, nonce, body)

	appID, e := f.v.Verify(context.Background(), testMethod, testPath, nil, body, h, testRemoteIPAddr)
	require.Nil(t, e)
	assert.Equal(t, testAppID, appID)
}

func TestSigVerify_ValidWithQuery(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	q := url.Values{"v10_client_id": {"1234"}}
	ts := f.now.Unix()
	nonce := newNonce()
	h := make(http.Header)
	h.Set(HeaderAppID, testAppID)
	h.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	h.Set(HeaderNonce, nonce)
	toSign := sign.StringToSign(http.MethodGet, "/internal/v1/usage/summary", sign.CanonicalQuery(q), nil, ts, nonce)
	h.Set(HeaderSignature, sign.Sign(testSecret, toSign))

	appID, e := f.v.Verify(context.Background(), http.MethodGet, "/internal/v1/usage/summary", q, nil, h, testRemoteIPAddr)
	require.Nil(t, e)
	assert.Equal(t, testAppID, appID)

	// The same signature does not validate a different query. A fresh verifier is
	// used so the nonce replay check cannot mask the signature mismatch.
	g := newSigFixture(t, SigConfig{})
	ts2 := g.now.Unix()
	nonce2 := newNonce()
	h2 := make(http.Header)
	h2.Set(HeaderAppID, testAppID)
	h2.Set(HeaderTimestamp, strconv.FormatInt(ts2, 10))
	h2.Set(HeaderNonce, nonce2)
	h2.Set(HeaderSignature, sign.Sign(testSecret, sign.StringToSign(http.MethodGet, "/internal/v1/usage/summary", sign.CanonicalQuery(q), nil, ts2, nonce2)))

	_, e = g.v.Verify(context.Background(), http.MethodGet, "/internal/v1/usage/summary",
		url.Values{"v10_client_id": {"9999"}}, nil, h2, testRemoteIPAddr)
	require.NotNil(t, e)
	assert.Equal(t, V1CodeAuthFailed, V1Code(e))
}

func TestSigVerify_TamperedBody(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	signed := []byte(testBodyPlain)
	h := f.signedHeaders(testAppID, testSecret, newNonce(), signed)

	tampered := []byte(testBodyPlain + " ")
	_, e := f.v.Verify(context.Background(), testMethod, testPath, nil, tampered, h, testRemoteIPAddr)
	require.NotNil(t, e)
	assert.Equal(t, http.StatusUnauthorized, e.Status)
	assert.Equal(t, V1CodeAuthFailed, V1Code(e))
}

func TestSigVerify_WrongSecret(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	body := []byte(testBodyPlain)
	h := f.signedHeaders(testAppID, "completely-wrong-secret", newNonce(), body)

	_, e := f.v.Verify(context.Background(), testMethod, testPath, nil, body, h, testRemoteIPAddr)
	require.NotNil(t, e)
	assert.Equal(t, http.StatusUnauthorized, e.Status)
	assert.Equal(t, V1CodeAuthFailed, V1Code(e))
}

func TestSigVerify_RotationViaPrevSecret(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	body := []byte(testBodyPlain)
	h := f.signedHeaders(testAppID, testSecretPrev, newNonce(), body)

	appID, e := f.v.Verify(context.Background(), testMethod, testPath, nil, body, h, testRemoteIPAddr)
	require.Nil(t, e, "the previous secret must validate during rotation")
	assert.Equal(t, testAppID, appID)
}

func TestSigVerify_StaleTimestamp(t *testing.T) {
	tests := []struct {
		name   string
		offset time.Duration
		want   int
	}{
		{name: "too old", offset: -6 * time.Minute},
		{name: "too new", offset: +6 * time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newSigFixture(t, SigConfig{})
			body := []byte(testBodyPlain)
			nonce := newNonce()
			ts := f.now.Add(tc.offset).Unix()
			h := make(http.Header)
			h.Set(HeaderAppID, testAppID)
			h.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
			h.Set(HeaderNonce, nonce)
			h.Set(HeaderSignature, sign.Sign(testSecret, sign.StringToSign(testMethod, testPath, "", body, ts, nonce)))

			_, e := f.v.Verify(context.Background(), testMethod, testPath, nil, body, h, testRemoteIPAddr)
			require.NotNil(t, e)
			assert.Equal(t, http.StatusUnauthorized, e.Status)
			assert.Equal(t, V1CodeTimestampSkew, V1Code(e))
		})
	}
}

func TestSigVerify_TimestampAtEdgeAccepted(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	body := []byte(testBodyPlain)
	nonce := newNonce()
	ts := f.now.Add(-DefaultClockSkew).Unix() // exactly at the boundary
	h := make(http.Header)
	h.Set(HeaderAppID, testAppID)
	h.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	h.Set(HeaderNonce, nonce)
	h.Set(HeaderSignature, sign.Sign(testSecret, sign.StringToSign(testMethod, testPath, "", body, ts, nonce)))

	_, e := f.v.Verify(context.Background(), testMethod, testPath, nil, body, h, testRemoteIPAddr)
	assert.Nil(t, e, "|now-ts| == ClockSkew must be accepted")
}

func TestSigVerify_UnparseableTimestamp(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	body := []byte(testBodyPlain)
	nonce := newNonce()
	h := make(http.Header)
	h.Set(HeaderAppID, testAppID)
	h.Set(HeaderTimestamp, "not-a-number")
	h.Set(HeaderNonce, nonce)
	h.Set(HeaderSignature, "deadbeef")

	_, e := f.v.Verify(context.Background(), testMethod, testPath, nil, body, h, testRemoteIPAddr)
	require.NotNil(t, e)
	assert.Equal(t, http.StatusUnauthorized, e.Status)
	assert.Equal(t, V1CodeTimestampSkew, V1Code(e))
}

func TestSigVerify_ReplayedNonce(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	body := []byte(testBodyPlain)
	nonce := newNonce()
	h := f.signedHeaders(testAppID, testSecret, nonce, body)

	_, e := f.v.Verify(context.Background(), testMethod, testPath, nil, body, h, testRemoteIPAddr)
	require.Nil(t, e)

	_, e = f.v.Verify(context.Background(), testMethod, testPath, nil, body, h, testRemoteIPAddr)
	require.NotNil(t, e)
	assert.Equal(t, http.StatusUnauthorized, e.Status)
	assert.Equal(t, V1CodeNonceReplay, V1Code(e))
}

func TestSigVerify_BadNonceShape(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	body := []byte(testBodyPlain)
	for _, nonce := range []string{"", "short", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", "0123456789abcdef0123456789abcdef0"} {
		h := f.signedHeaders(testAppID, testSecret, nonce, body)
		_, e := f.v.Verify(context.Background(), testMethod, testPath, nil, body, h, testRemoteIPAddr)
		require.NotNil(t, e, "nonce %q must be rejected", nonce)
		assert.Equal(t, V1CodeAuthFailed, V1Code(e))
	}
}

func TestSigVerify_UnknownApp(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	body := []byte(testBodyPlain)
	h := f.signedHeaders("ghost", testSecret, newNonce(), body)

	_, e := f.v.Verify(context.Background(), testMethod, testPath, nil, body, h, testRemoteIPAddr)
	require.NotNil(t, e)
	assert.Equal(t, http.StatusForbidden, e.Status)
	assert.Equal(t, V1CodeAppDisabled, V1Code(e))
}

func TestSigVerify_DisabledApp(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	body := []byte(testBodyPlain)
	h := f.signedHeaders(testDisabledApp, "disabled-secret", newNonce(), body)

	_, e := f.v.Verify(context.Background(), testMethod, testPath, nil, body, h, testRemoteIPAddr)
	require.NotNil(t, e)
	assert.Equal(t, http.StatusForbidden, e.Status)
	assert.Equal(t, V1CodeAppDisabled, V1Code(e))
}

func TestSigVerify_MissingHeaders(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	body := []byte(testBodyPlain)

	tests := []struct {
		name   string
		mutate func(h http.Header)
		want   int
	}{
		{name: "no app id", mutate: func(h http.Header) { h.Del(HeaderAppID) }, want: V1CodeAuthFailed},
		{name: "blank app id", mutate: func(h http.Header) { h.Set(HeaderAppID, "  ") }, want: V1CodeAuthFailed},
		{name: "no timestamp", mutate: func(h http.Header) { h.Del(HeaderTimestamp) }, want: V1CodeTimestampSkew},
		{name: "no nonce", mutate: func(h http.Header) { h.Del(HeaderNonce) }, want: V1CodeAuthFailed},
		{name: "no signature", mutate: func(h http.Header) { h.Del(HeaderSignature) }, want: V1CodeAuthFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := f.signedHeaders(testAppID, testSecret, newNonce(), body)
			tc.mutate(h)
			_, e := f.v.Verify(context.Background(), testMethod, testPath, nil, body, h, testRemoteIPAddr)
			require.NotNil(t, e)
			assert.Equal(t, http.StatusUnauthorized, e.Status)
			assert.Equal(t, tc.want, V1Code(e))
		})
	}
}

func TestSigVerify_AllowNoApp(t *testing.T) {
	f := newSigFixture(t, SigConfig{AllowNoApp: true})
	appID, e := f.v.Verify(context.Background(), testMethod, testPath, nil, nil, http.Header{}, "")
	require.Nil(t, e)
	assert.Equal(t, "dev", appID)
}

func TestSigVerify_PerAppIPAllowlist(t *testing.T) {
	f := newSigFixture(t, SigConfig{IPAllowPerApp: map[string][]string{
		testAppID: {"10.0.0.0/8"},
	}})
	body := []byte(testBodyPlain)

	_, e := f.v.Verify(context.Background(), testMethod, testPath, nil, body,
		f.signedHeaders(testAppID, testSecret, newNonce(), body), "10.1.2.3")
	assert.Nil(t, e)

	_, e = f.v.Verify(context.Background(), testMethod, testPath, nil, body,
		f.signedHeaders(testAppID, testSecret, newNonce(), body), "8.8.8.8")
	require.NotNil(t, e)
	assert.Equal(t, http.StatusForbidden, e.Status)
	assert.Equal(t, V1CodeAppDisabled, V1Code(e))
}

func TestSigVerify_ResolverErrorIs500(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	f.resolver.err = errs.Internal("store down", nil)
	_, e := f.v.Verify(context.Background(), testMethod, testPath, nil, nil,
		f.signedHeaders(testAppID, testSecret, newNonce(), nil), testRemoteIPAddr)
	require.NotNil(t, e)
	assert.Equal(t, http.StatusInternalServerError, e.Status)
}

func TestSigVerify_NoCacheStillWorks(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	v := NewSigVerifier(f.resolver, nil, SigConfig{Clock: f.clk})
	body := []byte(testBodyPlain)
	nonce := newNonce()
	h := f.signedHeaders(testAppID, testSecret, nonce, body)

	// Without a cache there is no replay protection, but signatures still verify.
	_, e := v.Verify(context.Background(), testMethod, testPath, nil, body, h, testRemoteIPAddr)
	assert.Nil(t, e)
	_, e = v.Verify(context.Background(), testMethod, testPath, nil, body, h, testRemoteIPAddr)
	assert.Nil(t, e)
}

func TestSigVerify_Unconfigured(t *testing.T) {
	var nilVerifier *SigVerifier
	_, e := nilVerifier.Verify(context.Background(), testMethod, testPath, nil, nil, http.Header{}, "")
	require.NotNil(t, e)

	noResolver := NewSigVerifier(nil, newFakeCache(), SigConfig{})
	_, e = noResolver.Verify(context.Background(), testMethod, testPath, nil, nil, http.Header{}, "")
	require.NotNil(t, e)
	assert.Equal(t, http.StatusInternalServerError, e.Status)
}

func TestSigVerifyCall_ReturnsSecret(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	body := []byte(testBodyPlain)
	nonce := newNonce()
	h := f.signedHeaders(testAppID, testSecret, nonce, body)

	call, e := f.v.VerifyCall(context.Background(), testMethod, testPath, nil, body, h, testRemoteIPAddr)
	require.Nil(t, e)
	require.NotNil(t, call)
	assert.Equal(t, testAppID, call.AppID)
	assert.Equal(t, testSecret, call.Secret)
	assert.Equal(t, nonce, call.Nonce)

	// Rotated keys report the secret that actually validated.
	call, e = f.v.VerifyCall(context.Background(), testMethod, testPath, nil, body,
		f.signedHeaders(testAppID, testSecretPrev, newNonce(), body), testRemoteIPAddr)
	require.Nil(t, e)
	assert.Equal(t, testSecretPrev, call.Secret)
}

func TestSignResponseSign(t *testing.T) {
	body := []byte(`{"code":0}`)
	got := SignResponseSign("s3cret", body, 1762300000)
	assert.Equal(t, sign.ResponseSign("s3cret", body, 1762300000), got)
	assert.Len(t, got, 64)
}

func TestV1CodeMapping(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{err: nil, want: 0},
		{err: v1Error(http.StatusUnauthorized, V1CodeNonceReplay, "x"), want: V1CodeNonceReplay},
		{err: errs.InvalidAPIKey(), want: V1CodeAuthFailed},
		{err: errs.IPNotAllowed(), want: V1CodeAppDisabled},
		{err: errs.InvalidRequest("x"), want: V1CodeBadRequest},
		{err: errs.NotFound("x"), want: 40400},
		{err: errs.Conflict("x"), want: 40900},
		{err: errs.InsufficientQuota(), want: 40200},
		{err: errs.RateLimited(""), want: 42900},
		{err: errs.FeatureDisabled("gw"), want: 50300},
		{err: errs.Internal("x", nil), want: V1CodeInternal},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, V1Code(tc.err))
	}
}

func TestInternalHTTPStatus(t *testing.T) {
	assert.Equal(t, http.StatusUnauthorized, internalHTTPStatus(V1CodeAuthFailed))
	assert.Equal(t, http.StatusUnauthorized, internalHTTPStatus(V1CodeTimestampSkew))
	assert.Equal(t, http.StatusUnauthorized, internalHTTPStatus(V1CodeNonceReplay))
	assert.Equal(t, http.StatusForbidden, internalHTTPStatus(V1CodeAppDisabled))
	assert.Equal(t, http.StatusBadRequest, internalHTTPStatus(V1CodeBadRequest))
	assert.Equal(t, http.StatusInternalServerError, internalHTTPStatus(V1CodeInternal))
	assert.Equal(t, http.StatusOK, internalHTTPStatus(0))
}

func TestIsHexNonce(t *testing.T) {
	assert.True(t, isHexNonce("0123456789abcdef0123456789abcdef"))
	assert.True(t, isHexNonce("0123456789ABCDEF0123456789ABCDEF"))
	assert.False(t, isHexNonce(""))
	assert.False(t, isHexNonce("0123456789abcdef0123456789abcde")) // 31
	assert.False(t, isHexNonce("0123456789abcdef0123456789abcdef0"))
	assert.False(t, isHexNonce("g123456789abcdef0123456789abcdef"))
}

func TestRemoteIP(t *testing.T) {
	r, err := http.NewRequest(http.MethodGet, "/x", nil)
	require.NoError(t, err)
	r.RemoteAddr = "203.0.113.7:54321"
	assert.Equal(t, "203.0.113.7", RemoteIP(r))

	r.RemoteAddr = "203.0.113.7" // no port
	assert.Equal(t, "203.0.113.7", RemoteIP(r))

	r.RemoteAddr = "[2001:db8::1]:443"
	assert.Equal(t, "2001:db8::1", RemoteIP(r))

	assert.Equal(t, "", RemoteIP(nil))
}
