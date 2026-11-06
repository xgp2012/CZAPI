package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"

	"czapi/internal/api/admin"
	"czapi/internal/config"
	"czapi/internal/domain"
	"czapi/internal/pkg/clock"
	"czapi/internal/pkg/errs"
	"czapi/internal/pkg/log"
	"czapi/internal/store"
)

// Report describes what the bootstrap actually did, so the caller can log it
// once and the operator knows what exists without reading the database.
type Report struct {
	// AdminCreated reports whether the default administrator was inserted.
	AdminCreated bool
	// AdminUsername echoes the account that now exists (created or pre-existing).
	AdminUsername string
	// SiteCreated reports whether a default V10 site was inserted.
	SiteCreated bool
	// SiteAppID is the app id V10 must send.
	SiteAppID string
	// SiteSecret is the plaintext secret. It is only known here when it was just
	// generated; it is printed once and never recoverable afterwards.
	SiteSecret string
	// DemoSeeded reports whether sample channel/model/plan rows were created.
	DemoSeeded bool
}

// Run creates the resources a fresh installation needs to be usable.
//
// Every step is idempotent and independent: an error in one step is reported but
// does not abort the others, because a partially bootstrapped install is still
// more useful than a process that refuses to start. Nothing is ever overwritten:
// restarting the gateway can never reset an existing password or rotate a secret.
func Run(ctx context.Context, st store.Store, cfg *config.Config, lg *log.Logger, clk clock.Clock) (Report, error) {
	var rep Report
	if clk == nil {
		clk = clock.System{}
	}

	if err := ensureAdmin(ctx, st, cfg.Bootstrap, clk, lg, &rep); err != nil {
		return rep, err
	}
	if err := ensureSite(ctx, st, cfg, clk, lg, &rep); err != nil {
		return rep, err
	}
	if cfg.Bootstrap.DemoContent {
		if err := ensureDemoContent(ctx, st, clk, lg, &rep); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// ensureAdmin seeds the first administrator when no administrator exists.
//
// The check is "is the table empty", not "does this username exist": once any
// admin exists the operator owns the roster, and re-creating a default account on
// every restart would be a standing backdoor.
func ensureAdmin(ctx context.Context, st store.Store, cfg config.Bootstrap, clk clock.Clock, lg *log.Logger, rep *Report) error {
	username := strings.TrimSpace(cfg.AdminUsername)
	if username == "" || cfg.AdminPassword == "" {
		return nil
	}

	n, err := st.Admins().Count(ctx)
	if err != nil {
		return errs.Internal("bootstrap: count admins", err)
	}
	if n > 0 {
		rep.AdminUsername = username
		return nil
	}

	hash, herr := admin.HashPassword(cfg.AdminPassword)
	if herr != nil {
		return errs.Internal("bootstrap: hash admin password", herr)
	}
	now := clk.Now()
	if cerr := st.Admins().Create(ctx, &domain.AdminUser{
		Username:     username,
		PasswordHash: hash,
		Role:         "admin",
		Status:       1,
		CreatedAt:    now,
		UpdatedAt:    now,
	}); cerr != nil {
		return errs.Internal("bootstrap: create admin", cerr)
	}

	rep.AdminCreated = true
	rep.AdminUsername = username
	if lg != nil {
		lg.Warn("default administrator created; change this password before exposing the console",
			"username", username)
	}
	return nil
}

// ensureSite seeds one V10 site so /internal/v1 is reachable immediately.
//
// Config-declared sites are also written to the store here, which is what makes
// the `internal.sites` block actually take effect: the signature verifier resolves
// app ids through the store, not through the config file.
func ensureSite(ctx context.Context, st store.Store, cfg *config.Config, clk clock.Clock, lg *log.Logger, rep *Report) error {
	// Explicitly configured sites take precedence.
	for _, s := range cfg.Internal.Sites {
		if strings.TrimSpace(s.AppID) == "" || s.Secret == "" {
			continue
		}
		existing, gerr := st.Sites().GetByAppID(ctx, s.AppID)
		if gerr != nil && !isNotFound(gerr) {
			return errs.Internal("bootstrap: look up site", gerr)
		}
		if existing != nil {
			continue // never overwrite a secret that is already in use
		}
		now := clk.Now()
		if cerr := st.Sites().Create(ctx, &domain.Site{
			AppID:      s.AppID,
			Name:       nameOr(s.Name, s.AppID),
			Secret:     s.Secret,
			SecretPrev: s.SecretPrev,
			IPAllow:    s.IPAllow,
			Enabled:    true,
			CreatedAt:  now,
			UpdatedAt:  now,
		}); cerr != nil {
			return errs.Internal("bootstrap: create site", cerr)
		}
		rep.SiteCreated = true
		rep.SiteAppID = s.AppID
		if lg != nil {
			lg.Info("site registered from config", "app_id", s.AppID)
		}
		return nil
	}

	// Otherwise make sure at least one site exists so V10 can connect at all.
	appID := strings.TrimSpace(cfg.Bootstrap.DefaultSiteAppID)
	if appID == "" {
		appID = "v10"
	}
	existing, gerr := st.Sites().GetByAppID(ctx, appID)
	if gerr != nil && !isNotFound(gerr) {
		return errs.Internal("bootstrap: look up default site", gerr)
	}
	if existing != nil {
		rep.SiteAppID = existing.AppID
		return nil
	}

	secret := cfg.Bootstrap.DefaultSiteSecret
	generated := secret == ""
	if generated {
		var rerr error
		secret, rerr = randomHex(32)
		if rerr != nil {
			return rerr
		}
	}
	now := clk.Now()
	if cerr := st.Sites().Create(ctx, &domain.Site{
		AppID:     appID,
		Name:      "Default V10 site",
		Secret:    secret,
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}); cerr != nil {
		return errs.Internal("bootstrap: create default site", cerr)
	}
	rep.SiteCreated = true
	rep.SiteAppID = appID
	if generated {
		// Only printable here: the store keeps the secret, but there is no API that
		// returns it in plaintext, so this is the operator's one chance to record it.
		rep.SiteSecret = secret
	}
	return nil
}

// ensureDemoContent seeds a channel, model and plan so /v1 and the console have
// something to show on a fresh install.
func ensureDemoContent(ctx context.Context, st store.Store, clk clock.Clock, lg *log.Logger, rep *Report) error {
	now := clk.Now()

	if _, err := st.Plans().Upsert(ctx, &domain.Plan{
		PlanCode: "demo", Name: "演示套餐", QuotaTokens: 1_000_000,
		Period: "month", RPMLimit: 60, TPMLimit: 100_000, Concurrency: 5,
		ModelGroup: "default", MaxKeys: 5, GroupRatio: 1, Status: 1,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		return errs.Internal("bootstrap: seed plan", err)
	}

	if _, err := st.Models().Upsert(ctx, &domain.Model{
		ModelName: "gpt-4o-mini", DisplayName: "GPT-4o mini", GroupName: "default",
		ContextLen: 128_000, ModelRatio: 1, CompletionRatio: 4, CachedRatio: 0.5,
		Status: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		return errs.Internal("bootstrap: seed model", err)
	}

	channels, err := st.Channels().List(ctx)
	if err != nil {
		return errs.Internal("bootstrap: list channels", err)
	}
	if len(channels) == 0 {
		if cerr := st.Channels().Create(ctx, &domain.Channel{
			Name: "demo-openai", Provider: "openai",
			BaseURL: "https://api.openai.com",
			KeyPool: []domain.ChannelKey{
				{Secret: "sk-REPLACE-ME", Weight: 1, Status: "active"},
			},
			Models: []string{"gpt-4o-mini"}, Weight: 100, Priority: 1,
			MaxConcurrency: 10, TimeoutMS: 60_000, Status: 1,
			CreatedAt: now, UpdatedAt: now,
		}); cerr != nil {
			return errs.Internal("bootstrap: seed channel", cerr)
		}
		if lg != nil {
			lg.Warn("demo channel created with a placeholder key; set a real key in the console")
		}
	}

	rep.DemoSeeded = true
	return nil
}

// randomHex returns n random bytes as URL-safe base64.
//
// It returns an error rather than panicking: a broken entropy source is fatal for
// generating a signing secret, but it must surface as a startup error the
// operator can read, not a crash with no context.
func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", errs.Internal("bootstrap: crypto/rand unavailable", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// nameOr returns v, or fallback when v is blank.
func nameOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// isNotFound reports whether err is a not-found condition.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var e *errs.Error
	if errors.As(err, &e) {
		return e.Code == errs.CodeNotFound
	}
	return false
}
