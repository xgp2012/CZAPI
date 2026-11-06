package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"czapi/internal/domain"
	"czapi/internal/pkg/clock"
	"czapi/internal/pkg/crypto"
	"czapi/internal/pkg/errs"
	"czapi/internal/pkg/sign"
)

// ---------------------------------------------------------------------------
// APIKeyMiddleware
// ---------------------------------------------------------------------------

func TestAPIKeyMiddleware_Success(t *testing.T) {
	f := newFixture(t)
	var seen *Principal
	h := APIKeyMiddleware(f.v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFrom(r.Context())
		require.True(t, ok, "principal must be in the context")
		seen = p
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"deepseek-chat"}`))
	req.Header.Set("Authorization", "Bearer "+testPlain)
	req.RemoteAddr = "1.2.3.4:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, seen)
	assert.Equal(t, uint64(321), seen.KeyID)
	assert.Equal(t, uint64(7), seen.AccountID)

	// Best-effort last_used_at must have been recorded.
	f.keys.mu.Lock()
	touches := append([]uint64(nil), f.keys.touches...)
	f.keys.mu.Unlock()
	assert.Equal(t, []uint64{321}, touches)
}

func TestAPIKeyMiddleware_BearerVariants(t *testing.T) {
	f := newFixture(t)
	h := APIKeyMiddleware(f.v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name   string
		header string
		want   int
	}{
		{name: "lowercase scheme accepted", header: "bearer " + testPlain, want: http.StatusOK},
		{name: "mixed case scheme accepted", header: "BeArEr " + testPlain, want: http.StatusOK},
		{name: "surrounding spaces accepted", header: "  Bearer   " + testPlain + "  ", want: http.StatusOK},
		{name: "missing header", header: "", want: http.StatusUnauthorized},
		{name: "wrong scheme", header: "Basic " + testPlain, want: http.StatusUnauthorized},
		{name: "scheme only", header: "Bearer", want: http.StatusUnauthorized},
		{name: "scheme with no token", header: "Bearer ", want: http.StatusUnauthorized},
		{name: "garbage token", header: "Bearer nope", want: http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			req.RemoteAddr = "1.2.3.4:5555"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			assert.Equal(t, tc.want, rec.Code, "header %q", tc.header)
		})
	}
}

func TestAPIKeyMiddleware_ErrorBodyShape(t *testing.T) {
	f := newFixture(t)
	h := APIKeyMiddleware(f.v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-doesnotexistatall")
	req.Header.Set(HeaderRequestID, "req-abc-123")
	req.RemoteAddr = "1.2.3.4:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "req-abc-123", rec.Header().Get(HeaderRequestID),
		"an inbound request id must be echoed")
	assert.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	inner, ok := body["error"].(map[string]any)
	require.True(t, ok, "the body must be {\"error\":{…}}")

	assert.Equal(t, "Invalid API key provided.", inner["message"])
	assert.Equal(t, "authentication_error", inner["type"])
	assert.Equal(t, "invalid_api_key", inner["code"])
	param, present := inner["param"]
	assert.True(t, present, "param key must exist")
	assert.Equal(t, "", param)
	assert.Len(t, inner, 4, "exactly message/type/param/code")
}

func TestAPIKeyMiddleware_ErrorBodyShapeForKeySuspended(t *testing.T) {
	f := newFixture(t)
	f.keys.byHash[crypto.HashKey(testSalt, testPlain)].Status = domain.KeySuspended
	h := APIKeyMiddleware(f.v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+testPlain)
	req.RemoteAddr = "1.2.3.4:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.NotEmpty(t, rec.Header().Get(HeaderRequestID), "a request id must be generated")

	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "key_suspended", body.Error.Code)
	assert.Equal(t, "permission_error", body.Error.Type)
	assert.Equal(t, "API key is suspended. Please renew your plan.", body.Error.Message)
}

func TestAPIKeyMiddleware_ModelCheckDeferredToHandler(t *testing.T) {
	f := newFixture(t)
	f.keys.byHash[crypto.HashKey(testSalt, testPlain)].Models = []string{"deepseek-chat"}

	// The middleware authenticates without a model, so a key restricted to
	// deepseek-chat still reaches the handler; the handler then applies CheckModel.
	reached := false
	h := APIKeyMiddleware(f.v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		p, _ := PrincipalFrom(r.Context())
		if e := f.v.CheckModel(p, "gpt-4o"); e != nil {
			WriteOpenAIError(w, e, RequestID(r))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o"}`))
	req.Header.Set("Authorization", "Bearer "+testPlain)
	req.RemoteAddr = "1.2.3.4:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.True(t, reached)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "model_not_allowed")
}

func TestAPIKeyMiddleware_NilNext(t *testing.T) {
	f := newFixture(t)
	h := APIKeyMiddleware(f.v)(nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+testPlain)
	req.RemoteAddr = "1.2.3.4:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestPrincipalFromAndWithPrincipal(t *testing.T) {
	_, ok := PrincipalFrom(context.Background())
	assert.False(t, ok)

	p := &Principal{KeyID: 9}
	got, ok := PrincipalFrom(WithPrincipal(context.Background(), p))
	require.True(t, ok)
	assert.Equal(t, uint64(9), got.KeyID)
}

func TestAdminFromAndWithAdmin(t *testing.T) {
	_, ok := AdminFrom(context.Background())
	assert.False(t, ok)

	c := &AdminClaims{AdminID: 3, Role: RoleAdmin}
	got, ok := AdminFrom(WithAdmin(context.Background(), c))
	require.True(t, ok)
	assert.Equal(t, uint64(3), got.AdminID)

	// A wrongly-typed context value must not panic.
	bad := context.WithValue(context.Background(), adminKey{}, "not-claims")
	_, ok = AdminFrom(bad)
	assert.False(t, ok)
}

// ---------------------------------------------------------------------------
// AdminJWTMiddleware
// ---------------------------------------------------------------------------

func TestAdminJWTMiddleware_Success(t *testing.T) {
	j, _ := newJWT(t)
	token, _, err := j.Issue(11, "root", RoleAdmin)
	require.NoError(t, err)

	var seen *AdminClaims
	h := AdminJWTMiddleware(j)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := AdminFrom(r.Context())
		require.True(t, ok)
		seen = c
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin/v1/accounts", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, seen)
	assert.Equal(t, uint64(11), seen.AdminID)
	assert.Equal(t, "root", seen.Username)
}

func TestAdminJWTMiddleware_Rejections(t *testing.T) {
	j, clk := newJWT(t)
	viewerToken, _, err := j.Issue(2, "viewer", RoleViewer)
	require.NoError(t, err)
	expiredToken, _, err := NewJWTIssuer(JWTConfig{
		Secret: testJWTSecret,
		TTL:    time.Minute,
		Clock:  clock.NewFake(clk.Now().Add(-time.Hour)),
	}).Issue(3, "old", RoleAdmin)
	require.NoError(t, err)

	tests := []struct {
		name   string
		header string
		roles  []string
		status int
	}{
		{name: "missing token", header: "", roles: nil, status: http.StatusUnauthorized},
		{name: "wrong scheme", header: "Basic abc", roles: nil, status: http.StatusUnauthorized},
		{name: "malformed token", header: "Bearer nope", roles: nil, status: http.StatusUnauthorized},
		{name: "expired token", header: "Bearer " + expiredToken, roles: nil, status: http.StatusUnauthorized},
		{name: "role mismatch", header: "Bearer " + viewerToken, roles: []string{RoleOperator}, status: http.StatusForbidden},
		{name: "role match", header: "Bearer " + viewerToken, roles: []string{RoleViewer}, status: http.StatusOK},
		{name: "no role restriction", header: "Bearer " + viewerToken, roles: nil, status: http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := AdminJWTMiddleware(j, tc.roles...)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(http.MethodGet, "/admin/v1/x", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			assert.Equal(t, tc.status, rec.Code)
			if tc.status != http.StatusOK {
				assert.Contains(t, rec.Body.String(), `"error"`)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// InternalSigMiddleware
// ---------------------------------------------------------------------------

func TestInternalSigMiddleware_BodyRestore(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	body := []byte(`{"v10_client_id":1234,"amount_micros":100000000}`)
	nonce := newNonce()
	ts := f.now.Unix()
	h := make(http.Header)
	h.Set(HeaderAppID, testAppID)
	h.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	h.Set(HeaderNonce, nonce)
	h.Set(HeaderSignature, sign.Sign(testSecret, sign.StringToSign(http.MethodPost, "/internal/v1/credit", "", body, ts, nonce)))

	var echoed []byte
	var appID string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		read, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		echoed = read
		if call, ok := InternalCallFrom(r.Context()); ok {
			appID = call.AppID
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":null}`))
	})

	req := httptest.NewRequest(http.MethodPost, "/internal/v1/credit", bytes.NewReader(body))
	req.Header = h
	req.RemoteAddr = testRemoteIPAddr + ":443"
	rec := httptest.NewRecorder()
	InternalSigMiddleware(f.v)(inner).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, body, echoed, "the handler must read the exact signed bytes")
	assert.Equal(t, testAppID, appID)
}

func TestInternalSigMiddleware_SkipsHealth(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	// No signature headers at all: the health path must still pass through.
	req := httptest.NewRequest(http.MethodGet, HealthPath, nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	InternalSigMiddleware(f.v)(inner).ServeHTTP(rec, req)

	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestInternalSigMiddleware_HealthSkipIsGettedOnly(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := InternalSigMiddleware(f.v)(inner)

	// GET is the documented unsigned probe.
	req := httptest.NewRequest(http.MethodGet, HealthPath, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rec.Code)

	// A POST to the same path is not the probe and must be signed.
	called = false
	req = httptest.NewRequest(http.MethodPost, HealthPath, strings.NewReader(`{}`))
	req.RemoteAddr = "127.0.0.1:1"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.False(t, called, "only GET on the health path bypasses verification")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestInternalSigMiddleware_CustomSkipPath(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/ping", nil)
	rec := httptest.NewRecorder()
	InternalSigMiddleware(f.v, "/internal/v1/ping", "")(inner).ServeHTTP(rec, req)
	assert.True(t, called)
}

func TestInternalSigMiddleware_RejectsUnsigned(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodPost, "/internal/v1/credit", strings.NewReader(`{}`))
	req.Header.Set(HeaderRequestID, "trace-1")
	req.RemoteAddr = testRemoteIPAddr + ":443"
	rec := httptest.NewRecorder()
	InternalSigMiddleware(f.v)(inner).ServeHTTP(rec, req)

	assert.False(t, called, "an unsigned request must not reach the handler")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "trace-1", rec.Header().Get(HeaderRequestID))

	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data any    `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.Equal(t, V1CodeAuthFailed, env.Code)
	assert.NotEmpty(t, env.Msg)
	assert.Nil(t, env.Data)
}

func TestInternalSigMiddleware_ReplayRejected(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	body := []byte(`{}`)
	nonce := newNonce()
	ts := f.now.Unix()
	hdr := make(http.Header)
	hdr.Set(HeaderAppID, testAppID)
	hdr.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	hdr.Set(HeaderNonce, nonce)
	hdr.Set(HeaderSignature, sign.Sign(testSecret, sign.StringToSign(http.MethodPost, "/internal/v1/credit", "", body, ts, nonce)))

	newReq := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/internal/v1/credit", bytes.NewReader(body))
		r.Header = hdr.Clone()
		r.RemoteAddr = testRemoteIPAddr + ":443"
		return r
	}
	handler := InternalSigMiddleware(f.v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newReq())
	require.Equal(t, http.StatusOK, rec.Code)

	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, newReq())
	require.Equal(t, http.StatusUnauthorized, rec2.Code)

	var env struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(rec2.Body.Bytes(), &env))
	assert.Equal(t, V1CodeNonceReplay, env.Code)
}

func TestInternalSigMiddleware_EmptyBody(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	nonce := newNonce()
	ts := f.now.Unix()
	hdr := make(http.Header)
	hdr.Set(HeaderAppID, testAppID)
	hdr.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	hdr.Set(HeaderNonce, nonce)
	// Empty body signs sha256("").
	hdr.Set(HeaderSignature, sign.Sign(testSecret, sign.StringToSign(http.MethodGet, "/internal/v1/models", "", nil, ts, nonce)))

	var readLen int
	handler := InternalSigMiddleware(f.v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		readLen = len(b)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/models", nil)
	req.Header = hdr
	req.RemoteAddr = testRemoteIPAddr + ":443"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Zero(t, readLen)
}

func TestInternalSigMiddleware_BodyTooLarge(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	big := bytes.Repeat([]byte("a"), MaxInternalBodyBytes+10)
	called := false
	handler := InternalSigMiddleware(f.v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodPost, "/internal/v1/credit", bytes.NewReader(big))
	req.RemoteAddr = testRemoteIPAddr + ":443"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.False(t, called)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "too large")
}

func TestInternalSigMiddleware_NilNext(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	handler := InternalSigMiddleware(f.v)(nil)
	req := httptest.NewRequest(http.MethodGet, HealthPath, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestSignResponseMiddleware(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	payload := []byte(`{"code":0,"msg":"ok","data":{"applied":true}}`)

	handler := InternalSigMiddleware(f.v)(f.v.SignResponse(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	})))

	body := []byte(`{"ref_id":"ORD1"}`)
	nonce := newNonce()
	ts := f.now.Unix()
	hdr := make(http.Header)
	hdr.Set(HeaderAppID, testAppID)
	hdr.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	hdr.Set(HeaderNonce, nonce)
	hdr.Set(HeaderSignature, sign.Sign(testSecret, sign.StringToSign(http.MethodPost, "/internal/v1/credit", "", body, ts, nonce)))

	req := httptest.NewRequest(http.MethodPost, "/internal/v1/credit", bytes.NewReader(body))
	req.Header = hdr
	req.RemoteAddr = testRemoteIPAddr + ":443"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, payload, rec.Body.Bytes(), "the body must be passed through unchanged")
	assert.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, strconv.FormatInt(ts, 10), rec.Header().Get(HeaderServerTime))
	assert.Equal(t, sign.ResponseSign(testSecret, payload, ts), rec.Header().Get(HeaderServerSign))
}

func TestSignResponseMiddleware_NoCall(t *testing.T) {
	f := newSigFixture(t, SigConfig{})
	// Without InternalSigMiddleware there is no secret, so it must not sign and
	// must not swallow the response.
	handler := f.v.SignResponse(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hi"))
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "hi", rec.Body.String())
	assert.Empty(t, rec.Header().Get(HeaderServerSign))
}

func TestWriteOpenAIError_NilError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteOpenAIError(rec, nil, "")
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotEmpty(t, rec.Header().Get(HeaderRequestID))
	assert.Contains(t, rec.Body.String(), "internal_error")
}

func TestWriteOpenAIErrorJSON(t *testing.T) {
	raw := WriteOpenAIErrorJSON(errs.ModelNotAllowed("gpt-4o"), "rid")
	var body map[string]map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))
	assert.Equal(t, "model_not_allowed", body["error"]["code"])
	assert.Equal(t, "permission_error", body["error"]["type"])

	// A nil error still produces a well-formed document.
	raw = WriteOpenAIErrorJSON(nil, "rid")
	require.True(t, json.Valid(raw))
}

func TestWriteInternalError_Defaults(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteInternalError(rec, 0, "", "")
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotEmpty(t, rec.Header().Get(HeaderRequestID))
	var env map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.Equal(t, float64(V1CodeInternal), env["code"])
	assert.Equal(t, "internal error", env["msg"])
}

func TestRequestID(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	first := RequestID(r)
	assert.NotEmpty(t, first)

	r.Header.Set(HeaderRequestID, "given")
	assert.Equal(t, "given", RequestID(r))

	assert.NotEmpty(t, RequestID(nil))
}

func TestOpenAIErrorType(t *testing.T) {
	assert.Equal(t, "authentication_error", openAIErrorType(http.StatusUnauthorized, ""))
	assert.Equal(t, "permission_error", openAIErrorType(http.StatusForbidden, ""))
	assert.Equal(t, "insufficient_quota", openAIErrorType(http.StatusPaymentRequired, ""))
	assert.Equal(t, "rate_limit_error", openAIErrorType(http.StatusTooManyRequests, ""))
	assert.Equal(t, "invalid_request_error", openAIErrorType(http.StatusBadRequest, ""))
	assert.Equal(t, "not_found_error", openAIErrorType(http.StatusNotFound, ""))
	assert.Equal(t, "server_error", openAIErrorType(http.StatusBadGateway, ""))
	assert.Equal(t, "custom_code", openAIErrorType(http.StatusConflict, errs.Code("custom_code")))
}
