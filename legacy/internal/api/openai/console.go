package openai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"czapi/internal/api/console"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"czapi/internal/domain"
	"czapi/internal/pkg/errs"
)

// ssoTicketTTL bounds how long a V10-issued console ticket stays valid. It is
// deliberately short because the ticket travels in a URL.
const ssoTicketTTL = 60 * time.Second

// ticketKey builds the cache key for a one-time SSO ticket.
func ticketKey(ticket string) string { return "cz:sso:" + hashTicket(ticket) }

// hashTicket hashes a ticket so the cache never holds the secret verbatim.
func hashTicket(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// MountConsole registers the browser-facing console routes.
//
// The SPA is served from the built dist directory. When no build is present the
// gateway still answers the session endpoints and returns a JSON notice for the
// page, so an API-only deployment stays healthy.
func (h *Handlers) MountConsole(r chi.Router) {
	cfg := h.console.normalized()

	// V10 hands the browser a one-time ticket; we exchange it for a session
	// cookie and redirect into the SPA (plan §8.5).
	r.Get("/v10-sso", h.SSORedeem)

	// Browser session endpoints.
	r.Route("/v1", func(cr chi.Router) {
		cr.Get("/overview", h.consoleOverview)
		cr.Get("/keys", h.consoleListKeys)
		cr.Post("/keys", h.consoleCreateKey)
		cr.Patch("/keys/{id}", h.consoleUpdateKey)
		cr.Delete("/keys/{id}", h.consoleDeleteKey)
		cr.Get("/models", h.consoleModels)
		cr.Get("/usage/summary", h.consoleUsageSummary)
		cr.Get("/usage/logs", h.consoleUsageLogs)
		cr.Get("/docs", h.consoleDocs)
	})

	// Static assets and SPA fallback.
	r.Get("/*", h.serveConsole(cfg))
	r.Get("/", h.serveConsole(cfg))
}

// IssueTicket mints a one-time console ticket for a V10 client. It is called by
// the internal API (docs/console-api-v1.md §3).
func (h *Handlers) IssueTicket(ctx context.Context, siteID string, clientID uint64, email string) (ticket string, ssoURL string, expiresIn int, err error) {
	cfg := h.console.normalized()
	ticket, err = newTicket()
	if err != nil {
		return "", "", 0, err
	}
	payload, _ := json.Marshal(ssoPayload{SiteID: siteID, ClientID: clientID, Email: email})
	// The cache is optional for the console; without it SSO is unavailable but
	// the rest of the gateway keeps working.
	if h.cache != nil {
		if serr := h.cache.Set(ctx, ticketKey(ticket), payload, ssoTicketTTL); serr != nil {
			return "", "", 0, serr
		}
	}
	base := trimSlash(cfg.BaseURL)
	return ticket, base + "/console/v10-sso?ticket=" + ticket, int(ssoTicketTTL.Seconds()), nil
}

// ssoPayload is what a ticket encodes.
type ssoPayload struct {
	SiteID   string `json:"site_id"`
	ClientID uint64 `json:"client_id"`
	Email    string `json:"email"`
}

// SSORedeem exchanges a ticket for a session cookie and redirects to the SPA.
func (h *Handlers) SSORedeem(w http.ResponseWriter, r *http.Request) {
	cfg := h.console.normalized()
	ticket := r.URL.Query().Get("ticket")
	if ticket == "" {
		http.Error(w, "missing ticket", http.StatusBadRequest)
		return
	}
	if h.cache == nil {
		http.Error(w, "console sessions are not configured", http.StatusServiceUnavailable)
		return
	}

	raw, found, err := h.cache.Get(r.Context(), ticketKey(ticket))
	if err != nil || !found {
		// One-time use: a second attempt fails by design.
		http.Error(w, "ticket invalid or expired", http.StatusUnauthorized)
		return
	}
	// Burn the ticket immediately so it cannot be replayed.
	_ = h.cache.Del(r.Context(), ticketKey(ticket))

	var p ssoPayload
	if jerr := json.Unmarshal(raw, &p); jerr != nil {
		http.Error(w, "ticket invalid", http.StatusUnauthorized)
		return
	}

	session, err := newTicket()
	if err != nil {
		http.Error(w, "could not start session", http.StatusInternalServerError)
		return
	}
	// The session stores the resolved account so later requests need no V10 call.
	acc, aerr := h.store.Accounts().GetByV10Client(r.Context(), p.SiteID, p.ClientID)
	if aerr != nil || acc == nil {
		http.Error(w, "account not found", http.StatusNotFound)
		return
	}
	sessPayload, _ := json.Marshal(sessionPayload{
		AccountID: acc.ID, SiteID: acc.SiteID, V10ClientID: acc.V10ClientID, Email: acc.Email,
	})
	if serr := h.cache.Set(r.Context(), sessionKey(session), sessPayload, time.Duration(cfg.SessionTTL)*time.Second); serr != nil {
		http.Error(w, "could not start session", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     cfg.SessionCookieName,
		Value:    session,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(cfg.SessionTTL),
	})
	http.Redirect(w, r, "/console/", http.StatusFound)
}

// sessionPayload is the resolved console identity.
type sessionPayload struct {
	AccountID   uint64 `json:"account_id"`
	SiteID      string `json:"site_id"`
	V10ClientID uint64 `json:"v10_client_id"`
	Email       string `json:"email"`
}

func sessionKey(s string) string { return "cz:sess:" + hashTicket(s) }

// session resolves the browser session from the cookie.
func (h *Handlers) session(r *http.Request) (*sessionPayload, *domain.Account, *errs.Error) {
	cfg := h.console.normalized()
	if h.cache == nil {
		return nil, nil, errs.New(http.StatusServiceUnavailable, errs.CodeFeatureDisabled, "console sessions are not configured")
	}
	c, err := r.Cookie(cfg.SessionCookieName)
	if err != nil || c.Value == "" {
		return nil, nil, errs.New(http.StatusUnauthorized, errs.CodeInvalidAPIKey, "console session required")
	}
	raw, found, cerr := h.cache.Get(r.Context(), sessionKey(c.Value))
	if cerr != nil || !found {
		return nil, nil, errs.New(http.StatusUnauthorized, errs.CodeInvalidAPIKey, "console session expired")
	}
	var p sessionPayload
	if jerr := json.Unmarshal(raw, &p); jerr != nil {
		return nil, nil, errs.New(http.StatusUnauthorized, errs.CodeInvalidAPIKey, "console session invalid")
	}
	acc, aerr := h.store.Accounts().GetByID(r.Context(), p.AccountID)
	if aerr != nil || acc == nil {
		return nil, nil, errs.New(http.StatusUnauthorized, errs.CodeInvalidAPIKey, "account not found")
	}
	return &p, acc, nil
}

// serveConsole serves the SPA with a history-mode fallback.
//
// Assets come from the binary itself unless a usable directory was configured, so
// a deployed gateway always has a working console: there is no "the UI is broken
// because someone deleted a folder" failure mode.
func (h *Handlers) serveConsole(cfg ConsoleConfig) http.HandlerFunc {
	src := console.Open(cfg.DistDir)
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Enabled {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"message": "console is disabled",
			})
			return
		}
		if !src.OK() {
			writeJSON(w, http.StatusOK, map[string]string{
				"message": "console assets are not built; run 'pnpm build' in web/",
			})
			return
		}

		rel := strings.TrimPrefix(r.URL.Path, "/console")
		rel = strings.TrimPrefix(rel, "/")

		if rel != "" && isStaticAsset(rel) {
			body, ok := src.Asset(rel)
			if !ok {
				http.NotFound(w, r)
				return
			}
			writeAsset(w, r, rel, body)
			return
		}

		// Anything else is a client-side route: hand back the SPA shell so a deep
		// link such as /console/admin/models reloads correctly.
		index, ok := src.Index()
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeAsset(w, r, "index.html", index)
	}
}

// writeAsset emits a console file with a correct type and a cache policy.
//
// Hashed asset names are safe to cache forever; index.html must not be cached or a
// deploy would keep serving the previous bundle. ETag revalidation covers the rest.
func writeAsset(w http.ResponseWriter, r *http.Request, name string, body []byte) {
	w.Header().Set("Content-Type", console.ContentType(name))
	if strings.EqualFold(filepath.Base(name), "index.html") {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeContent(w, r, filepath.Base(name), time.Time{}, bytes.NewReader(body))
}

// isStaticAsset reports whether rel looks like a file rather than a route.
func isStaticAsset(rel string) bool {
	ext := strings.ToLower(filepath.Ext(rel))
	switch ext {
	case ".js", ".css", ".svg", ".png", ".jpg", ".jpeg", ".gif", ".webp",
		".ico", ".woff", ".woff2", ".ttf", ".map", ".txt", ".json":
		return true
	}
	return false
}

// constantTimeEqual compares two strings without leaking timing.
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
