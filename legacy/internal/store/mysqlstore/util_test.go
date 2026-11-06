package mysqlstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"czapi/internal/pkg/clock"
	"czapi/internal/pkg/errs"
)

// These tests exercise the pure helpers of util.go: DSN normalisation, error
// mapping and the JSON column codec. None of them needs a database.

// ---------------------------------------------------------------------------
// normalizeDSN
// ---------------------------------------------------------------------------

func TestNormalizeDSNForcesRequiredSettings(t *testing.T) {
	got, err := normalizeDSN("user:pass@tcp(127.0.0.1:3306)/czapi")
	require.NoError(t, err)

	cfg, err := mysql.ParseDSN(got)
	require.NoError(t, err)

	assert.True(t, cfg.ParseTime, "DATETIME columns need parseTime to scan into time.Time")
	assert.Equal(t, time.UTC, cfg.Loc, "an explicit UTC location prevents silent timezone shifts")
	assert.Contains(t, got, "charset=utf8mb4", "the JSON columns require utf8mb4")
	assert.Equal(t, "czapi", cfg.DBName)
	assert.Equal(t, "user", cfg.User)
}

func TestNormalizeDSNIsIdempotent(t *testing.T) {
	once, err := normalizeDSN("user:pass@tcp(127.0.0.1:3306)/czapi")
	require.NoError(t, err)
	twice, err := normalizeDSN(once)
	require.NoError(t, err)
	assert.Equal(t, once, twice, "normalising a normalised DSN must not change it")
}

func TestNormalizeDSNKeepsAnExplicitCharset(t *testing.T) {
	got, err := normalizeDSN("user:pass@tcp(127.0.0.1:3306)/czapi?charset=utf8")
	require.NoError(t, err)
	assert.Contains(t, got, "charset=utf8", "an explicit charset choice is preserved")
	assert.NotContains(t, got, "charset=utf8mb4", "the default must not override the operator")
}

func TestNormalizeDSNOverridesParseTimeAndLoc(t *testing.T) {
	got, err := normalizeDSN("user:pass@tcp(127.0.0.1:3306)/czapi?parseTime=false&loc=Local")
	require.NoError(t, err)
	cfg, err := mysql.ParseDSN(got)
	require.NoError(t, err)
	assert.True(t, cfg.ParseTime, "parseTime is required regardless of what the caller wrote")
	assert.Equal(t, time.UTC, cfg.Loc, "the location is forced to UTC")
}

func TestNormalizeDSNRejectsEmpty(t *testing.T) {
	got, err := normalizeDSN("")
	assert.Empty(t, got)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DSN")
}

func TestNormalizeDSNRejectsMalformed(t *testing.T) {
	_, err := normalizeDSN("this is not a dsn")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid DSN")
}

// ---------------------------------------------------------------------------
// wrapDB
// ---------------------------------------------------------------------------

func TestWrapDBNilStaysNil(t *testing.T) {
	assert.NoError(t, wrapDB("op", nil))
}

func TestWrapDBMapsNoRowsToNotFound(t *testing.T) {
	err := wrapDB("mysqlstore: get thing", sql.ErrNoRows)
	require.Error(t, err)

	got := errs.As(err)
	require.NotNil(t, got, "the shared error vocabulary must recognise the result")
	assert.Equal(t, errs.CodeNotFound, got.Code)
}

func TestWrapDBMapsDuplicateKeyToConflict(t *testing.T) {
	err := wrapDB("mysqlstore: insert", &mysql.MySQLError{Number: errNumDuplicateEntry})
	require.Error(t, err)

	got := errs.As(err)
	require.NotNil(t, got)
	assert.Equal(t, errs.CodeConflict, got.Code,
		"a unique-key violation is a conflict, which the API layer reports as 409")
}

func TestWrapDBMapsMissingParentToNotFound(t *testing.T) {
	err := wrapDB("mysqlstore: insert", &mysql.MySQLError{Number: errNumNoReferencedRow})
	require.Error(t, err)

	got := errs.As(err)
	require.NotNil(t, got)
	assert.Equal(t, errs.CodeNotFound, got.Code,
		"a broken foreign key means the referenced row is gone")
}

func TestWrapDBMapsEverythingElseToInternal(t *testing.T) {
	cause := errors.New("connection reset")
	err := wrapDB("mysqlstore: query", cause)
	require.Error(t, err)

	got := errs.As(err)
	require.NotNil(t, got)
	assert.Equal(t, errs.CodeInternal, got.Code)
	assert.Contains(t, err.Error(), "connection reset", "the cause is kept for the logs")
}

func TestWrapDBPreservesAnUnknownMySQLErrorNumber(t *testing.T) {
	err := wrapDB("mysqlstore: query", &mysql.MySQLError{Number: 1213, Message: "deadlock"})
	require.Error(t, err)

	got := errs.As(err)
	require.NotNil(t, got)
	assert.Equal(t, errs.CodeInternal, got.Code, "an unmapped errno is an internal failure")
}

// ---------------------------------------------------------------------------
// isDuplicateKey
// ---------------------------------------------------------------------------

func TestIsDuplicateKey(t *testing.T) {
	assert.True(t, isDuplicateKey(&mysql.MySQLError{Number: errNumDuplicateEntry}))
	assert.False(t, isDuplicateKey(&mysql.MySQLError{Number: errNumNoReferencedRow}))
	assert.False(t, isDuplicateKey(sql.ErrNoRows))
	assert.False(t, isDuplicateKey(errors.New("duplicate entry")))
	assert.False(t, isDuplicateKey(nil))

	// A wrapped driver error must still be recognised, because database/sql may
	// return it inside its own wrapper.
	wrapped := errors.Join(errors.New("context"), &mysql.MySQLError{Number: errNumDuplicateEntry})
	assert.True(t, isDuplicateKey(wrapped))
}

// ---------------------------------------------------------------------------
// Nullable column helpers
// ---------------------------------------------------------------------------

func TestNullString(t *testing.T) {
	assert.Nil(t, nullString(""), "an empty string is stored as SQL NULL")
	assert.Equal(t, "value", nullString("value"))
}

func TestNilIfZero(t *testing.T) {
	assert.Nil(t, nilIfZero(0), "a zero foreign key must bind as SQL NULL")
	assert.Equal(t, uint64(7), nilIfZero(7))
}

func TestTimeRoundTrip(t *testing.T) {
	at := time.Date(2026, 11, 5, 7, 0, 0, 0, time.UTC)

	assert.Nil(t, toNullTime(nil), "a nil timestamp binds as NULL")

	bound := toNullTime(&at)
	require.NotNil(t, bound)
	assert.Equal(t, at, bound.(time.Time))

	var nullTime sql.NullTime
	assert.Nil(t, fromNullTime(nullTime), "an invalid DATETIME reads back as nil")

	nullTime = sql.NullTime{Time: at, Valid: true}
	got := fromNullTime(nullTime)
	require.NotNil(t, got)
	assert.True(t, at.Equal(*got))
	assert.Equal(t, time.UTC, got.Location(), "timestamps are always normalised to UTC")
}

func TestToNullTimeNormalisesToUTC(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("timezone database unavailable: %v", err)
	}
	local := time.Date(2026, 11, 5, 15, 0, 0, 0, shanghai)
	bound := toNullTime(&local).(time.Time)
	assert.Equal(t, time.UTC, bound.Location())
	assert.True(t, local.Equal(bound), "the instant must not change, only its representation")
}

func TestBoolInt(t *testing.T) {
	assert.Equal(t, int8(1), boolInt(true))
	assert.Equal(t, int8(0), boolInt(false))
}

// ---------------------------------------------------------------------------
// JSON column codec
// ---------------------------------------------------------------------------

func TestJSONColumnScanAcceptsEveryDriverShape(t *testing.T) {
	cases := []struct {
		name  string
		src   any
		null  bool
		want  []string
		wantE bool
	}{
		{name: "nil", src: nil, null: true},
		{name: "bytes", src: []byte(`["a","b"]`), want: []string{"a", "b"}},
		{name: "string", src: `["a"]`, want: []string{"a"}},
		{name: "json null literal", src: []byte("null"), null: true},
		{name: "empty array", src: []byte("[]"), want: []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c jsonColumn
			require.NoError(t, c.Scan(tc.src))
			assert.Equal(t, tc.null, c.IsNull())
			if tc.null {
				got, err := c.decodeSlice()
				require.NoError(t, err)
				assert.Nil(t, got, "a NULL column decodes to nil, preserving 'no restriction'")
				return
			}
			got, err := c.decodeSlice()
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestJSONColumnScanRejectsUnexpectedType(t *testing.T) {
	var c jsonColumn
	err := c.Scan(42)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported json column type")
}

func TestJSONColumnDecodeRejectsGarbage(t *testing.T) {
	var c jsonColumn
	require.NoError(t, c.Scan([]byte(`{"not":"an array"}`)))
	_, err := c.decodeSlice()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode json array")

	var m jsonColumn
	require.NoError(t, m.Scan([]byte(`["not","an object"]`)))
	_, err = m.decodeMap()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode json object")
}

func TestJSONColumnDecodeMap(t *testing.T) {
	var c jsonColumn
	require.NoError(t, c.Scan([]byte(`{"gpt-4o":"upstream-4o"}`)))
	got, err := c.decodeMap()
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"gpt-4o": "upstream-4o"}, got)

	var nullCol jsonColumn
	require.NoError(t, nullCol.Scan(nil))
	got, err = nullCol.decodeMap()
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestMarshalJSONSlice(t *testing.T) {
	got, err := marshalJSONSlice([]string{"a", "b"})
	require.NoError(t, err)
	assert.Equal(t, `["a","b"]`, got.String())

	empty, err := marshalJSONSlice(nil)
	require.NoError(t, err)
	assert.Equal(t, "[]", empty.String(),
		"an empty list stays an array so it is distinguishable from NULL")

	v, err := empty.Value()
	require.NoError(t, err)
	assert.Equal(t, "[]", v)
}

func TestMarshalJSONMap(t *testing.T) {
	got, err := marshalJSONMap(map[string]string{"k": "v"})
	require.NoError(t, err)
	assert.Equal(t, `{"k":"v"}`, got.String())

	empty, err := marshalJSONMap(nil)
	require.NoError(t, err)
	assert.Equal(t, "{}", empty.String())
}

func TestMarshalJSONAny(t *testing.T) {
	got, err := marshalJSONAny([]int{1, 2})
	require.NoError(t, err)
	assert.Equal(t, "[1,2]", got.String())

	// cz_channel.key_pool is NOT NULL, so a nil pool must still be valid JSON.
	nilPool, err := marshalJSONAny(nil)
	require.NoError(t, err)
	assert.Equal(t, "[]", nilPool.String())

	// An unmarshalable value must surface as an internal error, not a panic.
	_, err = marshalJSONAny(make(chan int))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encode json")
}

func TestJSONValueNilWhenEmpty(t *testing.T) {
	var zero jsonValue
	v, err := zero.Value()
	require.NoError(t, err)
	assert.Nil(t, v, "an empty encoder result binds as SQL NULL")
}

func TestChannelKeyJSONFieldNames(t *testing.T) {
	// The on-disk shape must stay in sync with domain.ChannelKey, because
	// channels written by an older build are read by a newer one.
	pool, err := marshalJSONAny([]domainChannelKey{
		{Secret: "s", Weight: 7, Status: "active", Cooldown: 99},
	})
	require.NoError(t, err)
	assert.Contains(t, pool.String(), `"secret":"s"`)
	assert.Contains(t, pool.String(), `"weight":7`)
	assert.Contains(t, pool.String(), `"status":"active"`)
	assert.Contains(t, pool.String(), `"cooldown_until_unix":99`)

	var c jsonColumn
	require.NoError(t, c.Scan([]byte(pool.String())))
	keys, err := c.decodeChannelKeys()
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, int64(99), keys[0].Cooldown)
}

// domainChannelKey mirrors domain.ChannelKey so this file does not need to
// import the domain package merely to build a literal.
type domainChannelKey = channelKeyJSON

// ---------------------------------------------------------------------------
// Constructor validation (no server needed)
// ---------------------------------------------------------------------------

func TestNewRequiresDSN(t *testing.T) {
	s, err := New(context.Background(), Config{})
	assert.Nil(t, s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DSN")
}

func TestNewStoreRejectsEmptyConfigWithoutPool(t *testing.T) {
	s, err := newStore(context.Background(), Config{}, nil, nil)
	assert.Nil(t, s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DSN")
}

func TestNewStoreDefaultsTheClock(t *testing.T) {
	// A nil clock must fall back to the system clock rather than panic on the
	// first timestamp.
	st := &Store{clk: nil}
	assert.WithinDuration(t, time.Now().UTC(), st.now(), time.Minute)
}

func TestStoreNowUsesTheInjectedClock(t *testing.T) {
	frozen := time.Date(2026, 11, 5, 7, 0, 0, 0, time.UTC)
	st := &Store{clk: clock.NewFake(frozen)}
	assert.True(t, frozen.Equal(st.now()))
	assert.Equal(t, time.UTC, st.now().Location())
}

func TestStoreCloseWithoutOwnershipIsNoop(t *testing.T) {
	st := &Store{}
	assert.NoError(t, st.Close(), "a store that does not own its pool must not close it")
}
