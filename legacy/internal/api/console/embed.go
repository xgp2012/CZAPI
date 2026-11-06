// Package console provides the built browser console to the HTTP layer.
//
// The assets are compiled into the binary so a deployed server cannot end up in a
// state where the API works but the console directory is missing, unreadable, or
// stale relative to the running binary. Disk is still consulted first when a
// directory is configured, which keeps the Vite dev loop (rebuild, refresh)
// working without recompiling Go.
package console

import (
	"embed"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// embedded holds the production build.
//
// "all:" includes files whose names begin with "." or "_", and the pattern must
// match at least one file or the build fails. dist/placeholder.txt guarantees
// that: a checkout without a built console still compiles, and the gateway then
// serves a clear notice instead of refusing to start.
//
//go:embed all:dist
var embedded embed.FS

// distRoot is the subdirectory of the embedded tree that holds index.html.
const distRoot = "dist"

// Source resolves console assets from disk or from the embedded copy.
type Source struct {
	// disk is set when a directory with an index.html was found.
	disk fs.FS
	// emb is the embedded tree rooted at dist/.
	emb fs.FS
	// mode reports where assets are coming from, for logs and diagnostics.
	mode string
}

// Open returns a Source, preferring diskDir when it actually contains a build.
//
// A configured but absent directory is not an error: falling back to the embedded
// copy is exactly what makes the binary self-contained. The disk path only wins
// when it is usable, so a half-copied directory cannot shadow a good build.
func Open(diskDir string) *Source {
	s := &Source{}
	if sub, err := fs.Sub(embedded, distRoot); err == nil {
		s.emb = sub
	}
	if dir := strings.TrimSpace(diskDir); dir != "" {
		if st, err := os.Stat(filepath.Join(dir, "index.html")); err == nil && !st.IsDir() {
			s.disk = os.DirFS(dir)
			s.mode = "disk:" + dir
		}
	}
	if s.mode == "" && s.emb != nil {
		if _, err := fs.Stat(s.emb, "index.html"); err == nil {
			s.mode = "embedded"
		}
	}
	return s
}

// Mode reports the asset origin: "disk:<path>", "embedded", or "" when unusable.
func (s *Source) Mode() string {
	if s == nil {
		return ""
	}
	return s.mode
}

// OK reports whether an index.html can be served.
func (s *Source) OK() bool { return s.Mode() != "" }

// Index returns the SPA entry document.
func (s *Source) Index() ([]byte, bool) {
	return s.read("index.html")
}

// Asset returns a static asset by its slash-separated relative path.
//
// The path is rejected unless it stays inside the tree: asset names arrive from
// the URL, so ".." segments must never escape.
func (s *Source) Asset(rel string) ([]byte, bool) {
	clean := path.Clean("/" + strings.ReplaceAll(rel, "\\", "/"))
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || clean == "." || strings.HasPrefix(clean, "..") {
		return nil, false
	}
	return s.read(clean)
}

// read looks the name up on disk first, then in the embedded tree.
func (s *Source) read(name string) ([]byte, bool) {
	if s == nil {
		return nil, false
	}
	if s.disk != nil {
		if b, err := fs.ReadFile(s.disk, name); err == nil {
			return b, true
		}
	}
	if s.emb != nil {
		if b, err := fs.ReadFile(s.emb, name); err == nil {
			return b, true
		}
	}
	return nil, false
}

// Has reports whether a name exists in the active source.
func (s *Source) Has(name string) bool {
	_, ok := s.read(name)
	return ok
}

// ContentType resolves a Content-Type for a file name.
//
// An explicit table is used for the types this build actually emits, because
// mime.TypeByExtension consults the OS registry on some platforms and can return
// surprising values (notably application/x-javascript for .js on Windows).
func ContentType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json", ".map":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".ico":
		return "image/x-icon"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	case ".txt":
		return "text/plain; charset=utf-8"
	}
	if t := mime.TypeByExtension(filepath.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}
