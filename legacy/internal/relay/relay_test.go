package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"czapi/internal/domain"
	"czapi/internal/pkg/clock"
	"czapi/internal/provider/openai"
	"czapi/internal/store"
)

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

// fakeChannelRepo is a minimal store.ChannelRepo for selector/forwarder tests.
type fakeChannelRepo struct {
	mu       sync.Mutex
	channels []domain.Channel
	health   map[uint64]*domain.ChannelHealth
}

func newFakeRepo(chs ...domain.Channel) *fakeChannelRepo {
	return &fakeChannelRepo{channels: chs, health: map[uint64]*domain.ChannelHealth{}}
}

func (r *fakeChannelRepo) Create(ctx context.Context, c *domain.Channel) error { return nil }
func (r *fakeChannelRepo) Update(ctx context.Context, c *domain.Channel) error { return nil }
func (r *fakeChannelRepo) Delete(ctx context.Context, id uint64) error         { return nil }
func (r *fakeChannelRepo) GetByID(ctx context.Context, id uint64) (*domain.Channel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.channels {
		if r.channels[i].ID == id {
			ch := r.channels[i]
			return &ch, nil
		}
	}
	return nil, errors.New("not found")
}
func (r *fakeChannelRepo) List(ctx context.Context) ([]domain.Channel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.Channel(nil), r.channels...), nil
}
func (r *fakeChannelRepo) ListByModel(ctx context.Context, model string) ([]domain.Channel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Channel
	for _, c := range r.channels {
		if c.Status != 1 {
			continue
		}
		for _, m := range c.Models {
			if m == model || m == "*" {
				out = append(out, c)
				break
			}
		}
	}
	return out, nil
}
func (r *fakeChannelRepo) GetHealth(ctx context.Context, id uint64) (*domain.ChannelHealth, error) {
	return r.health[id], nil
}
func (r *fakeChannelRepo) SaveHealth(ctx context.Context, h *domain.ChannelHealth) error {
	r.health[h.ChannelID] = h
	return nil
}

var _ store.ChannelRepo = (*fakeChannelRepo)(nil)

// ---------------------------------------------------------------------------
// adapter tests
// ---------------------------------------------------------------------------

func TestBuildURL(t *testing.T) {
	a := openai.New()
	cases := []struct{ base, path, want string }{
		{"https://api.deepseek.com", "/v1/chat/completions", "https://api.deepseek.com/v1/chat/completions"},
		{"https://api.deepseek.com/", "/v1/chat/completions", "https://api.deepseek.com/v1/chat/completions"},
		{"https://api.deepseek.com/v1", "/v1/chat/completions", "https://api.deepseek.com/v1/chat/completions"},
		{"https://api.x.com/v1/", "/v1/models", "https://api.x.com/v1/models"},
		{"https://api.x.com/openai", "v1/models", "https://api.x.com/openai/v1/models"},
	}
	for _, tc := range cases {
		got, err := a.BuildURL(tc.base, tc.path)
		if err != nil {
			t.Fatalf("BuildURL(%q,%q) error: %v", tc.base, tc.path, err)
		}
		if got != tc.want {
			t.Errorf("BuildURL(%q,%q) = %q, want %q", tc.base, tc.path, got, tc.want)
		}
	}
	if _, err := a.BuildURL("", "/x"); err == nil {
		t.Error("empty base url must fail")
	}
	if _, err := a.BuildURL("ftp://x", "/x"); err == nil {
		t.Error("non-http scheme must fail")
	}
}

func TestMapModel(t *testing.T) {
	a := openai.New()
	ch := &domain.Channel{ModelMap: map[string]string{"gpt-4o-mini": "deepseek-chat"}}
	if got := a.MapModel(ch, "gpt-4o-mini"); got != "deepseek-chat" {
		t.Errorf("mapped = %q, want deepseek-chat", got)
	}
	if got := a.MapModel(ch, "unknown"); got != "unknown" {
		t.Errorf("identity = %q, want unknown", got)
	}
	if got := a.MapModel(nil, "x"); got != "x" {
		t.Errorf("nil channel = %q, want x", got)
	}
	if got := a.MapModel(&domain.Channel{}, "x"); got != "x" {
		t.Errorf("empty map = %q, want x", got)
	}
}

func TestParseUsage(t *testing.T) {
	a := openai.New()

	body := []byte(`{"id":"1","usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":4}}}`)
	u := a.ParseUsage(body)
	if u == nil {
		t.Fatal("usage not parsed")
	}
	if u.PromptTokens != 10 || u.CompletionTokens != 5 || u.Cached() != 4 {
		t.Errorf("usage = %+v", u)
	}

	if got := a.ParseUsage([]byte(`{"id":"1"}`)); got != nil {
		t.Error("absent usage must be nil")
	}
	if got := a.ParseUsage([]byte(`not json`)); got != nil {
		t.Error("malformed must be nil")
	}
	if got := a.ParseUsage(nil); got != nil {
		t.Error("nil body must be nil")
	}
}

func TestClassifyError(t *testing.T) {
	a := openai.New()
	cases := []struct {
		status    int
		retryable bool
	}{
		{0, true}, {408, true}, {429, true}, {500, true}, {503, true},
		{400, false}, {401, false}, {403, false}, {404, false}, {422, false},
	}
	for _, tc := range cases {
		retry, code := a.ClassifyError(tc.status, []byte(`{"error":{"code":"x"}}`))
		if retry != tc.retryable {
			t.Errorf("status %d retryable = %v, want %v", tc.status, retry, tc.retryable)
		}
		if code == "" {
			t.Errorf("status %d produced empty code", tc.status)
		}
	}
	retry, code := a.ClassifyError(400, []byte(`{"error":{"code":"context_length_exceeded"}}`))
	if retry || code != "context_length_exceeded" {
		t.Errorf("upstream code not preferred: %v %q", retry, code)
	}
}

func TestBuildRequestDropsSensitiveHeaders(t *testing.T) {
	a := openai.New()
	client := http.Header{}
	client.Set("Authorization", "Bearer client-key")
	client.Set("Host", "evil")
	client.Set("Connection", "keep-alive")
	client.Set("Content-Length", "999")
	client.Set("OpenAI-Organization", "org-1")

	req, err := a.BuildRequest(context.Background(), "https://api.x.com", "/v1/chat/completions", "server-key", []byte(`{}`), client)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer server-key" {
		t.Errorf("Authorization = %q, must be the channel key only", got)
	}
	if got := req.Header.Get("OpenAI-Organization"); got != "org-1" {
		t.Errorf("allow-listed header dropped: %q", got)
	}
	for _, h := range []string{"Connection", "Content-Type "} {
		if h == "Content-Type " {
			continue
		}
		if req.Header.Get(h) != "" {
			t.Errorf("hop-by-hop header %q must be dropped", h)
		}
	}
	if req.Header.Get("Content-Type") != "application/json" {
		t.Error("content type not set")
	}
}

// ---------------------------------------------------------------------------
// breaker tests
// ---------------------------------------------------------------------------

func TestBreakerNeverOpensBelowMinSamples(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	b := NewBreaker(BreakerConfig{MinSamples: 20, ErrorRate: 0.5}, clk)
	for i := 0; i < 19; i++ {
		b.Record(1, errors.New("boom"))
	}
	if open, _, _ := b.State(1); open {
		t.Fatal("breaker opened below MinSamples")
	}
	b.Record(1, errors.New("boom"))
	if open, rate, samples := b.State(1); !open || rate != 1.0 || samples != 20 {
		t.Fatalf("breaker should be open: open=%v rate=%v samples=%d", open, rate, samples)
	}
}

func TestBreakerHalfOpenAllowsSingleProbe(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	b := NewBreaker(BreakerConfig{MinSamples: 2, ErrorRate: 0.5, OpenDuration: time.Minute}, clk)
	b.Record(1, errors.New("x"))
	b.Record(1, errors.New("x"))
	if open, _, _ := b.State(1); !open {
		t.Fatal("expected open")
	}
	if b.Allow(1) {
		t.Fatal("open breaker must reject")
	}

	clk.Advance(2 * time.Minute)

	var allowed int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.Allow(1) {
				atomic.AddInt64(&allowed, 1)
			}
		}()
	}
	wg.Wait()
	if allowed != 1 {
		t.Fatalf("half-open admitted %d probes, want exactly 1", allowed)
	}
}

func TestBreakerProbeSuccessCloses(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	b := NewBreaker(BreakerConfig{MinSamples: 2, ErrorRate: 0.5}, clk)
	b.Record(1, errors.New("x"))
	b.Record(1, errors.New("x"))
	clk.Advance(time.Hour)
	if !b.Allow(1) {
		t.Fatal("expected probe to be admitted")
	}
	b.Record(1, nil)
	if open, _, _ := b.State(1); open {
		t.Fatal("successful probe must close the breaker")
	}
}

func TestBreakerProbeFailureReopens(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	b := NewBreaker(BreakerConfig{MinSamples: 2, ErrorRate: 0.5, OpenDuration: time.Minute}, clk)
	b.Record(1, errors.New("x"))
	b.Record(1, errors.New("x"))
	clk.Advance(2 * time.Minute)
	if !b.Allow(1) {
		t.Fatal("expected probe")
	}
	b.Record(1, errors.New("still broken"))
	if open, _, _ := b.State(1); !open {
		t.Fatal("failed probe must re-open")
	}
	if b.Allow(1) {
		t.Fatal("re-opened breaker must reject")
	}
}

func TestBreakerWindowPrunes(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	b := NewBreaker(BreakerConfig{MinSamples: 2, ErrorRate: 0.5, Window: time.Minute}, clk)
	b.Record(1, errors.New("x"))
	b.Record(1, errors.New("x"))
	clk.Advance(5 * time.Minute)
	open, rate, samples := b.State(1)
	if open || samples != 0 || rate != 0 {
		t.Fatalf("stale samples not pruned: open=%v rate=%v samples=%d", open, rate, samples)
	}
}

// ---------------------------------------------------------------------------
// selector tests
// ---------------------------------------------------------------------------

func TestSelectorPriorityOrder(t *testing.T) {
	repo := newFakeRepo(
		domain.Channel{ID: 1, Priority: 0, Weight: 100, Models: []string{"m"}, Status: 1},
		domain.Channel{ID: 2, Priority: 10, Weight: 100, Models: []string{"m"}, Status: 1},
		domain.Channel{ID: 3, Priority: 5, Weight: 100, Models: []string{"m"}, Status: 1},
	)
	sel := NewSelector(repo, nil, rand.New(rand.NewSource(1)))
	got, err := sel.Select(context.Background(), "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []uint64{2, 3, 1}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("order = %v, want %v", idsOf(got), want)
		}
	}
}

func TestSelectorExcludesAndSkipsDisabled(t *testing.T) {
	repo := newFakeRepo(
		domain.Channel{ID: 1, Models: []string{"m"}, Status: 1},
		domain.Channel{ID: 2, Models: []string{"m"}, Status: 0},
		domain.Channel{ID: 3, Models: []string{"m"}, Status: 1},
	)
	sel := NewSelector(repo, nil, rand.New(rand.NewSource(1)))
	got, err := sel.Select(context.Background(), "m", map[uint64]bool{1: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != 3 {
		t.Fatalf("got %v, want [3]", idsOf(got))
	}
}

func TestSelectorWeightedDistribution(t *testing.T) {
	repo := newFakeRepo(
		domain.Channel{ID: 1, Weight: 70, Models: []string{"m"}, Status: 1},
		domain.Channel{ID: 2, Weight: 30, Models: []string{"m"}, Status: 1},
	)
	sel := NewSelector(repo, nil, rand.New(rand.NewSource(7)))
	counts := map[uint64]int{}
	const draws = 10000
	for i := 0; i < draws; i++ {
		got, err := sel.Select(context.Background(), "m", nil)
		if err != nil {
			t.Fatal(err)
		}
		counts[got[0].ID]++
	}
	share := float64(counts[1]) / draws
	if share < 0.66 || share > 0.74 {
		t.Fatalf("weight 70%% produced share %.3f", share)
	}
}

func TestSelectorSkipsOpenBreaker(t *testing.T) {
	repo := newFakeRepo(
		domain.Channel{ID: 1, Models: []string{"m"}, Status: 1},
		domain.Channel{ID: 2, Models: []string{"m"}, Status: 1},
	)
	clk := clock.NewFake(time.Now())
	b := NewBreaker(BreakerConfig{MinSamples: 2, ErrorRate: 0.5, OpenDuration: time.Hour}, clk)
	b.Record(1, errors.New("x"))
	b.Record(1, errors.New("x"))
	sel := NewSelector(repo, b, rand.New(rand.NewSource(1)))
	got, err := sel.Select(context.Background(), "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("got %v, want [2]", idsOf(got))
	}
}

func TestSelectorNoChannel(t *testing.T) {
	sel := NewSelector(newFakeRepo(), nil, rand.New(rand.NewSource(1)))
	if _, err := sel.Select(context.Background(), "m", nil); err == nil {
		t.Fatal("expected channel unavailable")
	}
}

func idsOf(chs []domain.Channel) []uint64 {
	out := make([]uint64, len(chs))
	for i, c := range chs {
		out[i] = c.ID
	}
	return out
}

// ---------------------------------------------------------------------------
// forwarder tests
// ---------------------------------------------------------------------------

// recordingMetrics counts observations.
type recordingMetrics struct {
	mu     sync.Mutex
	reject int
	tokens int
	errors int
	ttft   int
}

func (m *recordingMetrics) ObserveRequest(model, channel string, status int)          {}
func (m *recordingMetrics) ObserveLatency(model string, stream bool, d time.Duration) {}
func (m *recordingMetrics) ObserveTTFT(model string, d time.Duration) {
	m.mu.Lock()
	m.ttft++
	m.mu.Unlock()
}
func (m *recordingMetrics) ObserveTokens(model string, p, c, ca int) {
	m.mu.Lock()
	m.tokens++
	m.mu.Unlock()
}
func (m *recordingMetrics) ObserveChannelError(channel, code string) {
	m.mu.Lock()
	m.errors++
	m.mu.Unlock()
}

func newTestForwarder(t *testing.T, repo *fakeChannelRepo, mx Metrics) *forwarder {
	t.Helper()
	clk := clock.System{}
	b := NewBreaker(BreakerConfig{}, clk)
	sel := NewSelector(repo, b, rand.New(rand.NewSource(1)))
	f := NewForwarder(ForwarderConfig{
		ConnectTimeout:     2 * time.Second,
		FirstByteTimeout:   2 * time.Second,
		TotalTimeout:       5 * time.Second,
		StreamChunkTimeout: 2 * time.Second,
		MaxRetries:         2,
		RetryOn429:         true,
	}, sel, b, openai.New(), mx)
	f.SetChannelLoader(repo)
	return f
}

func TestForwardNonStreamSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":3}}`))
	}))
	defer srv.Close()

	repo := newFakeRepo(domain.Channel{ID: 1, BaseURL: srv.URL, Models: []string{"m"}, Status: 1,
		KeyPool: []domain.ChannelKey{{Secret: "k1", Status: "active"}}})
	mx := &recordingMetrics{}
	f := newTestForwarder(t, repo, mx)

	rec := httptest.NewRecorder()
	res, err := f.Forward(context.Background(), rec, Request{Path: "/v1/chat/completions", Body: []byte(`{}`), Model: "m", RequestID: "r1"})
	if err != nil {
		t.Fatalf("forward error: %v", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if res.Usage == nil || res.Usage.PromptTokens != 12 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("body not written to client")
	}
	if mx.tokens != 1 {
		t.Errorf("token metric not recorded")
	}
}

func TestForwardRetriesOn5xx(t *testing.T) {
	var hits int64
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer good.Close()

	repo := newFakeRepo(
		domain.Channel{ID: 1, BaseURL: bad.URL, Models: []string{"m"}, Status: 1, Priority: 10,
			KeyPool: []domain.ChannelKey{{Secret: "k", Status: "active"}}},
		domain.Channel{ID: 2, BaseURL: good.URL, Models: []string{"m"}, Status: 1, Priority: 1,
			KeyPool: []domain.ChannelKey{{Secret: "k", Status: "active"}}},
	)
	f := newTestForwarder(t, repo, nil)

	rec := httptest.NewRecorder()
	res, err := f.Forward(context.Background(), rec, Request{Path: "/v1/chat/completions", Body: []byte(`{}`), Model: "m"})
	if err != nil {
		t.Fatalf("expected success after retry: %v", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if atomic.LoadInt64(&hits) != 2 {
		t.Fatalf("hits = %d, want 2 (fail then success)", hits)
	}
	// The client must have seen exactly one body: the successful one.
	if got := rec.Body.String(); got != `{"ok":true,"usage":{"prompt_tokens":1,"completion_tokens":1}}` {
		t.Fatalf("client body = %q", got)
	}
}

func TestForwardNoRetryOn400(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"message":"bad param","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()

	repo := newFakeRepo(
		domain.Channel{ID: 1, BaseURL: srv.URL, Models: []string{"m"}, Status: 1, Priority: 10,
			KeyPool: []domain.ChannelKey{{Secret: "k", Status: "active"}}},
		domain.Channel{ID: 2, BaseURL: srv.URL, Models: []string{"m"}, Status: 1, Priority: 1,
			KeyPool: []domain.ChannelKey{{Secret: "k", Status: "active"}}},
	)
	f := newTestForwarder(t, repo, nil)

	rec := httptest.NewRecorder()
	res, err := f.Forward(context.Background(), rec, Request{Path: "/v1/chat/completions", Body: []byte(`{}`), Model: "m"})
	if err != nil {
		t.Fatalf("400 must be returned, not raised: %v", err)
	}
	if res.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	if atomic.LoadInt64(&hits) != 1 {
		t.Fatalf("hits = %d, want 1 (no retry on 400)", hits)
	}
}

func TestForwardRetries429Once(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
	}))
	defer srv.Close()

	repo := newFakeRepo(
		domain.Channel{ID: 1, BaseURL: srv.URL, Models: []string{"m"}, Status: 1, Priority: 10,
			KeyPool: []domain.ChannelKey{{Secret: "k", Status: "active"}}},
		domain.Channel{ID: 2, BaseURL: srv.URL, Models: []string{"m"}, Status: 1, Priority: 5,
			KeyPool: []domain.ChannelKey{{Secret: "k", Status: "active"}}},
		domain.Channel{ID: 3, BaseURL: srv.URL, Models: []string{"m"}, Status: 1, Priority: 1,
			KeyPool: []domain.ChannelKey{{Secret: "k", Status: "active"}}},
	)
	f := newTestForwarder(t, repo, nil)
	rec := httptest.NewRecorder()
	res, _ := f.Forward(context.Background(), rec, Request{Path: "/v1/chat/completions", Body: []byte(`{}`), Model: "m"})
	if res.StatusCode != 429 {
		t.Fatalf("status = %d, want 429", res.StatusCode)
	}
	if hits != 2 {
		t.Fatalf("hits = %d, want exactly 2 (one retry then give up)", hits)
	}
}

func TestForwardAllChannelsFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()
	repo := newFakeRepo(domain.Channel{ID: 1, BaseURL: srv.URL, Models: []string{"m"}, Status: 1,
		KeyPool: []domain.ChannelKey{{Secret: "k", Status: "active"}}})
	f := newTestForwarder(t, repo, nil)

	rec := httptest.NewRecorder()
	_, err := f.Forward(context.Background(), rec, Request{Path: "/v1/chat/completions", Body: []byte(`{}`), Model: "m"})
	if err == nil {
		t.Fatal("expected an upstream error")
	}
}

func TestForwardStreamSSE(t *testing.T) {
	chunks := []string{
		`data: {"choices":[{"delta":{"content":"he"}}]}`,
		`data: {"choices":[{"delta":{"content":"llo"}}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
		`data: [DONE]`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, c := range chunks {
			_, _ = w.Write([]byte(c + "\n\n"))
			fl.Flush()
		}
	}))
	defer srv.Close()

	repo := newFakeRepo(domain.Channel{ID: 1, BaseURL: srv.URL, Models: []string{"m"}, Status: 1,
		KeyPool: []domain.ChannelKey{{Secret: "k", Status: "active"}}})
	f := newTestForwarder(t, repo, nil)

	rec := httptest.NewRecorder()
	res, err := f.Forward(context.Background(), rec, Request{Path: "/v1/chat/completions", Body: []byte(`{}`), Model: "m", Stream: true})
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if res.Partial {
		t.Error("clean stream must not be partial")
	}
	if res.Usage == nil || res.Usage.PromptTokens != 5 || res.Usage.CompletionTokens != 2 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	body := rec.Body.String()
	if !contains(body, "[DONE]") {
		t.Error("[DONE] not forwarded")
	}
	if !contains(body, "he") || !contains(body, "llo") {
		t.Error("chunks not forwarded in order")
	}
}

func TestForwardStreamInterruptedIsPartial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"choices\":[]}\n\n"))
		fl.Flush()
		_, _ = w.Write([]byte("data: {\"choices\":[]}\n\n"))
		fl.Flush()
		// Abort abruptly without [DONE].
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
		}
	}))
	defer srv.Close()

	repo := newFakeRepo(domain.Channel{ID: 1, BaseURL: srv.URL, Models: []string{"m"}, Status: 1,
		KeyPool: []domain.ChannelKey{{Secret: "k", Status: "active"}}})
	f := newTestForwarder(t, repo, nil)

	rec := httptest.NewRecorder()
	res, err := f.Forward(context.Background(), rec, Request{Path: "/v1/chat/completions", Body: []byte(`{}`), Model: "m", Stream: true})
	if err != nil {
		t.Fatalf("interrupted stream must not raise: %v", err)
	}
	if !res.Partial {
		t.Fatal("interrupted stream must be marked partial")
	}
}

func TestForwardClientCancelMidStream(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"choices\":[]}\n\n"))
		fl.Flush()
		<-block
	}))
	defer srv.Close()
	defer close(block)

	repo := newFakeRepo(domain.Channel{ID: 1, BaseURL: srv.URL, Models: []string{"m"}, Status: 1,
		KeyPool: []domain.ChannelKey{{Secret: "k", Status: "active"}}})
	f := newTestForwarder(t, repo, nil)

	ctx, cancel := context.WithCancel(context.Background())
	rec := httptest.NewRecorder()
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	res, err := f.Forward(ctx, rec, Request{Path: "/v1/chat/completions", Body: []byte(`{}`), Model: "m", Stream: true})
	if err != nil {
		t.Fatalf("cancel must not raise: %v", err)
	}
	if !res.Partial {
		t.Fatal("cancelled stream must be partial")
	}
}

func TestForwardNoGoroutineLeak(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			_, _ = w.Write([]byte("data: {\"choices\":[]}\n\n"))
			fl.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		fl.Flush()
	}))
	defer srv.Close()

	repo := newFakeRepo(domain.Channel{ID: 1, BaseURL: srv.URL, Models: []string{"m"}, Status: 1,
		KeyPool: []domain.ChannelKey{{Secret: "k", Status: "active"}}})
	f := newTestForwarder(t, repo, nil)

	runtime.GC()
	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		rec := httptest.NewRecorder()
		if _, err := f.Forward(context.Background(), rec, Request{Path: "/v1/chat/completions", Body: []byte(`{}`), Model: "m", Stream: true}); err != nil {
			t.Fatalf("forward %d: %v", i, err)
		}
	}
	time.Sleep(300 * time.Millisecond)
	runtime.GC()
	after := runtime.NumGoroutine()
	if after > before+5 {
		t.Fatalf("goroutine leak: before=%d after=%d", before, after)
	}
}

func TestTestChannel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	repo := newFakeRepo(domain.Channel{ID: 1, BaseURL: srv.URL, Models: []string{"m"}, Status: 1,
		KeyPool: []domain.ChannelKey{{Secret: "k", Status: "active"}}})
	f := newTestForwarder(t, repo, nil)

	ok, msg, latency, err := f.TestChannel(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("probe failed: %s", msg)
	}
	if latency < 0 {
		t.Error("negative latency")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// unused import guard
var _ = json.Marshal
var _ = fmt.Sprintf
