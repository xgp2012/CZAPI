// Package openai implements the OpenAI-compatible upstream adapter (plan §6.5).
//
// Everything protocol-specific lives here: URL joining, header hygiene, model
// mapping, usage extraction and error classification. Adding a second protocol
// later means adding a sibling package, not touching the relay.
package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"czapi/internal/domain"
)

// Adapter is the OpenAI-compatible protocol implementation. It is stateless and
// safe for concurrent use.
type Adapter struct{}

// New returns the adapter.
func New() *Adapter { return &Adapter{} }

// hopByHopHeaders must never be forwarded to an upstream.
var hopByHopHeaders = map[string]bool{
	"connection":          true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
	"proxy-connection":    true,
	"content-length":      true,
	"host":                true,
	"authorization":       true,
	"cookie":              true,
}

// forwardedHeaders are the client headers worth passing through.
var forwardedHeaders = []string{
	"OpenAI-Organization",
	"OpenAI-Project",
	"OpenAI-Beta",
	"Accept-Language",
	"User-Agent",
}

// BuildURL joins baseURL and path without producing double slashes.
func (a *Adapter) BuildURL(baseURL, path string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return "", fmt.Errorf("openai: empty base url")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("openai: invalid base url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("openai: unsupported scheme %q", u.Scheme)
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	// A base that already ends with /v1 and a path that starts with /v1 would
	// duplicate the segment, which some upstreams reject.
	if strings.HasSuffix(u.Path, "/v1") && strings.HasPrefix(path, "/v1/") {
		path = strings.TrimPrefix(path, "/v1")
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = ""
	return u.String(), nil
}

// BuildRequest constructs the upstream request.
func (a *Adapter) BuildRequest(ctx context.Context, baseURL, path, apiKey string, body []byte, clientHeaders http.Header) (*http.Request, error) {
	full, err := a.BuildURL(baseURL, path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, full, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("openai: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	copySafeHeaders(req.Header, clientHeaders)
	return req, nil
}

// copySafeHeaders forwards the allow-listed client headers, dropping hop-by-hop
// and credential-bearing ones.
func copySafeHeaders(dst, src http.Header) {
	if src == nil {
		return
	}
	for _, k := range forwardedHeaders {
		if v := src.Get(k); v != "" {
			dst.Set(k, v)
		}
	}
}

// MapModel resolves the upstream model name for a channel.
func (a *Adapter) MapModel(channel *domain.Channel, external string) string {
	if channel == nil || len(channel.ModelMap) == 0 {
		return external
	}
	if mapped, ok := channel.ModelMap[external]; ok && mapped != "" {
		return mapped
	}
	return external
}

// usagePayload mirrors the OpenAI usage block including the cached-token detail.
type usagePayload struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// ParseUsage extracts the usage block from a response body. It tolerates both
// `{"usage":{...}}` and a bare usage object, returning nil when absent.
func (a *Adapter) ParseUsage(body []byte) *Usage {
	if len(body) == 0 {
		return nil
	}
	var wrapper struct {
		Usage *usagePayload `json:"usage"`
	}
	if err := json.Unmarshal(body, &wrapper); err == nil && wrapper.Usage != nil {
		return toUsage(wrapper.Usage)
	}
	var bare usagePayload
	if err := json.Unmarshal(body, &bare); err == nil && (bare.PromptTokens > 0 || bare.CompletionTokens > 0) {
		return toUsage(&bare)
	}
	return nil
}

func toUsage(u *usagePayload) *Usage {
	out := &Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
	if u.PromptTokensDetails != nil {
		out.PromptTokensDetails = &struct {
			CachedTokens int `json:"cached_tokens"`
		}{CachedTokens: u.PromptTokensDetails.CachedTokens}
	}
	return out
}

// IsStreaming reports whether the response is an SSE stream.
func (a *Adapter) IsStreaming(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	ct := resp.Header.Get("Content-Type")
	return strings.Contains(strings.ToLower(ct), "text/event-stream")
}

// ClassifyError maps an upstream failure to a retry decision and a stable code.
//
// Retryable: connection problems, timeouts, 5xx. A 429 may be retried once on a
// different channel. Every other 4xx is the client's fault and must not be
// retried — retrying would multiply a bad request across providers.
func (a *Adapter) ClassifyError(status int, body []byte) (retryable bool, code string) {
	switch {
	case status == 0:
		return true, "upstream_unreachable"
	case status == 408:
		return true, "upstream_timeout"
	case status == 429:
		return true, "rate_limit_exceeded"
	case status >= 500:
		return true, "upstream_error"
	case status >= 400:
		return false, clientErrorCode(status, body)
	default:
		return false, ""
	}
}

// clientErrorCode refines a 4xx into a stable code, preferring the upstream's
// own error code when it looks like an identifier.
func clientErrorCode(status int, body []byte) string {
	var payload struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		if payload.Error.Code != "" {
			return payload.Error.Code
		}
		if payload.Error.Type != "" {
			return payload.Error.Type
		}
	}
	switch status {
	case http.StatusUnauthorized:
		return "upstream_unauthorized"
	case http.StatusForbidden:
		return "upstream_forbidden"
	case http.StatusNotFound:
		return "upstream_not_found"
	}
	return "upstream_bad_request"
}

// ReadBodyLimited reads at most limit bytes, reporting whether it truncated.
func (a *Adapter) ReadBodyLimited(r io.Reader, limit int64) ([]byte, bool, error) {
	if limit <= 0 {
		limit = 32 << 20
	}
	buf, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(buf)) > limit {
		return buf[:limit], true, nil
	}
	return buf, false, nil
}

// Usage is the OpenAI usage block returned by an upstream response.
type Usage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// Cached returns the cached prompt token count (0 when absent).
func (u *Usage) Cached() int {
	if u == nil || u.PromptTokensDetails == nil {
		return 0
	}
	return u.PromptTokensDetails.CachedTokens
}
