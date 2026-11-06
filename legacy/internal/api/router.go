// Package api wires the HTTP surface of the gateway.
//
// Three distinct client groups share one server (plan §4.1):
//
//	/v1/*          OpenAI-compatible endpoints authenticated by sk- keys
//	/internal/v1/* server-to-server calls from the V10 plugin, HMAC signed
//	/admin/v1/*    the operator console, JWT authenticated
//	/console/*     the Vue3 SPA and its session endpoints
//
// This file holds only the router assembly and shared plumbing; each group has
// its own file so the money-critical paths stay readable.
package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"czapi/internal/api/admin"
	"czapi/internal/api/internalapi"
	"czapi/internal/api/openai"
	"czapi/internal/auth"
	"czapi/internal/metrics"
	"czapi/internal/pkg/log"
)

// Deps carries everything the routes need. It is assembled once in cmd/server
// so no handler reaches for globals.
type Deps struct {
	Log        *log.Logger
	Metrics    *metrics.Registry
	OpenAI     *openai.Handlers
	Internal   *internalapi.Handlers
	Admin      *admin.Handlers
	KeyMW      *auth.Verifier
	AdminJWT   *auth.JWTIssuer
	Sig        *auth.SigVerifier
	Health     HealthChecker
	ConsoleDir string
	// Version is reported by /healthz.
	Version string
}

// HealthChecker aggregates subsystem liveness for /healthz and /readyz.
type HealthChecker interface {
	// Check returns a map of component -> status ("ok"/"down") plus an overall
	// readiness verdict.
	Check(r *http.Request) (components map[string]string, ready bool)
}

// New builds the root router.
func New(d *Deps) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(requestIDMiddleware)
	r.Use(accessLogMiddleware(d.Log))
	r.Use(timeoutGuard(10 * time.Minute))

	// Operational endpoints stay outside every auth group.
	r.Get("/healthz", healthHandler(d))
	r.Get("/readyz", readinessHandler(d))
	if d.Metrics != nil {
		r.Method(http.MethodGet, "/metrics", d.Metrics.Handler())
	}

	// --- OpenAI-compatible surface (user API keys) ---------------------------
	r.Route("/v1", func(v1 chi.Router) {
		v1.Use(auth.APIKeyMiddleware(d.KeyMW))
		d.OpenAI.Mount(v1)
	})

	// --- V10 plugin surface (HMAC signed) ------------------------------------
	r.Route("/internal/v1", func(iv1 chi.Router) {
		// /health is reachable unsigned so the V10 module can probe connectivity.
		iv1.Get("/health", d.Internal.Health)
		iv1.Group(func(signed chi.Router) {
			signed.Use(auth.InternalSigMiddleware(d.Sig, "/internal/v1/health"))
			d.Internal.Mount(signed)
		})
	})

	// --- Admin console -------------------------------------------------------
	r.Route("/admin/v1", func(av1 chi.Router) {
		av1.Post("/login", d.Admin.Login)
		av1.Group(func(authed chi.Router) {
			authed.Use(auth.AdminJWTMiddleware(d.AdminJWT))
			d.Admin.Mount(authed)
		})
	})

	// --- Console SPA + its browser session ----------------------------------
	r.Route("/console", func(cr chi.Router) {
		d.OpenAI.MountConsole(cr)
	})

	return r
}

// --- middleware ------------------------------------------------------------

const requestIDHeader = "X-CZ-Request-Id"

// requestIDMiddleware assigns a correlation id to every request and echoes it.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if id == "" {
			id = r.Header.Get("X-Request-Id")
		}
		ctx, rid := withRequestID(r.Context(), id)
		w.Header().Set(requestIDHeader, rid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// accessLogMiddleware logs one structured line per request.
func accessLogMiddleware(l *log.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			if l == nil {
				return
			}
			l.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", RequestIDFrom(r.Context()),
				"remote", r.RemoteAddr,
			)
		})
	}
}

// timeoutGuard bounds the whole non-stream lifetime. Streaming is bounded by the
// relay's own per-chunk timeouts instead, so this ceiling is generous.
func timeoutGuard(d time.Duration) func(http.Handler) http.Handler {
	return middleware.Timeout(d)
}
