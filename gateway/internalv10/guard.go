package v10

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// 本文件是"钱不过网关"这条边界在**运行期**的强制点。
//
// 背景：new-api 自带的 /api/user/topup 其实是**卡密兑换**，只由
// operation_setting.IsPaymentComplianceConfirmed() 一个条件把守。
// 而运维为了建订阅套餐**必须**先确认合规（否则 /api/subscription/admin/plans
// 直接 403），确认之后这条路径对所有登录用户就都打开了。
//
// 实测确认过：V10 客户拿着 SSO 会话，可以直接用网关卡密给自己加额度，
// 完全绕过 V10 的订单流程。这不是"配置没关"，而是结构上没拦。
//
// 因此这里做的是**身份判定**拦截，而不是依赖某个配置项：
// 凡是由 V10 托管的账户，一律禁止使用网关自带的付费/兑换入口。
// 即使运维把开关全打开、甚至配置了支付渠道，也依然拦住。

// IsManagedAccount 判断某个用户是否由 V10 托管。
//
// 权威来源是 v10_accounts 表。表里查不到时**按托管处理**：
// 账号同步与用户创建之间存在极短的窗口，而错误放行比错误拦截危险得多——
// 拦截只会让一个页面报错，放行会让钱绕过账本。
//
// 注意不要用用户名前缀来判断：accountUsername 用的是 site.AppId 作前缀，
// 只有 app_id 恰好是 "v10" 时用户名才以 v10 开头。靠前缀会漏判。
func IsManagedAccount(userId int) bool {
	if userId <= 0 {
		return false
	}

	var count int64
	err := model.DB.Model(&V10Account{}).Where("user_id = ?", userId).Count(&count).Error
	if err == nil {
		return count > 0
	}
	// 查询失败时无法判定，按托管处理（fail-closed）。
	return true
}

// RequireSelfServiceAllowed 是给 new-api 自带付费入口用的中间件。
//
// 挂在 /api/user/topup 之类的路径上：V10 托管账户一律 403。
// 提示语明确指向 V10，避免客户以为是网关坏了。
func RequireSelfServiceAllowed() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !Enabled() {
			c.Next()
			return
		}
		userId := c.GetInt("id")
		if !IsManagedAccount(userId) {
			c.Next()
			return
		}
		common.ApiErrorMsg(c, "This account's balance is managed by the business system. Please top up from there.")
		c.Abort()
	}
}
