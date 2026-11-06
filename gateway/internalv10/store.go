package v10

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

// 本文件定义 V10 侧的持久化对象。三张表都与 new-api 原生表并列，不修改原生
// 表的语义：
//
//	V10Site     站点（一个 V10 实例 = 一个站点，用 app_id 区分）
//	V10Account  账户映射：V10 客户 -> 网关用户
//	V10Ref      幂等账本：任何一次加额/扣额都留下不可变凭证
//	V10Host     商品实例映射：V10 host -> 网关订阅
//
// 站点维度是必须的：同一套网关可以服务多个 V10 站点，V10 的 client_id 只在
// 单站点内唯一。

// V10Site 是一个 V10 实例的注册信息。
type V10Site struct {
	Id int `json:"id" gorm:"primaryKey"`

	// AppId 是插件 config.php 里配置的 app_id，全局唯一。
	AppId string `json:"app_id" gorm:"type:varchar(64);uniqueIndex;not null"`

	// Name 仅用于后台展示。
	Name string `json:"name" gorm:"type:varchar(128);not null;default:''"`

	// SecretEnc 存 app_secret 的密文。每次校验签名都在服务端解密使用，
	// 不做可逆性妥协：数据库被读走不等于签名密钥泄露。
	SecretEnc string `json:"-" gorm:"type:text;not null"`

	// SecretPrefix 是 secret 的前 6 位，供后台核对"配的是哪一把"。
	SecretPrefix string `json:"secret_prefix" gorm:"type:varchar(16);not null;default:''"`

	// Status 1 启用 / 2 停用。停用后该站点所有签名请求一律 403。
	Status int `json:"status" gorm:"type:int;not null;default:1"`

	CreatedAt int64 `json:"created_at" gorm:"bigint;not null;default:0"`
	UpdatedAt int64 `json:"updated_at" gorm:"bigint;not null;default:0"`
}

func (V10Site) TableName() string { return "v10_sites" }

const (
	V10SiteStatusEnabled  = 1
	V10SiteStatusDisabled = 2
)

// V10Account 把 V10 客户映射到 new-api 用户。
//
// 关键设计：**不复制用户信息**。用户名、额度、分组、状态全部以 new-api 的
// users 表为准，本表只存"哪个 V10 客户 = 哪个用户"这一条映射，避免双写不一致。
type V10Account struct {
	Id int `json:"id" gorm:"primaryKey"`

	SiteId int `json:"site_id" gorm:"uniqueIndex:uk_v10_account_site_client,priority:1;not null"`

	// ClientId 是 V10 的 client_id。
	ClientId int64 `json:"client_id" gorm:"uniqueIndex:uk_v10_account_site_client,priority:2;not null"`

	// UserId 指向 new-api users.id。一个 V10 客户恒定对应一个网关用户。
	UserId int `json:"user_id" gorm:"index;not null"`

	CreatedAt int64 `json:"created_at" gorm:"bigint;not null;default:0"`
	UpdatedAt int64 `json:"updated_at" gorm:"bigint;not null;default:0"`
}

func (V10Account) TableName() string { return "v10_accounts" }

// V10Ref 是幂等账本：一行代表"某站点某个凭证"已经被处理过。
//
// 这是整条资金链路唯一的正确性保障。V10 重推、插件重试、人工补单，都会命中
// 同一行 (site_id, ref_type, ref_id)，从而只生效一次。
type V10Ref struct {
	Id int `json:"id" gorm:"primaryKey"`

	SiteId int `json:"site_id" gorm:"uniqueIndex:uk_v10_ref,priority:1;not null"`

	RefType string `json:"ref_type" gorm:"uniqueIndex:uk_v10_ref,priority:2;type:varchar(32);not null"`
	RefId   string `json:"ref_id" gorm:"uniqueIndex:uk_v10_ref,priority:3;type:varchar(64);not null"`

	// Kind 是业务来源：v10_order / redeem_code / admin_adjust / refund / subscription。
	Kind string `json:"kind" gorm:"type:varchar(32);not null;default:''"`

	// UserId 与 DeltaQuota 记录本次凭证实际作用于谁、改了多少额度（网关内部额度单位）。
	UserId     int   `json:"user_id" gorm:"index;not null"`
	DeltaQuota int64 `json:"delta_quota" gorm:"bigint;not null;default:0"`

	// BalanceAfter 是本次处理后的用户额度快照，便于对账时直接比对。
	BalanceAfter int64 `json:"balance_after" gorm:"bigint;not null;default:0"`

	Remark    string `json:"remark" gorm:"type:varchar(255);not null;default:''"`
	CreatedAt int64  `json:"created_at" gorm:"bigint;not null;default:0"`
}

func (V10Ref) TableName() string { return "v10_refs" }

// V10Host 把 V10 的商品实例（host）映射到 new-api 的订阅。
//
// 一个 host 恒定对应一个 UserSubscription；续费时原地延长，不新建。
type V10Host struct {
	Id int `json:"id" gorm:"primaryKey"`

	SiteId int `json:"site_id" gorm:"uniqueIndex:uk_v10_host,priority:1;not null"`

	// HostId 是 V10 的 host_id，站内唯一。
	HostId int64 `json:"host_id" gorm:"uniqueIndex:uk_v10_host,priority:2;not null"`

	UserId int `json:"user_id" gorm:"index;not null"`

	// UserSubscriptionId 指向 new-api user_subscriptions.id。
	UserSubscriptionId int `json:"user_subscription_id" gorm:"index;not null;default:0"`

	// PlanId 指向 new-api subscription_plans.id。
	PlanId int `json:"plan_id" gorm:"not null;default:0"`

	// HostStatus 跟随 V10 的 host 状态：active / suspended / terminated。
	HostStatus string `json:"host_status" gorm:"type:varchar(16);not null;default:'active'"`

	CreatedAt int64 `json:"created_at" gorm:"bigint;not null;default:0"`
	UpdatedAt int64 `json:"updated_at" gorm:"bigint;not null;default:0"`
}

func (V10Host) TableName() string { return "v10_hosts" }

const (
	V10HostStatusActive     = "active"
	V10HostStatusSuspended  = "suspended"
	V10HostStatusTerminated = "terminated"
)

// V10Tables 供 AutoMigrate 注册，集中一处便于装配与测试。
func V10Tables() []any {
	tables := []any{&V10Site{}, &V10Account{}, &V10Ref{}, &V10Host{}, &V10LoginTicket{}}
	return append(tables, AdminTables()...)
}

// Migration 在数据库里建好本包的表。由装配方在 model.InitDB 之后调用。
func Migration(db *gorm.DB) error {
	return db.AutoMigrate(V10Tables()...)
}

// ---------------------------------------------------------------- 站点

var ErrSiteNotFound = errors.New("v10 site not found")
var ErrSiteDisabled = errors.New("v10 site disabled")
var ErrAccountNotFound = errors.New("v10 account not found")

// GetSiteByAppId 按 app_id 取站点。不存在返回 ErrSiteNotFound。
func GetSiteByAppId(appId string) (*V10Site, error) {
	var site V10Site
	err := model.DB.Where("app_id = ?", appId).First(&site).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrSiteNotFound
	}
	if err != nil {
		return nil, err
	}
	return &site, nil
}

// ---------------------------------------------------------------- 账户

// EnsureAccount 取或建 (site, client_id) 对应的账户，并把它绑定到一个网关用户。
//
// 绑定时优先复用已有映射；没有映射时为用户派生一个稳定的用户名并新建。
// 用户名带站点前缀，避免不同 V10 站点的同名客户互相覆盖。
//
// 该函数是幂等的：并发下由唯一索引兜底，失败方回读已写入的那一行。
func EnsureAccount(site *V10Site, clientId int64, displayName, email string) (*V10Account, error) {
	if clientId <= 0 {
		return nil, errors.New("client_id must be positive")
	}

	var account V10Account
	err := model.DB.Where("site_id = ? AND client_id = ?", site.Id, clientId).First(&account).Error
	if err == nil {
		return &account, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	now := time.Now().Unix()
	// username 必须由 (site, client_id) 唯一决定，不能取 V10 传来的显示名：
	// 显示名允许重复（两个客户都叫"张三"是常态），而 users.username 有唯一约束。
	// 用显示名当用户名会让第二个客户建号时撞唯一键，表现为"同步账户失败"。
	username := accountUsername(site, clientId)
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = username
	}

	// email 在 users 表上是全局唯一索引，而 V10 的客户邮箱只在 V10 内唯一：
	// 客户改绑邮箱、V10 允许两家客户填同一邮箱、或网关换库重建时，
	// 直接写入会撞唯一键并让整个"账户同步"失败——而同步失败意味着客户
	// 买了东西却拿不到额度。这里在冲突时降级为空邮箱。
	//
	// 安全性不受影响：V10 客户的登录只走单点票据，邮箱不参与任何认证、
	// 找回或通知流程（EmailVerificationEnabled 已被强制关闭）。
	email = strings.TrimSpace(email)
	if email != "" && model.IsEmailAlreadyTaken(email) {
		common.SysLog(common.LogText("v10: email %s already used by another user, client %d created without email", email, clientId))
		email = ""
	}

	user := model.User{
		Username:    username,
		Password:    "",
		DisplayName: displayName,
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Email:       email,
		Group:       "default",
		Remark:      "v10:" + strconv.FormatInt(clientId, 10),
	}
	// V10 客户没有网关密码，登录一律经 V10 单点。这里置一个不可用的密码值，
	// 使密码登录对其无效（common.Password2Hash("") 每次都不同，无法被命中）。
	user.Password = "v10-sso-only-no-password"

	created := V10Account{SiteId: site.Id, ClientId: clientId, CreatedAt: now, UpdatedAt: now}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		if err := user.InsertWithTx(tx, 0); err != nil {
			return err
		}
		created.UserId = user.Id
		return tx.Create(&created).Error
	})
	if err != nil {
		var existing V10Account
		if readErr := model.DB.Where("site_id = ? AND client_id = ?", site.Id, clientId).First(&existing).Error; readErr == nil {
			return &existing, nil
		}
		return nil, err
	}

	user.FinalizeOAuthUserCreation(0)
	return &created, nil
}

// accountUsername 生成网关侧用户名，受 users.username 的 20 字符上限约束。
//
// 形如 "<app_id 截断>_<client_id>"。client_id 始终保留在尾部，
// 因为它是唯一性来源；前缀被截断只影响可读性，不影响幂等。
func accountUsername(site *V10Site, clientId int64) string {
	prefix := strings.TrimSpace(site.AppId)
	if prefix == "" {
		prefix = "v10"
	}
	tail := "_" + strconv.FormatInt(clientId, 10)
	keep := 20 - len([]rune(tail))
	if keep < 1 {
		// client_id 长到前缀放不下时，宁可丢掉前缀也要保住唯一性。
		return "v10" + tail
	}
	runes := []rune(prefix)
	if len(runes) > keep {
		runes = runes[:keep]
	}
	return string(runes) + tail
}
