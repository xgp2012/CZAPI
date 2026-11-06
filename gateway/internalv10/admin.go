package v10

import (
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 站点与套餐的管理面。
//
// 站点数量是个位数，套餐通常也只有几档，所以这里给一组最小可用的后台接口，
// 让运维不必进数据库改表就能完成上线动作：注册站点、轮换密钥、建套餐。
// 所有接口都挂在管理员路由下（AdminAuth），不暴露给 V10 客户。

// RegisterAdmin 把 V10 管理接口挂到管理员分组下。
//
// 调用方传入的已经是带 /v10/admin 前缀且套了 AdminAuth 的分组，
// 所以这里直接在其上挂路由，不再自建子分组，避免前缀叠成 /v10/admin/v10。
func RegisterAdmin(group *gin.RouterGroup) {
	group.GET("/sites", handleAdminListSites)
	group.POST("/sites", handleAdminCreateSite)
	group.POST("/sites/:id/secret", handleAdminRotateSecret)
	group.POST("/sites/:id/status", handleAdminSetSiteStatus)
	group.GET("/site-options", handleAdminSiteOptions)
}

type siteOption struct {
	Id           int    `json:"id"`
	AppId        string `json:"app_id"`
	Name         string `json:"name"`
	SecretPrefix string `json:"secret_prefix"`
	Status       int    `json:"status"`
	CreatedAt    int64  `json:"created_at"`
}

func handleAdminListSites(c *gin.Context) {
	sites, err := ListSites()
	if err != nil {
		common.ApiError(c, err)
		return
	}

	items := make([]siteOption, 0, len(sites))
	for i := range sites {
		site := &sites[i]
		items = append(items, siteOption{
			Id:           site.Id,
			AppId:        site.AppId,
			Name:         site.Name,
			SecretPrefix: site.SecretPrefix,
			Status:       site.Status,
			CreatedAt:    site.CreatedAt,
		})
	}
	common.ApiSuccess(c, gin.H{"items": items, "total": len(items)})
}

func handleAdminCreateSite(c *gin.Context) {
	var req struct {
		AppId  string `json:"app_id"`
		Name   string `json:"name"`
		Secret string `json:"secret"`
	}
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorMsg(c, "Invalid parameters")
		return
	}

	site, err := CreateSite(req.AppId, req.Name, req.Secret)
	if err != nil {
		if errors.Is(err, ErrSiteExists) {
			common.ApiErrorMsg(c, "This app_id is already registered")
			return
		}
		common.ApiError(c, err)
		return
	}

	// 明文 secret 只在这里出现一次；库里存的是密文与前缀。
	common.ApiSuccess(c, gin.H{
		"id":            site.Id,
		"app_id":        site.AppId,
		"name":          site.Name,
		"secret":        req.Secret,
		"secret_prefix": site.SecretPrefix,
	})
}

func handleAdminRotateSecret(c *gin.Context) {
	siteId, err := strconv.Atoi(c.Param("id"))
	if err != nil || siteId <= 0 {
		common.ApiErrorMsg(c, "Invalid site id")
		return
	}

	var req struct {
		Secret string `json:"secret"`
	}
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorMsg(c, "Invalid parameters")
		return
	}

	if err := RotateSecret(siteId, req.Secret); err != nil {
		if errors.Is(err, ErrSiteNotFound) {
			common.ApiErrorMsg(c, "Site not found")
			return
		}
		common.ApiError(c, err)
		return
	}

	// 提醒运维：轮换后必须同步改 V10 插件配置，否则插件全部请求会 401。
	common.ApiSuccess(c, gin.H{
		"id":            siteId,
		"secret":        req.Secret,
		"secret_prefix": secretPrefix(req.Secret),
		"notice":        "Update app_secret in the V10 plugin, otherwise its requests will be rejected",
	})
}

func handleAdminSetSiteStatus(c *gin.Context) {
	siteId, err := strconv.Atoi(c.Param("id"))
	if err != nil || siteId <= 0 {
		common.ApiErrorMsg(c, "Invalid site id")
		return
	}

	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := common.DecodeJson(c.Request.Body, &req); err != nil || req.Enabled == nil {
		common.ApiErrorMsg(c, "Invalid parameters")
		return
	}

	if err := SetSiteStatus(siteId, *req.Enabled); err != nil {
		if errors.Is(err, ErrSiteNotFound) {
			common.ApiErrorMsg(c, "Site not found")
			return
		}
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"id": siteId, "enabled": *req.Enabled})
}

// handleAdminSiteOptions 把"注册站点/建套餐"需要的上下文一次给全：
// 已有站点、可选套餐、控制台地址。
//
// 上线时的顺序是"先建套餐、再建站点、再把 app_id/secret 填进 V10 插件"，
// 把这三样放一个响应里，运维不必在几个页面之间来回找。
func handleAdminSiteOptions(c *gin.Context) {
	sites, err := ListSites()
	if err != nil {
		common.ApiError(c, err)
		return
	}

	var plans []model.SubscriptionPlan
	if err := model.DB.Order("id asc").Find(&plans).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		common.ApiError(c, err)
		return
	}

	siteItems := make([]siteOption, 0, len(sites))
	for i := range sites {
		site := &sites[i]
		siteItems = append(siteItems, siteOption{
			Id:           site.Id,
			AppId:        site.AppId,
			Name:         site.Name,
			SecretPrefix: site.SecretPrefix,
			Status:       site.Status,
			CreatedAt:    site.CreatedAt,
		})
	}

	planItems := make([]gin.H, 0, len(plans))
	for i := range plans {
		plan := &plans[i]
		planItems = append(planItems, gin.H{
			"id":             plan.Id,
			"title":          plan.Title,
			"enabled":        plan.Enabled,
			"duration_unit":  plan.DurationUnit,
			"duration_value": plan.DurationValue,
			"total_amount":   plan.TotalAmount,
		})
	}

	common.ApiSuccess(c, gin.H{
		"sites":        siteItems,
		"plans":        planItems,
		"console_base": strings.TrimSpace(ConsoleBaseUrl(c)),
		"bridge": gin.H{
			"enabled": Enabled(),
			"prefix":  "/internal/v10",
			// 前端拿到这个比例才能把微元换算成界面上的额度，避免两边各写一套。
			"quota_per_unit": math.Round(common.QuotaPerUnit),
		},
	})
}
