package v10

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// 后台运营面的统一鉴权。
//
// 历史原因，V10 运营功能曾有一套独立前端（admin-ui/，Vue3）与一套独立账号
// （v10_admin_accounts 表 + czapi_admin Cookie）。融合之后，React 控制台里
// 的 V10 运营页复用 new-api 的管理员会话，不再要求运维登录两次。
//
// 因此这里接受**两种**凭据，落到同一批业务函数：
//   - 新：new-api 控制台会话（middleware.AdminAuth/TryUserAuth 写入 role/username）
//   - 旧：czapi_admin Cookie 会话（v10_admin_accounts），保留给 /admin/ 旧前端
//
// 两种都识别不出来的请求一律 401：前端靠状态码判断"会话失效"，不能回 200。

// consoleOperatorContextKey 存放当前操作人的用户名，供审计与手工调额留痕。
const consoleOperatorContextKey = "v10_console_operator"

// dashboardAdminName 从 new-api 会话上下文里取管理员名。
//
// 返回 false 表示当前请求没有 new-api 管理员会话（可能是匿名，也可能只是
// 普通用户）。普通用户不能进运营面：运营面能看到全部客户的额度与 Key。
func dashboardAdminName(c *gin.Context) (string, bool) {
	roleValue, exists := c.Get("role")
	if !exists {
		return "", false
	}

	role, ok := toInt(roleValue)
	if !ok || role < common.RoleAdminUser {
		return "", false
	}

	if nameValue, exists := c.Get("username"); exists {
		if name, ok := nameValue.(string); ok && name != "" {
			return name, true
		}
	}
	// role 达标但取不到用户名，说明上下文不完整，按未登录处理更安全。
	return "", false
}

// toInt 兼容不同来源写入的整型（int / int64 / float64）。
func toInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int8:
		return int(v), true
	case int16:
		return int(v), true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case uint:
		return int(v), true
	case uint64:
		return int(v), true
	case float64:
		return int(v), true
	}
	return 0, false
}

// ConsoleOperator 返回当前运营操作人的名字。
//
// 优先用本中间件写入的名字；取不到时回落到旧 Cookie 会话，最后给 "console"。
// 只用于日志与账本备注，不参与鉴权判断。
func ConsoleOperator(c *gin.Context) string {
	if value, exists := c.Get(consoleOperatorContextKey); exists {
		if name, ok := value.(string); ok && name != "" {
			return name
		}
	}
	if account := CurrentAdmin(c); account != nil {
		return account.Username
	}
	return "console"
}

// RequireConsoleAdmin 是运营面接口的鉴权中间件（双凭据）。
//
// 新会话（new-api）走 role 判定；旧会话（czapi_admin）除了身份还要满足
// 强制改密与同源校验，保持与 admin-ui 时期完全一致的安全语义。
func RequireConsoleAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 1) 旧 Cookie 会话：语义不变，先按原规则全量校验。
		if account := CurrentAdmin(c); account != nil {
			if account.MustChangePassword && !isAdminSessionExemptPath(c.Request.URL.Path) {
				common.ApiErrorStatus(c, http.StatusForbidden, ErrAdminMustChangePassword)
				c.Abort()
				return
			}
			if isStateChanging(c.Request.Method) && !isSameOrigin(c) {
				common.ApiErrorStatus(c, http.StatusForbidden, ErrAdminBadOrigin)
				c.Abort()
				return
			}
			c.Set(consoleOperatorContextKey, account.Username)
			c.Next()
			return
		}

		// 2) new-api 控制台管理员会话：已由中间件完成口令、状态与权限校验。
		if name, ok := dashboardAdminName(c); ok {
			c.Set(consoleOperatorContextKey, name)
			c.Next()
			return
		}

		common.ApiErrorStatus(c, http.StatusUnauthorized, ErrAdminUnauthorized)
		c.Abort()
	}
}
