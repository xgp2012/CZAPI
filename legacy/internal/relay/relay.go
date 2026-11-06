// Package relay defines the upstream forwarding contract (plan §6.5).
//
// Responsibilities: channel selection, retries, breaker, SSE passthrough.
// The relay never touches credits; it reports what happened to the caller.
package relay

import (
	"context"
	"io"
	"net/http"

	"czapi/internal/domain"
)

// Request is one chat/embeddings/completions call to forward.
type Request struct {
	// Path is the upstream path (usually /v1/chat/completions).
	Path string
	// Body is the JSON payload exactly as received from the client, already
	// validated. It is replayed byte-for-byte on retries.
	Body []byte
	// Model is the *external* model name requested by the client.
	Model string
	// Stream requests SSE passthrough.
	Stream bool
	// Headers are client headers worth forwarding (never Authorization).
	Headers http.Header
	// RequestID is the gateway correlation id.
	RequestID string
}

// Result reports the outcome of a forwarded request.
type Result struct {
	// StatusCode is what was sent (or should be sent) to the client.
	StatusCode int
	// Usage is parsed from the upstream payload; nil when unavailable.
	Usage *Usage
	// ChannelID is the channel that ultimately served the request.
	ChannelID uint64
	// Model is the upstream model name actually used.
	Model string
	// LatencyMS / TTFTMS are measured.
	LatencyMS int
	TTFTMS    int
	// ErrorCode is the classified failure (empty on success).
	ErrorCode string
	// Partial means streaming started but did not finish cleanly.
	Partial bool
}

// Usage is the OpenAI usage block.
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

// Forwarder is the relay entry point.
type Forwarder interface {
	// Forward executes the request against a selected channel and writes the
	// response to w. For non-stream requests it writes the JSON body; for
	// stream requests it flushes each chunk to the client as it arrives.
	//
	// Contract: Forward must never write to w before it knows the upstream
	// response is successful (status < 400), so a retry can still happen.
	Forward(ctx context.Context, w http.ResponseWriter, req Request) (Result, error)

	// TestChannel probes a channel's connectivity for the admin console.
	TestChannel(ctx context.Context, channelID uint64) (ok bool, message string, latencyMS int, err error)
}

// Selector chooses a channel for a model (implemented in relay).
type Selector interface {
	// Select returns candidate channels in preference order, excluding the
	// provided ids (used for retry).
	Select(ctx context.Context, model string, exclude map[uint64]bool) ([]domain.Channel, error)
	// Report feeds the breaker with a sample.
	Report(ctx context.Context, channelID uint64, err error)
}

// BodyReader is a helper the implementation may use to bound reads.
var _ io.Reader
