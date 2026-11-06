package console

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEmbeddedSourceIsUsable is the load-bearing assertion of this package: the
// binary must serve a console with no files on disk at all. If this fails, a
// deployment can lose its UI by copying only the executable.
func TestEmbeddedSourceIsUsable(t *testing.T) {
	src := Open("")
	assert.Equal(t, "embedded", src.Mode(), "no directory configured must fall back to embedded")

	index, ok := src.Index()
	require.True(t, ok, "the embedded tree must contain index.html")
	assert.Contains(t, string(index), "<div id=\"app\">",
		"index.html must be the SPA shell")
}

func TestEmbeddedAssetsAreComplete(t *testing.T) {
	// The HTML references its bundle by a hashed name; if the asset were missing
	// the console would load a blank page, which is the failure mode this whole
	// change exists to prevent.
	src := Open("")
	index, ok := src.Index()
	require.True(t, ok)

	html := string(index)
	var refs []string
	for _, marker := range []string{`src="`, `href="`} {
		rest := html
		for {
			i := strings.Index(rest, marker)
			if i < 0 {
				break
			}
			rest = rest[i+len(marker):]
			end := strings.IndexAny(rest, `"'`)
			if end < 0 {
				break
			}
			ref := rest[:end]
			if strings.HasPrefix(ref, "/console/assets/") {
				refs = append(refs, strings.TrimPrefix(ref, "/console/"))
			}
		}
	}
	require.NotEmpty(t, refs, "index.html must reference its built assets")
	for _, ref := range refs {
		body, found := src.Asset(ref)
		assert.True(t, found, "referenced asset must be embedded: %s", ref)
		assert.NotEmpty(t, body, "asset must not be empty: %s", ref)
	}
}

func TestAssetRejectsTraversal(t *testing.T) {
	src := Open("")
	for _, bad := range []string{
		"../go.mod",
		"../../etc/passwd",
		"assets/../../config.yaml",
		`..\\windows\\win.ini`,
		"./../secret",
	} {
		_, ok := src.Asset(bad)
		assert.False(t, ok, "traversal must be refused: %s", bad)
	}
}

func TestAssetRejectsEmpty(t *testing.T) {
	src := Open("")
	for _, bad := range []string{"", ".", "/", "./"} {
		_, ok := src.Asset(bad)
		assert.False(t, ok, "empty path must be refused: %q", bad)
	}
}

func TestMissingAssetIsNotAnError(t *testing.T) {
	src := Open("")
	_, ok := src.Asset("assets/does-not-exist-12345.js")
	assert.False(t, ok, "an unknown asset must simply be absent")
}

func TestDiskOverridesEmbeddedOnlyWhenComplete(t *testing.T) {
	// A directory without index.html must not shadow the embedded build: that is
	// how a half-copied deploy would otherwise produce a broken console.
	src := Open("this/directory/does/not/exist")
	assert.Equal(t, "embedded", src.Mode(),
		"an unusable directory must fall back to embedded, not report disk")

	// The legacy default path should also be tolerated whether or not it exists.
	src = Open("dist")
	assert.NotEmpty(t, src.Mode())
	index, ok := src.Index()
	assert.True(t, ok, "there must always be a servable index")
	assert.NotEmpty(t, index)
}

func TestContentTypesAreExplicit(t *testing.T) {
	// .js must not come back as application/x-javascript from an OS registry.
	cases := map[string]string{
		"index.html":   "text/html; charset=utf-8",
		"assets/x.js":  "text/javascript; charset=utf-8",
		"assets/x.css": "text/css; charset=utf-8",
		"favicon.svg":  "image/svg+xml",
		"a.woff2":      "font/woff2",
		"a.png":        "image/png",
		"data.json":    "application/json; charset=utf-8",
	}
	for name, want := range cases {
		assert.Equal(t, want, ContentType(name), "content type for %s", name)
	}
}

func TestOKReportsUsability(t *testing.T) {
	assert.True(t, Open("").OK())
	var nilSrc *Source
	assert.False(t, nilSrc.OK())
	assert.Equal(t, "", nilSrc.Mode())
	_, ok := nilSrc.Index()
	assert.False(t, ok, "a nil source must not panic and must report absence")
}

func TestHasReportsPresence(t *testing.T) {
	src := Open("")
	assert.True(t, src.Has("index.html"))
	assert.False(t, src.Has("nope.txt"))
}
