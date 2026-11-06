// Package mysqlstore is the MySQL 8 implementation of the store.Store contract.
//
// It is the production persistence driver; the in-process memstore is the
// reference implementation and this package mirrors its semantics exactly,
// most importantly the idempotency rules of docs/workstreams.md §5.1:
//
//   - Ledger.Credit is unique on the (ref_type, ref_id) pair (uk_ref). A replay
//     inserts nothing, reports applied=false and returns the current balance.
//   - Usage.Insert is unique on request_id (uk_request): the statement uses
//     ON DUPLICATE KEY UPDATE so RowsAffected distinguishes insert from replay.
//   - Subscription.Create is unique on v10_host_id (uk_host): a replay returns
//     the stored row with created=false.
//
// Layout: every raw SQL string lives in a small pure builder function
// (sqlbuild.go) returning (sql, args), so the exact statements can be asserted
// by unit tests without a database. The database/sql plumbing stays thin.
//
// requires MySQL 8; not integration-tested on the build machine.
//
// Concurrency: Safe for concurrent use. The *sql.DB pool is safe by
// construction, and the only multi-statement operation (Ledger.Credit) runs
// inside a single transaction with a SELECT ... FOR UPDATE row lock.
package mysqlstore

import (
	"context"
	"database/sql"
	"time"

	// The driver registers itself as "mysql".
	_ "github.com/go-sql-driver/mysql"

	"czapi/internal/pkg/clock"
	"czapi/internal/pkg/errs"
	"czapi/internal/store"
)

// Compile-time proof that the MySQL driver satisfies the frozen contract.
var _ store.Store = (*Store)(nil)

// pingTimeout bounds the startup connectivity check.
const pingTimeout = 5 * time.Second

// Config configures the MySQL connection.
type Config struct {
	// DSN is a go-sql-driver DSN, e.g.
	// "czapi:secret@tcp(127.0.0.1:3306)/czapi?charset=utf8mb4".
	// parseTime is forced on and the location is set to UTC.
	DSN string
	// MaxOpenConns caps the pool size (0 keeps the database/sql default).
	MaxOpenConns int
	// MaxIdleConns caps the idle pool (0 keeps the database/sql default).
	MaxIdleConns int
	// ConnMaxLifetime recycles connections (0 means no limit, which is not
	// recommended behind a load balancer or a proxy).
	ConnMaxLifetime time.Duration
}

// Store is the MySQL-backed store.Store implementation.
//
// Safe for concurrent use: it holds a *sql.DB pool and the repository values
// returned by the accessors are stateless wrappers around it, so they may be
// shared across goroutines and used from any number of handlers at once.
type Store struct {
	db *sql.DB
	// clk stamps timestamps for creates and updates.
	clk clock.Clock
	// ownsDB reports whether Close should close the *sql.DB. It is always true
	// for stores built with New; tests may wrap an externally managed pool.
	ownsDB bool
}

// New opens the pool, tunes it and verifies connectivity.
//
// The ping is bounded by a 5 second timeout (or the caller's earlier deadline).
// A failed ping closes the pool, so a returned error never leaks connections.
func New(ctx context.Context, cfg Config) (*Store, error) {
	return newStore(ctx, cfg, clock.System{}, nil)
}

// newStore is New with an injectable clock and an optional pre-built pool. The
// db parameter exists so that tests can supply a pool without a live server;
// production code goes through New.
func newStore(ctx context.Context, cfg Config, clk clock.Clock, db *sql.DB) (*Store, error) {
	if db == nil && cfg.DSN == "" {
		return nil, errs.Internal("mysqlstore: config.DSN is required", nil)
	}
	if clk == nil {
		clk = clock.System{}
	}
	owns := false
	if db == nil {
		dsn, err := normalizeDSN(cfg.DSN)
		if err != nil {
			return nil, err
		}
		opened, err := sql.Open("mysql", dsn)
		if err != nil {
			return nil, errs.Internal("mysqlstore: open", err)
		}
		db = opened
		owns = true
	}
	if cfg.MaxOpenConns > 0 {
		db.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		db.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}

	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		if owns {
			_ = db.Close()
		}
		return nil, errs.Internal("mysqlstore: ping", err)
	}
	return &Store{db: db, clk: clk, ownsDB: owns}, nil
}

// DB exposes the underlying pool for migration wiring and health checks. The
// caller must not close it.
func (s *Store) DB() *sql.DB { return s.db }

// now returns the current timestamp from the injected clock, in UTC so that
// DATETIME columns and the driver's parseTime agree.
//
// A nil clock falls back to the system clock rather than panicking, so a
// zero-valued Store built in a test is still usable.
func (s *Store) now() time.Time {
	if s.clk == nil {
		return time.Now().UTC()
	}
	return s.clk.Now().UTC()
}

// begin starts a transaction on the pool.
func (s *Store) begin(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, wrapDB("mysqlstore: begin transaction", err)
	}
	return tx, nil
}

// Sites returns the V10 installation repository.
func (s *Store) Sites() store.SiteRepo { return siteRepo{s} }

// Accounts returns the tenant repository.
func (s *Store) Accounts() store.AccountRepo { return accountRepo{s} }

// Plans returns the package repository.
func (s *Store) Plans() store.PlanRepo { return planRepo{s} }

// Subscriptions returns the V10 host projection repository.
func (s *Store) Subscriptions() store.SubscriptionRepo { return subscriptionRepo{s} }

// Keys returns the API key repository.
func (s *Store) Keys() store.APIKeyRepo { return apiKeyRepo{s} }

// Channels returns the upstream channel repository.
func (s *Store) Channels() store.ChannelRepo { return channelRepo{s} }

// Models returns the model catalogue repository.
func (s *Store) Models() store.ModelRepo { return modelRepo{s} }

// Usage returns the metering repository.
func (s *Store) Usage() store.UsageRepo { return usageRepo{s} }

// Ledger returns the balance book repository.
func (s *Store) Ledger() store.LedgerRepo { return ledgerRepo{s} }

// Admins returns the console operator repository.
func (s *Store) Admins() store.AdminRepo { return adminRepo{s} }

// Audit returns the administrative audit repository.
func (s *Store) Audit() store.AuditRepo { return auditRepo{s} }

// Flags returns the feature switch repository.
func (s *Store) Flags() store.FlagRepo { return flagRepo{s} }

// Aggregates returns the daily rollup repository.
func (s *Store) Aggregates() store.AggregateRepo { return aggregateRepo{s} }

// Ping verifies that the pool can reach the server.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return errs.Internal("mysqlstore: ping", err)
	}
	return nil
}

// Close releases the connection pool. It is safe to call more than once.
func (s *Store) Close() error {
	if s.db == nil || !s.ownsDB {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return errs.Internal("mysqlstore: close", err)
	}
	return nil
}

// accountBalance reads an account's balance without locking.
func (s *Store) accountBalance(ctx context.Context, accountID uint64) (int64, error) {
	var balance int64
	err := s.db.QueryRowContext(ctx, creditCurrentBalanceSQL(), accountID).Scan(&balance)
	switch {
	case err == sql.ErrNoRows:
		return 0, errs.NotFound("account not found")
	case err != nil:
		return 0, wrapDB("mysqlstore: read account balance", err)
	}
	return balance, nil
}

// scanRows runs query and scans every row with scan.
func scanRows[T any](ctx context.Context, db *sql.DB, query string, args []any, scan func(*sql.Rows) (T, error)) ([]T, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapDB("mysqlstore: query", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]T, 0, 16)
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, wrapDB("mysqlstore: scan", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("mysqlstore: iterate rows", err)
	}
	return out, nil
}

// countQuery runs a SELECT COUNT(*) style scalar query.
func countQuery(ctx context.Context, db *sql.DB, query string, args ...any) (int64, error) {
	var n int64
	if err := db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, wrapDB("mysqlstore: count", err)
	}
	return n, nil
}
