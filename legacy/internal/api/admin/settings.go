package admin

import (
	"net/http"
	"strings"

	"czapi/internal/pkg/errs"
	"czapi/internal/runtimecfg"
)

// runtimeSettingsView is what the console shows for live-tunable settings.
//
// Every field here is safe to change while serving: caching the value is enough
// to take effect, nothing in the database is keyed on it, and no in-flight
// request can be invalidated. Values that fail that test (key_hash_salt,
// storage DSN, listen address) are reported as read-only instead.
type runtimeSettingsView struct {
	Limits      runtimecfg.Limits `json:"limits"`
	ReadOnly    map[string]any    `json:"read_only"`
	MoneyUnit   string            `json:"money_unit"`
	ApplyNote   string            `json:"apply_note"`
	ConfigNotes map[string]string `json:"config_notes"`
}

// settingsReadOnly describes the values an operator must change on disk.
//
// These are surfaced rather than hidden: an operator looking for "the database
// password" should learn *where* it lives, not just fail to find a field.
var settingsReadOnly = map[string]string{
	"key_hash_salt":  "改这个会让所有已发出的 API Key 立即失效，只能改配置文件并重启",
	"storage":        "数据库驱动与连接串只能改配置文件并重启",
	"cache":          "缓存驱动与地址只能改配置文件并重启",
	"queue":          "队列驱动与地址只能改配置文件并重启",
	"server.addr":    "监听地址只能改配置文件并重启",
	"admin_password": "请在下方「修改密码」处更改，它会立即生效",
}

// RuntimeLimitsUpdate implements PATCH /admin/v1/settings/limits.
//
// Partial update: each supplied field is applied, absent fields are untouched.
// This keeps two admins editing different fields from clobbering each other.
func (h *Handlers) RuntimeLimitsUpdate(w http.ResponseWriter, r *http.Request) {
	if h.requireAdmin(w, r) == nil {
		return
	}
	if h.runtime == nil {
		failw(w, errs.New(http.StatusServiceUnavailable, errs.CodeFeatureDisabled,
			"runtime settings are not available"))
		return
	}

	var p runtimecfg.Patch
	if err := decodeBody(r, &p); err != nil {
		failw(w, errs.InvalidRequest("invalid body: "+err.Error()))
		return
	}
	if p.Empty() {
		failw(w, errs.InvalidRequest("no fields to update"))
		return
	}

	before := h.runtime.Get()
	next, err := h.runtime.Patch(p)
	if err != nil {
		// A negative cap is an operator mistake, not a server fault: 400 with the
		// field name is what lets the form point at the offending input.
		failw(w, errs.InvalidRequest(err.Error()))
		return
	}

	h.audit(r, "settings.limits.update", "settings", "limits",
		mustJSON(before.Limits), mustJSON(next.Limits))
	okw(w, runtimeSettingsView{
		Limits:      next.Limits,
		ReadOnly:    h.readOnlySettings(),
		MoneyUnit:   "micro-yuan (1 元 = 1000000)",
		ApplyNote:   "修改立即生效，无需重启",
		ConfigNotes: settingsReadOnly,
	})
}

// Settings now reports live-tunable values alongside the existing diagnostics.
func (h *Handlers) settingsPayload(r *http.Request) map[string]any {
	out := map[string]any{
		"flags":        map[string]bool{},
		"breaker_open": []uint64{},
		"key_salt_set": h.salt != "",
		"sealer_set":   h.sealer != nil,
		"version":      buildVersion,
		"time":         h.clk.Now().Unix(),
	}
	if h.flags != nil {
		flags := map[string]bool{}
		for _, k := range []string{
			FlagGatewayEnabled, FlagBillingEnforced,
			FlagModuleAutocreateEnabled, FlagRechargeEnabled,
		} {
			flags[k] = h.flags.Get(r.Context(), k, "")
		}
		out["flags"] = flags
	}
	if h.breaker != nil {
		out["breaker_open"] = h.breaker.OpenChannels()
	}
	if h.runtime != nil {
		out["limits"] = h.runtime.Limits()
		out["money_unit"] = "micro-yuan (1 元 = 1000000)"
	}
	out["read_only"] = h.readOnlySettings()
	out["config_notes"] = settingsReadOnly
	return out
}

// readOnlySettings names the values that require a restart, with their reasons.
func (h *Handlers) readOnlySettings() map[string]any {
	out := map[string]any{
		"version":      buildVersion,
		"key_salt_set": h.salt != "",
		"sealer_set":   h.sealer != nil,
	}
	if h.runtime != nil {
		out["limits_tunable"] = true
	}
	return out
}

// ChangePassword implements POST /admin/v1/password.
//
// Requires the current password, so a stolen session token alone cannot lock the
// real owner out. The new password is hashed with the same envelope as bootstrap.
func (h *Handlers) ChangePassword(w http.ResponseWriter, r *http.Request) {
	c := h.requireAdmin(w, r)
	if c == nil {
		return
	}

	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeBody(r, &req); err != nil {
		failw(w, errs.InvalidRequest("invalid body: "+err.Error()))
		return
	}
	if req.NewPassword == "" {
		failw(w, errs.InvalidRequest("new_password is required"))
		return
	}
	if err := validatePasswordStrength(req.NewPassword); err != nil {
		failw(w, errs.InvalidRequest(err.Error()))
		return
	}

	admin, err := h.store.Admins().GetByUsername(r.Context(), c.Username)
	if err != nil || admin == nil {
		failw(w, errs.Internal("load administrator", err))
		return
	}
	if !VerifyPassword(admin.PasswordHash, req.CurrentPassword) {
		// 401 rather than 400: this is an authentication failure, and the console
		// must not treat it as a validation hint.
		failw(w, errs.New(http.StatusUnauthorized, errs.CodeInvalidAPIKey,
			"current password is incorrect"))
		return
	}
	if req.CurrentPassword == req.NewPassword {
		failw(w, errs.InvalidRequest("new password must differ from the current one"))
		return
	}

	hash, herr := HashPassword(req.NewPassword)
	if herr != nil {
		failw(w, errs.Internal("hash password", herr))
		return
	}
	admin.PasswordHash = hash
	if uerr := h.store.Admins().Update(r.Context(), admin); uerr != nil {
		failw(w, errs.Internal("save administrator", uerr))
		return
	}

	// Never record the password itself, only that it changed.
	h.audit(r, "admin.password.change", "admin", c.Username, "", "")
	okw(w, map[string]any{"message": "密码已修改，请用新密码重新登录"})
}

// validatePasswordStrength rejects the passwords that make a console reachable
// from the internet trivially guessable.
//
// Deliberately modest: length plus "not the default" catches the real risk (an
// operator leaving admin/admin in place) without imposing composition rules that
// push people toward Post-it notes.
func validatePasswordStrength(pw string) error {
	if len(pw) < 8 {
		return errs.InvalidRequest("密码至少 8 位")
	}
	if len(pw) > 128 {
		return errs.InvalidRequest("密码不得超过 128 位")
	}
	lower := strings.ToLower(pw)
	for _, weak := range []string{"admin", "password", "12345678", "czapi", "qwerty"} {
		if lower == weak {
			return errs.InvalidRequest("这个密码太常见，请换一个")
		}
	}
	return nil
}
