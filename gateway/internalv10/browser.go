package v10

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// RegisterBrowser 挂载浏览器侧的唯一入口：用一次性票据换会话。
//
// 这条路由**不走签名**，因为调用方是浏览器而不是 V10 服务端。它的安全性
// 完全来自票据本身：票据是一次性的、短时的、由签名接口签发的。浏览器即使
// 猜到这条路径，没有票据也换不到任何东西。
func RegisterBrowser(group *gin.RouterGroup) {
	browser := group.Group("/api/v10")
	{
		browser.POST("/sso/exchange", handleExchangeTicket)
	}
}

// handleExchangeTicket 消费票据并建立网关会话。
//
// 成功后浏览器拿到的就是一个正常的 new-api 会话（refresh cookie + access token），
// 之后所有控制台接口都用它，无需再区分"这是 V10 来的用户"。
func handleExchangeTicket(c *gin.Context) {
	var req struct {
		Ticket string `json:"ticket"`
		AppId  string `json:"app_id"`
	}
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorT(c, "Invalid request body")
		return
	}

	site, err := GetSiteByAppId(req.AppId)
	if err != nil {
		common.ApiErrorT(c, "Invalid login ticket")
		return
	}
	if site.Status != V10SiteStatusEnabled {
		common.ApiErrorT(c, "Invalid login ticket")
		return
	}

	userId, err := ConsumeLoginTicket(site, req.Ticket)
	if err != nil {
		common.ApiErrorT(c, "Invalid login ticket")
		return
	}

	user, err := model.GetUserById(userId, true)
	if err != nil {
		common.ApiErrorT(c, "Invalid login ticket")
		return
	}
	if user.Status != common.UserStatusEnabled {
		common.ApiErrorT(c, "User has been banned")
		return
	}

	bundle, err := service.CreateLoginSession(
		user.Id,
		"v10",
		c.ClientIP(),
		c.Request.UserAgent(),
	)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	model.UpdateUserLastLoginAt(user.Id)
	service.WriteRefreshCookie(c, bundle.RefreshToken)

	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"access_token":      bundle.AccessToken,
			"token_type":        bundle.TokenType,
			"access_expires_at": bundle.AccessExpiresAt,
			"session":           bundle.Session,
			"user": gin.H{
				"id":           user.Id,
				"username":     user.Username,
				"display_name": user.DisplayName,
				"role":         user.Role,
			},
		},
	})
}
