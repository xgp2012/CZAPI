package controller

import (
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	v10 "github.com/QuantumNous/new-api/internalv10"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

type Setup struct {
	Status       bool   `json:"status"`
	RootInit     bool   `json:"root_init"`
	DatabaseType string `json:"database_type"`
}

type SetupRequest struct {
	Username           string `json:"username"`
	Password           string `json:"password"`
	ConfirmPassword    string `json:"confirmPassword"`
	SelfUseModeEnabled bool   `json:"SelfUseModeEnabled"`
	DemoSiteEnabled    bool   `json:"DemoSiteEnabled"`
	// V10 站点引导：两项都留空则跳过，什么都不建。
	// 填了 AppId 就在初始化时顺手建好站点，并把明文 secret 回一次，
	// 让部署方照抄进 V10 插件——省掉"装完再进后台点一圈"的一步。
	V10AppId    string `json:"v10_app_id"`
	V10SiteName string `json:"v10_site_name"`
}

func GetSetup(c *gin.Context) {
	setup := Setup{
		Status: constant.Setup,
	}
	if constant.Setup {
		c.JSON(200, gin.H{
			"success": true,
			"data":    setup,
		})
		return
	}
	setup.RootInit = model.RootUserExists()
	setup.DatabaseType = string(common.MainDatabaseType())
	c.JSON(200, gin.H{
		"success": true,
		"data":    setup,
	})
}

func PostSetup(c *gin.Context) {
	// Check if setup is already completed
	if constant.Setup {
		common.ApiErrorT(c, "The system is already initialized")
		return
	}

	// Check if root user already exists
	rootExists := model.RootUserExists()

	var req SetupRequest
	err := c.ShouldBindJSON(&req)
	if err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}

	// If root doesn't exist, validate and create admin account
	if !rootExists {
		// Validate username length: max 12 characters to align with model.User validation
		if len(req.Username) > 12 {
			common.ApiErrorT(c, "Username must be {{max}} characters or fewer", map[string]any{"max": 12})
			return
		}
		// Validate password
		if req.Password != req.ConfirmPassword {
			common.ApiErrorT(c, "Passwords do not match")
			return
		}

		if err := common.ValidateNewAccountPassword(req.Password); err != nil {
			c.JSON(200, gin.H{
				"success": false,
				"message": err.Error(),
			})
			return
		}

		// Create root user
		hashedPassword, err := common.HashAccountPassword(req.Password)
		if err != nil {
			common.ApiErrorT(c, "System error: {{error}}", map[string]any{"error": err.Error()})
			return
		}
		rootUser := model.User{
			Username:    req.Username,
			Password:    hashedPassword,
			Role:        common.RoleRootUser,
			Status:      common.UserStatusEnabled,
			DisplayName: "Root User",
			AccessToken: nil,
			Quota:       100000000,
		}
		err = model.DB.Create(&rootUser).Error
		if err != nil {
			common.ApiErrorT(c, "Failed to create the administrator account: {{error}}", map[string]any{"error": err.Error()})
			return
		}
	}

	// Set operation modes
	operation_setting.SelfUseModeEnabled = req.SelfUseModeEnabled
	operation_setting.DemoSiteEnabled = req.DemoSiteEnabled

	// Save operation modes to database for persistence
	err = model.UpdateOption("SelfUseModeEnabled", boolToString(req.SelfUseModeEnabled))
	if err != nil {
		common.ApiErrorT(c, "Failed to save the self-use mode setting: {{error}}", map[string]any{"error": err.Error()})
		return
	}

	err = model.UpdateOption("DemoSiteEnabled", boolToString(req.DemoSiteEnabled))
	if err != nil {
		common.ApiErrorT(c, "Failed to save the demo site mode setting: {{error}}", map[string]any{"error": err.Error()})
		return
	}

	// Update setup status
	constant.Setup = true

	setup := model.Setup{
		Version:       common.Version,
		InitializedAt: time.Now().Unix(),
	}
	err = model.DB.Create(&setup).Error
	if err != nil {
		common.ApiErrorT(c, "Failed to initialize the system: {{error}}", map[string]any{"error": err.Error()})
		return
	}

	// 本接口会按请求体写入 SelfUseModeEnabled 等选项，等于把 V10 在启动时
	// 施加的"唯一入口"约束冲掉（实测：初始化后 options 表里变成 false，
	// 内存里仍是 true，两种状态不一致最危险）。这里立刻重新施加一次。
	if err := v10.ApplyUserFacingLock(); err != nil {
		common.SysError(common.LogText("failed to re-apply V10 user-facing lock: %s", err.Error()))
	}

	// 可选的 V10 站点引导。建失败不回滚整套初始化：系统已经可用了，
	// 站点随时可以到 V10 运营页补建，这里只把失败原因如实带回去。
	appId := strings.TrimSpace(req.V10AppId)
	if appId == "" {
		common.ApiSuccessT(c, "System initialized successfully", nil)
		return
	}

	siteName := strings.TrimSpace(req.V10SiteName)
	if siteName == "" {
		siteName = appId
	}
	secret, err := v10.GenerateSiteSecret()
	if err != nil {
		common.SysError(common.LogText("failed to generate V10 site secret: %s", err.Error()))
		common.ApiSuccessT(c, "System initialized successfully, but the V10 site secret could not be generated. Create the site later from V10 Operations.", nil)
		return
	}
	if _, err := v10.CreateSite(appId, siteName, secret); err != nil {
		common.SysError(common.LogText("failed to create bootstrap V10 site: %s", err.Error()))
		common.ApiErrorT(c, "System initialized, but creating the V10 site failed: {{error}}. Create it later from V10 Operations.", map[string]any{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"success": true,
		"message": "System initialized successfully",
		"data": gin.H{
			"v10_app_id": appId,
			// 明文 secret 只在此刻出现一次，库中只存密文与前缀。
			"v10_app_secret": secret,
		},
	})
}

func boolToString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
