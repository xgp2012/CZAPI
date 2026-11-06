package router

import (
	"embed"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
)

// WebAssets holds the embedded dashboard frontend assets.
type WebAssets struct {
	BuildFS   embed.FS
	IndexPage []byte
}

func SetWebRouter(router *gin.Engine, assets WebAssets, pluginDispatcher gin.HandlerFunc) {
	frontendFS := common.EmbedFolder(assets.BuildFS, "web/dist")
	compress := gzip.Gzip(gzip.DefaultCompression)

	router.NoRoute(
		pluginDispatcher,
		middleware.RouteTag("web"),
		func(c *gin.Context) {
			// ServeFrontendFiles sends build files already compressed.
			if !frontendFS.Exists("/", c.Request.URL.Path) {
				compress(c)
			}
		},
		middleware.AccessTokenAudit(),
		middleware.GlobalWebRateLimit(frontendFS),
		middleware.Cache(),
		middleware.ServeFrontendFiles(frontendFS),
		func(c *gin.Context) {
			// 这里判断"这是不是一个 API 路径"，而不是"要不要交给前端"。
			// 必须按路径段匹配：`/v10/login` 是前端的单点登录页，
			// 但它以 `/v1` 开头，用 HasPrefix("/v1") 判断会被误判成 relay 接口
			// 而返回 404（实测踩过）。
			if isAPIPath(c.Request.URL.Path) {
				controller.RelayNotFound(c)
				return
			}
			c.Header("Cache-Control", "no-cache")
			c.Data(http.StatusOK, "text/html; charset=utf-8", assets.IndexPage)
		},
	)
}

// isAPIPath 判断路径是否属于后端接口命名空间。
//
// 按整段比对而非字符串前缀：`/v1`、`/v10`、`/api`、`/assets` 这类命名的
// 共同点容易让人写 HasPrefix，但 `/v10/login` 与 `/v1beta` 恰好会被误伤。
func isAPIPath(path string) bool {
	for _, prefix := range []string{"/v1", "/api", "/assets"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}
