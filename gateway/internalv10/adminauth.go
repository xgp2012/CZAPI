package v10

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// 管理后台（/admin/）的账号体系。
//
// 与网关客户账号完全隔离：客户走 V10 SSO，运维走这里。不复用 new-api 的
// 用户表，是因为那张表里混着大量由 V10 托管的账户，运维账号一旦混进去，
// 任何一次"批量改用户"都可能误伤客户。
//
// 安全基线（OWASP ASVS / Authentication Cheat Sheet）：
//   - 口令只存 bcrypt 哈希，永不落明文；日志与审计里也不出现口令。
//   - 首次启动生成默认 admin/admin，但强制改密；改密前除豁免接口外一律拒绝。
//   - 登录失败按"账号 + 来源 IP"计数并在窗口内锁定，抵抗在线爆破。
//   - 会话 Cookie 为 HttpOnly + SameSite=Lax，库中只存 token 的 SHA-256。

const (
	adminSessionCookie = "czapi_admin"
	adminSessionTTL    = 12 * time.Hour

	adminLoginMaxFailures = 5
	adminLoginWindow      = 15 * time.Minute
	adminLoginLockout     = 15 * time.Minute
)

// AdminAccount 是后台运维账号。
type AdminAccount struct {
	Id           int    `json:"id" gorm:"primaryKey"`
	Username     string `json:"username" gorm:"type:varchar(32);uniqueIndex;not null"`
	PasswordHash string `json:"-" gorm:"type:varchar(100);not null"`

	// MustChangePassword 为 true 时，除改密/登出/取当前身份外所有管理接口都拒绝。
	MustChangePassword bool `json:"must_change_password" gorm:"not null;default:false"`

	LastLoginAt int64 `json:"last_login_at" gorm:"bigint;not null;default:0"`
	CreatedAt   int64 `json:"created_at" gorm:"bigint;not null;default:0"`
	UpdatedAt   int64 `json:"updated_at" gorm:"bigint;not null;default:0"`
}

func (AdminAccount) TableName() string { return "v10_admin_accounts" }

// AdminSession 是后台会话。只存 token 的 SHA-256，库被读走也无法直接冒用。
type AdminSession struct {
	Id        int    `json:"id" gorm:"primaryKey"`
	TokenHash string `json:"-" gorm:"type:varchar(64);uniqueIndex;not null"`
	AdminId   int    `json:"admin_id" gorm:"index;not null"`
	ExpiresAt int64  `json:"expires_at" gorm:"bigint;not null;default:0"`
	CreatedAt int64  `json:"created_at" gorm:"bigint;not null;default:0"`
}

func (AdminSession) TableName() string { return "v10_admin_sessions" }

// AdminTables 供 AutoMigrate 使用。
func AdminTables() []any {
	return []any{&AdminAccount{}, &AdminSession{}}
}

var (
	ErrAdminBadCredentials     = errors.New("用户名或密码不正确")
	ErrAdminLocked             = errors.New("登录失败次数过多，请稍后再试")
	ErrAdminWeakPassword       = errors.New("密码至少 8 位，且不能与用户名相同")
	ErrAdminUnauthorized       = errors.New("请先登录")
	ErrAdminMustChangePassword = errors.New("请先修改默认密码")
	ErrAdminBadOrigin          = errors.New("请求来源不合法")
)

type adminLoginFailure struct {
	count       int
	firstAt     time.Time
	lockedUntil time.Time
}

var (
	adminFailureMu sync.Mutex
	adminFailures  = map[string]*adminLoginFailure{}
)

// 单副本进程内计数即可。多副本时各副本独立计数，攻击者能放大尝试次数，
// 因此多副本部署应在反向代理层再加一道限流。
func adminFailureKey(username, ip string) string {
	return strings.ToLower(strings.TrimSpace(username)) + "|" + ip
}

// checkAdminLoginAllowed 在验证口令前先判断是否已锁定，省掉无谓的 bcrypt 计算。
func checkAdminLoginAllowed(username, ip string) error {
	adminFailureMu.Lock()
	defer adminFailureMu.Unlock()

	key := adminFailureKey(username, ip)
	rec, ok := adminFailures[key]
	if !ok {
		return nil
	}
	if time.Now().Before(rec.lockedUntil) {
		return ErrAdminLocked
	}
	if time.Since(rec.firstAt) > adminLoginWindow {
		delete(adminFailures, key)
	}
	return nil
}

func recordAdminLoginFailure(username, ip string) {
	adminFailureMu.Lock()
	defer adminFailureMu.Unlock()

	key := adminFailureKey(username, ip)
	rec, ok := adminFailures[key]
	if !ok || time.Since(rec.firstAt) > adminLoginWindow {
		adminFailures[key] = &adminLoginFailure{count: 1, firstAt: time.Now()}
		return
	}
	rec.count++
	if rec.count >= adminLoginMaxFailures {
		rec.lockedUntil = time.Now().Add(adminLoginLockout)
	}
}

func clearAdminLoginFailures(username, ip string) {
	adminFailureMu.Lock()
	defer adminFailureMu.Unlock()
	delete(adminFailures, adminFailureKey(username, ip))
}

// EnsureAdminAccountBootstrap 保证后台至少有一个可用账号。
//
// 默认 admin/admin，标记必须改密。可用环境变量覆盖：
// ADMIN_BOOTSTRAP_USERNAME / ADMIN_BOOTSTRAP_PASSWORD。
// 同名账号已存在时**不覆盖**，避免重启把运维改过的口令重置回默认值。
func EnsureAdminAccountBootstrap() error {
	username := strings.TrimSpace(os.Getenv("ADMIN_BOOTSTRAP_USERNAME"))
	if username == "" {
		username = "admin"
	}
	password := os.Getenv("ADMIN_BOOTSTRAP_PASSWORD")
	if password == "" {
		password = "admin"
	}

	var count int64
	if err := model.DB.Model(&AdminAccount{}).Where("username = ?", username).
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	now := time.Now().Unix()
	account := AdminAccount{
		Username:           username,
		PasswordHash:       string(hash),
		MustChangePassword: password == "admin",
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := model.DB.Create(&account).Error; err != nil {
		return err
	}

	common.SysLog(common.LogText(
		"v10: admin console account %q created; change the default password on first login",
		username))
	if password == "admin" {
		// 显式告警：默认口令在内网可能"够用"，但后台端口一旦暴露到公网，
		// 任何人都可能在运维之前登录并改掉口令，把真正的管理员锁在外面。
		common.SysError(common.LogText(
			"v10: admin console still uses the default password; set ADMIN_BOOTSTRAP_PASSWORD before exposing /admin/ to the internet"))
	}
	return nil
}

// CreateAdminSession 生成会话 token，返回原始值供写入 Cookie。
//
// 复用 sso.go 的 hashToken：库里只留 SHA-256，被读走也无法直接冒用会话。
func CreateAdminSession(c *gin.Context, adminId int) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)

	now := time.Now()
	session := AdminSession{
		TokenHash: hashToken(token),
		AdminId:   adminId,
		ExpiresAt: now.Add(adminSessionTTL).Unix(),
		CreatedAt: now.Unix(),
	}
	if err := model.DB.Create(&session).Error; err != nil {
		return "", err
	}

	secure := c.Request.TLS != nil ||
		strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(adminSessionCookie, token, int(adminSessionTTL.Seconds()), "/", "", secure, true)
	return token, nil
}

// CurrentAdmin 从 Cookie 还原当前登录的后台账号；nil 表示未登录或已过期。
func CurrentAdmin(c *gin.Context) *AdminAccount {
	token, err := c.Cookie(adminSessionCookie)
	if err != nil || token == "" {
		return nil
	}

	var session AdminSession
	if err := model.DB.Where("token_hash = ? AND expires_at > ?",
		hashToken(token), time.Now().Unix()).First(&session).Error; err != nil {
		return nil
	}

	var account AdminAccount
	if err := model.DB.First(&account, session.AdminId).Error; err != nil {
		return nil
	}
	return &account
}

// DestroyAdminSession 同时清库与 Cookie，保证登出后旧 token 立即失效。
func DestroyAdminSession(c *gin.Context) {
	if token, err := c.Cookie(adminSessionCookie); err == nil && token != "" {
		model.DB.Where("token_hash = ?", hashToken(token)).Delete(&AdminSession{})
	}
	secure := c.Request.TLS != nil ||
		strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(adminSessionCookie, "", -1, "/", "", secure, true)
}

// isAdminSessionExemptPath 列出"必须改密"状态下仍然允许访问的接口。
// 少了这条豁免，用户会卡在"要改密但改密接口也被拦"的死循环里。
func isAdminSessionExemptPath(path string) bool {
	switch path {
	case "/api/v10/console/password",
		"/api/v10/console/logout",
		"/api/v10/console/me",
		"/api/v10/console/login":
		return true
	}
	return false
}

// RequireAdmin 是后台所有管理接口的鉴权中间件。
//
// 必须返回 401/403 而不是 new-api 惯用的 HTTP 200 + success:false：
// 前者是前端拦截器判断"会话失效、请重新登录"的唯一依据。若一律回 200，
// 前端无从区分"没登录"和"业务报错"，只能在每个页面各自猜。
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		account := CurrentAdmin(c)
		if account == nil {
			common.ApiErrorStatus(c, http.StatusUnauthorized, ErrAdminUnauthorized)
			c.Abort()
			return
		}

		if account.MustChangePassword && !isAdminSessionExemptPath(c.Request.URL.Path) {
			common.ApiErrorStatus(c, http.StatusForbidden, ErrAdminMustChangePassword)
			c.Abort()
			return
		}

		// CSRF 纵深防御：Cookie 是 SameSite=Lax，跨站 POST 本来就不带 Cookie，
		// 但 Lax 对顶级导航的 GET 仍会带上。凡是改状态的方法，再校验一次
		// Origin 必须与 Host 同源，防止边缘场景（旧浏览器、代理改写）被利用。
		if isStateChanging(c.Request.Method) && !isSameOrigin(c) {
			common.ApiErrorStatus(c, http.StatusForbidden, ErrAdminBadOrigin)
			c.Abort()
			return
		}

		c.Set("admin_account", account)
		c.Next()
	}
}

func isStateChanging(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// isSameOrigin 在带 Origin 头时要求其 host 与请求 Host 一致。
// 没有 Origin 头（如 curl、同源老浏览器）时放行——此时不存在浏览器发起的
// 跨站表单提交，而这类请求本来也拿不到会话 Cookie。
func isSameOrigin(c *gin.Context) bool {
	origin := strings.TrimSpace(c.GetHeader("Origin"))
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, c.Request.Host)
}

// CleanupExpiredAdminSessions 清过期会话，避免表无限增长。
func CleanupExpiredAdminSessions() {
	res := model.DB.Where("expires_at <= ?", time.Now().Unix()).Delete(&AdminSession{})
	if res.Error == nil && res.RowsAffected > 0 {
		common.SysLog(common.LogText("v10: cleaned %d expired admin sessions", res.RowsAffected))
	}
}
