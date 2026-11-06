// Package admin implements the operator console API (/admin/v1).
//
// Access is JWT based (the gateway keeps its own operator accounts, separate
// from V10 users). Every mutation is written to the audit log, because these
// endpoints can move money and disable channels (plan §12).
package admin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"czapi/internal/auth"
	"czapi/internal/billing"
	"czapi/internal/cache"
	"czapi/internal/domain"
	"czapi/internal/metrics"
	"czapi/internal/pkg/clock"
	"czapi/internal/pkg/crypto"
	"czapi/internal/pkg/errs"
	"czapi/internal/pkg/log"
	"czapi/internal/pkg/sign"
	"czapi/internal/runtimecfg"
	"czapi/internal/store"
)

// Handlers serves /admin/v1.
type Handlers struct {
	store       store.Store
	jwt         *auth.JWTIssuer
	credit      creditApplier
	relay       channelProber
	breaker     breakerState
	mx          *metrics.Registry
	log         *log.Logger
	clk         clock.Clock
	sealer      *crypto.Sealer
	cache       cache.Cache
	salt        string
	flags       *FlagStore
	invalidator keyInvalidator
	// runtime holds operator-tunable settings that apply without a restart.
	runtime *runtimecfg.Store
}

// SetRuntimeSettings installs the live settings store.
//
// Called during wiring, before the server starts accepting traffic.
func (h *Handlers) SetRuntimeSettings(s *runtimecfg.Store) { h.runtime = s }

// creditApplier applies manual balance adjustments (satisfied by quota.Service).
type creditApplier interface {
	Credit(ctx context.Context, req billing.CreditRequest) (billing.CreditResult, error)
}

// channelProber probes an upstream channel (satisfied by relay.Forwarder).
type channelProber interface {
	TestChannel(ctx context.Context, channelID uint64) (ok bool, message string, latencyMS int, err error)
}

// keyInvalidator drops a cached API key (satisfied by *auth.Verifier).
type keyInvalidator interface {
	InvalidateByID(ctx context.Context, keyID uint64) error
}

// breakerState reads breaker diagnostics (satisfied by *relay.Breaker).
type breakerState interface {
	State(channelID uint64) (open bool, errorRate float64, samples int)
	Reset(channelID uint64)
	OpenChannels() []uint64
}

// Deps assembles Handlers.
type Deps struct {
	Store       store.Store
	JWT         *auth.JWTIssuer
	Credit      creditApplier
	Relay       channelProber
	Breaker     breakerState
	Metrics     *metrics.Registry
	Log         *log.Logger
	Clock       clock.Clock
	Sealer      *crypto.Sealer
	Cache       cache.Cache
	KeySalt     string
	Flags       *FlagStore
	Invalidator keyInvalidator
}

// New builds the handlers.
func New(d Deps) *Handlers {
	if d.Clock == nil {
		d.Clock = clock.System{}
	}
	return &Handlers{
		store: d.Store, jwt: d.JWT, credit: d.Credit, relay: d.Relay,
		breaker: d.Breaker, mx: d.Metrics, log: d.Log, clk: d.Clock,
		sealer: d.Sealer, cache: d.Cache, salt: d.KeySalt, flags: d.Flags, invalidator: d.Invalidator,
	}
}

// Mount registers the authenticated admin routes. Login is registered by the
// router outside this group.
func (h *Handlers) Mount(r chi.Router) {
	r.Post("/logout", h.Logout)
	r.Get("/me", h.Me)

	r.Get("/dashboard", h.Dashboard)

	r.Route("/channels", func(cr chi.Router) {
		cr.Get("/", h.ListChannels)
		cr.Post("/", h.CreateChannel)
		cr.Get("/{id}", h.GetChannel)
		cr.Patch("/{id}", h.UpdateChannel)
		cr.Delete("/{id}", h.DeleteChannel)
		cr.Post("/{id}/test", h.TestChannel)
		cr.Post("/{id}/sync-models", h.SyncModels)
		cr.Post("/{id}/reset-breaker", h.ResetBreaker)
	})

	r.Route("/models", func(mr chi.Router) {
		mr.Get("/", h.ListModels)
		mr.Post("/", h.UpsertModel)
		mr.Patch("/{name}", h.UpdateModel)
		mr.Delete("/{name}", h.DeleteModel)
	})

	r.Route("/plans", func(pr chi.Router) {
		pr.Get("/", h.ListPlans)
		pr.Post("/", h.UpsertPlan)
		pr.Get("/{id}", h.GetPlan)
		pr.Patch("/{id}", h.UpdatePlan)
	})

	r.Route("/accounts", func(ar chi.Router) {
		ar.Get("/", h.ListAccounts)
		ar.Get("/{id}", h.GetAccount)
		ar.Patch("/{id}", h.UpdateAccount)
		ar.Post("/{id}/adjust", h.AdjustBalance)
	})

	r.Route("/keys", func(kr chi.Router) {
		kr.Get("/", h.ListKeysAcrossAccounts)
		kr.Patch("/{id}", h.UpdateKeyStatus)
	})

	r.Route("/subscriptions", func(sr chi.Router) {
		sr.Get("/", h.ListSubscriptions)
		sr.Get("/{id}", h.GetSubscription)
		sr.Post("/{id}/suspend", h.SuspendSubscription)
		sr.Post("/{id}/resume", h.ResumeSubscription)
		sr.Post("/{id}/reset-quota", h.ResetQuota)
	})

	r.Get("/ledger", h.ListLedger)
	r.Get("/reconcile", h.Reconcile)

	r.Route("/flags", func(fr chi.Router) {
		fr.Get("/", h.ListFlags)
		fr.Put("/{key}", h.SetFlag)
	})

	r.Route("/sites", func(sr chi.Router) {
		sr.Get("/", h.ListSites)
		sr.Post("/", h.CreateSite)
		sr.Post("/{app_id}/rotate-secret", h.RotateSecret)
	})

	r.Get("/settings", h.Settings)
	r.Patch("/settings/limits", h.RuntimeLimitsUpdate)
	r.Post("/password", h.ChangePassword)
	r.Get("/audit", h.ListAudit)
}

// ---------------------------------------------------------------------------
// envelope + auth plumbing
// ---------------------------------------------------------------------------

type envelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data"`
}

func okw(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, envelope{Code: 0, Msg: "ok", Data: data})
}

func failw(w http.ResponseWriter, e *errs.Error) {
	if e == nil {
		e = errs.Internal("unknown error", nil)
	}
	writeJSON(w, e.Status, envelope{Code: e.Status, Msg: e.Message, Data: nil})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = jsonEncode(w, v)
}

// actor returns the current admin claims.
func actor(r *http.Request) *auth.AdminClaims {
	if c, ok := auth.AdminFrom(r.Context()); ok {
		return c
	}
	return nil
}

// requireAdmin resolves the acting administrator or writes 401.
func (h *Handlers) requireAdmin(w http.ResponseWriter, r *http.Request) *auth.AdminClaims {
	c := actor(r)
	if c == nil {
		failw(w, errs.New(http.StatusUnauthorized, errs.CodeInvalidAPIKey, "admin authentication required"))
		return nil
	}
	return c
}

// audit records a mutation.
func (h *Handlers) audit(r *http.Request, action, objectType, objectID, before, after string) {
	c := actor(r)
	actorID := ""
	if c != nil {
		actorID = c.Username
	}
	entry := &domain.AuditLog{
		ActorType: "admin", ActorID: actorID, Action: action,
		ObjectType: objectType, ObjectID: objectID,
		Before: before, After: after, IP: clientIP(r), CreatedAt: h.clk.Now(),
	}
	if err := h.store.Audit().Append(r.Context(), entry); err != nil && h.log != nil {
		h.log.Warn("audit append failed", "action", action, "err", err)
	}
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Real-Ip"); v != "" {
		return v
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}

// ---------------------------------------------------------------------------
// Login
// ---------------------------------------------------------------------------

// Login implements POST /admin/v1/login.
func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeBody(r, &req); err != nil {
		failw(w, errs.InvalidRequest("invalid body: "+err.Error()))
		return
	}
	if strings.TrimSpace(req.Username) == "" || req.Password == "" {
		failw(w, errs.InvalidRequest("username and password are required"))
		return
	}
	admin, err := h.store.Admins().GetByUsername(r.Context(), req.Username)
	if err != nil || admin == nil {
		// Constant-ish response: never reveal whether the username exists.
		failw(w, errs.New(http.StatusUnauthorized, errs.CodeInvalidAPIKey, "invalid username or password"))
		return
	}
	if admin.Status != 1 {
		failw(w, errs.New(http.StatusForbidden, errs.CodeKeySuspended, "administrator is disabled"))
		return
	}
	if !VerifyPassword(admin.PasswordHash, req.Password) {
		failw(w, errs.New(http.StatusUnauthorized, errs.CodeInvalidAPIKey, "invalid username or password"))
		return
	}
	token, expiresAt, terr := h.jwt.Issue(admin.ID, admin.Username, admin.Role)
	if terr != nil {
		failw(w, errs.Internal("issue token", terr))
		return
	}
	h.audit(r, "admin.login", "admin", strconv.FormatUint(admin.ID, 10), "", "")
	okw(w, map[string]any{
		"token": token, "expires_at": expiresAt.Unix(),
		"admin": map[string]any{"id": admin.ID, "username": admin.Username, "role": admin.Role},
	})
}

// Logout implements POST /admin/v1/logout (stateless JWT: client-side drop).
func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	if c := h.requireAdmin(w, r); c == nil {
		return
	}
	h.audit(r, "admin.logout", "admin", "", "", "")
	okw(w, map[string]any{})
}

// Me implements GET /admin/v1/me.
func (h *Handlers) Me(w http.ResponseWriter, r *http.Request) {
	c := h.requireAdmin(w, r)
	if c == nil {
		return
	}
	okw(w, map[string]any{
		"id": c.AdminID, "username": c.Username, "role": c.Role,
		"expires_at": c.ExpiresAt.Unix(),
	})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// verifyPassword compares a stored hash with a plaintext password.
//
// Hashes are stored as bcrypt-compatible "sha256$<salt>$<hex>" envelopes so the
// gateway needs no cgo (bcrypt in x/crypto is pure Go, but avoiding a new
// dependency keeps the deployment footprint small). See HashPassword.
// VerifyPassword reports whether plain matches the stored envelope.
//
// Exported so the bootstrap package can assert that a seeded credential really
// authenticates, using exactly the same check the login handler performs.
func VerifyPassword(stored, plain string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 3 || parts[0] != "sha256" {
		return false
	}
	sum := sha256Hex(parts[1] + plain)
	return subtleEqual(sum, parts[2])
}

// HashPassword produces a storable password envelope.
func HashPassword(plain string) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	salt := hex.EncodeToString(buf)
	return "sha256$" + salt + "$" + sha256Hex(salt+plain), nil
}

func sha256Hex(s string) string {
	return crypto.HashKey("pwd:", s)
}

func subtleEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// randomSecret returns a base64url secret for a V10 site.
func randomSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// requestSign is re-exported so tests can assert the wire format without
// importing the sign package directly.
var requestSign = sign.Sign
