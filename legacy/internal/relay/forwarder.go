package relay

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"czapi/internal/domain"
	"czapi/internal/pkg/errs"
	"czapi/internal/provider/openai"
)

// Metrics is the instrumentation surface the forwarder reports to. It is
// declared here (rather than importing a concrete metrics package) to keep the
// relay free of upward dependencies. All methods must tolerate concurrent calls;
// a nil Metrics disables reporting.
type Metrics interface {
	ObserveRequest(model, channel string, status int)
	ObserveLatency(model string, stream bool, d time.Duration)
	ObserveTTFT(model string, d time.Duration)
	ObserveTokens(model string, prompt, completion, cached int)
	ObserveChannelError(channel, code string)
}

// ForwarderConfig tunes the forwarder. Zero values are replaced by the plan's
// defaults so a zero config is still usable.
type ForwarderConfig struct {
	HTTPClient          *http.Client
	ConnectTimeout      time.Duration
	FirstByteTimeout    time.Duration
	TotalTimeout        time.Duration
	StreamChunkTimeout  time.Duration
	MaxRetries          int
	MaxIdleConnsPerHost int
	RetryOn429          bool
	// MaxBodyBytes bounds non-stream response reads (default 32MB).
	MaxBodyBytes int64
}

func (c ForwarderConfig) withDefaults() ForwarderConfig {
	if c.ConnectTimeout <= 0 {
		c.ConnectTimeout = 10 * time.Second
	}
	if c.FirstByteTimeout <= 0 {
		c.FirstByteTimeout = 60 * time.Second
	}
	if c.TotalTimeout <= 0 {
		c.TotalTimeout = 300 * time.Second
	}
	if c.StreamChunkTimeout <= 0 {
		c.StreamChunkTimeout = 120 * time.Second
	}
	if c.MaxRetries < 0 {
		c.MaxRetries = 0
	}
	if c.MaxIdleConnsPerHost <= 0 {
		c.MaxIdleConnsPerHost = 200
	}
	if c.MaxBodyBytes <= 0 {
		c.MaxBodyBytes = 32 << 20
	}
	return c
}

// forwarder implements Forwarder.
type forwarder struct {
	cfg     ForwarderConfig
	client  *http.Client
	sel     Selector
	breaker *Breaker
	adapter *openai.Adapter
	mx      Metrics
	loader  channelLoader
}

// NewForwarder builds a forwarder. sel, breaker and adapter must be non-nil.
func NewForwarder(cfg ForwarderConfig, sel Selector, breaker *Breaker, adapter *openai.Adapter, mx Metrics) *forwarder {
	cfg = cfg.withDefaults()
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					Timeout:   cfg.ConnectTimeout,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				MaxIdleConns:          0, // unlimited
				MaxIdleConnsPerHost:   cfg.MaxIdleConnsPerHost,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   cfg.ConnectTimeout,
				ExpectContinueTimeout: time.Second,
				// No ResponseHeaderTimeout: it would abort streaming responses
				// that legitimately take a while to emit the first chunk
				// (plan §6.7).
				ForceAttemptHTTP2: true,
			},
			// Timeouts are enforced per attempt via context, not here, so that
			// streaming responses are not cut off mid-flight.
			Timeout: 0,
		}
	}
	return &forwarder{cfg: cfg, client: client, sel: sel, breaker: breaker, adapter: adapter, mx: mx}
}

// attemptResult carries the outcome of one upstream attempt.
type attemptResult struct {
	resp       *http.Response
	status     int
	body       []byte
	usage      *Usage
	partial    bool
	ttft       time.Duration
	errCode    string
	written    bool
	retryable  bool
	channelID  uint64
	upstreamID string
}

// Forward executes the request with retries, breaker feedback and SSE
// passthrough.
func (f *forwarder) Forward(ctx context.Context, w http.ResponseWriter, req Request) (Result, error) {
	start := time.Now()

	exclude := make(map[uint64]bool)
	var lastErr error
	var lastCode string
	attempts := 0
	maxAttempts := f.cfg.MaxRetries + 1
	retriedOnceFor429 := false

	for attempts < maxAttempts {
		candidates, err := f.sel.Select(ctx, req.Model, exclude)
		if err != nil {
			if lastErr != nil {
				return f.fail(start, req, lastCode, lastErr)
			}
			return f.fail(start, req, string(errs.CodeChannelUnavailable), err)
		}
		if len(candidates) == 0 {
			break
		}
		ch := candidates[0]
		exclude[ch.ID] = true
		attempts++

		res, ferr := f.attempt(ctx, w, req, ch, start)
		if ferr != nil {
			// Transport-level failure: record and try the next channel.
			lastErr = ferr
			lastCode = res.errCode
			f.sel.Report(ctx, ch.ID, ferr)
			f.observeError(ch, res.errCode)
			if res.written {
				// Already streamed to the client; cannot retry.
				return f.partialResult(start, req, res, nil)
			}
			continue
		}

		// The upstream answered. Feed the breaker.
		if res.status >= 400 {
			f.sel.Report(ctx, ch.ID, errors.New("upstream status "+http.StatusText(res.status)))
		} else {
			f.sel.Report(ctx, ch.ID, nil)
		}

		if res.status < 400 {
			return f.complete(start, req, res, nil)
		}

		// 4xx other than 429 is final: the client's request is wrong.
		lastErr = nil
		lastCode = res.errCode
		f.observeError(ch, res.errCode)

		if res.retryable {
			if res.status == http.StatusTooManyRequests {
				if !f.cfg.RetryOn429 || retriedOnceFor429 {
					return f.complete(start, req, res, nil)
				}
				retriedOnceFor429 = true
				continue
			}
			continue
		}
		return f.complete(start, req, res, nil)
	}

	if lastErr != nil {
		return f.fail(start, req, lastCode, lastErr)
	}
	return f.fail(start, req, string(errs.CodeChannelUnavailable), nil)
}

// attempt performs a single upstream call. When it returns a client-visible
// error (transport failure) it also reports whether anything was written.
func (f *forwarder) attempt(ctx context.Context, w http.ResponseWriter, req Request, ch domain.Channel, start time.Time) (attemptResult, error) {
	res := attemptResult{channelID: ch.ID}

	apiKey := ""
	if len(ch.KeyPool) > 0 {
		for _, k := range ch.KeyPool {
			if k.Status == "" || k.Status == "active" {
				apiKey = k.Secret
				break
			}
		}
	}

	path := req.Path
	if path == "" {
		path = "/v1/chat/completions"
	}

	// Per-attempt context: the first byte must arrive within FirstByteTimeout;
	// afterwards a stream may continue for up to TotalTimeout.
	firstByteCtx, cancelFirst := context.WithTimeout(ctx, f.cfg.FirstByteTimeout)
	defer cancelFirst()

	upReq, err := f.adapter.BuildRequest(firstByteCtx, ch.BaseURL, path, apiKey, req.Body, req.Headers)
	if err != nil {
		res.errCode = "build_request_failed"
		return res, err
	}
	if req.RequestID != "" {
		upReq.Header.Set("X-Request-Id", req.RequestID)
	}

	resp, err := f.client.Do(upReq)
	if err != nil {
		cancelFirst()
		res.errCode = classifyTransport(err)
		return res, err
	}
	defer resp.Body.Close()

	res.status = resp.StatusCode
	res.ttft = time.Since(start)

	if resp.StatusCode >= 400 {
		body, _, rerr := f.adapter.ReadBodyLimited(resp.Body, f.cfg.MaxBodyBytes)
		if rerr != nil {
			res.errCode = "read_error_body_failed"
			return res, rerr
		}
		res.body = body
		res.retryable, res.errCode = f.adapter.ClassifyError(resp.StatusCode, body)
		if f.mx != nil {
			f.mx.ObserveTTFT(req.Model, res.ttft)
		}
		return res, nil
	}

	if f.adapter.IsStreaming(resp) {
		return f.stream(ctx, w, req, resp, res, start, cancelFirst)
	}

	body, _, rerr := f.adapter.ReadBodyLimited(resp.Body, f.cfg.MaxBodyBytes)
	if rerr != nil {
		res.errCode = "read_body_failed"
		return res, rerr
	}
	cancelFirst()
	res.usage = convertUsage(f.adapter.ParseUsage(body))

	// Only now, with a successful upstream response in hand, touch the client.
	writeJSON(w, resp, body)
	res.written = true
	return res, nil
}

// stream copies an SSE response to the client chunk by chunk.
func (f *forwarder) stream(ctx context.Context, w http.ResponseWriter, req Request, resp *http.Response, res attemptResult, start time.Time, cancelFirst context.CancelFunc) (attemptResult, error) {
	// Response headers and status must be written before the first flush.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if fl, ok := w.(http.Flusher); ok {
		fl.Flush()
	}
	// From here on the client has our headers: retries are impossible.
	res.written = true
	if f.mx != nil {
		f.mx.ObserveTTFT(req.Model, res.ttft)
	}

	reader := newSSEReader(resp.Body)
	writer := newFlushWriter(w)

	for {
		select {
		case <-ctx.Done():
			// Client went away: stop reading and report a partial stream so
			// settlement still runs.
			res.partial = true
			cancelFirst()
			return res, nil
		default:
		}

		// Enforce an idle timeout between chunks.
		type readOut struct {
			line string
			err  error
		}
		ch := make(chan readOut, 1)
		go func() {
			line, err := reader.next()
			ch <- readOut{line: line, err: err}
		}()

		var out readOut
		select {
		case <-ctx.Done():
			res.partial = true
			cancelFirst()
			return res, nil
		case <-time.After(f.cfg.StreamChunkTimeout):
			res.partial = true
			res.errCode = "stream_idle_timeout"
			cancelFirst()
			return res, nil
		case out = <-ch:
		}

		if out.err != nil {
			if errors.Is(out.err, io.EOF) {
				break
			}
			// Upstream died mid-stream: partial, not an error.
			res.partial = reader.SawData() && !reader.Finished()
			res.errCode = "stream_interrupted"
			cancelFirst()
			return res, nil
		}

		reader.inspect(out.line)
		if err := writer.writeLine(out.line); err != nil {
			res.partial = true
			cancelFirst()
			return res, nil
		}
		if reader.Finished() {
			break
		}
	}

	cancelFirst()
	res.usage = reader.Usage()
	res.partial = !reader.Finished() && res.partial
	return res, nil
}

// complete builds the successful Result and reports metrics.
func (f *forwarder) complete(start time.Time, req Request, res attemptResult, err error) (Result, error) {
	out := Result{
		StatusCode: res.status,
		Usage:      res.usage,
		ChannelID:  res.channelID,
		Model:      req.Model,
		LatencyMS:  int(time.Since(start).Milliseconds()),
		TTFTMS:     int(res.ttft.Milliseconds()),
		ErrorCode:  res.errCode,
		Partial:    res.partial,
	}
	f.report(req, out)
	return out, err
}

// partialResult builds a Result for a stream that broke after headers were sent.
func (f *forwarder) partialResult(start time.Time, req Request, res attemptResult, err error) (Result, error) {
	out := Result{
		StatusCode: http.StatusOK,
		Usage:      res.usage,
		ChannelID:  res.channelID,
		Model:      req.Model,
		LatencyMS:  int(time.Since(start).Milliseconds()),
		TTFTMS:     int(res.ttft.Milliseconds()),
		ErrorCode:  "stream_interrupted",
		Partial:    true,
	}
	f.report(req, out)
	return out, err
}

// fail turns accumulated retry failures into a client error.
func (f *forwarder) fail(start time.Time, req Request, code string, cause error) (Result, error) {
	out := Result{
		StatusCode: http.StatusBadGateway,
		Model:      req.Model,
		LatencyMS:  int(time.Since(start).Milliseconds()),
		ErrorCode:  string(code),
	}
	if code == "" {
		code = string(errs.CodeUpstreamError)
		out.ErrorCode = code
	}
	f.report(req, out)

	if errors.Is(cause, context.DeadlineExceeded) || isTimeout(cause) {
		return out, errs.UpstreamTimeout("upstream timeout", cause)
	}
	return out, errs.Upstream("upstream service error, please retry", cause)
}

// report publishes metrics for a finished request.
func (f *forwarder) report(req Request, res Result) {
	if f.mx == nil {
		return
	}
	f.mx.ObserveRequest(req.Model, channelLabel(res.ChannelID), res.StatusCode)
	f.mx.ObserveLatency(req.Model, req.Stream, time.Duration(res.LatencyMS)*time.Millisecond)
	if res.Usage != nil {
		f.mx.ObserveTokens(req.Model, res.Usage.PromptTokens, res.Usage.CompletionTokens, res.Usage.Cached())
	}
}

func (f *forwarder) observeError(ch domain.Channel, code string) {
	if f.mx == nil {
		return
	}
	f.mx.ObserveChannelError(channelLabel(ch.ID), code)
}

func channelLabel(id uint64) string {
	if id == 0 {
		return "none"
	}
	// Keep cardinality bounded: a channel id is already low-cardinality.
	buf := make([]byte, 0, 20)
	if id == 0 {
		return "0"
	}
	for id > 0 {
		buf = append([]byte{byte('0' + id%10)}, buf...)
		id /= 10
	}
	return string(buf)
}

// writeJSON writes a buffered upstream body to the client.
func writeJSON(w http.ResponseWriter, resp *http.Response, body []byte) {
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("Content-Length", itoa(len(body)))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// classifyTransport maps a transport error to a stable code.
func classifyTransport(err error) string {
	if err == nil {
		return ""
	}
	if isTimeout(err) {
		return "upstream_timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "client_cancelled"
	}
	return "upstream_unreachable"
}

func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// TestChannel probes a channel's /v1/models endpoint.
func (f *forwarder) TestChannel(ctx context.Context, channelID uint64) (bool, string, int, error) {
	ch, err := f.loadChannel(ctx, channelID)
	if err != nil {
		return false, "", 0, err
	}
	apiKey := ""
	for _, k := range ch.KeyPool {
		if k.Status == "" || k.Status == "active" {
			apiKey = k.Secret
			break
		}
	}
	urlStr, err := f.adapter.BuildURL(ch.BaseURL, "/v1/models")
	if err != nil {
		return false, err.Error(), 0, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return false, err.Error(), 0, nil
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	start := time.Now()
	resp, err := f.client.Do(req)
	latency := int(time.Since(start).Milliseconds())
	if err != nil {
		return false, classifyTransport(err), latency, nil
	}
	defer resp.Body.Close()
	_, _, _ = f.adapter.ReadBodyLimited(resp.Body, 1<<20)
	if resp.StatusCode >= 400 {
		return false, "upstream returned " + http.StatusText(resp.StatusCode), latency, nil
	}
	return true, "ok", latency, nil
}

// channelLoader is satisfied by store.ChannelRepo; kept narrow on purpose.
type channelLoader interface {
	GetByID(ctx context.Context, id uint64) (*domain.Channel, error)
}

// loadChannel resolves a channel for probing. The forwarder needs a repo it did
// not receive at construction, so selection is used as a fallback source and a
// dedicated setter installs the repo in production wiring.
func (f *forwarder) loadChannel(ctx context.Context, channelID uint64) (*domain.Channel, error) {
	if f.loader == nil {
		return nil, errs.NotFound("channel repository not configured")
	}
	ch, err := f.loader.GetByID(ctx, channelID)
	if err != nil {
		return nil, err
	}
	return ch, nil
}

// SetChannelLoader installs the repository used by TestChannel. It must be
// called during wiring, before serving traffic.
func (f *forwarder) SetChannelLoader(l channelLoader) { f.loader = l }

// convertUsage adapts the provider usage type to the relay DTO, keeping the
// provider package free of an import cycle with this one.
func convertUsage(u *openai.Usage) *Usage {
	if u == nil {
		return nil
	}
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
