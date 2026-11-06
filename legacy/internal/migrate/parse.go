package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// filePattern matches a migration file name such as "0001_init.sql" and
// captures the numeric version prefix and the remainder of the name.
var filePattern = regexp.MustCompile(`^(\d+)_(.+)\.sql$`)

// markerPattern matches a section marker line. The gateway's migration files
// use exactly the form "-- 0001_up" / "-- 0001_down".
var markerPattern = regexp.MustCompile(`^--\s*(\d+)_(up|down)\s*$`)

// Migration is one parsed migration file.
//
// A file is split into an "up" section and an optional "down" section by the
// marker comments "-- <version>_up" and "-- <version>_down". Everything before
// the first marker is preamble and is ignored.
type Migration struct {
	// Version is the numeric prefix of the file name (0001_init.sql -> 1).
	Version int64
	// Name is the file name without the .sql suffix, e.g. "0001_init".
	Name string
	// FileName is the base name of the source file.
	FileName string
	// Up holds the individual statements of the up section.
	Up []string
	// Down holds the individual statements of the down section; it is empty
	// when the file is not reversible.
	Down []string
}

// Reversible reports whether the migration carries a down section.
func (m *Migration) Reversible() bool { return len(m.Down) > 0 }

// ParseMigration parses one migration file's contents.
//
// fileName is used only for the version/name metadata and to build error
// messages, so callers may pass a base name.
func ParseMigration(fileName string, src []byte) (*Migration, error) {
	base := filepath.Base(fileName)
	match := filePattern.FindStringSubmatch(base)
	if match == nil {
		return nil, fmt.Errorf("migrate: %s: file name must look like <version>_<name>.sql", base)
	}
	version, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("migrate: %s: bad version prefix: %w", base, err)
	}

	mig := &Migration{
		Version:  version,
		Name:     strings.TrimSuffix(base, ".sql"),
		FileName: base,
	}

	var upLines, downLines []string
	var cur *[]string
	sawUp := false
	for _, line := range strings.Split(string(src), "\n") {
		if marker := markerPattern.FindStringSubmatch(strings.TrimSpace(line)); marker != nil {
			switch marker[2] {
			case "up":
				cur = &upLines
				sawUp = true
			case "down":
				cur = &downLines
			}
			continue
		}
		if cur != nil {
			*cur = append(*cur, line)
		}
	}
	if !sawUp {
		return nil, fmt.Errorf("migrate: %s: no \"-- %04d_up\" marker found", base, version)
	}

	mig.Up = SplitStatements(strings.Join(upLines, "\n"))
	mig.Down = SplitStatements(strings.Join(downLines, "\n"))
	if len(mig.Up) == 0 {
		return nil, fmt.Errorf("migrate: %s: up section has no statements", base)
	}
	return mig, nil
}

// LoadMigrations reads and parses every *.sql file in dir, ordered by version
// ascending. Files whose name does not match <version>_<name>.sql are ignored;
// two files sharing a version are an error.
func LoadMigrations(dir string) ([]*Migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("migrate: read %s: %w", dir, err)
	}
	out := make([]*Migration, 0, len(entries))
	seen := make(map[int64]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		if filePattern.FindStringSubmatch(entry.Name()) == nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("migrate: read %s: %w", entry.Name(), err)
		}
		mig, err := ParseMigration(entry.Name(), raw)
		if err != nil {
			return nil, err
		}
		if other, ok := seen[mig.Version]; ok {
			return nil, fmt.Errorf("migrate: duplicate version %d (%s and %s)", mig.Version, other, mig.FileName)
		}
		seen[mig.Version] = mig.FileName
		out = append(out, mig)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// SplitStatements splits a SQL section into individual statements on the
// semicolons that are outside quotes and comments.
//
// It understands:
//   - line comments introduced by "--" (to end of line);
//   - block comments wrapped in "/*" and "*/";
//   - single quotes, double quotes and backticks, including backslash escapes
//     (except inside backticks) and doubled-quote escapes (” inside '...').
//
// Comments are removed from the returned statements and empty statements are
// dropped, so an empty or comment-only input yields an empty slice.
func SplitStatements(src string) []string {
	out := make([]string, 0, 16)
	var buf strings.Builder
	i, n := 0, len(src)
	flush := func() {
		if stmt := strings.TrimSpace(buf.String()); stmt != "" {
			out = append(out, stmt)
		}
		buf.Reset()
	}
	for i < n {
		c := src[i]
		switch {
		case c == '-' && i+1 < n && src[i+1] == '-':
			for i < n && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && src[i+1] == '*':
			i += 2
			for i+1 < n && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			if i+1 < n {
				i += 2
			} else {
				i = n
			}
			// Keep tokens separated across a removed comment.
			buf.WriteByte(' ')
		case c == '\'' || c == '"' || c == '`':
			quote := c
			buf.WriteByte(c)
			i++
			for i < n {
				ch := src[i]
				if ch == '\\' && quote != '`' && i+1 < n {
					buf.WriteByte(ch)
					buf.WriteByte(src[i+1])
					i += 2
					continue
				}
				buf.WriteByte(ch)
				i++
				if ch != quote {
					continue
				}
				// A doubled quote is an escaped quote, still inside the literal.
				if i < n && src[i] == quote {
					buf.WriteByte(quote)
					i++
					continue
				}
				break
			}
		case c == ';':
			flush()
			i++
		default:
			buf.WriteByte(c)
			i++
		}
	}
	flush()
	return out
}
