package router

import (
	v10 "github.com/QuantumNous/new-api/internalv10"

	"github.com/gin-gonic/gin"
)

// SetV10Router 挂载 V10 对接层。
//
// 单独成文件、单独成函数，是为了让"这是二开新增的部分"在目录结构上就一目了然，
// 也方便跟随上游升级时确认哪些改动需要保留。
func SetV10Router(router *gin.Engine) {
	if !v10.Enabled() {
		return
	}
	v10.Register(&router.RouterGroup)
	v10.RegisterBrowser(&router.RouterGroup)
}
