// Package migrate is a small, dependency-free, rollback-capable schema
// migrator for the CZAPI gateway.
//
// Migration files live in a directory (default "migrations") and are named
// <version>_<name>.sql. Each file carries two sections delimited by marker
// comment lines:
//
//	-- 0001_up
//	CREATE TABLE IF NOT EXISTS cz_site (...);
//
//	-- 0001_down
//	DROP TABLE IF EXISTS cz_site;
//
// Applied versions are recorded in the schema_migrations table, so Up is
// idempotent and safe to run on every boot (plan §14: auto-migrate, and every
// migration must be reversible).
//
// requires MySQL 8; not integration-tested on the build machine.
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"czapi/internal/pkg/clock"
	"czapi/internal/pkg/errs"
)

// DefaultTable is the bookkeeping table used when none is configured.
const DefaultTable = "schema_migrations"

// Migrator applies and reverts migration files against a *sql.DB.
//
// Safe for concurrent use only in the sense that each method is a short,
// self-contained sequence; running two migrators against the same database
// concurrently is not supported (MySQL DDL is not transactional).
type Migrator struct {
	db    *sql.DB
	dir   string
	table string
	// clk stamps applied_at; tests inject a fake so migrations are deterministic.
	clk clock.Clock
}

// New returns a Migrator reading migration files from dir and recording
// versions in schema_migrations.
//
// The database handle is not touched until one of the methods is called, so a
// nil or unreachable handle is detected at use time rather than at
// construction.
func New(db *sql.DB, dir string) *Migrator {
	if dir == "" {
		dir = "migrations"
	}
	return &Migrator{
		db:    db,
		dir:   dir,
		table: DefaultTable,
		clk:   clock.System{},
	}
}

// WithClock returns a copy of m that stamps applied_at from clk. It is the test
// seam for deterministic migration timestamps.
func (m *Migrator) WithClock(clk clock.Clock) *Migrator {
	out := *m
	out.clk = clk
	return &out
}

// now returns the current time from the injected clock, always in UTC.
func (m *Migrator) now() time.Time {
	if m.clk == nil {
		return time.Now().UTC()
	}
	return m.clk.Now().UTC()
}

// Table returns the bookkeeping table name.
func (m *Migrator) Table() string { return m.table }

// Dir returns the directory migrations are read from.
func (m *Migrator) Dir() string { return m.dir }

// createTableSQL returns the DDL for the bookkeeping table.
func (m *Migrator) createTableSQL() string {
	return "CREATE TABLE IF NOT EXISTS " + m.table + ` (
  version    BIGINT NOT NULL,
  name       VARCHAR(255) NOT NULL,
  applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`
}

// EnsureTable creates the bookkeeping table when it is missing.
func (m *Migrator) EnsureTable(ctx context.Context) error {
	if m.db == nil {
		return errs.Internal("migrate: nil database handle", nil)
	}
	if _, err := m.db.ExecContext(ctx, m.createTableSQL()); err != nil {
		return errs.Internal("migrate: ensure "+m.table, err)
	}
	return nil
}

// appliedVersions returns the set of versions already recorded.
func (m *Migrator) appliedVersions(ctx context.Context) (map[int64]string, error) {
	rows, err := m.db.QueryContext(ctx, "SELECT version, name FROM "+m.table+" ORDER BY version")
	if err != nil {
		return nil, errs.Internal("migrate: read "+m.table, err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[int64]string, 16)
	for rows.Next() {
		var (
			version int64
			name    string
		)
		if err := rows.Scan(&version, &name); err != nil {
			return nil, errs.Internal("migrate: scan "+m.table, err)
		}
		out[version] = name
	}
	if err := rows.Err(); err != nil {
		return nil, errs.Internal("migrate: iterate "+m.table, err)
	}
	return out, nil
}

// Up applies every pending migration in version order and returns the names of
// the applied files.
//
// Each migration runs statement by statement; MySQL commits DDL implicitly, so
// a failure mid-file leaves earlier statements of that file applied. The
// version row is only written after the whole file succeeded, which makes a
// re-run retry the remaining statements (all statements in this repository are
// written to be idempotent: CREATE TABLE IF NOT EXISTS / DROP TABLE IF EXISTS).
func (m *Migrator) Up(ctx context.Context) (applied []string, err error) {
	if err := m.EnsureTable(ctx); err != nil {
		return nil, err
	}
	done, err := m.appliedVersions(ctx)
	if err != nil {
		return nil, err
	}
	migs, err := LoadMigrations(m.dir)
	if err != nil {
		return nil, err
	}
	for _, mig := range migs {
		if _, ok := done[mig.Version]; ok {
			continue
		}
		if err := m.runSection(ctx, mig, mig.Up); err != nil {
			return applied, err
		}
		insert := "INSERT INTO " + m.table + " (version, name, applied_at) VALUES (?, ?, ?)"
		if _, err := m.db.ExecContext(ctx, insert, mig.Version, mig.Name, m.now()); err != nil {
			return applied, errs.Internal("migrate: record "+mig.FileName, err)
		}
		applied = append(applied, mig.Name)
	}
	return applied, nil
}

// Down reverts the steps most recently applied migrations and returns the names
// of the reverted files. A steps value <= 0 reverts nothing.
func (m *Migrator) Down(ctx context.Context, steps int) (reverted []string, err error) {
	if steps <= 0 {
		return nil, nil
	}
	if err := m.EnsureTable(ctx); err != nil {
		return nil, err
	}
	done, err := m.appliedVersions(ctx)
	if err != nil {
		return nil, err
	}
	migs, err := LoadMigrations(m.dir)
	if err != nil {
		return nil, err
	}
	byVersion := make(map[int64]*Migration, len(migs))
	for _, mig := range migs {
		byVersion[mig.Version] = mig
	}

	versions := make([]int64, 0, len(done))
	for v := range done {
		versions = append(versions, v)
	}
	// Most recent first.
	sort.Slice(versions, func(i, j int) bool { return versions[i] > versions[j] })
	if steps < len(versions) {
		versions = versions[:steps]
	}

	for _, v := range versions {
		mig, ok := byVersion[v]
		if !ok {
			return reverted, errs.Conflict("migrate: applied version " + strconv.FormatInt(v, 10) + " has no matching file")
		}
		if !mig.Reversible() {
			return reverted, errs.Conflict("migrate: " + mig.FileName + " is not reversible (no down section)")
		}
		if err := m.runSection(ctx, mig, mig.Down); err != nil {
			return reverted, err
		}
		del := "DELETE FROM " + m.table + " WHERE version = ?"
		if _, err := m.db.ExecContext(ctx, del, v); err != nil {
			return reverted, errs.Internal("migrate: delete version "+strconv.FormatInt(v, 10), err)
		}
		reverted = append(reverted, mig.Name)
	}
	return reverted, nil
}

// Status reports which migrations are still pending and which are applied.
// Both lists are ordered by version ascending.
func (m *Migrator) Status(ctx context.Context) (pending, done []string, err error) {
	if err := m.EnsureTable(ctx); err != nil {
		return nil, nil, err
	}
	applied, err := m.appliedVersions(ctx)
	if err != nil {
		return nil, nil, err
	}
	migs, err := LoadMigrations(m.dir)
	if err != nil {
		return nil, nil, err
	}
	for _, mig := range migs {
		if _, ok := applied[mig.Version]; ok {
			done = append(done, mig.Name)
			continue
		}
		pending = append(pending, mig.Name)
	}
	return pending, done, nil
}

// runSection executes every statement of one section in order.
func (m *Migrator) runSection(ctx context.Context, mig *Migration, stmts []string) error {
	for i, stmt := range stmts {
		if _, err := m.db.ExecContext(ctx, stmt); err != nil {
			return errs.Internal(
				fmt.Sprintf("migrate: %s statement %d: %s", mig.FileName, i+1, firstLine(stmt)), err)
		}
	}
	return nil
}

// firstLine returns the first non-empty line of s, truncated for error text.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 80 {
			line = line[:80] + "..."
		}
		return line
	}
	return ""
}
