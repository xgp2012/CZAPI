package v10

import (
	"os"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// Enabled 判断是否启用 V10 对接层。
//
// 默认**启用**：这个网关的存在意义就是对接 V10，多一个开关只会多一条
// "为什么没生效"的排查路径。需要关掉时显式设 V10_BRIDGE_DISABLED=true。
func Enabled() bool {
	return os.Getenv("V10_BRIDGE_DISABLED") != "true"
}

// Bootstrap 在服务启动时建表、注册引导站点，并关闭 new-api 自带的用户面。
//
// 引导站点只在"环境变量给了 app_id 与 secret"且该站点尚不存在时创建，
// 方便首次部署：部署方把 V10 插件里要填的 app_id / secret 写进环境变量，
// 网关启动后插件配置照抄即可，不需要先进后台点一圈。
func Bootstrap() error {
	if !Enabled() {
		common.SysLog(common.LogText("v10 bridge disabled by V10_BRIDGE_DISABLED"))
		return nil
	}

	if err := Migration(model.DB); err != nil {
		return err
	}
	if err := lockUserFacingSurfaces(); err != nil {
		return err
	}

	// 后台（/admin/）账号：默认 admin/admin，强制首次改密。
	// 失败不阻断启动——网关的转发能力与后台可用性应当解耦，
	// 后台起不来不该让客户的模型调用一起挂掉。
	if err := EnsureAdminAccountBootstrap(); err != nil {
		common.SysError(common.LogText("v10: failed to bootstrap admin console account: %s", err.Error()))
	}
	CleanupExpiredAdminSessions()

	appId := os.Getenv("V10_BOOTSTRAP_APP_ID")
	secret := os.Getenv("V10_BOOTSTRAP_APP_SECRET")
	if appId == "" || secret == "" {
		return nil
	}

	existing, err := GetSiteByAppId(appId)
	if err != nil && err != ErrSiteNotFound {
		return err
	}
	if existing != nil {
		return nil
	}

	_, err = CreateSite(appId, "bootstrap", secret)
	if err != nil {
		return err
	}
	common.SysLog(common.LogText("v10: bootstrap site %q created; configure the same app_id/secret in the V10 plugin", appId))
	return nil
}

// ApplyUserFacingLock 重新施加"V10 是唯一入口"的开关约束。
//
// 启动时会自动调用一次；但 /api/setup 这类初始化接口会在启动之后直接写
// 选项表（它按请求体的值写 SelfUseModeEnabled），从而把锁面冲掉。
// 因此凡是会在运行期改写这些选项的地方，都必须再调一次本函数。
func ApplyUserFacingLock() error {
	if !Enabled() {
		return nil
	}
	return lockUserFacingSurfaces()
}

// lockUserFacingSurfaces 关闭 new-api 所有面向最终用户的注册、登录与付费入口。
//
// 这条约束是本项目的立身之本：V10 是唯一入口，用户不该能在网关这里注册、
// 输密码登录、充值、自购订阅。放在启动时强制执行而不是靠运维点后台，是因为
// "某次重装数据库后忘了关"会直接变成一个可以白嫖的注册口子。
//
// 保留管理员账号与 API Key 鉴权，因此 new-api 自身的后台仍可登录运维。
func lockUserFacingSurfaces() error {
	locked := map[string]string{
		// 不允许自助注册，账号只能由 V10 经 /internal/v10/accounts/sync 建立。
		"RegisterEnabled":         "false",
		"PasswordRegisterEnabled": "false",
		// 邮箱验证与找回密码都建立在"用户自己管邮箱"的前提上，此项目不成立。
		"EmailVerificationEnabled": "false",
		// 充值、兑换码、订阅自购全部属于 V10 的商品流程，网关不开。
		"SelfUseModeEnabled": "true",
	}
	// 刻意**不关** PasswordLoginEnabled：关掉它会把运维自己锁在后台外面，
	// 而 V10 客户本来就没有可用密码（EnsureAccount 写入的是不可用占位值，
	// 任何密码的哈希都不会命中），所以密码登录天然只对运维账号有效。

	for key, value := range locked {
		current := common.OptionMap[key]
		if current == value {
			continue
		}
		if err := model.UpdateOption(key, value); err != nil {
			return err
		}
		common.SysLog(common.LogText("v10: locked %s=%s (V10 is the only entry point)", key, value))
	}

	// 充值链接与自助充值开关直接置空：即使有人手工改库打开，界面也无入口。
	clearZero := map[string]string{
		"TopUpLink": "",
	}
	for key, value := range clearZero {
		if common.OptionMap[key] == value {
			continue
		}
		if err := model.UpdateOption(key, value); err != nil {
			return err
		}
	}

	// 下单金额相关的公开配置也一并关闭，避免错误引导用户去网关付款。
	if !isSurfaceLocked() {
		common.SysLog(common.LogText("v10: warning - user-facing surfaces are not fully locked, check option table"))
	}
	return nil
}

// isSurfaceLocked 复核一遍内存中的开关，作为"锁面真的生效了"的自检。
func isSurfaceLocked() bool {
	if common.RegisterEnabled || common.PasswordRegisterEnabled {
		return false
	}
	enabled, err := strconv.ParseBool(common.OptionMap["SelfUseModeEnabled"])
	return err == nil && enabled && operation_setting.SelfUseModeEnabled
}
