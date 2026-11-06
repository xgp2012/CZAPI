// Package adminui 只负责把管理后台前端产物嵌进二进制。
//
// 单独成包的理由：//go:embed 只能引用所在目录及其子目录，而前端源码与
// 构建配置在 admin-ui/。把产物输出到本包的 dist/ 下，Go 侧才能 embed 到。
//
// 构建：cd admin-ui && bun run build   （产物自动落到 adminui/dist）
package adminui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed all:dist
var assets embed.FS

// Dist 返回前端产物的只读文件系统。
func Dist() (fs.FS, error) {
	return fs.Sub(assets, "dist")
}

// Available 报告产物是否存在。
//
// dist 里带一个 .gitkeep 之外的占位文件，因此正常构建下恒为 true；
// 若有人清理了 dist，这里能安全返回 false 而不是 panic。
func Available() bool {
	sub, err := Dist()
	if err != nil {
		return false
	}
	_, err = fs.Stat(sub, "index.html")
	return err == nil
}

// Register 挂载 /admin/ 的静态资源与 SPA 回落。
//
// Vue Router 用 history 模式，/admin/ledger 这类地址在服务端没有对应文件。
// 必须把「非静态资源」的请求统一回落到 index.html，否则刷新就是 404。
func Register(router *gin.Engine) {
	sub, err := Dist()
	if err != nil {
		return
	}
	// StripPrefix 不能省：FileServer 按 req.URL.Path 找文件，
	// 而这里的路径是 /admin/assets/x.js，不剥掉 /admin/ 就会一律 404。
	fileServer := http.StripPrefix("/admin/", http.FileServer(http.FS(sub)))

	router.GET("/admin/*filepath", func(c *gin.Context) {
		reqPath := strings.TrimPrefix(c.Param("filepath"), "/")
		if reqPath == "" {
			serveIndex(c, sub)
			return
		}

		if f, openErr := sub.Open(reqPath); openErr == nil {
			f.Close()
			// 带内容 hash 的静态资源可以长缓存
			if strings.HasPrefix(reqPath, "assets/") {
				c.Header("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(c.Writer, c.Request)
			return
		}

		serveIndex(c, sub)
	})

	// /admin 不带斜杠时补一个跳转，避免相对路径解析错乱
	router.GET("/admin", func(c *gin.Context) {
		c.Redirect(http.StatusFound, "/admin/")
	})
}

func serveIndex(c *gin.Context, sub fs.FS) {
	data, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		c.String(http.StatusNotFound, "admin ui not built")
		return
	}
	// 入口页不缓存，否则更新后台后浏览器仍用旧版本
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "text/html; charset=utf-8", data)
}
