package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"czapi/internal/pkg/errs"
	"czapi/internal/pkg/idgen"
)

// MaxInternalBodyBytes caps how much of an /internal/v1 body the signature
// middleware will buffer before rejecting the request.
const MaxInternalBodyBytes = 1 << 20 // 1 MiB

type (
	principalKey struct{}
	adminKey     struct{}
)

// PrincipalFrom returns the Principal stored by APIKeyMiddleware.
func PrincipalFrom(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(*Principal)
	return p, ok
}

// WithPrincipal attaches p to ctx; mainly useful in tests and non-HTTP callers.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// AdminFrom returns the AdminClaims stored by AdminJWTMiddleware.
func AdminFrom(ctx context.Context) (*AdminClaims, bool) {
	c, ok := ctx.Value(adminKey{}).(*AdminClaims)
	return c, ok
}

// WithAdmin attaches claims to ctx; mainly useful in tests.
func WithAdmin(ctx context.Context, c *AdminClaims) context.Context {
	return context.WithValue(ctx, adminKey{}, c)
}

// bearerToken extracts the token from an "Authorization: Bearer x" header,
// returning "" when the scheme or value is missing.
func bearerToken(r *http.Request) string {
	if r == nil {
		return ""
	}
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(h) < 7 || !strings.EqualFold(h[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

// RequestID returns the client-supplied X-CZ-Request-Id or a fresh ULID. It never
// returns an empty string, so every error response is traceable.
func RequestID(r *http.Request) string {
	if r != nil {
		if v := strings.TrimSpace(r.Header.Get(HeaderRequestID)); v != "" {
			return v
		}
	}
	return idgen.ULID()
}

// WriteOpenAIError writes e using the OpenAI-compatible error body
//
//	{"error":{"message":…,"type":…,"param":…,"code":…}}
//
// and sets X-CZ-Request-Id (plan appendix B). A nil e is treated as a generic
// internal error so the helper never panics.
func WriteOpenAIError(w http.ResponseWriter, e *errs.Error, requestID string) {
	if w == nil {
		return
	}
	if e == nil {
		e = errs.Internal("unknown error", nil)
	}
	if requestID == "" {
		requestID = idgen.ULID()
	}
	body := openAIErrorBody{Error: openAIError{
		Message: e.Message,
		Type:    openAIErrorType(e.Status, e.Code),
		Param:   e.Param,
		Code:    string(e.Code),
	}}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set(HeaderRequestID, requestID)
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(body)
}

// WriteOpenAIErrorJSON marshals the OpenAI error body, for callers that build the
// response themselves.
func WriteOpenAIErrorJSON(e *errs.Error, requestID string) []byte {
	if e == nil {
		e = errs.Internal("unknown error", nil)
	}
	body := openAIErrorBody{Error: openAIError{
		Message: e.Message,
		Type:    openAIErrorType(e.Status, e.Code),
		Param:   e.Param,
		Code:    string(e.Code),
	}}
	out, err := json.Marshal(body)
	if err != nil {
		return []byte(`{"error":{"message":"internal error","type":"server_error","param":null,"code":"internal_error"}}`)
	}
	return out
}

type openAIErrorBody struct {
	Error openAIError `json:"error"`
}

type openAIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param"`
	Code    string `json:"code"`
}

// openAIErrorType maps a status/code pair onto the OpenAI "type" vocabulary.
func openAIErrorType(status int, code errs.Code) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusPaymentRequired:
		return "insufficient_quota"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusNotFound:
		return "not_found_error"
	}
	if status >= 500 {
		return "server_error"
	}
	if code != "" {
		return string(code)
	}
	return "invalid_request_error"
}

// APIKeyMiddleware authenticates the Bearer API key and stores the resulting
// *Principal in the request context.
//
// It does not inspect the request body: the model whitelist is enforced by the
// handler, which knows the model, via Verifier.CheckModel. The subscription,
// status, expiry, account and IP checks all run here. On success it also records
// last_used_at (best effort).
func APIKeyMiddleware(v *Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if next == nil {
			next = http.NotFoundHandler()
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := RequestID(r)
			token := bearerToken(r)
			if token == "" {
				WriteOpenAIError(w, errs.InvalidAPIKey(), reqID)
				return
			}
			principal, e := v.Authenticate(r.Context(), token, RemoteIP(r), "")
			if e != nil {
				WriteOpenAIError(w, e, reqID)
				return
			}
			v.TouchUsed(r.Context(), principal.KeyID)
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	}
}

// AdminJWTMiddleware verifies the Bearer admin token, optionally enforces a role
// allow-list and stores the *AdminClaims in the request context.
func AdminJWTMiddleware(j *JWTIssuer, roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if next == nil {
			next = http.NotFoundHandler()
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := RequestID(r)
			token := bearerToken(r)
			if token == "" {
				WriteOpenAIError(w, errs.New(http.StatusUnauthorized, errs.CodeInvalidAPIKey, "Missing admin token."), reqID)
				return
			}
			claims, err := j.Verify(token)
			if err != nil {
				WriteOpenAIError(w, errs.As(err), reqID)
				return
			}
			if err := RequireRole(claims, roles...); err != nil {
				WriteOpenAIError(w, errs.As(err), reqID)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithAdmin(r.Context(), claims)))
		})
	}
}

// InternalSigMiddleware verifies the V10 request signature, buffers the body so
// the handler can still read it, and stores the proven SignedCall in the request
// context. skipPaths lists exact paths that bypass verification; only the
// unsigned GET probe (HealthPath, docs/internal-api-v1.md §2.1) is skipped by
// default. Other methods on a skipped path are still verified.
//
// The body is capped at MaxInternalBodyBytes; oversized bodies are rejected with
// HTTP 400 and code 40000.
func InternalSigMiddleware(v *SigVerifier, skipPaths ...string) func(http.Handler) http.Handler {
	skip := make(map[string]struct{}, len(skipPaths)+1)
	for _, p := range skipPaths {
		if p != "" {
			skip[p] = struct{}{}
		}
	}
	skip[HealthPath] = struct{}{}
	return func(next http.Handler) http.Handler {
		if next == nil {
			next = http.NotFoundHandler()
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := skip[r.URL.Path]; ok && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
				next.ServeHTTP(w, r)
				return
			}
			reqID := RequestID(r)

			body, tooLarge, err := readBody(r)
			if err != nil {
				WriteInternalError(w, V1CodeBadRequest, "cannot read request body", reqID)
				return
			}
			if tooLarge {
				WriteInternalError(w, V1CodeBadRequest, "request body too large", reqID)
				return
			}
			// Restore the body so the handler (and any re-reading middleware)
			// sees exactly the bytes that were signed.
			r.Body = io.NopCloser(bytes.NewReader(body))

			call, e := v.VerifyCall(r.Context(), r.Method, r.URL.Path, r.URL.Query(), body, r.Header, RemoteIP(r))
			if e != nil {
				WriteInternalError(w, V1Code(e), e.Message, reqID)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithInternalCall(r.Context(), call)))
		})
	}
}

// HealthPath is the unsigned connectivity probe of the internal API.
const HealthPath = "/internal/v1/health"

// readBody reads at most MaxInternalBodyBytes+1 bytes. tooLarge reports that the
// cap was exceeded; body then holds the truncated prefix and must be discarded.
func readBody(r *http.Request) (body []byte, tooLarge bool, err error) {
	if r.Body == nil {
		return nil, false, nil
	}
	limited := io.LimitReader(r.Body, MaxInternalBodyBytes+1)
	body, err = io.ReadAll(limited)
	if err != nil {
		return nil, false, err
	}
	if len(body) > MaxInternalBodyBytes {
		return nil, true, nil
	}
	return body, false, nil
}

// WriteInternalError writes the /internal/v1 envelope
// {"code":N,"msg":"…","data":null} (docs/internal-api-v1.md §1.1).
func WriteInternalError(w http.ResponseWriter, code int, msg, requestID string) {
	if w == nil {
		return
	}
	if code == 0 {
		code = V1CodeInternal
	}
	if msg == "" {
		msg = "internal error"
	}
	status := internalHTTPStatus(code)
	if requestID == "" {
		requestID = idgen.ULID()
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set(HeaderRequestID, requestID)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(internalEnvelope{Code: code, Msg: msg, Data: nil})
}

type internalEnvelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data"`
}

// internalHTTPStatus maps an /internal/v1 business code to its HTTP status.
func internalHTTPStatus(code int) int {
	switch code {
	case 0:
		return http.StatusOK
	case V1CodeBadRequest, V1CodeMissingRef:
		return http.StatusBadRequest
	case V1CodeAuthFailed, V1CodeTimestampSkew, V1CodeNonceReplay:
		return http.StatusUnauthorized
	case V1CodeAppDisabled:
		return http.StatusForbidden
	case 40400:
		return http.StatusNotFound
	case 40200:
		return http.StatusPaymentRequired
	case 40900:
		return http.StatusConflict
	case 42900:
		return http.StatusTooManyRequests
	case 50300:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
