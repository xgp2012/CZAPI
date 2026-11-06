// Package errs defines the error vocabulary shared by every layer.
//
// The OpenAI-compatible layer maps these to the response bodies in plan
// appendix B, so handlers only ever return an *Error and the mapping stays in
// one place (internal/api/openai/errors.go).
package errs

import (
	"errors"
	"fmt"
	"net/http"
)

// Code is a stable machine-readable error code (plan appendix B).
type Code string

const (
	CodeInvalidAPIKey      Code = "invalid_api_key"
	CodeKeySuspended       Code = "key_suspended"
	CodeModelNotAllowed    Code = "model_not_allowed"
	CodeIPNotAllowed       Code = "ip_not_allowed"
	CodeInsufficientQuota  Code = "insufficient_quota"
	CodeRateLimitExceeded  Code = "rate_limit_exceeded"
	CodeInvalidRequest     Code = "invalid_request_error"
	CodeUpstreamError      Code = "upstream_error"
	CodeUpstreamTimeout    Code = "upstream_timeout"
	CodeChannelUnavailable Code = "channel_unavailable"
	CodeNotFound           Code = "not_found"
	CodeConflict           Code = "conflict"
	CodeSignInvalid        Code = "sign_invalid"
	CodeFeatureDisabled    Code = "feature_disabled"
	CodeInternal           Code = "internal_error"
)

// Error is a domain error carrying an HTTP status and a stable code.
type Error struct {
	Status  int
	Code    Code
	Message string
	// Param mirrors the OpenAI error body field.
	Param string
	// Err keeps the wrapped cause for logging (never sent to clients).
	Err error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// New builds an *Error.
func New(status int, code Code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg}
}

// Wrap builds an *Error carrying a cause.
func Wrap(status int, code Code, msg string, err error) *Error {
	return &Error{Status: status, Code: code, Message: msg, Err: err}
}

// Convenience constructors for the cases used most often.

func InvalidAPIKey() *Error {
	return New(http.StatusUnauthorized, CodeInvalidAPIKey, "Invalid API key provided.")
}

func KeySuspended(msg string) *Error {
	if msg == "" {
		msg = "API key is suspended. Please renew your plan."
	}
	return New(http.StatusForbidden, CodeKeySuspended, msg)
}

func ModelNotAllowed(model string) *Error {
	return New(http.StatusForbidden, CodeModelNotAllowed, "Model "+model+" is not available in your plan.")
}

func IPNotAllowed() *Error {
	return New(http.StatusForbidden, CodeIPNotAllowed, "Request from a non-whitelisted IP.")
}

func InsufficientQuota() *Error {
	return New(http.StatusPaymentRequired, CodeInsufficientQuota, "Quota exceeded. Please top up.")
}

func RateLimited(msg string) *Error {
	if msg == "" {
		msg = "Rate limit reached. Please retry later."
	}
	return New(http.StatusTooManyRequests, CodeRateLimitExceeded, msg)
}

func InvalidRequest(msg string) *Error {
	return New(http.StatusBadRequest, CodeInvalidRequest, msg)
}

func Upstream(msg string, err error) *Error {
	return Wrap(http.StatusBadGateway, CodeUpstreamError, msg, err)
}

func UpstreamTimeout(msg string, err error) *Error {
	return Wrap(http.StatusGatewayTimeout, CodeUpstreamTimeout, msg, err)
}

func ChannelUnavailable(msg string) *Error {
	if msg == "" {
		msg = "Service temporarily unavailable."
	}
	return New(http.StatusServiceUnavailable, CodeChannelUnavailable, msg)
}

func NotFound(msg string) *Error {
	return New(http.StatusNotFound, CodeNotFound, msg)
}

func Conflict(msg string) *Error {
	return New(http.StatusConflict, CodeConflict, msg)
}

func SignInvalid(msg string) *Error {
	if msg == "" {
		msg = "signature verification failed"
	}
	return New(http.StatusUnauthorized, CodeSignInvalid, msg)
}

func FeatureDisabled(flag string) *Error {
	return New(http.StatusServiceUnavailable, CodeFeatureDisabled, "feature disabled: "+flag)
}

func Internal(msg string, err error) *Error {
	return Wrap(http.StatusInternalServerError, CodeInternal, msg, err)
}

// As converts any error into an *Error, defaulting to a 500 internal error.
func As(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return Internal(err.Error(), err)
}
