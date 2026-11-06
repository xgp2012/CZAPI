package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"czapi/internal/pkg/idgen"
)

// --- request id ------------------------------------------------------------

type ridKey struct{}

// RequestIDFrom returns the correlation id stored in ctx ("" when absent).
func RequestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(ridKey{}).(string); ok {
		return v
	}
	return ""
}

// withRequestID stores id in ctx, generating one when empty.
func withRequestID(ctx context.Context, id string) (context.Context, string) {
	if id == "" {
		id = idgen.ULID()
	}
	return context.WithValue(ctx, ridKey{}, id), id
}

// --- health ----------------------------------------------------------------

// healthHandler reports liveness plus the version. It never touches the
// database so a dead dependency cannot make the process look dead.
func healthHandler(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"version": d.Version,
		})
	}
}

// readinessHandler reports dependency health.
func readinessHandler(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := map[string]any{"status": "ok"}
		code := http.StatusOK
		if d.Health != nil {
			components, ready := d.Health.Check(r)
			out["components"] = components
			if !ready {
				out["status"] = "degraded"
				code = http.StatusServiceUnavailable
			}
		}
		writeJSON(w, code, out)
	}
}

// --- helpers ---------------------------------------------------------------

// writeJSON writes an arbitrary payload as JSON.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// envelope is the shared response shape for /internal/v1, /admin/v1 and
// /console/v1 (docs/internal-api-v1.md §1.1).
type envelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data"`
}

// ok writes a successful envelope.
func ok(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, envelope{Code: 0, Msg: "ok", Data: data})
}

// okMsg writes a successful envelope with a custom message.
func okMsg(w http.ResponseWriter, msg string, data any) {
	writeJSON(w, http.StatusOK, envelope{Code: 0, Msg: msg, Data: data})
}

// failEnvelope writes a business error using the internal-api code table.
func failEnvelope(w http.ResponseWriter, status, code int, msg string) {
	writeJSON(w, status, envelope{Code: code, Msg: msg, Data: nil})
}

// decodeJSON reads a JSON body with a size cap.
func decodeJSON(r *http.Request, dst any, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = 4 << 20
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBytes))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// webIndex resolves the SPA entry file, returning "" when no build is present.
//
// The console is optional: a gateway deployed without frontend assets must still
// serve API traffic, so a missing build degrades to a JSON notice instead of a
// 500.
func webIndex(dir string) string {
	if dir == "" {
		return ""
	}
	p := filepath.Join(dir, "index.html")
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p
	}
	return ""
}

// isAssetPath reports whether a console path looks like a static file.
func isAssetPath(p string) bool {
	ext := strings.ToLower(filepath.Ext(p))
	switch ext {
	case ".js", ".css", ".svg", ".png", ".jpg", ".jpeg", ".gif", ".webp",
		".ico", ".woff", ".woff2", ".ttf", ".map", ".json", ".txt":
		return true
	}
	return false
}
