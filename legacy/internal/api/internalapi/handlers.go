// Package internalapi implements the V10 plugin contract described in
// docs/internal-api-v1.md.
//
// Every response uses the envelope {"code","msg","data"}. Every mutation that
// moves credits or subscriptions is idempotent on a documented key, so the V10
// side can retry blindly without double-crediting a customer.
package internalapi

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"czapi/internal/billing"
	"czapi/internal/cache"
	"czapi/internal/metrics"
	"czapi/internal/pkg/errs"
	"czapi/internal/pkg/log"
	"czapi/internal/store"
)

// Handlers serves /internal/v1.
type Handlers struct {
	store  store.Store
	ledger billing.Ledger
	quota  ledgerCredit
	mx     *metrics.Registry
	log    *log.Logger
	clock  clockish
	cache  cache.Cache
	salt   string
	// console mints SSO tickets for the browser console.
	console ticketIssuer
	version string
}

// clockish is the minimal time source.
type clockish interface{ Now() time.Time }

// ledgerCredit is the credit surface the endpoints need (satisfied by
// *quota.Service).
type ledgerCredit interface {
	Credit(ctx context.Context, req billing.CreditRequest) (billing.CreditResult, error)
	Balance(ctx context.Context, accountID uint64) (int64, error)
}

// ticketIssuer mints console SSO tickets (satisfied by *openai.Handlers).
type ticketIssuer interface {
	IssueTicket(ctx context.Context, siteID string, clientID uint64, email string) (ticket, ssoURL string, expiresIn int, err error)
}

// Deps assembles Handlers.
type Deps struct {
	Store   store.Store
	Ledger  billing.Ledger
	Credit  ledgerCredit
	Metrics *metrics.Registry
	Log     *log.Logger
	Clock   clockish
	Cache   cache.Cache
	KeySalt string
	Console ticketIssuer
	Version string
}

// New builds the handlers.
func New(d Deps) *Handlers {
	return &Handlers{
		store: d.Store, ledger: d.Ledger, quota: d.Credit, mx: d.Metrics,
		log: d.Log, clock: d.Clock, cache: d.Cache, salt: d.KeySalt,
		console: d.Console, version: d.Version,
	}
}

// Mount registers every signed /internal/v1 route.
func (h *Handlers) Mount(r chi.Router) {
	r.Post("/accounts/sync", h.SyncAccount)
	r.Post("/credit", h.Credit)
	r.Get("/accounts/{client_id}/quota", h.Quota)

	r.Route("/keys", func(kr chi.Router) {
		kr.Post("/", h.CreateKey)
		kr.Get("/", h.ListKeys)
		kr.Patch("/{id}", h.UpdateKey)
		kr.Delete("/{id}", h.DeleteKey)
	})

	r.Get("/models", h.Models)
	r.Get("/usage/summary", h.UsageSummary)
	r.Get("/usage/logs", h.UsageLogs)

	r.Route("/subscriptions", func(sr chi.Router) {
		sr.Post("/", h.CreateSubscription)
		sr.Get("/{id}", h.GetSubscription)
		sr.Post("/{id}/suspend", h.SuspendSubscription)
		sr.Post("/{id}/resume", h.ResumeSubscription)
		sr.Delete("/{id}", h.DeleteSubscription)
	})

	r.Get("/billing/host-usage", h.HostUsage)
	r.Post("/console/sso-ticket", h.SSOTicket)
}

// ---------------------------------------------------------------------------
// envelope helpers
// ---------------------------------------------------------------------------

type envelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data"`
}

// okw writes a success envelope.
func okw(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, envelope{Code: 0, Msg: "ok", Data: data})
}

// failw writes a documented error envelope (docs/internal-api-v1.md §1.4).
func failw(w http.ResponseWriter, e *errs.Error) {
	if e == nil {
		e = errs.Internal("unknown error", nil)
	}
	writeJSON(w, e.Status, envelope{Code: internalCode(e), Msg: e.Message, Data: nil})
}

// internalCode maps a domain error to the numeric code table the V10 plugin
// switches on.
func internalCode(e *errs.Error) int {
	if e == nil {
		return 50000
	}
	switch {
	case e.Status == http.StatusBadRequest && e.Code == errs.CodeInvalidRequest:
		return 40000
	case e.Status == http.StatusUnauthorized && e.Code == errs.CodeSignInvalid:
		return 40100
	case e.Status == http.StatusForbidden:
		return 40300
	case e.Status == http.StatusNotFound:
		return 40400
	case e.Status == http.StatusConflict:
		return 40900
	case e.Status == http.StatusPaymentRequired:
		return 40200
	case e.Status == http.StatusTooManyRequests:
		return 42900
	case e.Status == http.StatusServiceUnavailable:
		return 50300
	case e.Code == errs.CodeFeatureDisabled:
		return 50300
	default:
		if e.Status >= 500 {
			return 50000
		}
		return 40000
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = jsonEncode(w, v)
}
