package migrate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"czapi/internal/pkg/clock"
)

// realMigrationPath is the schema shipped with the repository.
const realMigrationPath = "../../migrations/0001_init.sql"

// loadReal parses the real migration file, failing the test when it is absent.
func loadReal(t *testing.T) *Migration {
	t.Helper()
	raw, err := os.ReadFile(realMigrationPath)
	require.NoError(t, err, "read %s", realMigrationPath)
	mig, err := ParseMigration("0001_init.sql", raw)
	require.NoError(t, err)
	return mig
}

func TestParseRealMigrationMetadata(t *testing.T) {
	mig := loadReal(t)
	assert.Equal(t, int64(1), mig.Version)
	assert.Equal(t, "0001_init", mig.Name)
	assert.Equal(t, "0001_init.sql", mig.FileName)
	assert.True(t, mig.Reversible(), "0001_init.sql must carry a down section")
}

func TestParseRealMigrationUpSection(t *testing.T) {
	mig := loadReal(t)
	require.NotEmpty(t, mig.Up, "up section must contain statements")
	assert.Greater(t, len(mig.Up), 10, "up section should create every cz_* table")

	// Every up statement must be a CREATE TABLE, since that is all the file does.
	createRe := regexp.MustCompile(`^CREATE TABLE IF NOT EXISTS`)
	for i, stmt := range mig.Up {
		assert.Regexp(t, createRe, stmt, "up statement %d", i+1)
		assert.NotContains(t, stmt, "--", "comments must be stripped from statement %d", i+1)
		assert.NotContains(t, stmt, "0001_up", "marker must not leak into statement %d", i+1)
	}
}

func TestParseRealMigrationDownSection(t *testing.T) {
	mig := loadReal(t)
	require.NotEmpty(t, mig.Down)
	dropRe := regexp.MustCompile(`^DROP TABLE IF EXISTS cz_\w+$`)
	for i, stmt := range mig.Down {
		assert.Regexp(t, dropRe, stmt, "down statement %d", i+1)
		assert.NotContains(t, stmt, "0001_down", "marker must not leak into statement %d", i+1)
	}
}

// TestParseRealMigrationUpDownSymmetry verifies the reversibility invariant the
// plan section 14 requires: the down section drops exactly the tables the up
// section created, in reverse order.
func TestParseRealMigrationUpDownSymmetry(t *testing.T) {
	mig := loadReal(t)

	createRe := regexp.MustCompile(`(?i)CREATE TABLE IF NOT EXISTS (cz_\w+)`)
	dropRe := regexp.MustCompile(`(?i)DROP TABLE IF EXISTS (cz_\w+)`)

	upOrder := make([]string, 0, len(mig.Up))
	for _, stmt := range mig.Up {
		m := createRe.FindStringSubmatch(stmt)
		require.NotNil(t, m, "up statement is not a CREATE TABLE: %q", stmt)
		upOrder = append(upOrder, m[1])
	}
	downOrder := make([]string, 0, len(mig.Down))
	for _, stmt := range mig.Down {
		m := dropRe.FindStringSubmatch(stmt)
		require.NotNil(t, m, "down statement is not a DROP TABLE: %q", stmt)
		downOrder = append(downOrder, m[1])
	}

	assert.ElementsMatch(t, upOrder, downOrder, "created and dropped table sets must match")
	assert.Len(t, downOrder, len(upOrder))

	reversed := make([]string, len(upOrder))
	for i, name := range upOrder {
		reversed[len(upOrder)-1-i] = name
	}
	assert.Equal(t, reversed, downOrder, "down order must be the exact reverse of the up order")
}

func TestParseRealMigrationTableNames(t *testing.T) {
	mig := loadReal(t)
	createRe := regexp.MustCompile(`(?i)CREATE TABLE IF NOT EXISTS (cz_\w+)`)
	got := make(map[string]bool, len(mig.Up))
	for _, stmt := range mig.Up {
		if m := createRe.FindStringSubmatch(stmt); m != nil {
			got[m[1]] = true
		}
	}
	// Spot-check the tables the mysql driver depends on.
	for _, want := range []string{
		"cz_site", "cz_account", "cz_plan", "cz_subscription", "cz_api_key",
		"cz_channel", "cz_channel_health", "cz_model", "cz_usage_record",
		"cz_credit_ledger", "cz_admin_user", "cz_audit_log", "cz_flag",
		"cz_daily_usage", "cz_nonce",
	} {
		assert.True(t, got[want], "expected table %s in the up section", want)
	}
}

func TestParseMigrationVersionOrdering(t *testing.T) {
	cases := []struct {
		name string
		want int64
	}{
		{"0001_init.sql", 1},
		{"0002_add_index.sql", 2},
		{"0010_usage_partition.sql", 10},
		{"20261105001_big.sql", 20261105001},
	}
	for _, tc := range cases {
		mig, err := ParseMigration(tc.name, []byte("-- 1_up\nSELECT 1;\n"))
		require.NoError(t, err, tc.name)
		assert.Equal(t, tc.want, mig.Version, tc.name)
		assert.Equal(t, len(tc.name)-4, len(mig.Name), tc.name)
	}
}

func TestParseMigrationRejectsBadNames(t *testing.T) {
	for _, name := range []string{"init.sql", "0001-init.sql", "abc_init.sql", "0001_init.txt"} {
		_, err := ParseMigration(name, []byte("-- 0001_up\nSELECT 1;\n"))
		assert.Error(t, err, name)
	}
}

func TestParseMigrationRequiresUpMarker(t *testing.T) {
	_, err := ParseMigration("0002_x.sql", []byte("-- 0002_down\nDROP TABLE t;\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "0002_up")

	_, err = ParseMigration("0002_x.sql", []byte("SELECT 1;\n"))
	require.Error(t, err)
}

func TestParseMigrationEmptyUpSection(t *testing.T) {
	_, err := ParseMigration("0003_x.sql", []byte("-- 0003_up\n-- only comments\n\n-- 0003_down\nDROP TABLE t;\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no statements")
}

func TestParseMigrationWithoutDownSection(t *testing.T) {
	mig, err := ParseMigration("0004_x.sql", []byte("-- 0004_up\nCREATE TABLE t (id INT);\n"))
	require.NoError(t, err)
	assert.False(t, mig.Reversible())
	assert.Empty(t, mig.Down)
	assert.Equal(t, []string{"CREATE TABLE t (id INT)"}, mig.Up)
}

func TestParseMigrationIgnoresPreamble(t *testing.T) {
	src := "-- 0005_x.sql file header\n" +
		"-- nothing to see before the marker\n" +
		"CREATE TABLE ignored_before_marker (id INT);\n" +
		"\n" +
		"-- 0005_up\n" +
		"CREATE TABLE a (id INT);\n" +
		"\n" +
		"-- 0005_down\n" +
		"DROP TABLE a;\n"
	mig, err := ParseMigration("0005_x.sql", []byte(src))
	require.NoError(t, err)
	assert.Equal(t, []string{"CREATE TABLE a (id INT)"}, mig.Up)
	assert.Equal(t, []string{"DROP TABLE a"}, mig.Down)
}

func TestLoadMigrationsSortedAndFiltered(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	write("0002_second.sql", "-- 2_up\nSELECT 2;\n-- 2_down\nSELECT -2;\n")
	write("0001_first.sql", "-- 1_up\nSELECT 1;\n-- 1_down\nSELECT -1;\n")
	write("0010_tenth.sql", "-- 10_up\nSELECT 10;\n")
	write("notes.txt", "not a migration")
	write("readme.md", "not a migration")

	migs, err := LoadMigrations(dir)
	require.NoError(t, err)
	require.Len(t, migs, 3)
	assert.Equal(t, []int64{1, 2, 10}, []int64{migs[0].Version, migs[1].Version, migs[2].Version})
	assert.Equal(t, []string{"0001_first", "0002_second", "0010_tenth"},
		[]string{migs[0].Name, migs[1].Name, migs[2].Name})
}

func TestLoadMigrationsRejectsDuplicateVersion(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "0001_a.sql"), []byte("-- 1_up\nSELECT 1;\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "0001_b.sql"), []byte("-- 1_up\nSELECT 2;\n"), 0o600))

	_, err := LoadMigrations(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate version")
}

func TestLoadMigrationsMissingDir(t *testing.T) {
	_, err := LoadMigrations(filepath.Join(t.TempDir(), "nope"))
	require.Error(t, err)
}

func TestLoadMigrationsRealDirectory(t *testing.T) {
	migs, err := LoadMigrations("../../migrations")
	require.NoError(t, err)
	require.NotEmpty(t, migs, "the repository must ship at least one migration")
	assert.Equal(t, int64(1), migs[0].Version)
	// Versions must be strictly ascending.
	for i := 1; i < len(migs); i++ {
		assert.Greater(t, migs[i].Version, migs[i-1].Version)
	}
}

func TestSplitStatementsBasics(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"empty", "", []string{}},
		{"whitespace", "  \n\t\n ", []string{}},
		{"comments only", "-- nothing here\n/* block */\n", []string{}},
		{
			"two statements",
			"SELECT 1;\nSELECT 2;",
			[]string{"SELECT 1", "SELECT 2"},
		},
		{
			"trailing semicolon optional",
			"SELECT 1",
			[]string{"SELECT 1"},
		},
		{
			"empty statements dropped",
			";;;SELECT 1;;",
			[]string{"SELECT 1"},
		},
		{
			"line comment stripped",
			"SELECT 1 -- inline\n, 2;",
			[]string{"SELECT 1 \n, 2"},
		},
		{
			"block comment stripped",
			"SELECT 1 /* mid */ + 2;",
			// A removed block comment leaves one separator space in place.
			[]string{"SELECT 1   + 2"},
		},
		{
			"unterminated block comment",
			"SELECT 1; /* forever",
			[]string{"SELECT 1"},
		},
		{
			"semicolon inside single quotes",
			"INSERT INTO t VALUES ('a;b');",
			[]string{"INSERT INTO t VALUES ('a;b')"},
		},
		{
			"semicolon inside double quotes",
			`INSERT INTO t VALUES ("a;b");`,
			[]string{`INSERT INTO t VALUES ("a;b")`},
		},
		{
			"semicolon inside backticks",
			"SELECT `weird;col` FROM t;",
			[]string{"SELECT `weird;col` FROM t"},
		},
		{
			"escaped quote inside string",
			`INSERT INTO t VALUES ('it\'s;fine');`,
			[]string{`INSERT INTO t VALUES ('it\'s;fine')`},
		},
		{
			"doubled quote inside string",
			"INSERT INTO t VALUES ('it''s;fine');",
			[]string{"INSERT INTO t VALUES ('it''s;fine')"},
		},
		{
			"comment marker inside string is literal",
			"INSERT INTO t VALUES ('-- not a comment; really');",
			[]string{"INSERT INTO t VALUES ('-- not a comment; really')"},
		},
		{
			"comment marker inside quoted string",
			"SELECT '/* nope */';",
			[]string{"SELECT '/* nope */'"},
		},
		{
			"multiline statement",
			"CREATE TABLE t (\n  id INT,\n  name VARCHAR(10)\n);",
			[]string{"CREATE TABLE t (\n  id INT,\n  name VARCHAR(10)\n)"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, SplitStatements(tc.src))
		})
	}
}

func TestSplitStatementsPartitionedTable(t *testing.T) {
	// The real schema's cz_usage_record carries a PARTITION BY clause followed
	// by another statement; make sure the split stays clean across it.
	src := "CREATE TABLE a (id INT) PARTITION BY RANGE COLUMNS(id) (PARTITION p0 VALUES LESS THAN (10), PARTITION pmax VALUES LESS THAN (MAXVALUE));\nCREATE TABLE b (id INT);"
	got := SplitStatements(src)
	require.Len(t, got, 2)
	assert.Contains(t, got[0], "PARTITION pmax")
	assert.Equal(t, "CREATE TABLE b (id INT)", got[1])
}

func TestNewDefaults(t *testing.T) {
	m := New(nil, "")
	assert.Equal(t, DefaultTable, m.Table())
	assert.Equal(t, "migrations", m.Dir())

	m = New(nil, "db/migrations")
	assert.Equal(t, "db/migrations", m.Dir())
}

func TestWithClockPinsAppliedAt(t *testing.T) {
	frozen := time.Date(2026, 11, 5, 7, 0, 0, 0, time.UTC)
	m := New(nil, "").WithClock(clock.NewFake(frozen))

	assert.True(t, frozen.Equal(m.now()), "the injected clock drives applied_at")
	assert.Equal(t, time.UTC, m.now().Location())

	// WithClock copies, so the original keeps the system clock.
	base := New(nil, "")
	assert.NotEqual(t, frozen, base.now())
}

func TestNowFallsBackToSystemClock(t *testing.T) {
	m := &Migrator{}
	got := m.now()
	assert.WithinDuration(t, time.Now().UTC(), got, time.Minute)
	assert.Equal(t, time.UTC, got.Location())
}

func TestEnsureTableNilDB(t *testing.T) {
	m := New(nil, "../../migrations")
	err := m.EnsureTable(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil database handle")
}

func TestCreateTableSQL(t *testing.T) {
	m := New(nil, "")
	sql := m.createTableSQL()
	assert.Contains(t, sql, "CREATE TABLE IF NOT EXISTS schema_migrations")
	assert.Contains(t, sql, "PRIMARY KEY (version)")
	assert.Contains(t, sql, "applied_at")
}

func TestFirstLine(t *testing.T) {
	assert.Equal(t, "", firstLine(""))
	assert.Equal(t, "", firstLine("\n\n  \n"))
	assert.Equal(t, "SELECT 1", firstLine("\n  SELECT 1\nFROM t"))
	long := firstLine(strings.Repeat("x", 200))
	assert.Len(t, long, 83)
	assert.True(t, strings.HasSuffix(long, "..."))
}

func TestDownNonPositiveStepsIsNoop(t *testing.T) {
	m := New(nil, "../../migrations")
	got, err := m.Down(t.Context(), 0)
	require.NoError(t, err)
	assert.Nil(t, got)

	got, err = m.Down(t.Context(), -3)
	require.NoError(t, err)
	assert.Nil(t, got)
}
