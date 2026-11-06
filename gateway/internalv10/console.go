package v10

import (
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// 管理后台（/admin/）的 API。
//
// 与 /api/v10/admin/* 的区别：那一组复用 new-api 的管理员会话，给"顺手调个
// 接口"的场景用；这一组是独立后台自己的会话，供 Vue 控制台使用。
// 两组最终都落到同一批 internalv10 业务函数上，不存在两套逻辑。

// RegisterConsole 挂载后台接口。
//
// 分为两层：未登录也能访问的登录接口，和必须登录的管理接口。
func RegisterConsole(public *gin.RouterGroup, authed *gin.RouterGroup) {
	public.POST("/login", handleConsoleLogin)
	public.GET("/bootstrap", handleConsoleBootstrap)

	authed.GET("/me", handleConsoleMe)
	authed.POST("/logout", handleConsoleLogout)
	authed.POST("/password", handleConsoleChangePassword)

	// 概览
	authed.GET("/overview", handleConsoleOverview)

	// 站点
	authed.GET("/sites", handleConsoleListSites)
	authed.POST("/sites", handleConsoleCreateSite)
	authed.POST("/sites/:id/secret", handleConsoleRotateSecret)
	authed.POST("/sites/:id/status", handleConsoleSetSiteStatus)

	// 账户与额度
	authed.GET("/accounts", handleConsoleListAccounts)
	authed.POST("/accounts/:userId/credit", handleConsoleManualCredit)

	// 账本
	authed.GET("/ledger", handleConsoleListLedger)

	// Key
	authed.GET("/keys", handleConsoleListKeys)
	authed.POST("/keys/:id/revoke", handleConsoleRevokeKey)

	// host / 订阅
	authed.GET("/hosts", handleConsoleListHosts)
	authed.POST("/hosts/:id/status", handleConsoleSetHostStatus)

	// 用量
	authed.GET("/usage/summary", handleConsoleUsageSummary)
	authed.GET("/usage/models", handleConsoleUsageByModel)
}

// handleConsoleBootstrap 给前端一个"是否需要初始化"的探针。
//
// 前端加载时先问这个，避免把登录页闪一下又跳走。
func handleConsoleBootstrap(c *gin.Context) {
	var count int64
	if err := model.DB.Model(&AdminAccount{}).Count(&count).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{
		"has_admin": count > 0,
		"version":   common.Version,
	})
}

type consoleLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func handleConsoleLogin(c *gin.Context) {
	var req consoleLoginRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorMsg(c, "请求格式不正确")
		return
	}

	username := strings.TrimSpace(req.Username)
	if username == "" || req.Password == "" {
		common.ApiErrorMsg(c, ErrAdminBadCredentials.Error())
		return
	}

	ip := c.ClientIP()
	if err := checkAdminLoginAllowed(username, ip); err != nil {
		common.ApiErrorMsg(c, err.Error())
		return
	}

	var account AdminAccount
	err := model.DB.Where("username = ?", username).First(&account).Error
	// 账号不存在与口令错误返回同一句话，避免账号枚举。
	if err != nil {
		recordAdminLoginFailure(username, ip)
		common.ApiErrorMsg(c, ErrAdminBadCredentials.Error())
		return
	}

	if bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte(req.Password)) != nil {
		recordAdminLoginFailure(username, ip)
		common.ApiErrorMsg(c, ErrAdminBadCredentials.Error())
		return
	}

	clearAdminLoginFailures(username, ip)

	if _, err := CreateAdminSession(c, account.Id); err != nil {
		common.ApiError(c, err)
		return
	}

	model.DB.Model(&AdminAccount{}).Where("id = ?", account.Id).
		Update("last_login_at", time.Now().Unix())

	common.ApiSuccess(c, gin.H{
		"id":                   account.Id,
		"username":             account.Username,
		"must_change_password": account.MustChangePassword,
	})
}

func handleConsoleMe(c *gin.Context) {
	account := CurrentAdmin(c)
	if account == nil {
		common.ApiErrorStatus(c, http.StatusUnauthorized, ErrAdminUnauthorized)
		return
	}
	common.ApiSuccess(c, gin.H{
		"id":                   account.Id,
		"username":             account.Username,
		"must_change_password": account.MustChangePassword,
		"last_login_at":        account.LastLoginAt,
		"version":              common.Version,
	})
}

func handleConsoleLogout(c *gin.Context) {
	DestroyAdminSession(c)
	common.ApiSuccess(c, nil)
}

type consoleChangePasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

func handleConsoleChangePassword(c *gin.Context) {
	account := CurrentAdmin(c)
	if account == nil {
		common.ApiErrorStatus(c, http.StatusUnauthorized, ErrAdminUnauthorized)
		return
	}

	var req consoleChangePasswordRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorMsg(c, "请求格式不正确")
		return
	}

	// 修改敏感凭据必须重新验证原口令，防止会话被劫持后直接改密。
	if bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte(req.OldPassword)) != nil {
		common.ApiErrorMsg(c, "原密码不正确")
		return
	}

	newPassword := req.NewPassword
	if len(newPassword) < 8 {
		common.ApiErrorMsg(c, ErrAdminWeakPassword.Error())
		return
	}
	if strings.EqualFold(newPassword, account.Username) {
		common.ApiErrorMsg(c, ErrAdminWeakPassword.Error())
		return
	}
	if newPassword == req.OldPassword {
		common.ApiErrorMsg(c, "新密码不能与原密码相同")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	err = model.DB.Model(&AdminAccount{}).Where("id = ?", account.Id).Updates(map[string]any{
		"password_hash":        string(hash),
		"must_change_password": false,
		"updated_at":           time.Now().Unix(),
	}).Error
	if err != nil {
		common.ApiError(c, err)
		return
	}

	// 改密后作废其它会话：若口令因泄露而被动过，攻击者的会话也随之失效。
	// 当前会话保留，避免改完立刻被踢下线。
	current, _ := c.Cookie(adminSessionCookie)
	model.DB.Where("admin_id = ? AND token_hash <> ?", account.Id, hashToken(current)).
		Delete(&AdminSession{})

	common.ApiSuccess(c, nil)
}

// handleConsoleOverview 是后台首页的汇总数字。
func handleConsoleOverview(c *gin.Context) {
	var siteCount, accountCount, hostCount, activeHostCount int64
	model.DB.Model(&V10Site{}).Count(&siteCount)
	model.DB.Model(&V10Account{}).Count(&accountCount)
	model.DB.Model(&V10Host{}).Count(&hostCount)
	model.DB.Model(&V10Host{}).Where("host_status = ?", "active").Count(&activeHostCount)

	// 今日入账净额（quota 单位）。
	// 必须按本地时区的 00:00 取整：Truncate(24h) 以 UTC 零点为基准，
	// 在东八区会把"今天"错算成前一天 08:00 起，日报数字对不上财务。
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var todayDelta int64
	model.DB.Model(&V10Ref{}).
		Where("created_at >= ?", todayStart.Unix()).
		Select("COALESCE(SUM(delta_quota), 0)").Scan(&todayDelta)

	var refCount int64
	model.DB.Model(&V10Ref{}).Count(&refCount)

	common.ApiSuccess(c, gin.H{
		"site_count":        siteCount,
		"account_count":     accountCount,
		"host_count":        hostCount,
		"active_host_count": activeHostCount,
		"today_delta_quota": todayDelta,
		"ref_count":         refCount,
		"quota_per_unit":    common.QuotaPerUnit,
	})
}
