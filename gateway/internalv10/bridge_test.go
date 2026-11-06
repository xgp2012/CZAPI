package v10

import (
	"errors"
	"math"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 这些测试保护 V10 对接层最容易被改坏的三条约定：
// 签名必须与 PHP 侧逐字节一致、加额必须幂等、金额换算必须可逆。
// 任何一条失守都会变成线上对账不平或重复到账，所以用表驱动把它们钉住。

func TestCanonicalQueryUsesPercent20(t *testing.T) {
	// 空格必须编码成 %20 而不是 +，否则 PHP 的 rawurlencode 与 Go 的
	// url.QueryEscape 会产生两套签名，联调时会莫名其妙 401。
	got := CanonicalQuery(url.Values{"name": {"my key"}, "note": {"测试"}})
	assert.NotContains(t, got, "+")
	assert.Contains(t, got, "name=my%20key")

	// 键必须按字典序排列，否则两端拼接顺序不同。
	assert.Less(t, indexOf(got, "name"), indexOf(got, "note"))
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return len(haystack)
}

func TestSignIsStableAndSensitive(t *testing.T) {
	query := CanonicalQuery(url.Values{"client_id": {"1001"}})
	body := []byte(`{"client_id":1001}`)

	base := SignRequest("secret", "POST", "/internal/v10/accounts/sync", query, body, 1700000000, "nonce-1")
	require.Equal(t, base,
		SignRequest("secret", "POST", "/internal/v10/accounts/sync", query, body, 1700000000, "nonce-1"),
		"同一输入必须得到同一签名")

	// 换 nonce 必须换签名，否则重放防护没有意义。
	assert.NotEqual(t, base, SignRequest("secret", "POST", "/internal/v10/accounts/sync", query, body, 1700000000, "nonce-2"))
	// 换 secret 必须换签名。
	assert.NotEqual(t, base, SignRequest("other-secret", "POST", "/internal/v10/accounts/sync", query, body, 1700000000, "nonce-1"))
	// 换 body 必须换签名，否则中间人可以改金额。
	assert.NotEqual(t, base, SignRequest("secret", "POST", "/internal/v10/accounts/sync", query, []byte(`{"client_id":1002}`), 1700000000, "nonce-1"))
	// 换 path 必须换签名，否则可以挪用到别的接口。
	assert.NotEqual(t, base, SignRequest("secret", "POST", "/internal/v10/credit", query, body, 1700000000, "nonce-1"))
}

func TestMicrosQuotaConversionRoundTrip(t *testing.T) {
	// 1 元 = 1_000_000 micros，必须等于 common.QuotaPerUnit 个额度，
	// 因为 V10 前台展示的"余额"就是按这个比例算出来的。
	cases := []struct {
		micros int64
		quota  int64
	}{
		{1_000_000, int64(common.QuotaPerUnit)},
		{2_500_000, int64(2.5 * common.QuotaPerUnit)},
		{0, 0},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.quota, microsToQuota(tc.micros), "micros=%d", tc.micros)
		assert.Equal(t, tc.micros, QuotaToMicros(tc.quota), "quota=%d", tc.quota)
	}

	// 极端金额不能回绕成负数。先乘后除的中间结果会被放大 QuotaPerUnit 倍，
	// int64 溢出后符号翻转——那等于凭空扣客户的钱，比截断严重得多。
	// 上界对齐 users.quota 的实际列宽（type:int = 32 位），
	// 而不是 common.MaxWalletQuota：后者在 MySQL 上根本存不进去。
	assert.Positive(t, microsToQuota(math.MaxInt64), "极大金额必须饱和而非回绕成负数")
	assert.Equal(t, int64(common.MaxQuota), microsToQuota(math.MaxInt64))
	assert.Equal(t, -int64(common.MaxQuota), microsToQuota(-math.MaxInt64))
}

// TestApplyCreditIsIdempotent 是整条资金链路最重要的一条：同一个凭证
// 无论重推多少次，用户额度都只能加一次。
func TestApplyCreditIsIdempotent(t *testing.T) {
	newTestDB(t)

	site, err := CreateSite("v10test", "test", "test-secret")
	require.NoError(t, err)

	account, err := EnsureAccount(site, 1001, "", "")
	require.NoError(t, err)

	req := CreditRequest{
		ClientId:     1001,
		AmountMicros: 1_000_000,
		RefType:      "v10_order",
		RefId:        "ORD-1",
	}

	first, err := ApplyCredit(site, req)
	require.NoError(t, err)
	require.True(t, first.Applied, "首次必须真正加额")
	require.Equal(t, int64(common.QuotaPerUnit), first.BalanceAfter)

	second, err := ApplyCredit(site, req)
	require.NoError(t, err)
	assert.False(t, second.Applied, "重放不得再次加额")
	assert.Equal(t, first.BalanceAfter, second.BalanceAfter, "重放必须返回原余额")

	user, err := model.GetUserById(account.UserId, true)
	require.NoError(t, err)
	assert.Equal(t, int64(common.QuotaPerUnit), int64(user.Quota), "额度只应加一次")
}

// TestApplyCreditRejectsOversizedAmount 保护"宁报错也不静默少给"这条约定。
//
// users.quota 是 type:int（MySQL 上是 32 位），而换算过程会饱和。
// 若不在这里拦住，客户付了远超上限的钱只会到账一个被截断的数字——
// 那是直接吞钱。必须明确拒绝，让 V10 看到 40000 并人工处理。
func TestApplyCreditRejectsOversizedAmount(t *testing.T) {
	newTestDB(t)

	site, err := CreateSite("v10test", "test", "test-secret")
	require.NoError(t, err)
	account, err := EnsureAccount(site, 5002, "", "")
	require.NoError(t, err)

	// 让额度换算结果刚好超过 int32 上限。
	oversized := (int64(common.MaxQuota) + 1) * 1_000_000 / int64(common.QuotaPerUnit)
	_, err = ApplyCredit(site, CreditRequest{
		ClientId:     5002,
		AmountMicros: oversized,
		RefType:      "v10_order",
		RefId:        "ORD-HUGE",
	})
	require.ErrorIs(t, err, ErrAmountTooLarge)

	// 被拒的请求不得留下痕迹，也不得改动余额。
	user, err := model.GetUserById(account.UserId, true)
	require.NoError(t, err)
	assert.Zero(t, int64(user.Quota), "超限拒绝后余额必须不动")

	var refCount int64
	require.NoError(t, model.DB.Model(&V10Ref{}).Where("ref_id = ?", "ORD-HUGE").Count(&refCount).Error)
	assert.Zero(t, refCount, "超限拒绝不得占用凭证，否则 V10 重试会被误判成已处理")

	// 正常金额仍然可以入账，说明拒绝没有误伤。
	ok, err := ApplyCredit(site, CreditRequest{
		ClientId:     5002,
		AmountMicros: 1_000_000,
		RefType:      "v10_order",
		RefId:        "ORD-NORMAL",
	})
	require.NoError(t, err)
	require.True(t, ok.Applied)
}

// TestBindHostIsIdempotent 保护续期幂等。
//
// V10 的 HTTP 请求超时后会自动重试，而超时的那一次往往**已经成功**了。
// 如果续期不去重，重试就会再顺延一个周期，等于白送客户一段时间。
func TestBindHostIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.SubscriptionPlan{}, &model.UserSubscription{}))

	plan := &model.SubscriptionPlan{
		Title:         "测试套餐",
		PriceAmount:   1,
		Currency:      "CNY",
		DurationUnit:  model.SubscriptionDurationMonth,
		DurationValue: 1,
		Enabled:       true,
	}
	require.NoError(t, db.Create(plan).Error)

	site, err := CreateSite("v10test", "test", "test-secret")
	require.NoError(t, err)

	req := BindHostRequest{
		ClientId: 1001,
		HostId:   90001,
		PlanId:   plan.Id,
		RefType:  "v10_host_open",
		RefId:    "OPEN-90001",
	}

	first, err := BindHost(site, req)
	require.NoError(t, err)
	require.Greater(t, first.EndTime, int64(0))
	firstEnd := first.EndTime

	// 同一凭证重放：到期时间必须与第一次完全一致，不能又顺延一个月。
	second, err := BindHost(site, req)
	require.NoError(t, err)
	assert.Equal(t, firstEnd, second.EndTime, "重放同一凭证不得再次续期")

	// 换一个凭证才是真正的续期，且必须从原到期时间往后顺延。
	renew := req
	renew.RefType = "v10_host_renew"
	renew.RefId = "RENEW-90001-1"
	third, err := BindHost(site, renew)
	require.NoError(t, err)
	assert.Greater(t, third.EndTime, firstEnd, "新凭证必须延长到期时间")
}

// TestIsManagedAccount 保护"钱不过网关"这条边界。
//
// 背景：new-api 的 /api/user/topup 实际是卡密兑换，只由"合规已确认"把守，
// 而运维建套餐必须先确认合规。实测过 V10 客户拿会话可直接兑换网关卡密
// 给自己加额度，完全绕过 V10 的订单。这里钉住身份判定本身。
func TestIsManagedAccount(t *testing.T) {
	newTestDB(t)

	site, err := CreateSite("v10test", "test", "test-secret")
	require.NoError(t, err)

	account, err := EnsureAccount(site, 1001, "", "")
	require.NoError(t, err)

	assert.True(t, IsManagedAccount(account.UserId), "V10 账户必须被识别为托管")

	// 非正数 ID 直接返回 false，避免把匿名请求误判成托管（那样会连
	// 正常的管理员请求一起拦掉）。
	assert.False(t, IsManagedAccount(0))
	assert.False(t, IsManagedAccount(-1))
}

// TestIsDuplicateKeyAcrossDialects 保护"唯一冲突必须被识别"这条约定。
//
// 背景（真实事故）：本函数原来只匹配英文字样，而上线的 PostgreSQL 实例
// lc_messages 是中文，报错是「重复键违反唯一约束 (SQLSTATE 23505)」。
// 结果重放请求没有被识别成幂等命中，而是当成普通错误返回 400——
// 数据没脏，但 V10 会一直重试，客户看到的是"充值失败"。
//
// 这个测试故意不连真实数据库：错误文本取决于服务器 locale，
// 没法在单测里稳定复现。直接把各引擎的真实报错原文喂进来。
func TestIsDuplicateKeyAcrossDialects(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"PostgreSQL 中文 locale", errors.New(`错误: 重复键违反唯一约束"uk_v10_ref" (SQLSTATE 23505)`), true},
		{"PostgreSQL 英文 locale", errors.New(`ERROR: duplicate key value violates unique constraint "uk_v10_ref" (SQLSTATE 23505)`), true},
		{"MySQL", errors.New("Error 1062: Duplicate entry '1-x' for key 'uk_v10_ref'"), true},
		// 驱动结构化错误码优先于文本，这条不依赖任何语言设置。
		{"MySQL 驱动错误码", &mysql.MySQLError{Number: 1062, Message: "whatever"}, true},
		{"PostgreSQL 驱动错误码", &pgconn.PgError{Code: "23505", Message: "whatever"}, true},
		// PG 的 23503(外键)/23502(非空) 不是唯一冲突，不能当成幂等命中。
		{"PostgreSQL 外键冲突", &pgconn.PgError{Code: "23503", Message: "fk"}, false},
		{"MySQL 其他错误号", &mysql.MySQLError{Number: 1213, Message: "deadlock"}, false},
		{"SQLite", errors.New("UNIQUE constraint failed: v10_refs.site_id, v10_refs.ref_type"), true},
		{"GORM 哨兵", gorm.ErrDuplicatedKey, true},
		// 不能把普通错误误判成幂等命中，否则真正的失败会被静默吞掉。
		{"无关错误", errors.New("connection refused"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isDuplicateKey(tc.err))
		})
	}
}

// newTestDB 用独立的临时 SQLite 文件建库，避免测试之间互相污染。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	// 这些测试只验证数据库语义，不验证缓存。关掉 Redis 让用户写入路径
	// 走纯数据库分支，避免依赖一个测试环境里并不存在的 Redis 实例。
	previousRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedis })

	dir := t.TempDir()
	dsn := filepath.Join(dir, "test.db") + "?_pragma=busy_timeout(30000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)

	previous := model.DB
	model.DB = db
	t.Cleanup(func() {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		model.DB = previous
	})

	require.NoError(t, db.AutoMigrate(&model.User{}))
	require.NoError(t, Migration(db))
	return db
}
