package mysqlstore

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"czapi/internal/pkg/errs"
)

// mysql error numbers that carry a domain meaning.
const (
	errNumDuplicateEntry  = 1062
	errNumNoReferencedRow = 1452
)

// normalizeDSN parses a go-sql-driver DSN and forces the settings this package
// depends on:
//
//   - parseTime=true so DATETIME columns scan into time.Time;
//   - loc=UTC so no implicit timezone shifting happens;
//   - the utf8mb4 charset, required by the JSON columns in the schema.
//
// An empty DSN is rejected; a malformed one is reported as a config error.
func normalizeDSN(dsn string) (string, error) {
	if dsn == "" {
		return "", errs.Internal("mysqlstore: config.DSN is required", nil)
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return "", errs.Internal("mysqlstore: invalid DSN", err)
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC

	// The driver keeps the charset in an unexported field, so it can only be
	// set through its own Option (which replaces the list) rather than through
	// cfg.Params. An explicit charset chosen by the operator is therefore
	// detected on the raw DSN and left alone; only the default is filled in.
	if !strings.Contains(dsn, "charset=") {
		if err := cfg.Apply(mysql.Charset("utf8mb4", "utf8mb4_general_ci")); err != nil {
			return "", errs.Internal("mysqlstore: apply charset", err)
		}
	}
	return cfg.FormatDSN(), nil
}

// wrapDB converts a driver error into the shared error vocabulary.
//
// A duplicate-key violation becomes errs.Conflict, a missing parent row becomes
// errs.NotFound, and everything else becomes an internal error carrying the
// cause for the logs.
func wrapDB(msg string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return errs.NotFound(msg)
	}
	var myErr *mysql.MySQLError
	if errors.As(err, &myErr) {
		switch myErr.Number {
		case errNumDuplicateEntry:
			return errs.Conflict(msg + ": duplicate entry")
		case errNumNoReferencedRow:
			return errs.NotFound(msg + ": referenced row missing")
		}
	}
	return errs.Internal(msg, err)
}

// isDuplicateKey reports whether err is a MySQL unique-constraint violation.
func isDuplicateKey(err error) bool {
	var myErr *mysql.MySQLError
	return errors.As(err, &myErr) && myErr.Number == errNumDuplicateEntry
}

// ---------------------------------------------------------------------------
// Nullable column helpers
// ---------------------------------------------------------------------------

// nullString maps an empty string to SQL NULL, matching how the schema stores
// "unset" for optional text columns.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nilIfZero maps a zero uint64 to SQL NULL, used for the nullable foreign keys
// in cz_subscription and cz_usage_record.
func nilIfZero(v uint64) any {
	if v == 0 {
		return nil
	}
	return v
}

// fromNullTime unwraps a nullable DATETIME column into a *time.Time.
func fromNullTime(n sql.NullTime) *time.Time {
	if !n.Valid {
		return nil
	}
	t := n.Time.UTC()
	return &t
}

// toNullTime maps a *time.Time to a nullable DATETIME value.
func toNullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}

// boolInt maps a Go bool onto the TINYINT(1) columns the schema uses.
func boolInt(b bool) int8 {
	if b {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// JSON columns
// ---------------------------------------------------------------------------

// jsonColumn is a sql.Scanner for a JSON column.
//
// It keeps the raw bytes so that decoding stays in one place, and it treats
// both SQL NULL and the JSON literals "null" as "no value".
type jsonColumn struct {
	raw   []byte
	valid bool
}

// Scan implements sql.Scanner. MySQL may hand back []byte, string or nil.
func (j *jsonColumn) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		j.raw, j.valid = nil, false
	case []byte:
		j.raw, j.valid = append([]byte(nil), v...), true
	case string:
		j.raw, j.valid = []byte(v), true
	default:
		return errs.Internal("mysqlstore: unsupported json column type", nil)
	}
	return nil
}

// IsNull reports whether the column was NULL or the JSON literal null.
func (j jsonColumn) IsNull() bool {
	if !j.valid || len(j.raw) == 0 {
		return true
	}
	return string(j.raw) == "null"
}

// decodeSlice decodes the column into a []string. A NULL column yields nil, so
// "no restriction" survives a round trip.
func (j jsonColumn) decodeSlice() ([]string, error) {
	if j.IsNull() {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal(j.raw, &out); err != nil {
		return nil, errs.Internal("mysqlstore: decode json array", err)
	}
	return out, nil
}

// decodeMap decodes the column into a map[string]string. A NULL column yields
// nil.
func (j jsonColumn) decodeMap() (map[string]string, error) {
	if j.IsNull() {
		return nil, nil
	}
	var out map[string]string
	if err := json.Unmarshal(j.raw, &out); err != nil {
		return nil, errs.Internal("mysqlstore: decode json object", err)
	}
	return out, nil
}

// decodeChannelKeys decodes the cz_channel.key_pool column.
func (j jsonColumn) decodeChannelKeys() ([]channelKeyJSON, error) {
	if j.IsNull() {
		return nil, nil
	}
	var out []channelKeyJSON
	if err := json.Unmarshal(j.raw, &out); err != nil {
		return nil, errs.Internal("mysqlstore: decode key pool", err)
	}
	return out, nil
}

// jsonValue is a driver.Valuer for JSON columns.
//
// An empty slice or map is written as [] / {} rather than NULL, because
// cz_channel.key_pool and cz_channel.models are NOT NULL in the schema.
type jsonValue struct{ raw []byte }

// Value implements driver.Valuer.
func (j jsonValue) Value() (driver.Value, error) {
	if len(j.raw) == 0 {
		return nil, nil
	}
	return string(j.raw), nil
}

// String returns the encoded JSON, for logging and tests.
func (j jsonValue) String() string { return string(j.raw) }

// marshalJSONSlice encodes a []string, writing "[]" for nil/empty.
func marshalJSONSlice(v []string) (jsonValue, error) {
	if len(v) == 0 {
		return jsonValue{raw: []byte("[]")}, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return jsonValue{}, errs.Internal("mysqlstore: encode json array", err)
	}
	return jsonValue{raw: raw}, nil
}

// marshalJSONAny encodes an arbitrary value, mapping nil to SQL NULL. Used for
// cz_channel.key_pool, whose element type is a domain struct.
func marshalJSONAny(v any) (jsonValue, error) {
	if v == nil {
		return jsonValue{raw: []byte("[]")}, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return jsonValue{}, errs.Internal("mysqlstore: encode json", err)
	}
	return jsonValue{raw: raw}, nil
}

// marshalJSONMap encodes a map, writing "{}" for nil/empty.
func marshalJSONMap(v map[string]string) (jsonValue, error) {
	if len(v) == 0 {
		return jsonValue{raw: []byte("{}")}, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return jsonValue{}, errs.Internal("mysqlstore: encode json object", err)
	}
	return jsonValue{raw: raw}, nil
}

// channelKeyJSON mirrors domain.ChannelKey on the wire; a separate type keeps
// the decode tolerant of an unknown key by ignoring it.
type channelKeyJSON struct {
	Secret   string `json:"secret"`
	Weight   int    `json:"weight"`
	Status   string `json:"status"`
	Cooldown int64  `json:"cooldown_until_unix"`
}
