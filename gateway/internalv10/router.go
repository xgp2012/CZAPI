package v10

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// Register 挂载 /internal/v10 全部路由。
//
// 这一层只服务于 V10 服务端。它**不挂 CORS**：浏览器不该也不能调用它，
// 没有 CORS 头就等于连预检都过不去，少一个被前端脚本利用的面。
func Register(group *gin.RouterGroup) {
	internal := group.Group("/internal/v10")

	// 健康探测免签名，供 V10 插件的"测试连接"按钮使用。
	internal.GET("/health", handleHealth)

	authed := internal.Group("")
	authed.Use(SiteAuth())
	{
		authed.POST("/accounts/sync", handleAccountSync)
		authed.GET("/accounts/quota", handleAccountQuota)

		authed.POST("/credit", handleCredit)

		authed.POST("/keys", handleKeyCreate)
		authed.GET("/keys", handleKeyList)
		authed.PATCH("/keys/:id", handleKeyUpdate)
		authed.DELETE("/keys/:id", handleKeyRevoke)

		// 只读数据：V10 前台渲染模型下拉与账单页用。
		authed.GET("/models", handleModels)
		authed.GET("/usage/summary", handleUsageSummary)
		authed.GET("/usage/logs", handleUsageLogs)

		authed.POST("/hosts", handleHostBind)
		authed.POST("/hosts/:id/status", handleHostStatus)
		authed.GET("/hosts/:id/status", handleHostStatus)

		// 单点登录：V10 服务端换取一张一次性登录链接。
		authed.POST("/sso/ticket", handleIssueLoginTicket)
	}
}

// handleIssueLoginTicket 由 V10 服务端调用，签一张一次性登录票据。
//
// 返回值里同时给 login_url，V10 前台直接把它当跳转目标即可，
// 不需要自己拼路径；也避免 V10 与网关对 URL 结构各自理解。
func handleIssueLoginTicket(c *gin.Context) {
	site := SiteFrom(c)

	var req struct {
		ClientId int64 `json:"client_id"`
	}
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		replyError(c, http.StatusBadRequest, CodeParamError, "invalid json body")
		return
	}

	ticket, expiresAt, err := IssueLoginTicket(site, req.ClientId)
	if err != nil {
		if err == ErrAccountNotFound {
			replyError(c, http.StatusNotFound, CodeNotFound, "account not found")
			return
		}
		replyError(c, http.StatusBadRequest, CodeParamError, err.Error())
		return
	}

	replyOK(c, gin.H{
		"ticket":     ticket,
		"expires_at": expiresAt,
		"login_url":  LoginURL(ConsoleBaseUrl(c), ticket),
	})
}

func handleHealth(c *gin.Context) {
	replyOK(c, gin.H{
		"status":  "ok",
		"time":    common.GetTimestamp(),
		"db":      dbHealth(),
		"version": common.Version,
	})
}

// dbHealth 用一个最轻的查询确认数据库可用，不暴露任何业务数据。
func dbHealth() string {
	if model_DBIsReady() {
		return "ok"
	}
	return "down"
}

type accountSyncRequest struct {
	ClientId    int64  `json:"client_id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}

func handleAccountSync(c *gin.Context) {
	site := SiteFrom(c)

	var req accountSyncRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		replyError(c, http.StatusBadRequest, CodeParamError, "invalid json body")
		return
	}

	account, err := EnsureAccount(site, req.ClientId, req.DisplayName, req.Email)
	if err != nil {
		replyError(c, http.StatusBadRequest, CodeParamError, err.Error())
		return
	}

	user, err := getUserBrief(account.UserId)
	if err != nil {
		replyError(c, http.StatusInternalServerError, CodeInternal, "cannot read account")
		return
	}
	replyOK(c, user)
}

func handleAccountQuota(c *gin.Context) {
	site := SiteFrom(c)

	clientId, err := strconv.ParseInt(c.Query("client_id"), 10, 64)
	if err != nil || clientId <= 0 {
		replyError(c, http.StatusBadRequest, CodeParamError, "client_id is required")
		return
	}

	quota, err := GetAccountQuota(site, clientId)
	if err != nil {
		replyError(c, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	if quota == nil {
		replyError(c, http.StatusNotFound, CodeNotFound, "account not found")
		return
	}
	replyOK(c, quota)
}

func handleCredit(c *gin.Context) {
	site := SiteFrom(c)

	var req CreditRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		replyError(c, http.StatusBadRequest, CodeParamError, "invalid json body")
		return
	}

	result, err := ApplyCredit(site, req)
	if err != nil {
		switch {
		case err == ErrMissingRef:
			replyError(c, http.StatusBadRequest, CodeMissingRef, "ref_type and ref_id are required")
		case err == ErrInvalidAmount:
			replyError(c, http.StatusBadRequest, CodeParamError, "amount_micros must be non-zero")
		case err == ErrAmountTooLarge:
			// 明确告知上限与拆分建议，避免运维误以为是网关故障。
			replyError(c, http.StatusBadRequest, CodeParamError,
				fmt.Sprintf("amount_micros exceeds the largest single credit the gateway can store (max %d quota units); split it into multiple credits with distinct ref_id",
					maxSingleCreditQuota))
		case err == ErrNegativeQuota:
			replyError(c, http.StatusPaymentRequired, CodeInsufficient, "insufficient balance")
		case err == ErrRefConflict:
			replyError(c, http.StatusConflict, CodeRefConflict, "ref already applied with different parameters")
		default:
			common.SysError(common.LogText("v10: credit failed site=%s ref=%s/%s: %s",
				site.AppId, req.RefType, req.RefId, err.Error()))
			replyError(c, http.StatusBadRequest, CodeParamError, err.Error())
		}
		return
	}
	replyOK(c, result)
}

type keyCreateRequest struct {
	ClientId    int64    `json:"client_id"`
	HostId      int64    `json:"host_id"`
	Name        string   `json:"name"`
	Models      []string `json:"models"`
	IpWhitelist []string `json:"ip_whitelist"`
	ExpiresAt   int64    `json:"expires_at"`
	MaxKeys     int      `json:"max_keys"`
}

func handleKeyCreate(c *gin.Context) {
	site := SiteFrom(c)

	var req keyCreateRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		replyError(c, http.StatusBadRequest, CodeParamError, "invalid json body")
		return
	}

	created, err := CreateKey(site, CreateKeyRequest{
		ClientId:    req.ClientId,
		HostId:      req.HostId,
		Name:        req.Name,
		Model:       req.Models,
		IpWhitelist: req.IpWhitelist,
		ExpiresAt:   req.ExpiresAt,
		MaxKeys:     req.MaxKeys,
	})
	if err != nil {
		if err == ErrKeyLimitReached {
			replyError(c, http.StatusBadRequest, CodeParamError, "key limit reached for this account")
			return
		}
		replyError(c, http.StatusBadRequest, CodeParamError, err.Error())
		return
	}
	replyOK(c, created)
}

func handleKeyList(c *gin.Context) {
	site := SiteFrom(c)

	clientId, err := strconv.ParseInt(c.Query("client_id"), 10, 64)
	if err != nil || clientId <= 0 {
		replyError(c, http.StatusBadRequest, CodeParamError, "client_id is required")
		return
	}

	keys, err := ListKeys(site, clientId)
	if err != nil {
		replyError(c, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	replyOK(c, gin.H{"total": len(keys), "items": keys})
}

func handleKeyUpdate(c *gin.Context) {
	site := SiteFrom(c)

	keyId, err := strconv.Atoi(c.Param("id"))
	if err != nil || keyId <= 0 {
		replyError(c, http.StatusBadRequest, CodeParamError, "invalid key id")
		return
	}

	var req UpdateKeyRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		replyError(c, http.StatusBadRequest, CodeParamError, "invalid json body")
		return
	}

	view, err := UpdateKey(site, keyId, req)
	if err != nil {
		if err == ErrKeyNotFound {
			replyError(c, http.StatusNotFound, CodeNotFound, "key not found")
			return
		}
		replyError(c, http.StatusBadRequest, CodeParamError, err.Error())
		return
	}
	replyOK(c, view)
}

func handleKeyRevoke(c *gin.Context) {
	site := SiteFrom(c)

	keyId, err := strconv.Atoi(c.Param("id"))
	if err != nil || keyId <= 0 {
		replyError(c, http.StatusBadRequest, CodeParamError, "invalid key id")
		return
	}

	clientId, err := strconv.ParseInt(c.Query("client_id"), 10, 64)
	if err != nil || clientId <= 0 {
		replyError(c, http.StatusBadRequest, CodeParamError, "client_id is required")
		return
	}

	if err := RevokeKey(site, clientId, keyId); err != nil {
		if err == ErrKeyNotFound {
			replyError(c, http.StatusNotFound, CodeNotFound, "key not found")
			return
		}
		replyError(c, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	replyOK(c, gin.H{"key_id": keyId, "status": KeyStatusRevoked})
}

type hostBindRequest struct {
	ClientId     int64  `json:"client_id"`
	HostId       int64  `json:"host_id"`
	PlanId       int    `json:"plan_id"`
	DurationDays int    `json:"duration_days"`
	RefType      string `json:"ref_type"`
	RefId        string `json:"ref_id"`
}

func handleHostBind(c *gin.Context) {
	site := SiteFrom(c)

	var req hostBindRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		replyError(c, http.StatusBadRequest, CodeParamError, "invalid json body")
		return
	}

	summary, err := BindHost(site, BindHostRequest{
		ClientId:     req.ClientId,
		HostId:       req.HostId,
		PlanId:       req.PlanId,
		DurationDays: req.DurationDays,
		RefType:      req.RefType,
		RefId:        req.RefId,
	})
	if err != nil {
		replyError(c, http.StatusBadRequest, CodeParamError, err.Error())
		return
	}
	replyOK(c, summary)
}

// handleModels 返回可售模型目录，供 V10 前台渲染"可选模型"。
func handleModels(c *gin.Context) {
	models := ListModels()
	replyOK(c, gin.H{"total": len(models), "items": models})
}

// handleUsageSummary 返回用量合计与按 day/model/token 分组的明细。
func handleUsageSummary(c *gin.Context) {
	site := SiteFrom(c)

	clientId, err := strconv.ParseInt(c.Query("client_id"), 10, 64)
	if err != nil || clientId <= 0 {
		replyError(c, http.StatusBadRequest, CodeParamError, "client_id is required")
		return
	}

	from, _ := strconv.ParseInt(c.Query("from"), 10, 64)
	to, _ := strconv.ParseInt(c.Query("to"), 10, 64)

	summary, err := GetUsageSummary(site, clientId, from, to, c.Query("group_by"))
	if err != nil {
		replyError(c, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	if summary == nil {
		replyError(c, http.StatusNotFound, CodeNotFound, "account not found")
		return
	}
	replyOK(c, summary)
}

// handleUsageLogs 分页返回用量明细，不含请求正文。
func handleUsageLogs(c *gin.Context) {
	site := SiteFrom(c)

	clientId, err := strconv.ParseInt(c.Query("client_id"), 10, 64)
	if err != nil || clientId <= 0 {
		replyError(c, http.StatusBadRequest, CodeParamError, "client_id is required")
		return
	}

	from, _ := strconv.ParseInt(c.Query("from"), 10, 64)
	to, _ := strconv.ParseInt(c.Query("to"), 10, 64)
	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("page_size"))

	logs, err := GetUsageLogs(site, clientId, from, to, page, pageSize, c.Query("model"))
	if err != nil {
		replyError(c, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	if logs == nil {
		replyError(c, http.StatusNotFound, CodeNotFound, "account not found")
		return
	}
	replyOK(c, logs)
}

type hostStatusRequest struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func handleHostStatus(c *gin.Context) {
	site := SiteFrom(c)

	hostId, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || hostId <= 0 {
		replyError(c, http.StatusBadRequest, CodeParamError, "invalid host id")
		return
	}

	status := c.Query("status")
	reason := c.Query("reason")
	if c.Request.Method == http.MethodPost {
		var req hostStatusRequest
		if err := common.DecodeJson(c.Request.Body, &req); err == nil {
			if req.Status != "" {
				status = req.Status
			}
			reason = req.Reason
		}
	}
	if strings.TrimSpace(status) == "" {
		replyError(c, http.StatusBadRequest, CodeParamError, "status is required")
		return
	}

	summary, err := SetHostStatus(site, hostId, status, reason)
	if err != nil {
		replyError(c, http.StatusBadRequest, CodeParamError, err.Error())
		return
	}
	replyOK(c, summary)
}
