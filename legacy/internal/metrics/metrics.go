// Package metrics exposes the gateway's Prometheus instrumentation (plan §6.9).
//
// Every metric is registered on a private registry so tests and multiple
// instances in one process do not collide. The relay, API layer and worker all
// report through *Registry; a nil *Registry is safe and silently no-ops, which
// keeps instrumentation optional at call sites.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry holds every gateway collector plus their registry.
type Registry struct {
	reg *prometheus.Registry

	RequestsTotal   *prometheus.CounterVec
	RequestDuration *prometheus.HistogramVec
	TTFT            *prometheus.HistogramVec
	TokensTotal     *prometheus.CounterVec
	QuotaRejected   *prometheus.CounterVec
	ChannelErrors   *prometheus.CounterVec
	BillingLag      prometheus.Gauge
	QueueDepth      *prometheus.GaugeVec
	ChannelOpen     *prometheus.GaugeVec
	CreditsApplied  *prometheus.CounterVec
	ReconcileDiff   prometheus.Gauge
	InFlight        prometheus.Gauge
}

// New builds and registers all collectors.
func New() *Registry {
	reg := prometheus.NewRegistry()
	r := &Registry{reg: reg}

	r.RequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "czapi_requests_total",
		Help: "Inference requests by model, channel and status class.",
	}, []string{"model", "channel", "status"})

	r.RequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "czapi_request_duration_seconds",
		Help:    "End-to-end request latency.",
		Buckets: []float64{.05, .1, .25, .5, 1, 2, 5, 10, 30, 60, 120, 300},
	}, []string{"model", "stream"})

	r.TTFT = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "czapi_ttft_seconds",
		Help:    "Time to first token.",
		Buckets: []float64{.02, .05, .1, .2, .4, .8, 1.6, 3, 6, 12, 30, 60},
	}, []string{"model"})

	r.TokensTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "czapi_tokens_total",
		Help: "Tokens metered by kind (prompt|completion|cached).",
	}, []string{"model", "kind"})

	r.QuotaRejected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "czapi_quota_rejected_total",
		Help: "Requests rejected for quota reasons.",
	}, []string{"reason"})

	r.ChannelErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "czapi_channel_errors_total",
		Help: "Upstream errors by channel and code.",
	}, []string{"channel", "code"})

	r.BillingLag = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "czapi_billing_lag_seconds",
		Help: "Age of the oldest unsettled usage event.",
	})

	r.QueueDepth = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "czapi_queue_depth",
		Help: "Pending messages per topic.",
	}, []string{"topic"})

	r.ChannelOpen = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "czapi_channel_breaker_open",
		Help: "1 when the channel circuit breaker is open.",
	}, []string{"channel"})

	r.CreditsApplied = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "czapi_credits_applied_total",
		Help: "Credit ledger applications by kind and result.",
	}, []string{"kind", "result"})

	r.ReconcileDiff = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "czapi_reconcile_diff_micros",
		Help: "Absolute reconciliation difference of the last run, in micro-yuan.",
	})

	r.InFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "czapi_inflight_requests",
		Help: "Requests currently being forwarded.",
	})

	reg.MustRegister(
		r.RequestsTotal, r.RequestDuration, r.TTFT, r.TokensTotal, r.QuotaRejected,
		r.ChannelErrors, r.BillingLag, r.QueueDepth, r.ChannelOpen, r.CreditsApplied,
		r.ReconcileDiff, r.InFlight,
	)
	return r
}

// Handler returns the Prometheus scrape handler.
func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{})
}

// Gatherer exposes the underlying registry (for tests).
func (r *Registry) Gatherer() prometheus.Gatherer { return r.reg }

// --- Convenience reporters used across the codebase. All are nil-safe. ---

// ObserveRequest records one finished request.
func (r *Registry) ObserveRequest(model, channel string, status int) {
	if r == nil {
		return
	}
	r.RequestsTotal.WithLabelValues(model, channel, StatusClass(status)).Inc()
}

// ObserveLatency records end-to-end latency.
func (r *Registry) ObserveLatency(model string, stream bool, d time.Duration) {
	if r == nil {
		return
	}
	r.RequestDuration.WithLabelValues(model, strconv.FormatBool(stream)).Observe(d.Seconds())
}

// ObserveTTFT records time to first token.
func (r *Registry) ObserveTTFT(model string, d time.Duration) {
	if r == nil {
		return
	}
	r.TTFT.WithLabelValues(model).Observe(d.Seconds())
}

// ObserveTokens records metered token counts.
func (r *Registry) ObserveTokens(model string, prompt, completion, cached int) {
	if r == nil {
		return
	}
	if prompt > 0 {
		r.TokensTotal.WithLabelValues(model, "prompt").Add(float64(prompt))
	}
	if completion > 0 {
		r.TokensTotal.WithLabelValues(model, "completion").Add(float64(completion))
	}
	if cached > 0 {
		r.TokensTotal.WithLabelValues(model, "cached").Add(float64(cached))
	}
}

// ObserveChannelError records an upstream failure.
func (r *Registry) ObserveChannelError(channel, code string) {
	if r == nil {
		return
	}
	r.ChannelErrors.WithLabelValues(channel, code).Inc()
}

// ObserveQuotaRejected records a quota rejection.
func (r *Registry) ObserveQuotaRejected(reason string) {
	if r == nil {
		return
	}
	r.QuotaRejected.WithLabelValues(reason).Inc()
}

// SetQueueDepth publishes the pending depth of a topic.
func (r *Registry) SetQueueDepth(topic string, depth int64) {
	if r == nil {
		return
	}
	r.QueueDepth.WithLabelValues(topic).Set(float64(depth))
}

// SetChannelOpen publishes breaker state for a channel.
func (r *Registry) SetChannelOpen(channel string, open bool) {
	if r == nil {
		return
	}
	v := 0.0
	if open {
		v = 1.0
	}
	r.ChannelOpen.WithLabelValues(channel).Set(v)
}

// SetBillingLag publishes the age of the oldest unsettled event.
func (r *Registry) SetBillingLag(d time.Duration) {
	if r == nil {
		return
	}
	r.BillingLag.Set(d.Seconds())
}

// ObserveCredit records a credit application result.
func (r *Registry) ObserveCredit(kind, result string) {
	if r == nil {
		return
	}
	r.CreditsApplied.WithLabelValues(kind, result).Inc()
}

// SetReconcileDiff publishes the last reconciliation difference.
func (r *Registry) SetReconcileDiff(micros int64) {
	if r == nil {
		return
	}
	if micros < 0 {
		micros = -micros
	}
	r.ReconcileDiff.Set(float64(micros))
}

// SetInFlight publishes the current in-flight request count.
func (r *Registry) SetInFlight(n int) {
	if r == nil {
		return
	}
	r.InFlight.Set(float64(n))
}

// StatusClass buckets an HTTP status into 2xx/4xx/5xx for cardinality safety.
func StatusClass(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500:
		return "5xx"
	default:
		return "other"
	}
}
