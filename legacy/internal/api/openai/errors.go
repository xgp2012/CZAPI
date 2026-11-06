package openai

import (
	"encoding/json"
	"net/http"
	"strings"

	"czapi/internal/pkg/errs"
)

// errorBody is the OpenAI-compatible error envelope (plan appendix B).
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param"`
	Code    string `json:"code"`
}

// writeError emits an OpenAI-style error with the documented status and code.
func writeError(w http.ResponseWriter, e *errs.Error, requestID string) {
	if e == nil {
		e = errs.Internal("unknown error", nil)
	}
	if requestID != "" {
		w.Header().Set("X-CZ-Request-Id", requestID)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(errorBody{Error: errorDetail{
		Message: e.Message,
		Type:    errorType(e.Status),
		Param:   e.Param,
		Code:    string(e.Code),
	}})
}

// errorType maps a status to the OpenAI "type" field.
func errorType(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "invalid_request_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "invalid_request_error"
	case http.StatusPaymentRequired:
		return "insufficient_quota"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusServiceUnavailable:
		return "service_unavailable"
	default:
		if status >= 500 {
			return "api_error"
		}
		return "invalid_request_error"
	}
}

// writeJSON writes a JSON payload with the correlation header.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ConsoleConfig describes the browser console endpoints.
type ConsoleConfig struct {
	// Enabled false disables the /console routes entirely.
	Enabled bool
	// DistDir is the built SPA directory.
	DistDir string
	// RechargeURL is where users top up (V10 owns payments, plan §0).
	RechargeURL string
	// BaseURL is the public gateway origin used in docs snippets.
	BaseURL string
	// SessionCookieName is the HttpOnly cookie holding the console session.
	SessionCookieName string
	// SessionTTL bounds a console session.
	SessionTTL int64
}

// normalized applies defaults so the console works with a minimal config.
func (c ConsoleConfig) normalized() ConsoleConfig {
	if c.SessionCookieName == "" {
		c.SessionCookieName = "cz_ss"
	}
	if c.SessionTTL <= 0 {
		c.SessionTTL = 12 * 3600
	}
	return c
}

// trimSlash removes a trailing slash from a URL.
func trimSlash(s string) string { return strings.TrimRight(s, "/") }
