// Package config loads CZAPI gateway configuration from YAML with env overrides.
//
// Design rule (availability first): every setting has a safe default so that
// `go run ./cmd/server` with an empty config file boots successfully.
// Drivers default to "memory" so the binary runs with zero external services;
// production configs switch them to "mysql" and "redis".
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root configuration document.
type Config struct {
	Server    Server    `yaml:"server"`
	Storage   Storage   `yaml:"storage"`
	Cache     Cache     `yaml:"cache"`
	Auth      Auth      `yaml:"auth"`
	Billing   Billing   `yaml:"billing"`
	Relay     Relay     `yaml:"relay"`
	Limits    Limits    `yaml:"limits"`
	Flags     Flags     `yaml:"flags"`
	Log       Log       `yaml:"log"`
	Metrics   Metrics   `yaml:"metrics"`
	Worker    Worker    `yaml:"worker"`
	Console   Console   `yaml:"console"`
	Internal  Internal  `yaml:"internal"`
	Bootstrap Bootstrap `yaml:"bootstrap"`
}

// Server holds HTTP listener settings.
type Server struct {
	Addr            string        `yaml:"addr"`
	ReadHeaderTO    time.Duration `yaml:"read_header_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
	BasePath        string        `yaml:"base_path"`
}

// Storage selects and configures the persistence driver.
type Storage struct {
	Driver string `yaml:"driver"` // memory | mysql
	DSN    string `yaml:"dsn"`
	// MaxOpenConns / MaxIdleConns tune the MySQL pool.
	MaxOpenConns int           `yaml:"max_open_conns"`
	MaxIdleConns int           `yaml:"max_idle_conns"`
	ConnMaxLife  time.Duration `yaml:"conn_max_lifetime"`
	// AutoMigrate runs pending migrations on boot (production-safe: idempotent).
	AutoMigrate bool `yaml:"auto_migrate"`
}

// Cache selects and configures the cache driver (quota, nonce, ratelimit, key cache).
type Cache struct {
	Driver   string `yaml:"driver"` // memory | redis
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
	PoolSize int    `yaml:"pool_size"`
}

// Auth holds key, JWT and internal HMAC settings.
type Auth struct {
	// KeyHashSalt is prepended to API keys before SHA-256 hashing.
	KeyHashSalt string `yaml:"key_hash_salt"`
	// KeyCacheTTL is how long a validated key is cached in the cache driver.
	KeyCacheTTL time.Duration `yaml:"key_cache_ttl"`

	AdminJWTSecret string        `yaml:"admin_jwt_secret"`
	AdminJWTIssuer string        `yaml:"admin_jwt_issuer"`
	AdminJWTTTL    time.Duration `yaml:"admin_jwt_ttl"`

	// InternalClockSkew is the allowed |now-timestamp| for signed internal calls.
	InternalClockSkew time.Duration `yaml:"internal_clock_skew"`
	// InternalNonceTTL is how long a nonce is remembered (replay protection).
	InternalNonceTTL time.Duration `yaml:"internal_nonce_ttl"`
	// InternalAllowNoApp skips signature validation entirely (LOCAL DEV ONLY).
	InternalAllowNoApp bool `yaml:"internal_allow_no_app"`
}

// Billing holds the ratio engine and accounting settings.
type Billing struct {
	// TokenToMicros converts one "standard ratio token" into micro-yuan.
	// 1 yuan = 1_000_000 micros. Default 1 => 1 standard token costs 1 micro-yuan
	// (i.e. 1M standard tokens per yuan).
	TokenToMicros int64 `yaml:"token_to_micros"`
	// DefaultCompletionTokens is the assumed max_tokens when the request omits it.
	DefaultCompletionTokens int `yaml:"default_completion_tokens"`
	// EnforceBilling=false means meter and record but never deduct (shadow mode).
	EnforceBilling bool `yaml:"enforce_billing"`
	// ShadowMode records a shadow usage row in addition to the real one.
	ShadowMode bool `yaml:"shadow_mode"`
	// WorkerBacklogAlert is the queue depth that triggers the alert gauge.
	WorkerBacklogAlert int64 `yaml:"worker_backlog_alert"`
}

// Relay holds upstream forwarding settings.
type Relay struct {
	ConnectTimeout      time.Duration `yaml:"connect_timeout"`
	FirstByteTimeout    time.Duration `yaml:"first_byte_timeout"`
	TotalTimeout        time.Duration `yaml:"total_timeout"`
	StreamChunkTimeout  time.Duration `yaml:"stream_chunk_timeout"`
	MaxRetries          int           `yaml:"max_retries"`
	MaxIdleConnsPerHost int           `yaml:"max_idle_conns_per_host"`
	// Breaker settings.
	BreakerWindow        time.Duration `yaml:"breaker_window"`
	BreakerErrorRate     float64       `yaml:"breaker_error_rate"`
	BreakerMinSamples    int           `yaml:"breaker_min_samples"`
	BreakerOpenDuration  time.Duration `yaml:"breaker_open_duration"`
	RetryOn4xxRateLimit  bool          `yaml:"retry_on_429"`
	EstimateWhenNoUsage  bool          `yaml:"estimate_when_no_usage"`
	SettleOnPartialByPre bool          `yaml:"settle_partial_by_prehold"`
}

// Limits holds default rate limits applied when a plan does not override them.
type Limits struct {
	RPM         int `yaml:"rpm"`
	TPM         int `yaml:"tpm"`
	Concurrency int `yaml:"concurrency"`
	// Per-key caps (the "ceiling" insurance, plan §15.2 #2).
	KeyDailyMicros       int64 `yaml:"key_daily_micros"`
	AccountMonthlyMicros int64 `yaml:"account_monthly_micros"`
	GlobalDailyMicros    int64 `yaml:"global_daily_micros"`
}

// Flags are hot-reloadable feature switches (plan §15.2 #1).
type Flags struct {
	GatewayEnabled          bool `yaml:"gateway_enabled"`
	BillingEnforced         bool `yaml:"billing_enforced"`
	ModuleAutocreateEnabled bool `yaml:"module_autocreate_enabled"`
	RechargeEnabled         bool `yaml:"recharge_enabled"`
	// StreamUsageInjection appends a usage chunk to streams lacking one.
	StreamUsageInjection bool `yaml:"stream_usage_injection"`
}

// Log configures structured logging.
type Log struct {
	Level  string `yaml:"level"`  // debug|info|warn|error
	Format string `yaml:"format"` // json|text
}

// Metrics configures Prometheus exposition.
type Metrics struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}

// Worker configures the async accounting worker.
type Worker struct {
	Enabled      bool          `yaml:"enabled"`
	PollInterval time.Duration `yaml:"poll_interval"`
	BatchSize    int           `yaml:"batch_size"`
	// SyncFallback writes usage synchronously when the queue is unavailable.
	SyncFallback bool `yaml:"sync_fallback"`
}

// Console configures static asset serving for the Vue3 SPA.
type Console struct {
	Enabled bool `yaml:"enabled"`
	// Dir optionally overrides the embedded assets with a directory on disk.
	// Leave it empty (the default) to serve the console compiled into the binary,
	// which is what makes a deployment self-contained. Set it only while working
	// on the front end, where a rebuild of web/ should be visible on refresh
	// without recompiling Go.
	Dir string `yaml:"dir"`
}

// Internal holds V10 site registry bootstrapping.
type Internal struct {
	// Sites are statically configured app_id/secret pairs. Additional sites can
	// be managed at runtime through the admin API.
	Sites []Site `yaml:"sites"`
}

// Site is one V10 installation allowed to call /internal/v1.
type Site struct {
	AppID      string   `yaml:"app_id"`
	Secret     string   `yaml:"secret"`
	SecretPrev string   `yaml:"secret_prev"`
	Name       string   `yaml:"name"`
	IPAllow    []string `yaml:"ip_allow"`
	Enabled    bool     `yaml:"enabled"`
}

// Default returns a fully-populated config that boots without external services.
func Default() *Config {
	return &Config{
		Server: Server{
			Addr:            ":9999",
			ReadHeaderTO:    10 * time.Second,
			ShutdownTimeout: 20 * time.Second,
		},
		Storage: Storage{
			Driver:       "memory",
			MaxOpenConns: 50,
			MaxIdleConns: 10,
			ConnMaxLife:  time.Hour,
			AutoMigrate:  false,
		},
		Cache: Cache{
			Driver:   "memory",
			Addr:     "127.0.0.1:6379",
			PoolSize: 20,
		},
		Auth: Auth{
			KeyHashSalt:        "czapi-dev-salt-change-me",
			KeyCacheTTL:        60 * time.Second,
			AdminJWTIssuer:     "czapi",
			AdminJWTTTL:        12 * time.Hour,
			InternalClockSkew:  300 * time.Second,
			InternalNonceTTL:   300 * time.Second,
			InternalAllowNoApp: false,
		},
		Billing: Billing{
			TokenToMicros:           1,
			DefaultCompletionTokens: 512,
			EnforceBilling:          true,
			ShadowMode:              false,
			WorkerBacklogAlert:      10000,
		},
		Relay: Relay{
			ConnectTimeout:       10 * time.Second,
			FirstByteTimeout:     60 * time.Second,
			TotalTimeout:         300 * time.Second,
			StreamChunkTimeout:   120 * time.Second,
			MaxRetries:           2,
			MaxIdleConnsPerHost:  200,
			BreakerWindow:        5 * time.Minute,
			BreakerErrorRate:     0.5,
			BreakerMinSamples:    20,
			BreakerOpenDuration:  60 * time.Second,
			RetryOn4xxRateLimit:  true,
			EstimateWhenNoUsage:  true,
			SettleOnPartialByPre: true,
		},
		Limits: Limits{
			RPM:                  60,
			TPM:                  100000,
			Concurrency:          5,
			KeyDailyMicros:       50_000_000,  // 50 yuan/day per key
			AccountMonthlyMicros: 500_000_000, // 500 yuan/month per account
			GlobalDailyMicros:    0,           // 0 = disabled
		},
		Flags: Flags{
			GatewayEnabled:          true,
			BillingEnforced:         true,
			ModuleAutocreateEnabled: true,
			RechargeEnabled:         true,
			StreamUsageInjection:    true,
		},
		Log:     Log{Level: "info", Format: "json"},
		Metrics: Metrics{Enabled: true, Path: "/metrics"},
		Worker: Worker{
			Enabled:      true,
			PollInterval: time.Second,
			BatchSize:    100,
			SyncFallback: true,
		},
		Console: Console{Enabled: true, Dir: ""},
		Bootstrap: Bootstrap{
			AdminUsername: "admin",
			AdminPassword: "admin",
		},
	}
}

// Load reads path (YAML), applies env overrides and validates the result.
// A missing file is not an error: defaults are used.
func Load(path string) (*Config, error) {
	cfg := Default()
	if path != "" {
		raw, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := yaml.Unmarshal(raw, cfg); err != nil {
				return nil, fmt.Errorf("config: parse %s: %w", path, err)
			}
		case os.IsNotExist(err):
			// keep defaults
		default:
			return nil, fmt.Errorf("config: read %s: %w", path, err)
		}
	}
	applyEnv(cfg)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyEnv lets every important knob be overridden by environment variables,
// which is how Docker/K8s deployments inject secrets.
func applyEnv(c *Config) {
	str := func(key string, dst *string) {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			*dst = v
		}
	}
	boolean := func(key string, dst *bool) {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			if b, err := strconv.ParseBool(v); err == nil {
				*dst = b
			}
		}
	}
	integer := func(key string, dst *int) {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				*dst = n
			}
		}
	}
	integer64 := func(key string, dst *int64) {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				*dst = n
			}
		}
	}
	duration := func(key string, dst *time.Duration) {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				*dst = d
			}
		}
	}

	str("CZ_SERVER_ADDR", &c.Server.Addr)
	str("CZ_STORAGE_DRIVER", &c.Storage.Driver)
	str("CZ_STORAGE_DSN", &c.Storage.DSN)
	boolean("CZ_STORAGE_AUTOMIGRATE", &c.Storage.AutoMigrate)
	str("CZ_CACHE_DRIVER", &c.Cache.Driver)
	str("CZ_CACHE_ADDR", &c.Cache.Addr)
	str("CZ_CACHE_PASSWORD", &c.Cache.Password)
	str("CZ_KEY_HASH_SALT", &c.Auth.KeyHashSalt)
	str("CZ_ADMIN_JWT_SECRET", &c.Auth.AdminJWTSecret)
	duration("CZ_KEY_CACHE_TTL", &c.Auth.KeyCacheTTL)
	duration("CZ_INTERNAL_CLOCK_SKEW", &c.Auth.InternalClockSkew)
	boolean("CZ_INTERNAL_ALLOW_NO_APP", &c.Auth.InternalAllowNoApp)
	integer64("CZ_TOKEN_TO_MICROS", &c.Billing.TokenToMicros)
	boolean("CZ_ENFORCE_BILLING", &c.Billing.EnforceBilling)
	boolean("CZ_SHADOW_MODE", &c.Billing.ShadowMode)
	boolean("CZ_GATEWAY_ENABLED", &c.Flags.GatewayEnabled)
	boolean("CZ_BILLING_ENFORCED_FLAG", &c.Flags.BillingEnforced)
	boolean("CZ_MODULE_AUTOCREATE_ENABLED", &c.Flags.ModuleAutocreateEnabled)
	boolean("CZ_RECHARGE_ENABLED", &c.Flags.RechargeEnabled)
	integer("CZ_RPM", &c.Limits.RPM)
	integer("CZ_TPM", &c.Limits.TPM)
	integer("CZ_CONCURRENCY", &c.Limits.Concurrency)
	integer64("CZ_KEY_DAILY_MICROS", &c.Limits.KeyDailyMicros)
	integer64("CZ_ACCOUNT_MONTHLY_MICROS", &c.Limits.AccountMonthlyMicros)
	integer64("CZ_GLOBAL_DAILY_MICROS", &c.Limits.GlobalDailyMicros)
	str("CZ_LOG_LEVEL", &c.Log.Level)
	str("CZ_LOG_FORMAT", &c.Log.Format)

	// Composite site definition: CZ_INTERNAL_SITES="appid:secret,appid2:secret2"
	if v, ok := os.LookupEnv("CZ_INTERNAL_SITES"); ok && v != "" {
		var sites []Site
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			kv := strings.SplitN(part, ":", 2)
			if len(kv) != 2 {
				continue
			}
			sites = append(sites, Site{AppID: kv[0], Secret: kv[1], Name: kv[0], Enabled: true})
		}
		if len(sites) > 0 {
			c.Internal.Sites = sites
		}
	}
}

// Validate rejects configurations that cannot work in the selected mode.
func (c *Config) Validate() error {
	switch c.Storage.Driver {
	case "memory":
	case "mysql":
		if c.Storage.DSN == "" {
			return fmt.Errorf("config: storage.driver=mysql requires storage.dsn (or CZ_STORAGE_DSN)")
		}
	default:
		return fmt.Errorf("config: unknown storage.driver %q (want memory|mysql)", c.Storage.Driver)
	}
	switch c.Cache.Driver {
	case "memory":
	case "redis":
		if c.Cache.Addr == "" {
			return fmt.Errorf("config: cache.driver=redis requires cache.addr")
		}
	default:
		return fmt.Errorf("config: unknown cache.driver %q (want memory|redis)", c.Cache.Driver)
	}
	if c.Billing.TokenToMicros <= 0 {
		return fmt.Errorf("config: billing.token_to_micros must be > 0")
	}
	if c.Relay.MaxRetries < 0 || c.Relay.MaxRetries > 5 {
		return fmt.Errorf("config: relay.max_retries must be within 0..5")
	}
	return nil
}

// IsMemory reports whether the gateway runs without external services.
func (c *Config) IsMemory() bool {
	return c.Storage.Driver == "memory" && c.Cache.Driver == "memory"
}

// Bootstrap describes the resources created on first start when they are absent.
//
// It exists so a fresh install is usable immediately: without a seeded admin there
// is no way to log into the console, and without a seeded site V10 cannot call
// /internal/v1 at all. Both are idempotent: a resource that already exists is
// never modified, so restarting the gateway cannot reset a password.
//
// The default credentials are admin/admin. That is intentional for a first run,
// but it must be changed before the console is reachable from an untrusted
// network -- see docs/runbook.md.
type Bootstrap struct {
	// AdminUsername and AdminPassword seed the first administrator. Set
	// AdminPassword to "" to skip admin creation entirely (e.g. when the admin
	// already exists in MySQL and you do not want a default account created).
	AdminUsername string `yaml:"admin_username"`
	AdminPassword string `yaml:"admin_password"`
	// DefaultSiteAppID and DefaultSiteSecret seed one V10 site when internal.sites
	// is empty. Leave the secret empty to have a random one generated; it is
	// printed once at startup.
	DefaultSiteAppID  string `yaml:"default_site_app_id"`
	DefaultSiteSecret string `yaml:"default_site_secret"`
	// DemoContent seeds a sample channel, model and plan so the console and the
	// /v1 API are demonstrable without manual setup. Off by default in
	// production: a demo channel has no real upstream key.
	DemoContent bool `yaml:"demo_content"`
}
