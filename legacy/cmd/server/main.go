// Command server runs the CZAPI gateway.
//
// Wiring lives here and only here: every dependency is constructed explicitly and
// handed to the layer that needs it, so there are no package-level globals and a
// test can build the same graph with different drivers.
//
// The availability contract (see docs/runbook.md):
//   - storage.driver=memory + cache.driver=memory needs no external service;
//   - a missing MySQL/Redis at startup is fatal only when that driver was
//     explicitly selected;
//   - billing can be disabled at runtime through the billing_enforced flag.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"czapi/internal/api"
	"czapi/internal/api/admin"
	"czapi/internal/api/internalapi"
	openaiapi "czapi/internal/api/openai"
	"czapi/internal/auth"
	"czapi/internal/billing"
	"czapi/internal/bootstrap"
	"czapi/internal/cache"
	"czapi/internal/cache/memcache"
	"czapi/internal/cache/rediscache"
	"czapi/internal/config"
	"czapi/internal/metrics"
	"czapi/internal/pkg/clock"
	"czapi/internal/pkg/crypto"
	"czapi/internal/pkg/log"
	openaiadapter "czapi/internal/provider/openai"
	"czapi/internal/queue"
	"czapi/internal/queue/memqueue"
	"czapi/internal/queue/redisqueue"
	"czapi/internal/quota"
	"czapi/internal/ratelimit"
	"czapi/internal/relay"
	"czapi/internal/runtimecfg"
	"czapi/internal/store"
	"czapi/internal/store/memstore"
	"czapi/internal/store/mysqlstore"
	"czapi/internal/worker"
)

// version is overridden at build time with -ldflags.
var version = "dev"

func main() {
	var (
		configPath = flag.String("config", "", "path to config.yaml")
		showVer    = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Println(version)
		return
	}

	if err := run(*configPath); err != nil {
		fmt.Fprintf(os.Stderr, "czapi: %v\n", err)
		os.Exit(1)
	}
}

// run assembles the application, serves until a signal arrives, then drains.
func run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	lg := log.New(cfg.Log.Level, cfg.Log.Format)
	clk := clock.System{}
	mx := metrics.New()

	// --- storage -----------------------------------------------------------
	st, err := buildStore(cfg, clk)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	// --- cache + queue -----------------------------------------------------
	ce, err := buildCache(cfg, clk)
	if err != nil {
		return err
	}
	defer func() { _ = ce.Close() }()

	q, err := buildQueue(cfg, clk)
	if err != nil {
		return err
	}
	defer func() { _ = q.Close() }()

	// --- secrets -----------------------------------------------------------
	salt := cfg.Auth.KeyHashSalt
	if salt == "" {
		// Without a salt the key hashes are unsalted. Warn loudly: this is
		// acceptable for development but must never happen in production.
		salt = "czapi-dev-salt"
		lg.Warn("auth.key_hash_salt is empty; using the development default")
	}
	jwtSecret := cfg.Auth.AdminJWTSecret
	if jwtSecret == "" {
		jwtSecret = "czapi-dev-jwt-" + salt
		lg.Warn("auth.admin_jwt_secret is empty; using a derived development default")
	}

	sealer, serr := crypto.NewSealer(cfg.Auth.AdminJWTSecret)
	if serr != nil || cfg.Auth.AdminJWTSecret == "" {
		lg.Warn("channel secret encryption unavailable; upstream keys cannot be stored")
		sealer = nil
	}

	// --- first-run bootstrap -----------------------------------------------
	// A fresh install has no administrator and possibly no site, which would make
	// the console unreachable and /internal/v1 unusable. Seed what is missing.
	// Every step is idempotent and never overwrites, so restarting cannot reset a
	// password or rotate a secret.
	rep, berr := bootstrap.Run(context.Background(), st, cfg, lg, clk)
	if berr != nil {
		return fmt.Errorf("bootstrap: %w", berr)
	}
	if rep.AdminCreated {
		lg.Warn("use these credentials to sign in, then change them",
			"console", cfg.Server.Addr+"/console/admin/login",
			"username", rep.AdminUsername, "password", "(see bootstrap.admin_password)")
	}
	// Report where the console UI comes from, so an operator can tell at a glance
	// whether this process is serving its built-in assets or a dev directory.
	if cfg.Console.Enabled {
		mode := "embedded"
		if cfg.Console.Dir != "" {
			mode = "disk:" + cfg.Console.Dir
		}
		lg.Info("console ui ready", "mode", mode, "path", "/console/admin/login")
	}
	if rep.SiteCreated {
		lg.Info("V10 site ready for /internal/v1",
			"app_id", rep.SiteAppID, "secret_source",
			siteSecretSource(rep.SiteSecret))
		if rep.SiteSecret != "" {
			// Printed exactly once: there is no API that returns it again.
			fmt.Printf("\n  V10 site secret for app_id %q (store it now, it is not shown again):\n  %s\n\n",
				rep.SiteAppID, rep.SiteSecret)
		}
	}

	// --- billing -----------------------------------------------------------
	engine := billing.NewEngine(billing.EngineConfig{
		TokenToMicros:           cfg.Billing.TokenToMicros,
		DefaultCompletionTokens: cfg.Billing.DefaultCompletionTokens,
	})
	ledgerSvc := quota.New(st, ce, q, engine, clk, mx, quota.Config{
		EnforceBilling:       cfg.Billing.EnforceBilling,
		ShadowMode:           cfg.Billing.ShadowMode,
		TokenToMicros:        cfg.Billing.TokenToMicros,
		KeyDailyMicros:       cfg.Limits.KeyDailyMicros,
		AccountMonthlyMicros: cfg.Limits.AccountMonthlyMicros,
		GlobalDailyMicros:    cfg.Limits.GlobalDailyMicros,
	})

	// --- relay -------------------------------------------------------------
	breaker := relay.NewBreaker(relay.BreakerConfig{
		Window:       cfg.Relay.BreakerWindow,
		MinSamples:   cfg.Relay.BreakerMinSamples,
		ErrorRate:    cfg.Relay.BreakerErrorRate,
		OpenDuration: cfg.Relay.BreakerOpenDuration,
	}, clk)
	selector := relay.NewSelector(st.Channels(), breaker, nil)
	fwd := relay.NewForwarder(relay.ForwarderConfig{
		ConnectTimeout:     cfg.Relay.ConnectTimeout,
		FirstByteTimeout:   cfg.Relay.FirstByteTimeout,
		TotalTimeout:       cfg.Relay.TotalTimeout,
		StreamChunkTimeout: cfg.Relay.StreamChunkTimeout,
		MaxRetries:         cfg.Relay.MaxRetries,
		MaxBodyBytes:       32 << 20,
	}, selector, breaker, openaiadapter.New(), mx)

	// --- auth --------------------------------------------------------------
	verifierDeps := auth.DepsFromStore(st)
	verifierDeps.Cache = ce
	verifier := auth.NewVerifier(verifierDeps, auth.VerifierConfig{
		Salt:     salt,
		Clock:    clk,
		CacheTTL: cfg.Auth.KeyCacheTTL,
	})

	jwtIssuer := auth.NewJWTIssuer(auth.JWTConfig{
		Secret: jwtSecret,
		Issuer: cfg.Auth.AdminJWTIssuer,
		TTL:    cfg.Auth.AdminJWTTTL,
		Clock:  clk,
	})
	sigVerifier := auth.NewSigVerifier(siteResolver{st: st}, ce, auth.SigConfig{
		Clock:      clk,
		ClockSkew:  cfg.Auth.InternalClockSkew,
		NonceTTL:   cfg.Auth.InternalNonceTTL,
		AllowNoApp: cfg.Auth.InternalAllowNoApp,
	})

	// --- rate limiter ------------------------------------------------------
	limiter := ratelimit.New(ce, clk, ratelimit.Config{
		RPM:         cfg.Limits.RPM,
		TPM:         cfg.Limits.TPM,
		Concurrency: cfg.Limits.Concurrency,
	})

	// --- live-tunable settings ---------------------------------------------
	// Seeded from the file, then adjustable from the console without a restart.
	// Only values that are safe to change while serving live here; anything that
	// would invalidate stored data stays file-only.
	runtimeSettings := runtimecfg.New(runtimecfg.Settings{Limits: runtimecfg.Limits{
		KeyDailyMicros:       cfg.Limits.KeyDailyMicros,
		AccountMonthlyMicros: cfg.Limits.AccountMonthlyMicros,
		GlobalDailyMicros:    cfg.Limits.GlobalDailyMicros,
		RPM:                  cfg.Limits.RPM,
		TPM:                  cfg.Limits.TPM,
		Concurrency:          cfg.Limits.Concurrency,
	}})
	ledgerSvc.SetCapsSource(runtimeSettings)

	// --- feature flags -----------------------------------------------------
	flags := admin.NewFlagStore(st.Flags(), map[string]bool{
		admin.FlagGatewayEnabled:          cfg.Flags.GatewayEnabled,
		admin.FlagBillingEnforced:         cfg.Flags.BillingEnforced,
		admin.FlagModuleAutocreateEnabled: cfg.Flags.ModuleAutocreateEnabled,
		admin.FlagRechargeEnabled:         cfg.Flags.RechargeEnabled,
	})

	// --- HTTP handlers -----------------------------------------------------
	openaiHandlers := openaiapi.New(openaiapi.Deps{
		Store: st, Verifier: verifier, Ledger: ledgerSvc, Engine: engine,
		Quota: ledgerSvc, Relay: fwd, Limiter: limiter, Metrics: mx, Log: lg,
		Clock: clk, Cache: ce, KeySalt: salt,
		Console: openaiapi.ConsoleConfig{
			Enabled: cfg.Console.Enabled,
			DistDir: cfg.Console.Dir,
		},
	})

	internalHandlers := internalapi.New(internalapi.Deps{
		Store: st, Ledger: ledgerSvc, Credit: ledgerSvc, Metrics: mx, Log: lg,
		Clock: clk, Cache: ce, KeySalt: salt, Console: openaiHandlers, Version: version,
	})

	adminHandlers := admin.New(admin.Deps{
		Store: st, JWT: jwtIssuer, Credit: ledgerSvc, Relay: fwd, Breaker: breaker,
		Metrics: mx, Log: lg, Clock: clk, Sealer: sealer, Cache: ce, KeySalt: salt,
		Flags: flags, Invalidator: verifier,
	})
	adminHandlers.SetRuntimeSettings(runtimeSettings)

	handler := api.New(&api.Deps{
		Log: lg, Metrics: mx, OpenAI: openaiHandlers, Internal: internalHandlers,
		Admin: adminHandlers, KeyMW: verifier, AdminJWT: jwtIssuer, Sig: sigVerifier,
		Health:     healthChecker{st: st, ce: ce, q: q},
		ConsoleDir: cfg.Console.Dir, Version: version,
	})

	// --- background workers ------------------------------------------------
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	consumer := worker.NewUsageConsumer(st, q, mx, lg, clk, worker.Config{
		RollupInterval: cfg.Worker.PollInterval,
	})
	go func() {
		if rerr := consumer.Run(ctx); rerr != nil && !errors.Is(rerr, context.Canceled) {
			lg.Error("usage consumer stopped", "err", rerr)
		}
	}()

	sweeper := worker.NewSubscriptionSweeper(st, lg, clk)
	go func() {
		if rerr := sweeper.Run(ctx, 5*time.Minute); rerr != nil &&
			!errors.Is(rerr, context.Canceled) {
			lg.Error("subscription sweeper stopped", "err", rerr)
		}
	}()

	// --- serve -------------------------------------------------------------
	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           handler,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTO,
		ReadTimeout:       cfg.Relay.TotalTimeout,
		WriteTimeout:      0, // streaming responses must not be cut off
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	// Bind before logging. Claiming "listening" ahead of a successful bind would
	// make a port conflict look like a healthy start in the log, which is exactly
	// the misdiagnosis this ordering avoids.
	ln, lnErr := net.Listen("tcp", cfg.Server.Addr)
	if lnErr != nil {
		return fmt.Errorf("listen on %s: %w (another process may already be using this port)", cfg.Server.Addr, lnErr)
	}

	errCh := make(chan error, 1)
	go func() {
		lg.Info("czapi listening",
			"addr", ln.Addr().String(), "version", version,
			"storage", cfg.Storage.Driver, "cache", cfg.Cache.Driver,
			"worker", cfg.Worker.Enabled, "billing_enforced", cfg.Billing.EnforceBilling)
		if lerr := srv.Serve(ln); lerr != nil && !errors.Is(lerr, http.ErrServerClosed) {
			errCh <- lerr
		}
	}()

	// --- shutdown ----------------------------------------------------------
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case lerr := <-errCh:
		return fmt.Errorf("listen: %w", lerr)
	case sig := <-sigCh:
		lg.Info("shutting down", "signal", sig.String())
	}

	// Stop accepting work first, then drain in-flight requests so no settlement
	// is lost (plan §15.1).
	cancel()
	consumer.Close()
	sweeper.Close()

	timeout := cfg.Server.ShutdownTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), timeout)
	defer shutdownCancel()
	if serr := srv.Shutdown(shutdownCtx); serr != nil {
		lg.Error("graceful shutdown incomplete", "err", serr)
		_ = srv.Close()
	}
	lg.Info("stopped")
	return nil
}

// ---------------------------------------------------------------------------
// driver construction
// ---------------------------------------------------------------------------

// buildStore selects the storage driver from the configuration.
func buildStore(cfg *config.Config, clk clock.Clock) (store.Store, error) {
	switch strings.ToLower(cfg.Storage.Driver) {
	case "", "memory":
		return memstore.NewWithClock(clk), nil
	case "mysql":
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return mysqlstore.New(ctx, mysqlstore.Config{
			DSN:             cfg.Storage.DSN,
			MaxOpenConns:    cfg.Storage.MaxOpenConns,
			MaxIdleConns:    cfg.Storage.MaxIdleConns,
			ConnMaxLifetime: cfg.Storage.ConnMaxLife,
		})
	default:
		return nil, fmt.Errorf("unknown storage.driver %q (want memory or mysql)", cfg.Storage.Driver)
	}
}

// buildCache selects the cache driver.
func buildCache(cfg *config.Config, clk clock.Clock) (cache.Cache, error) {
	switch strings.ToLower(cfg.Cache.Driver) {
	case "", "memory":
		return memcache.NewWithClock(clk, time.Minute), nil
	case "redis":
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return rediscache.New(ctx, rediscache.Config{
			Addr:     cfg.Cache.Addr,
			Password: cfg.Cache.Password,
			DB:       cfg.Cache.DB,
			PoolSize: cfg.Cache.PoolSize,
		})
	default:
		return nil, fmt.Errorf("unknown cache.driver %q (want memory or redis)", cfg.Cache.Driver)
	}
}

// buildQueue selects the queue driver.
//
// The worker section controls whether asynchronous metering is used at all; when
// it is disabled the in-memory queue still exists so the publish path is a cheap
// no-op sink rather than a nil check at every call site.
func buildQueue(cfg *config.Config, clk clock.Clock) (queue.Queue, error) {
	switch strings.ToLower(cfg.Cache.Driver) {
	case "", "memory":
		return memqueue.NewWithClock(clk), nil
	case "redis":
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return redisqueue.New(ctx, redisqueue.Config{
			Addr:     cfg.Cache.Addr,
			Password: cfg.Cache.Password,
			DB:       cfg.Cache.DB,
			PoolSize: cfg.Cache.PoolSize,
		})
	default:
		return memqueue.NewWithClock(clk), nil
	}
}

// siteResolver adapts the site repository to auth.Resolver.
type siteResolver struct{ st store.Store }

// Lookup returns the signing secrets for an app id.
func (r siteResolver) Lookup(ctx context.Context, appID string) (secret, secretPrev string, enabled bool, err error) {
	site, gerr := r.st.Sites().GetByAppID(ctx, appID)
	if gerr != nil || site == nil {
		return "", "", false, nil
	}
	return site.Secret, site.SecretPrev, site.Enabled, nil
}

// healthChecker aggregates subsystem liveness for /healthz and /readyz.
type healthChecker struct {
	st store.Store
	ce cache.Cache
	q  queue.Queue
}

// Check reports component status plus overall readiness.
//
// Storage is required; cache and queue are reported but only degrade
// functionality, so a cache outage does not take the gateway out of rotation.
func (h healthChecker) Check(r *http.Request) (map[string]string, bool) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	components := map[string]string{}
	ready := true

	if err := h.st.Ping(ctx); err != nil {
		components["storage"] = "down"
		ready = false
	} else {
		components["storage"] = "ok"
	}
	if h.ce != nil {
		if err := h.ce.Ping(ctx); err != nil {
			components["cache"] = "degraded"
		} else {
			components["cache"] = "ok"
		}
	}
	if h.q != nil {
		if err := h.q.Ping(ctx); err != nil {
			components["queue"] = "degraded"
		} else {
			components["queue"] = "ok"
		}
	}
	return components, ready
}

// siteSecretSource describes where a site secret came from, without printing it.
func siteSecretSource(secret string) string {
	if secret != "" {
		return "generated"
	}
	return "configured"
}
