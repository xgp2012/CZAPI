// Package e2e drives the whole gateway over real HTTP.
//
// These tests exist because the availability promise is end-to-end: memory
// drivers, real router, real auth, real settlement. A unit test per package
// cannot prove that the wiring composes, and the money path is exactly where a
// wiring mistake is most expensive.
package e2e

import (
	"bytes"
	"context"
	"czapi/internal/store"
	"czapi/internal/worker"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"czapi/internal/api"
	"czapi/internal/api/admin"
	"czapi/internal/api/internalapi"
	openaiapi "czapi/internal/api/openai"
	"czapi/internal/auth"
	"czapi/internal/billing"
	"czapi/internal/bootstrap"
	"czapi/internal/cache/memcache"
	"czapi/internal/config"
	"czapi/internal/domain"
	"czapi/internal/metrics"
	"czapi/internal/pkg/crypto"
	"czapi/internal/pkg/log"
	"czapi/internal/pkg/sign"
	openaiadapter "czapi/internal/provider/openai"
	"czapi/internal/queue/memqueue"
	"czapi/internal/quota"
	"czapi/internal/ratelimit"
	"czapi/internal/relay"
	"czapi/internal/store/memstore"
)

// nonceCounter keeps signed-request nonces unique within a test binary run.
var nonceCounter int64

const (
	testSalt   = "test-salt"
	testSecret = "test-app-secret"
	testAppID  = "v10-app-1"
)

// gateway is a fully wired server plus the collaborator handles a test needs.
type gateway struct {
	ts     *httptest.Server
	store  *memstore.Store
	clk    *fixedClock
	engine billing.Engine
	q      *memqueue.Queue
	upstr  *httptest.Server
	// upstreamHits counts how many times the fake upstream was called.
	upstreamHits int
}

// fixedClock is a deterministic clock.
type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time          { return c.now }
func (c *fixedClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// newGateway builds the whole stack over a fresh memory store, pre-seeded with a
// site, an upstream channel and one model.
func newGateway(t *testing.T) *gateway {
	t.Helper()
	clk := &fixedClock{now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	st := memstore.NewWithClock(clk)

	// A site must exist for signed internal calls to authenticate.
	require.NoError(t, st.Sites().Create(t.Context(), &domain.Site{
		AppID: testAppID, Name: "test", Secret: testSecret, Enabled: true,
		CreatedAt: clk.Now(),
	}))

	gw := newGatewayWithStore(t, st, clk)

	// Register the upstream as a channel.
	require.NoError(t, st.Channels().Create(t.Context(), &domain.Channel{
		Name: "upstream", Provider: "openai", BaseURL: gw.upstr.URL,
		Models: []string{"test-model"}, Weight: 100, Status: 1,
		MaxConcurrency: 100, TimeoutMS: 30000,
		KeyPool:   []domain.ChannelKey{{Secret: "sk-upstream", Weight: 1, Status: "active"}},
		CreatedAt: clk.Now(),
	}))
	_, merr := st.Models().Upsert(t.Context(), &domain.Model{
		ModelName: "test-model", DisplayName: "Test Model", GroupName: "default",
		ContextLen: 8192, ModelRatio: 1, CompletionRatio: 1, CachedRatio: 0.5, Status: 1,
	})
	require.NoError(t, merr)
	return gw
}

// newGatewayWithStore wires the full HTTP stack over a caller-provided store.
//
// Separating this from newGateway lets a test pre-seed the store (bootstrap,
// migrations) before the router is built, which is what the first-run tests need.
func newGatewayWithStore(t *testing.T, st *memstore.Store, clk *fixedClock) *gateway {
	t.Helper()
	ce := memcache.NewWithClock(clk, time.Minute)
	q := memqueue.NewWithClock(clk)
	mx := metrics.New()
	lg := log.New("error", "text")
	eng := billing.NewEngine(billing.EngineConfig{TokenToMicros: 1})

	gw := &gateway{store: st, clk: clk, engine: eng, q: q}
	gw.upstr = gw.startUpstream(t)

	breaker := relay.NewBreaker(relay.BreakerConfig{
		Window: time.Minute, MinSamples: 5, ErrorRate: 0.5, OpenDuration: 30 * time.Second,
	}, clk)
	selector := relay.NewSelector(st.Channels(), breaker, nil)
	fwd := relay.NewForwarder(relay.ForwarderConfig{
		ConnectTimeout: 2 * time.Second, FirstByteTimeout: 5 * time.Second,
		TotalTimeout: 10 * time.Second, MaxRetries: 1,
	}, selector, breaker, openaiadapter.New(), mx)

	ledger := quota.New(st, ce, q, eng, clk, mx, quota.Config{
		EnforceBilling: true, TokenToMicros: 1,
		KeyDailyMicros:       50_000_000,
		AccountMonthlyMicros: 500_000_000,
	})

	verifierDeps := auth.DepsFromStore(st)
	verifierDeps.Cache = ce
	verifier := auth.NewVerifier(verifierDeps, auth.VerifierConfig{
		Salt: testSalt, Clock: clk, CacheTTL: time.Minute,
	})
	jwtIssuer := auth.NewJWTIssuer(auth.JWTConfig{
		Secret: "test-jwt-secret", Issuer: "czapi-test", TTL: time.Hour, Clock: clk,
	})
	sigVerifier := auth.NewSigVerifier(siteResolver{st: st}, ce, auth.SigConfig{Clock: clk})

	limiter := ratelimit.New(ce, clk, ratelimit.Config{RPM: 10000, TPM: 1000000, Concurrency: 100})
	flags := admin.NewFlagStore(st.Flags(), map[string]bool{
		admin.FlagGatewayEnabled: true, admin.FlagBillingEnforced: true,
	})

	oh := openaiapi.New(openaiapi.Deps{
		Store: st, Verifier: verifier, Ledger: ledger, Engine: eng, Quota: ledger,
		Relay: fwd, Limiter: limiter, Metrics: mx, Log: lg, Clock: clk,
		Cache: ce, KeySalt: testSalt,
		Console: openaiapi.ConsoleConfig{Enabled: true, DistDir: ""},
	})
	ih := internalapi.New(internalapi.Deps{
		Store: st, Ledger: ledger, Credit: ledger, Metrics: mx, Log: lg,
		Clock: clk, Cache: ce, KeySalt: testSalt, Console: oh, Version: "test",
	})
	ah := admin.New(admin.Deps{
		Store: st, JWT: jwtIssuer, Credit: ledger, Relay: fwd, Breaker: breaker,
		Metrics: mx, Log: lg, Clock: clk, Cache: ce, KeySalt: testSalt, Flags: flags,
		Invalidator: verifier,
	})

	handler := api.New(&api.Deps{
		Log: lg, Metrics: mx, OpenAI: oh, Internal: ih, Admin: ah,
		KeyMW: verifier, AdminJWT: jwtIssuer, Sig: sigVerifier,
		Health: healthChecker{st: st}, Version: "test",
	})

	// Drain the usage queue synchronously in tests: the async consumer is a
	// production concern, and asserting on stored rows needs determinism.
	consumer := worker.NewUsageConsumer(st, q, mx, lg, clk, worker.Config{
		IdleSleep: 5 * time.Millisecond,
	})
	consumerCtx, consumerCancel := context.WithCancel(context.Background())
	go func() { _ = consumer.Run(consumerCtx) }()
	t.Cleanup(func() {
		consumerCancel()
		consumer.Close()
	})

	gw.ts = httptest.NewServer(handler)
	t.Cleanup(func() {
		gw.ts.Close()
		gw.upstr.Close()
	})
	return gw
}

// siteResolver adapts the site repository to auth.Resolver.
type siteResolver struct{ st *memstore.Store }

func (r siteResolver) Lookup(ctx context.Context, appID string) (string, string, bool, error) {
	site, err := r.st.Sites().GetByAppID(ctx, appID)
	if err != nil || site == nil {
		return "", "", false, nil
	}
	return site.Secret, site.SecretPrev, site.Enabled, nil
}

// healthChecker is the minimal health surface the router needs.
type healthChecker struct{ st *memstore.Store }

func (h healthChecker) Check(r *http.Request) (map[string]string, bool) {
	if err := h.st.Ping(r.Context()); err != nil {
		return map[string]string{"storage": "down"}, false
	}
	return map[string]string{"storage": "ok"}, true
}

// startUpstream runs a fake OpenAI endpoint.
func (g *gateway) startUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.upstreamHits++
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(body, &req)

		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fl, _ := w.(http.Flusher)
			for _, line := range []string{
				`data: {"id":"c1","choices":[{"delta":{"content":"hello "}}]}`,
				`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
				`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`,
				`data: [DONE]`,
			} {
				_, _ = io.WriteString(w, line+"\n\n")
				if fl != nil {
					fl.Flush()
				}
			}
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{
			"id":"cmpl-1","object":"chat.completion","model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello there"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16}
		}`)
	}))
}

// ---------------------------------------------------------------------------
// Seeding helpers
// ---------------------------------------------------------------------------

// seedTenant creates a plan, account, subscription and key, returning the
// plaintext API key.
func (g *gateway) seedTenant(t *testing.T, clientID uint64, balanceMicros int64) (*domain.Account, *domain.Subscription, string) {
	t.Helper()
	ctx := t.Context()

	plan, err := g.store.Plans().Upsert(ctx, &domain.Plan{
		PlanCode: "test-plan", Name: "Test Plan", QuotaTokens: 100_000,
		Period: "month", RPMLimit: 10000, TPMLimit: 1000000, Concurrency: 100,
		ModelGroup: "default", MaxKeys: 5, GroupRatio: 1, Status: 1,
	})
	require.NoError(t, err)

	acc, err := g.store.Accounts().Upsert(ctx, &domain.Account{
		SiteID: testAppID, V10ClientID: clientID, Email: "u@example.com",
		Status: domain.AccountEnabled, BalanceCredit: balanceMicros,
		CreatedAt: g.clk.Now(), UpdatedAt: g.clk.Now(),
	})
	require.NoError(t, err)

	sub, _, err := g.store.Subscriptions().Create(ctx, &domain.Subscription{
		AccountID: acc.ID, V10HostID: clientID * 10, PlanID: plan.ID,
		PeriodStart: g.clk.Now(), PeriodEnd: g.clk.Now().AddDate(0, 1, 0),
		QuotaTotal: plan.QuotaTokens, BillingMode: domain.BillingHybrid,
		Status: domain.SubActive, CreatedAt: g.clk.Now(), UpdatedAt: g.clk.Now(),
	})
	require.NoError(t, err)

	plain, prefix := crypto.GenerateAPIKey()
	require.NoError(t, g.store.Keys().Create(ctx, &domain.APIKey{
		AccountID: acc.ID, SubscriptionID: sub.ID,
		KeyHash: crypto.HashKey(testSalt, plain), KeyPrefix: prefix,
		Name: "test", Status: domain.KeyActive,
		CreatedAt: g.clk.Now(), UpdatedAt: g.clk.Now(),
	}))
	return acc, sub, plain
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

// postJSON sends a JSON body and returns status plus decoded payload.
func postJSON(t *testing.T, url string, body any, headers map[string]string) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	return decodeResponse(t, resp)
}

func getJSON(t *testing.T, url string, headers map[string]string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	return decodeResponse(t, resp)
}

func decodeResponse(t *testing.T, resp *http.Response) (int, map[string]any) {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var out map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out
}

// chatRequest is the body sent to /v1/chat/completions.
func chatRequest(model string, stream bool) map[string]any {
	return map[string]any{
		"model":    model,
		"messages": []map[string]any{{"role": "user", "content": "hello"}},
		"stream":   stream,
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestHealthEndpointsArePublic(t *testing.T) {
	gw := newGateway(t)

	code, body := getJSON(t, gw.ts.URL+"/healthz", nil)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "ok", body["status"])

	code, _ = getJSON(t, gw.ts.URL+"/readyz", nil)
	assert.Equal(t, http.StatusOK, code)
}

func TestChatCompletionsRequiresKey(t *testing.T) {
	gw := newGateway(t)

	code, body := postJSON(t, gw.ts.URL+"/v1/chat/completions",
		chatRequest("test-model", false), nil)
	assert.Equal(t, http.StatusUnauthorized, code)
	errObj, ok := body["error"].(map[string]any)
	require.True(t, ok, "the error must use the OpenAI envelope: %v", body)
	assert.NotEmpty(t, errObj["message"])
}

func TestChatCompletionsNonStream(t *testing.T) {
	gw := newGateway(t)
	acc, sub, key := gw.seedTenant(t, 1001, 10_000_000)

	code, body := postJSON(t, gw.ts.URL+"/v1/chat/completions",
		chatRequest("test-model", false),
		map[string]string{"Authorization": "Bearer " + key})
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	choices, ok := body["choices"].([]any)
	require.True(t, ok)
	require.Len(t, choices, 1)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	assert.Equal(t, "hello there", msg["content"])

	// The upstream usage block must have been billed against the subscription.
	updated, err := gw.store.Subscriptions().GetByID(t.Context(), sub.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(16), updated.QuotaUsed, "12 prompt + 4 completion tokens")
	assert.Equal(t, int64(10_000_000), mustBalance(t, gw, acc.ID),
		"a hybrid key must prefer the subscription over the balance")

	// Exactly one usage row must exist and carry the request id. The consumer is
	// asynchronous, so poll briefly rather than sleeping a fixed interval.
	var rows []domain.UsageRecord
	require.Eventually(t, func() bool {
		rows, _ = gw.store.Usage().List(t.Context(), storeUsageAll())
		return len(rows) == 1
	}, 2*time.Second, 5*time.Millisecond, "the usage row must be persisted")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, 12, rows[0].PromptTokens)
	assert.Equal(t, 4, rows[0].CompletionTokens)
	assert.Equal(t, domain.PaidBySubscription, rows[0].BillingMode)
}

func TestChatCompletionsStream(t *testing.T) {
	gw := newGateway(t)
	_, sub, key := gw.seedTenant(t, 1002, 10_000_000)

	req, err := http.NewRequest(http.MethodPost, gw.ts.URL+"/v1/chat/completions",
		strings.NewReader(mustJSON(t, chatRequest("test-model", true))))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	stream := string(raw)
	assert.Contains(t, stream, "data: ")
	assert.Contains(t, stream, "hello")
	assert.Contains(t, stream, "[DONE]", "the stream must terminate cleanly")

	// Streaming usage must still settle.
	updated, err := gw.store.Subscriptions().GetByID(t.Context(), sub.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(10), updated.QuotaUsed, "7 prompt + 3 completion tokens")
}

func TestUnknownModelIsRejected(t *testing.T) {
	gw := newGateway(t)
	_, _, key := gw.seedTenant(t, 1003, 10_000_000)

	// An unregistered model is refused before any upstream call. 403 is the
	// documented answer: the model is not permitted for this plan.
	before := gw.upstreamHits
	code, body := postJSON(t, gw.ts.URL+"/v1/chat/completions",
		chatRequest("does-not-exist", false),
		map[string]string{"Authorization": "Bearer " + key})
	assert.Equal(t, http.StatusForbidden, code, "body: %v", body)
	assert.Equal(t, before, gw.upstreamHits,
		"an unknown model must never be forwarded upstream")
}

func TestKeyAllowListIsEnforced(t *testing.T) {
	gw := newGateway(t)
	acc, sub, key := gw.seedTenant(t, 1004, 10_000_000)

	// Restrict the key to a different model.
	keys, err := gw.store.Keys().ListBySubscription(t.Context(), sub.ID)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	k := keys[0]
	k.Models = []string{"only-this-model"}
	require.NoError(t, gw.store.Keys().Update(t.Context(), &k))

	code, body := postJSON(t, gw.ts.URL+"/v1/chat/completions",
		chatRequest("test-model", false),
		map[string]string{"Authorization": "Bearer " + key})
	assert.Equal(t, http.StatusForbidden, code, "body: %v", body)
	_ = acc
}

func TestInsufficientBalanceIsRejected(t *testing.T) {
	gw := newGateway(t)
	ctx := t.Context()

	// A balance-paid tenant with nothing in the account.
	plan, err := gw.store.Plans().Upsert(ctx, &domain.Plan{
		PlanCode: "bal-plan", Name: "Balance", QuotaTokens: 0, Period: "month",
		RPMLimit: 10000, TPMLimit: 1000000, Concurrency: 100,
		ModelGroup: "default", MaxKeys: 5, GroupRatio: 1, Status: 1,
	})
	require.NoError(t, err)
	acc, err := gw.store.Accounts().Upsert(ctx, &domain.Account{
		SiteID: testAppID, V10ClientID: 1005, Status: domain.AccountEnabled,
		BalanceCredit: 0,
	})
	require.NoError(t, err)
	sub, _, err := gw.store.Subscriptions().Create(ctx, &domain.Subscription{
		AccountID: acc.ID, V10HostID: 10050, PlanID: plan.ID,
		PeriodStart: gw.clk.Now(), PeriodEnd: gw.clk.Now().AddDate(0, 1, 0),
		QuotaTotal: 0, BillingMode: domain.BillingBalance, Status: domain.SubActive,
	})
	require.NoError(t, err)
	plain, prefix := crypto.GenerateAPIKey()
	require.NoError(t, gw.store.Keys().Create(ctx, &domain.APIKey{
		AccountID: acc.ID, SubscriptionID: sub.ID,
		KeyHash: crypto.HashKey(testSalt, plain), KeyPrefix: prefix,
		Status: domain.KeyActive, CreatedAt: gw.clk.Now(), UpdatedAt: gw.clk.Now(),
	}))

	before := gw.upstreamHits
	code, body := postJSON(t, gw.ts.URL+"/v1/chat/completions",
		chatRequest("test-model", false),
		map[string]string{"Authorization": "Bearer " + plain})
	assert.Equal(t, http.StatusPaymentRequired, code, "body: %v", body)
	assert.Equal(t, before, gw.upstreamHits,
		"a request with no quota must never reach the upstream")
}

func TestListModels(t *testing.T) {
	gw := newGateway(t)
	_, _, key := gw.seedTenant(t, 1006, 10_000_000)

	code, body := getJSON(t, gw.ts.URL+"/v1/models",
		map[string]string{"Authorization": "Bearer " + key})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	data, ok := body["data"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, data)
	found := false
	for _, item := range data {
		if item.(map[string]any)["id"] == "test-model" {
			found = true
		}
	}
	assert.True(t, found, "the catalogue must include the seeded model")
}

func TestInternalCreditIsIdempotent(t *testing.T) {
	gw := newGateway(t)

	// Register the V10 client first.
	code, body := signedPost(t, gw, "/internal/v1/accounts/sync", map[string]any{
		"v10_client_id": 2001, "email": "a@example.com",
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	credit := map[string]any{
		"v10_client_id": 2001, "kind": "v10_order",
		"amount_micros": 20_000_000,
		"ref_type":      "v10_order", "ref_id": "ORD-9001",
	}
	code, body = signedPost(t, gw, "/internal/v1/credit", credit)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	data := body["data"].(map[string]any)
	assert.Equal(t, true, data["applied"])
	firstBalance := data["balance_micros"]

	// Replaying the same reference must not double-credit.
	code, body = signedPost(t, gw, "/internal/v1/credit", credit)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	data = body["data"].(map[string]any)
	assert.Equal(t, false, data["applied"], "the replay must be a no-op")
	assert.Equal(t, firstBalance, data["balance_micros"], "the balance must not move")
}

func TestInternalCreditRejectsMissingRef(t *testing.T) {
	gw := newGateway(t)
	signedPost(t, gw, "/internal/v1/accounts/sync", map[string]any{"v10_client_id": 2002})

	code, body := signedPost(t, gw, "/internal/v1/credit", map[string]any{
		"v10_client_id": 2002, "kind": "v10_order", "amount_micros": 1000,
	})
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, float64(40001), body["code"],
		"a missing voucher must map to the documented 40001 code")
}

func TestInternalSignatureIsRequired(t *testing.T) {
	gw := newGateway(t)

	// No signature headers at all.
	code, body := postJSON(t, gw.ts.URL+"/internal/v1/credit",
		map[string]any{"v10_client_id": 1, "amount_micros": 1}, nil)
	assert.Equal(t, http.StatusUnauthorized, code, "body: %v", body)
	assert.Equal(t, float64(40100), body["code"])

	// A wrong secret must also fail.
	code, _ = signedPostWithSecret(t, gw, "/internal/v1/credit",
		map[string]any{"v10_client_id": 1, "amount_micros": 1}, "the-wrong-secret")
	assert.Equal(t, http.StatusUnauthorized, code)
}

func TestInternalHealthNeedsNoSignature(t *testing.T) {
	gw := newGateway(t)
	code, body := getJSON(t, gw.ts.URL+"/internal/v1/health", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "ok", body["data"].(map[string]any)["status"])
}

func TestSubscriptionLifecycleOverInternalAPI(t *testing.T) {
	gw := newGateway(t)
	signedPost(t, gw, "/internal/v1/accounts/sync", map[string]any{"v10_client_id": 3001})
	// The plan must exist for provisioning to resolve plan_code.
	_, perr := gw.store.Plans().Upsert(t.Context(), &domain.Plan{
		PlanCode: "test-plan", Name: "Test Plan", QuotaTokens: 100_000, Period: "month",
		RPMLimit: 10000, TPMLimit: 1000000, Concurrency: 100,
		ModelGroup: "default", MaxKeys: 5, GroupRatio: 1, Status: 1,
	})
	require.NoError(t, perr)

	// Provision.
	code, body := signedPost(t, gw, "/internal/v1/subscriptions", map[string]any{
		"v10_client_id": 3001, "v10_host_id": 30010,
		"plan_code": "test-plan", "quota_tokens": 50_000,
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	data := body["data"].(map[string]any)
	subID := uint64(data["subscription_id"].(float64))
	assert.Equal(t, true, data["created"])

	// Re-provisioning the same host and period is idempotent.
	code, body = signedPost(t, gw, "/internal/v1/subscriptions", map[string]any{
		"v10_client_id": 3001, "v10_host_id": 30010,
		"plan_code": "test-plan", "quota_tokens": 50_000,
		"period_start": data["period_start"], "period_end": data["period_end"],
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	data2 := body["data"].(map[string]any)
	assert.Equal(t, false, data2["created"], "the second call must not create a second row")

	// Suspend, then resume.
	code, _ = signedPost(t, gw, fmt.Sprintf("/internal/v1/subscriptions/%d/suspend", subID), nil)
	assert.Equal(t, http.StatusOK, code)
	sub, err := gw.store.Subscriptions().GetByID(t.Context(), subID)
	require.NoError(t, err)
	assert.Equal(t, domain.SubSuspended, sub.Status)

	code, _ = signedPost(t, gw, fmt.Sprintf("/internal/v1/subscriptions/%d/resume", subID), nil)
	assert.Equal(t, http.StatusOK, code)
	sub, err = gw.store.Subscriptions().GetByID(t.Context(), subID)
	require.NoError(t, err)
	assert.Equal(t, domain.SubActive, sub.Status)
}

func TestSuspendedKeyIsRejected(t *testing.T) {
	gw := newGateway(t)
	_, sub, key := gw.seedTenant(t, 1007, 10_000_000)

	// Suspending the subscription must cascade to its keys.
	require.NoError(t, gw.store.Subscriptions().Update(t.Context(), &domain.Subscription{
		ID: sub.ID, AccountID: sub.AccountID, V10HostID: sub.V10HostID, PlanID: sub.PlanID,
		PeriodStart: sub.PeriodStart, PeriodEnd: sub.PeriodEnd,
		QuotaTotal: sub.QuotaTotal, QuotaUsed: sub.QuotaUsed,
		BillingMode: sub.BillingMode, Status: domain.SubSuspended,
		CreatedAt: sub.CreatedAt, UpdatedAt: gw.clk.Now(),
	}))
	_, err := gw.store.Keys().SetStatusBySubscription(t.Context(), sub.ID, domain.KeySuspended)
	require.NoError(t, err)

	code, body := postJSON(t, gw.ts.URL+"/v1/chat/completions",
		chatRequest("test-model", false),
		map[string]string{"Authorization": "Bearer " + key})
	assert.Equal(t, http.StatusForbidden, code, "body: %v", body)
}

func TestAdminLoginAndDashboard(t *testing.T) {
	gw := newGateway(t)
	ctx := t.Context()

	hash, err := admin.HashPassword("secret123")
	require.NoError(t, err)
	require.NoError(t, gw.store.Admins().Create(ctx, &domain.AdminUser{
		Username: "ops", PasswordHash: hash, Role: "admin", Status: 1,
		CreatedAt: gw.clk.Now(),
	}))

	code, body := postJSON(t, gw.ts.URL+"/admin/v1/login",
		map[string]any{"username": "ops", "password": "secret123"}, nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	data := body["data"].(map[string]any)
	token, _ := data["token"].(string)
	require.NotEmpty(t, token)

	code, body = getJSON(t, gw.ts.URL+"/admin/v1/dashboard",
		map[string]string{"Authorization": "Bearer " + token})
	assert.Equal(t, http.StatusOK, code, "body: %v", body)

	// A wrong password must be refused.
	code, _ = postJSON(t, gw.ts.URL+"/admin/v1/login",
		map[string]any{"username": "ops", "password": "wrong"}, nil)
	assert.Equal(t, http.StatusUnauthorized, code)
}

func TestAdminRequiresToken(t *testing.T) {
	gw := newGateway(t)
	code, _ := getJSON(t, gw.ts.URL+"/admin/v1/dashboard", nil)
	assert.Equal(t, http.StatusUnauthorized, code)
}

// TestConsoleServesEmbeddedUI proves the console is served from the binary itself.
//
// The gateway in this harness configures no console directory at all, so anything
// that comes back can only have come from the embedded filesystem. This is the
// regression guard for deployment self-containment: if someone reintroduces a hard
// dependency on a dist/ directory, the console silently becomes a JSON notice and
// this test fails.
func TestConsoleServesEmbeddedUI(t *testing.T) {
	gw := newGateway(t)

	resp, err := http.Get(gw.ts.URL + "/console/")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	raw, rerr := io.ReadAll(resp.Body)
	require.NoError(t, rerr)
	body := string(raw)

	// Either the embedded SPA shell is returned, or (on a checkout where the front
	// end has never been built) the explicit notice. Never an error status.
	if strings.Contains(body, `id="app"`) {
		assert.Contains(t, resp.Header.Get("Content-Type"), "text/html",
			"the SPA shell must be served as HTML")
		return
	}
	assert.Contains(t, body, "message",
		"without a build the console must explain itself in JSON, not fail")
}

// TestConsoleServesDeepRouteAsSPA checks history-mode routing still works: a deep
// link has to return the shell, otherwise a browser refresh lands on a 404.
func TestConsoleServesDeepRouteAsSPA(t *testing.T) {
	gw := newGateway(t)
	resp, err := http.Get(gw.ts.URL + "/console/admin/models")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "a deep console route must not 404")
}

// ---------------------------------------------------------------------------
// signed internal helpers
// ---------------------------------------------------------------------------

// signedPost signs and sends an /internal/v1 request.
func signedPost(t *testing.T, gw *gateway, path string, body any) (int, map[string]any) {
	t.Helper()
	return signedPostWithSecret(t, gw, path, body, testSecret)
}

// signedPostWithSecret signs with an explicit secret against the default app id.
func signedPostWithSecret(t *testing.T, gw *gateway, path string, body any, secret string) (int, map[string]any) {
	t.Helper()
	return signedPostAs(t, gw, testAppID, secret, path, body)
}

// signedPostAs signs an internal call with an explicit app id and secret.
func signedPostAs(t *testing.T, gw *gateway, appID, secret, path string, body any) (int, map[string]any) {
	t.Helper()
	var raw []byte
	if body != nil {
		raw = []byte(mustJSON(t, body))
	} else {
		raw = []byte("{}")
	}

	tsUnix := gw.clk.Now().Unix()
	ts := fmt.Sprintf("%d", tsUnix)
	// The verifier requires a hex nonce (replay window key); derive a unique
	// 32-hex-char value from the timestamp and a counter.
	nonceCounter++
	nonce := fmt.Sprintf("%016x%016x", gw.clk.Now().UnixNano(), nonceCounter)
	headers := map[string]string{
		auth.HeaderAppID:     appID,
		auth.HeaderTimestamp: ts,
		auth.HeaderNonce:     nonce,
		"Content-Type":       "application/json",
	}
	headers[auth.HeaderSignature] = sign.SignRequest(secret, http.MethodPost, path, "", raw, tsUnix, nonce)

	req, err := http.NewRequest(http.MethodPost, gw.ts.URL+path, bytes.NewReader(raw))
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	return decodeResponse(t, resp)
}

// mustJSON marshals a value or fails the test.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return string(raw)
}

// storeUsageAll returns a filter that matches every usage row.
func storeUsageAll() store.UsageFilter {
	return store.UsageFilter{IncludeShadow: true}
}

// mustBalance reads an account balance or fails.
func mustBalance(t *testing.T, gw *gateway, accountID uint64) int64 {
	t.Helper()
	acc, err := gw.store.Accounts().GetByID(t.Context(), accountID)
	require.NoError(t, err)
	return acc.BalanceCredit
}

// ---------------------------------------------------------------------------
// First-run usability
// ---------------------------------------------------------------------------

// TestBootstrapDefaultAdminCanLogIn proves the out-of-the-box promise end to end:
// a fresh install with an empty store must accept admin/admin over real HTTP and
// reach the admin console API. This is the difference between "it compiles" and
// "someone can use it".
func TestBootstrapDefaultAdminCanLogIn(t *testing.T) {
	clk := &fixedClock{now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	st := memstore.NewWithClock(clk)
	cfg := config.Default()
	cfg.Bootstrap = config.Bootstrap{AdminUsername: "admin", AdminPassword: "admin"}

	rep, err := bootstrap.Run(t.Context(), st, cfg, log.New("error", "text"), clk)
	require.NoError(t, err)
	require.True(t, rep.AdminCreated)
	require.True(t, rep.SiteCreated, "a site must exist or V10 cannot connect")

	// Build a router over this pre-seeded store.
	gw := newGatewayWithStore(t, st, clk)

	code, body := postJSON(t, gw.ts.URL+"/admin/v1/login",
		map[string]any{"username": "admin", "password": "admin"}, nil)
	require.Equal(t, http.StatusOK, code, "admin/admin must work on a fresh install: %v", body)

	token, _ := body["data"].(map[string]any)["token"].(string)
	require.NotEmpty(t, token)

	// The token must be usable for a real admin operation, not just issued.
	code, body = getJSON(t, gw.ts.URL+"/admin/v1/dashboard",
		map[string]string{"Authorization": "Bearer " + token})
	assert.Equal(t, http.StatusOK, code, "body: %v", body)

	// And a wrong password must still be refused.
	code, _ = postJSON(t, gw.ts.URL+"/admin/v1/login",
		map[string]any{"username": "admin", "password": "not-the-password"}, nil)
	assert.Equal(t, http.StatusUnauthorized, code)
}

// TestBootstrapSiteSecretSignsInternalCalls proves the generated site secret is
// the one V10 must use: a call signed with it is accepted, one signed with any
// other value is not.
func TestBootstrapSiteSecretSignsInternalCalls(t *testing.T) {
	clk := &fixedClock{now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	st := memstore.NewWithClock(clk)
	cfg := config.Default()
	cfg.Bootstrap = config.Bootstrap{AdminUsername: "admin", AdminPassword: "admin"}

	rep, err := bootstrap.Run(t.Context(), st, cfg, nil, clk)
	require.NoError(t, err)
	require.NotEmpty(t, rep.SiteSecret, "the secret must be reported once")

	gw := newGatewayWithStore(t, st, clk)

	// Graft the reported app id/secret onto the gateway's signing helper.
	code, body := signedPostAs(t, gw, rep.SiteAppID, rep.SiteSecret,
		"/internal/v1/accounts/sync", map[string]any{"v10_client_id": 9001})
	assert.Equal(t, http.StatusOK, code,
		"the generated site secret must authenticate: %v", body)

	// Same app id, wrong secret.
	code, _ = signedPostAs(t, gw, rep.SiteAppID, "the-wrong-secret",
		"/internal/v1/accounts/sync", map[string]any{"v10_client_id": 9002})
	assert.Equal(t, http.StatusUnauthorized, code)
}

// TestDemoContentIsUsable sends a chat request through the demo channel, proving
// the seeded plan/model/channel form a coherent set rather than three unrelated
// rows. The upstream is a stub so no network is needed.
func TestDemoContentIsUsable(t *testing.T) {
	gw := newGateway(t)
	ctx := t.Context()

	cfg := config.Default()
	cfg.Bootstrap = config.Bootstrap{DemoContent: true}
	_, err := bootstrap.Run(ctx, gw.store, cfg, nil, gw.clk)
	require.NoError(t, err)

	// Bootstrap deliberately does not add a channel when one already exists, so
	// this store keeps its real upstream. Route the demo model through it: that is
	// what an operator does after seeding.
	channels, err := gw.store.Channels().List(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, channels)
	ch := channels[0]
	ch.Models = append(ch.Models, "gpt-4o-mini")
	require.NoError(t, gw.store.Channels().Update(ctx, &ch))

	// A tenant on the demo plan.
	acc, err := gw.store.Accounts().Upsert(ctx, &domain.Account{
		SiteID: testAppID, V10ClientID: 9100, Status: domain.AccountEnabled,
		BalanceCredit: 10_000_000,
	})
	require.NoError(t, err)
	plan, err := gw.store.Plans().GetByCode(ctx, "demo")
	require.NoError(t, err)
	sub, _, err := gw.store.Subscriptions().Create(ctx, &domain.Subscription{
		AccountID: acc.ID, V10HostID: 91000, PlanID: plan.ID,
		PeriodStart: gw.clk.Now(), PeriodEnd: gw.clk.Now().AddDate(0, 1, 0),
		QuotaTotal: plan.QuotaTokens, BillingMode: domain.BillingSubscription,
		Status: domain.SubActive, CreatedAt: gw.clk.Now(), UpdatedAt: gw.clk.Now(),
	})
	require.NoError(t, err)
	plain, prefix := crypto.GenerateAPIKey()
	require.NoError(t, gw.store.Keys().Create(ctx, &domain.APIKey{
		AccountID: acc.ID, SubscriptionID: sub.ID,
		KeyHash: crypto.HashKey(testSalt, plain), KeyPrefix: prefix,
		Status: domain.KeyActive, CreatedAt: gw.clk.Now(), UpdatedAt: gw.clk.Now(),
	}))

	code, body := postJSON(t, gw.ts.URL+"/v1/chat/completions",
		chatRequest("gpt-4o-mini", false),
		map[string]string{"Authorization": "Bearer " + plain})
	assert.Equal(t, http.StatusOK, code,
		"the seeded demo content must be able to serve a request: %v", body)
}
