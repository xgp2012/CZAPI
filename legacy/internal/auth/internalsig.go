package auth

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"czapi/internal/cache"
	"czapi/internal/pkg/clock"
	"czapi/internal/pkg/errs"
	"czapi/internal/pkg/sign"
)

// Request header names of the V10 signing protocol (docs/internal-api-v1.md §1.2).
const (
	HeaderAppID      = "X-CZ-App-Id"
	HeaderTimestamp  = "X-CZ-Timestamp"
	HeaderNonce      = "X-CZ-Nonce"
	HeaderSignature  = "X-CZ-Signature"
	HeaderServerSign = "X-CZ-Sign-Server"
	HeaderServerTime = "X-CZ-Timestamp-Server"
	HeaderRequestID  = "X-CZ-Request-Id"
	nonceHexLength   = 32
	devAppID         = "dev"
	// DefaultClockSkew is the accepted |now-timestamp| window (§1.2).
	DefaultClockSkew = 300 * time.Second
	// DefaultNonceTTL is how long nonces are remembered.
	DefaultNonceTTL = 300 * time.Second
)

// Internal API business codes (docs/internal-api-v1.md §1.4). They travel in the
// {"code":…} envelope rather than in the HTTP status.
const (
	// V1CodeAuthFailed is a signature/credential failure.
	V1CodeAuthFailed = 40100
	// V1CodeTimestampSkew is a timestamp outside the accepted window.
	V1CodeTimestampSkew = 40101
	// V1CodeNonceReplay is a replayed nonce.
	V1CodeNonceReplay = 40102
	// V1CodeAppDisabled is an unknown or disabled app_id.
	V1CodeAppDisabled = 40300
	// V1CodeBadRequest is a malformed request.
	V1CodeBadRequest = 40000
	// V1CodeMissingRef signals a missing idempotency voucher (ref_type/ref_id).
	V1CodeMissingRef = 40001
	// V1CodeInternal is an unexpected server failure.
	V1CodeInternal = 50000
)

// Resolver resolves an app_id to its HMAC secrets. Implementations are expected
// to be backed by the site registry (config.Internal.Sites or store.Sites).
type Resolver interface {
	// Lookup returns the current secret, the optional previous secret kept for
	// rotation, and whether the app is enabled. A nil error with empty secrets
	// means "unknown app_id".
	Lookup(ctx context.Context, appID string) (secret, secretPrev string, enabled bool, err error)
}

// SigConfig configures the internal HMAC verifier.
type SigConfig struct {
	// Clock supplies the current time. nil means clock.System{}.
	Clock clock.Clock
	// ClockSkew is the maximum accepted |now - timestamp|. ≤0 means DefaultClockSkew.
	ClockSkew time.Duration
	// NonceTTL is how long a nonce is remembered for replay protection.
	// ≤0 means DefaultNonceTTL.
	NonceTTL time.Duration
	// AllowNoApp disables signature verification entirely (LOCAL DEV ONLY).
	AllowNoApp bool
	// IPAllowPerApp optionally restricts each app_id to a set of CIDRs/IPs.
	IPAllowPerApp map[string][]string
}

// SigVerifier validates signed /internal/v1 requests.
// Safe for concurrent use.
type SigVerifier struct {
	resolver Resolver
	cache    cache.Cache
	cfg      SigConfig
}

// NewSigVerifier builds a SigVerifier, applying defaults for zero-valued options.
func NewSigVerifier(r Resolver, c cache.Cache, cfg SigConfig) *SigVerifier {
	if cfg.Clock == nil {
		cfg.Clock = clock.System{}
	}
	if cfg.ClockSkew <= 0 {
		cfg.ClockSkew = DefaultClockSkew
	}
	if cfg.NonceTTL <= 0 {
		cfg.NonceTTL = DefaultNonceTTL
	}
	return &SigVerifier{resolver: r, cache: c, cfg: cfg}
}

// ClockSkew returns the effective clock window.
func (v *SigVerifier) ClockSkew() time.Duration { return v.cfg.ClockSkew }

// NonceTTL returns the effective nonce retention.
func (v *SigVerifier) NonceTTL() time.Duration { return v.cfg.NonceTTL }

// SignedCall records what was proven about an authenticated internal request.
type SignedCall struct {
	AppID  string
	Secret string
	Nonce  string
}

type signedCallKey struct{}

// InternalCallFrom returns the SignedCall stored by InternalSigMiddleware.
func InternalCallFrom(ctx context.Context) (*SignedCall, bool) {
	call, ok := ctx.Value(signedCallKey{}).(*SignedCall)
	return call, ok
}

// WithInternalCall attaches call to ctx. It is exported so non-HTTP callers
// (queues, tests) can reuse SignResponse-style helpers.
func WithInternalCall(ctx context.Context, call *SignedCall) context.Context {
	return context.WithValue(ctx, signedCallKey{}, call)
}

// Verify validates a signed request and returns the app_id on success.
//
// The checks run in this order and the first failure wins:
//
//  1. AllowNoApp bypass (returns "dev"),
//  2. presence of X-CZ-App-Id,
//  3. resolver: unknown or disabled app_id => 403/40300,
//  4. parseable timestamp within ClockSkew => 401/40101,
//  5. per-app IP allowlist => 403/40300,
//  6. nonce shape and replay protection => 401/40102,
//  7. HMAC over the canonical string with the current secret, then the previous
//     secret (key rotation) => 401/40100.
func (v *SigVerifier) Verify(ctx context.Context, method, path string, query url.Values, body []byte, hdr http.Header, remoteIP string) (string, *errs.Error) {
	call, e := v.VerifyCall(ctx, method, path, query, body, hdr, remoteIP)
	if e != nil {
		return "", e
	}
	return call.AppID, nil
}

// VerifyCall behaves exactly like Verify but also returns the secret that
// validated the request, which SignResponse needs to sign the reply.
func (v *SigVerifier) VerifyCall(ctx context.Context, method, path string, query url.Values, body []byte, hdr http.Header, remoteIP string) (*SignedCall, *errs.Error) {
	if v == nil {
		return nil, v1Error(http.StatusInternalServerError, V1CodeInternal, "signature verifier is not configured")
	}
	if v.cfg.AllowNoApp {
		return &SignedCall{AppID: devAppID}, nil
	}
	if v.resolver == nil {
		return nil, v1Error(http.StatusInternalServerError, V1CodeInternal, "site resolver is not configured")
	}

	appID := strings.TrimSpace(hdr.Get(HeaderAppID))
	if appID == "" {
		return nil, v1Error(http.StatusUnauthorized, V1CodeAuthFailed, "missing "+HeaderAppID)
	}
	secret, secretPrev, enabled, err := v.resolver.Lookup(ctx, appID)
	if err != nil {
		return nil, errs.Internal("auth: site lookup failed", err)
	}
	if secret == "" && secretPrev == "" {
		return nil, v1Error(http.StatusForbidden, V1CodeAppDisabled, "unknown app_id")
	}
	if !enabled {
		return nil, v1Error(http.StatusForbidden, V1CodeAppDisabled, "app_id is disabled")
	}

	rawTS := strings.TrimSpace(hdr.Get(HeaderTimestamp))
	ts, convErr := strconv.ParseInt(rawTS, 10, 64)
	if convErr != nil {
		return nil, v1Error(http.StatusUnauthorized, V1CodeTimestampSkew, "invalid timestamp")
	}
	skew := v.cfg.Clock.Now().Sub(time.Unix(ts, 0))
	if skew < 0 {
		skew = -skew
	}
	if skew > v.cfg.ClockSkew {
		return nil, v1Error(http.StatusUnauthorized, V1CodeTimestampSkew, "timestamp outside the accepted window")
	}

	if allow, ok := v.cfg.IPAllowPerApp[appID]; ok && len(allow) > 0 && !IPAllowed(allow, remoteIP) {
		return nil, v1Error(http.StatusForbidden, V1CodeAppDisabled, "source IP is not allowed for this app_id")
	}

	nonce := strings.TrimSpace(hdr.Get(HeaderNonce))
	if !isHexNonce(nonce) {
		return nil, v1Error(http.StatusUnauthorized, V1CodeAuthFailed, "invalid nonce")
	}
	if v.cache != nil {
		ok, err := v.cache.CheckNonce(ctx, appID, nonce, v.cfg.NonceTTL)
		if err != nil {
			return nil, errs.Internal("auth: nonce check failed", err)
		}
		if !ok {
			return nil, v1Error(http.StatusUnauthorized, V1CodeNonceReplay, "nonce already used")
		}
	}

	provided := hdr.Get(HeaderSignature)
	toSign := sign.StringToSign(method, path, sign.CanonicalQuery(query), body, ts, nonce)
	if secret != "" && sign.Verify(secret, toSign, provided) {
		return &SignedCall{AppID: appID, Secret: secret, Nonce: nonce}, nil
	}
	if secretPrev != "" && sign.Verify(secretPrev, toSign, provided) {
		return &SignedCall{AppID: appID, Secret: secretPrev, Nonce: nonce}, nil
	}
	return nil, v1Error(http.StatusUnauthorized, V1CodeAuthFailed, "signature verification failed")
}

// SignResponse wraps next so the response carries X-CZ-Sign-Server and
// X-CZ-Timestamp-Server computed from the secret proven by Verify. It must be
// installed *inside* InternalSigMiddleware; without a SignedCall in the request
// context it is a no-op passthrough.
func (v *SigVerifier) SignResponse(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call, ok := InternalCallFrom(r.Context())
		if !ok || call == nil || call.Secret == "" || v == nil {
			next.ServeHTTP(w, r)
			return
		}
		rec := &bufferedResponse{}
		next.ServeHTTP(rec, r)
		ts := v.cfg.Clock.Now().Unix()
		for k, vals := range rec.header {
			for _, val := range vals {
				w.Header().Add(k, val)
			}
		}
		w.Header().Set(HeaderServerSign, sign.ResponseSign(call.Secret, rec.body.Bytes(), ts))
		w.Header().Set(HeaderServerTime, strconv.FormatInt(ts, 10))
		status := rec.status
		if !rec.wroteHeader {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write(rec.body.Bytes())
	})
}

// SignResponseSign computes the value of X-CZ-Sign-Server for a body/timestamp
// pair. It is the building block SignResponse uses, exposed for callers that do
// not go through the middleware.
func SignResponseSign(secret string, body []byte, timestamp int64) string {
	return sign.ResponseSign(secret, body, timestamp)
}

// bufferedResponse captures a handler's status, headers and body so the response
// can be signed before anything reaches the client. It intentionally does not
// implement http.Flusher: the internal API returns buffered JSON, not SSE.
type bufferedResponse struct {
	header      http.Header
	status      int
	wroteHeader bool
	body        bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header {
	if b.header == nil {
		b.header = make(http.Header)
	}
	return b.header
}

func (b *bufferedResponse) WriteHeader(status int) {
	if b.wroteHeader {
		return // first status wins, mirroring net/http
	}
	b.status = status
	b.wroteHeader = true
}

func (b *bufferedResponse) Write(p []byte) (int, error) {
	if !b.wroteHeader {
		b.WriteHeader(http.StatusOK)
	}
	return b.body.Write(p)
}

// isHexNonce reports whether nonce is exactly 32 lowercase/uppercase hex chars.
func isHexNonce(nonce string) bool {
	if len(nonce) != nonceHexLength {
		return false
	}
	for i := 0; i < len(nonce); i++ {
		c := nonce[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// v1Error builds an *errs.Error carrying an /internal/v1 business code.
func v1Error(status, code int, msg string) *errs.Error {
	return errs.New(status, errs.Code(strconv.Itoa(code)), msg)
}

// V1Code maps an error to the /internal/v1 business code table (§1.4).
// Numeric codes produced by this package round-trip; other *errs.Error values
// fall back to a code derived from the HTTP status.
func V1Code(err error) int {
	e := errs.As(err)
	if e == nil {
		return 0
	}
	if n, convErr := strconv.Atoi(string(e.Code)); convErr == nil {
		return n
	}
	switch e.Status {
	case http.StatusBadRequest:
		return V1CodeBadRequest
	case http.StatusUnauthorized:
		return V1CodeAuthFailed
	case http.StatusForbidden:
		return V1CodeAppDisabled
	case http.StatusNotFound:
		return 40400
	case http.StatusConflict:
		return 40900
	case http.StatusPaymentRequired:
		return 40200
	case http.StatusTooManyRequests:
		return 42900
	case http.StatusServiceUnavailable:
		return 50300
	default:
		return V1CodeInternal
	}
}

// RemoteIP extracts the caller address from a request, preferring RemoteAddr and
// never trusting client-supplied forwarding headers.
func RemoteIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr)); err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}
