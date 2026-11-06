package v10

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// 本文件是整条资金链路的正确性核心：**幂等入账**。
//
// 规则：
//  1. 任何加/扣额度都必须带 (ref_type, ref_id)，两者非空。
//  2. 同一站点内 (ref_type, ref_id) 只能生效一次；重放返回 applied=false。
//  3. 金额单位是 V10 侧的**微元**（1 元 = 1_000_000），入账时换算成网关额度。
//  4. 凭证先登记后改额度，且两者在同一事务里，保证不会出现"扣了钱没记账"。

var (
	ErrMissingRef       = errors.New("ref_type and ref_id are required")
	ErrRefConflict      = errors.New("same ref already applied with different parameters")
	ErrInvalidAmount    = errors.New("amount_micros must be non-zero")
	ErrNegativeQuota    = errors.New("operation would make quota negative and allow_negative is false")
	ErrCreditNotEnabled = errors.New("credit topup is disabled by configuration")
	ErrAmountTooLarge   = errors.New("amount_micros exceeds the largest single credit the gateway can store")
)

// maxSingleCreditQuota 是单笔入账允许的最大额度增量。
//
// users.quota 的列类型是 `type:int`（见 model/user.go）。在 MySQL 上这会建成
// 32 位 INT，上限 2147483647；而 common.MaxWalletQuota 是 1<<53-1（JS 安全
// 整数上限），两者不是一回事。若只按 MaxWalletQuota 校验，一笔大额充值在
// MySQL/PG 上会撞到驱动/列宽限制，返回一个底层数据库错误——那种错误对运维
// 毫无指向性，还会被当成"网关坏了"。
//
// 因此这里按 int32 上限做**单笔**校验，让超限变成明确的 40000 参数错误。
// 需要更大金额时应当拆成多笔（各自带不同 ref_id），这本来也是财务上
// 更可对账的做法。
const maxSingleCreditQuota = int64(common.MaxQuota) // math.MaxInt32

// CreditRequest 是 V10 调 /internal/v10/credit 的请求体。
type CreditRequest struct {
	ClientId int64 `json:"client_id"`

	// Kind 是业务来源，用于审计与对账分类。
	Kind string `json:"kind"`

	// AmountMicros 是 V10 侧的微元金额，正数入账、负数出账。
	AmountMicros int64 `json:"amount_micros"`

	RefType string `json:"ref_type"`
	RefId   string `json:"ref_id"`

	Remark string `json:"remark"`

	// AllowNegative 允许扣减后额度为负，退款场景用。
	AllowNegative bool `json:"allow_negative"`
}

// CreditResult 是加额结果。Applied=false 表示该凭证此前已处理过。
type CreditResult struct {
	Applied      bool   `json:"applied"`
	UserId       int    `json:"user_id"`
	ClientId     int64  `json:"client_id"`
	DeltaQuota   int64  `json:"delta_quota"`
	BalanceAfter int64  `json:"balance_after"`
	RefId        string `json:"ref_id"`
}

// microsToQuota 把 V10 微元换算成网关额度。
//
// 换算率来自 common.QuotaPerUnit：new-api 里 1 单位额度 = QuotaPerUnit 个内部
// token 额度（默认 500000）。V10 侧 1 元 = 1_000_000 微元，因此：
//
//	quota = amount_micros * QuotaPerUnit / 1_000_000
//
// 必须先乘后除。先除再乘会把不足 1 元的金额整体抹成 0（2.5 元变成 2 元），
// 那是直接吞用户的钱。先乘后除只会在最后截掉不到 1 个额度单位的零头，
// 而这个零头本来就小于 1/QuotaPerUnit 元，无损可忽略。
// 用整数运算避免浮点误差。
func microsToQuota(amountMicros int64) int64 {
	if amountMicros == 0 {
		return 0
	}
	perUnit := int64(common.QuotaPerUnit)
	if perUnit <= 0 {
		perUnit = 500000
	}
	microsPerUnit := int64(1_000_000)
	if amountMicros > 0 {
		if amountMicros > (maxSingleCreditQuota*microsPerUnit)/perUnit {
			return maxSingleCreditQuota
		}
		return amountMicros * perUnit / microsPerUnit
	}
	if -amountMicros > (maxSingleCreditQuota*microsPerUnit)/perUnit {
		return -maxSingleCreditQuota
	}
	return -((-amountMicros) * perUnit / microsPerUnit)
}

// ApplyCredit 执行一次幂等加额。
//
// 事务边界刻意包裹"登记凭证 + 改额度"两步：任何一步失败都整体回滚，
// 因此不存在"凭证已占用但额度没到"的中间态。
func ApplyCredit(site *V10Site, req CreditRequest) (*CreditResult, error) {
	if strings.TrimSpace(req.RefType) == "" || strings.TrimSpace(req.RefId) == "" {
		return nil, ErrMissingRef
	}
	if req.AmountMicros == 0 {
		return nil, ErrInvalidAmount
	}
	if req.ClientId <= 0 {
		return nil, errors.New("client_id must be positive")
	}

	account, err := EnsureAccount(site, req.ClientId, "", "")
	if err != nil {
		return nil, err
	}

	// 单笔金额超限必须在这里拒绝，而不是让数据库报错。
	// microsToQuota 会把超限值饱和到 maxSingleCreditQuota，若不拦住，
	// 客户付了 5000 元却只到账 4294 元——静默少给钱，比报错糟得多。
	if req.AmountMicros > 0 && microsToQuota(req.AmountMicros) >= maxSingleCreditQuota {
		return nil, ErrAmountTooLarge
	}
	if req.AmountMicros < 0 && microsToQuota(req.AmountMicros) <= -maxSingleCreditQuota {
		return nil, ErrAmountTooLarge
	}

	delta := microsToQuota(req.AmountMicros)
	if delta == 0 {
		// 金额小于一个额度单位：不产生入账，但也必须登记凭证，
		// 否则 V10 会无限重试。
		return recordZeroDelta(site, req, account)
	}

	result := &CreditResult{UserId: account.UserId, ClientId: req.ClientId, DeltaQuota: delta, RefId: req.RefId}

	err = model.DB.Transaction(func(tx *gorm.DB) error {
		now := time.Now().Unix()

		// 先占用凭证。唯一索引让并发重复请求在这里失败，从而只生效一次。
		ref := V10Ref{
			SiteId:     site.Id,
			RefType:    req.RefType,
			RefId:      req.RefId,
			Kind:       req.Kind,
			UserId:     account.UserId,
			DeltaQuota: delta,
			Remark:     req.Remark,
			CreatedAt:  now,
		}
		if err := tx.Create(&ref).Error; err != nil {
			return err
		}

		// 用带条件的原子 UPDATE 改额度，避免"读—改—写"丢失更新。
		// 条件是额度变化后仍在合法区间内；不满足时 RowsAffected=0。
		update := tx.Model(&model.User{}).Where("id = ?", account.UserId)
		if delta < 0 && !req.AllowNegative {
			update = update.Where("quota + ? >= 0", delta)
		}
		update = update.Where("ABS(quota + ?) <= ?", delta, common.MaxWalletQuota)
		res := update.Update("quota", gorm.Expr("quota + ?", delta))
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			// 区分"用户不存在"与"会被扣成负数/超额"，给出可操作的错误。
			var current model.User
			if err := tx.Select("id", "quota").First(&current, account.UserId).Error; err != nil {
				return err
			}
			if int64(current.Quota)+delta < 0 && !req.AllowNegative {
				return ErrNegativeQuota
			}
			return model.ErrWalletQuotaLimitExceeded
		}

		var after model.User
		if err := tx.Select("id", "quota").First(&after, account.UserId).Error; err != nil {
			return err
		}
		if err := tx.Model(&V10Ref{}).Where("id = ?", ref.Id).Update("balance_after", after.Quota).Error; err != nil {
			return err
		}

		result.Applied = true
		result.BalanceAfter = int64(after.Quota)
		return nil
	})

	if err != nil {
		// 唯一索引冲突 = 该凭证已处理过，回读并返回幂等命中。
		if isDuplicateKey(err) {
			return lookupAppliedRef(site, req, account)
		}
		return nil, err
	}

	// 事务外同步额度缓存：失败只记日志，因为数据库已是事实来源，
	// 缓存会在 TTL 后自愈。
	model.SyncUserQuotaCacheDelta(account.UserId, delta)
	return result, nil
}

// lookupAppliedRef 处理"凭证已存在"的分支：返回当时的入账结果，applied=false。
func lookupAppliedRef(site *V10Site, req CreditRequest, account *V10Account) (*CreditResult, error) {
	var ref V10Ref
	err := model.DB.Where("site_id = ? AND ref_type = ? AND ref_id = ?", site.Id, req.RefType, req.RefId).First(&ref).Error
	if err != nil {
		return nil, err
	}
	if ref.UserId != account.UserId {
		return nil, ErrRefConflict
	}
	return &CreditResult{
		Applied:      false,
		UserId:       ref.UserId,
		ClientId:     account.ClientId,
		DeltaQuota:   ref.DeltaQuota,
		BalanceAfter: ref.BalanceAfter,
		RefId:        ref.RefId,
	}, nil
}

// recordZeroDelta 处理"金额太小、换算后为 0"的分支：只登记凭证，不动额度。
func recordZeroDelta(site *V10Site, req CreditRequest, account *V10Account) (*CreditResult, error) {
	now := time.Now().Unix()
	ref := V10Ref{
		SiteId:     site.Id,
		RefType:    req.RefType,
		RefId:      req.RefId,
		Kind:       req.Kind,
		UserId:     account.UserId,
		DeltaQuota: 0,
		Remark:     req.Remark + " (amount below one quota unit, ignored)",
		CreatedAt:  now,
	}
	err := model.DB.Create(&ref).Error
	if err != nil {
		if isDuplicateKey(err) {
			return lookupAppliedRef(site, req, account)
		}
		return nil, err
	}
	return &CreditResult{Applied: false, UserId: account.UserId, ClientId: account.ClientId, RefId: req.RefId}, nil
}

// isDuplicateKey 判断是否为唯一约束冲突。
//
// 三套数据库的错误文本各不相同，而且**会跟随服务器 locale 变化**：
// 本项目的 PostgreSQL 实测返回的是中文 "重复键违反唯一约束"（SQLSTATE 23505），
// 该实例 lc_messages 被设成了中文。只匹配英文字样会在这种实例上失效，
// 表现为"重复入账被当成普通错误返回 400"——数据没脏，但 V10 会一直重试。
//
// 所以这里的策略是：
//  1. 优先用驱动给的 SQLSTATE（唯一可靠、与语言无关）；
//  2. 其次用 GORM 的哨兵错误；
//  3. 最后才退回文本匹配，英文与中文都覆盖，作为兜底。
func isDuplicateKey(err error) bool {
	if err == nil {
		return false
	}
	// 本项目没有开 gorm.Config.TranslateError，所以这个哨兵实际上不会出现；
	// 留着是为了将来若有人打开它也不会退化。
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	// 驱动错误码是与 locale 无关的最可靠判据。
	// PostgreSQL 唯一冲突：23505；MySQL 重复键：1062。
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return true
	}
	var myErr *mysql.MySQLError
	if errors.As(err, &myErr) && myErr.Number == 1062 {
		return true
	}

	// 文本兜底。只放具体到"唯一约束"的短语：
	// 绝不能只匹配 "duplicate"——GORM 的 ErrDuplicatedKey 文本就是
	// "duplicated key not allowed"，一旦放宽会把真正该暴露的失败
	// 误判成幂等命中，从而静默吞掉错误、让客户以为充值成功。
	msg := strings.ToLower(err.Error())
	return slices.ContainsFunc([]string{
		"duplicate entry",           // MySQL（英文）
		"duplicate key",             // PostgreSQL（英文）
		"unique constraint",         // PostgreSQL / SQLite（英文）
		"constraint failed: unique", // SQLite
		"unique failed",             // SQLite 变体
		"重复键违反唯一约束",                 // PostgreSQL（中文 locale）
		"违反唯一约束",                    // PostgreSQL（中文 locale，变体）
		"1062",                      // MySQL 错误号（文本里带编号时）
	}, func(s string) bool { return strings.Contains(msg, s) })
}

// QuotaToMicros 把网关额度换算回微元，供 V10 对账与展示使用。
func QuotaToMicros(quota int64) int64 {
	perUnit := int64(common.QuotaPerUnit)
	if perUnit <= 0 {
		perUnit = 500000
	}
	return quota * 1_000_000 / perUnit
}

// formatMicros 输出 6 位小数的元字符串，供 V10 直接出账，避免浮点。
func formatMicros(micros int64) string {
	sign := ""
	if micros < 0 {
		sign = "-"
		micros = -micros
	}
	return fmt.Sprintf("%s%d.%06d", sign, micros/1_000_000, micros%1_000_000)
}
