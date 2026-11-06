package v10

import (
	"errors"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

// GetAccount 按 (site, client_id) 取账户映射；不存在返回 (nil, nil)，
// 调用方据此区分"没有这个客户"和"查库失败"。
func GetAccount(site *V10Site, clientId int64) (*V10Account, error) {
	if clientId <= 0 {
		return nil, errors.New("client_id must be positive")
	}
	var account V10Account
	err := model.DB.Where("site_id = ? AND client_id = ?", site.Id, clientId).First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &account, nil
}

// AccountQuota 是账号的额度快照，供 V10 前台展示"还剩多少"。
type AccountQuota struct {
	UserId        int           `json:"user_id"`
	ClientId      int64         `json:"client_id"`
	BalanceQuota  int64         `json:"balance_quota"`
	BalanceMicros int64         `json:"balance_micros"`
	BalanceAmount string        `json:"balance_amount"`
	UsedQuota     int64         `json:"used_quota"`
	UsedMicros    int64         `json:"used_micros"`
	RequestCount  int64         `json:"request_count"`
	Status        int           `json:"status"`
	Group         string        `json:"group"`
	Subscriptions []HostSummary `json:"subscriptions"`
}

// HostSummary 是一个 host（商品实例）对应的订阅摘要。
type HostSummary struct {
	HostId             int64  `json:"host_id"`
	UserSubscriptionId int    `json:"user_subscription_id"`
	PlanId             int    `json:"plan_id"`
	PlanTitle          string `json:"plan_title"`
	HostStatus         string `json:"host_status"`
	QuotaTotal         int64  `json:"quota_total"`
	QuotaUsed          int64  `json:"quota_used"`
	QuotaRemaining     int64  `json:"quota_remaining"`
	StartTime          int64  `json:"start_time"`
	EndTime            int64  `json:"end_time"`
	NextResetTime      int64  `json:"next_reset_time"`
	Status             string `json:"status"`
}

// GetAccountQuota 取账户额度与订阅明细。
func GetAccountQuota(site *V10Site, clientId int64) (*AccountQuota, error) {
	account, err := GetAccount(site, clientId)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, nil
	}

	user, err := model.GetUserById(account.UserId, true)
	if err != nil {
		return nil, err
	}

	out := &AccountQuota{
		UserId:        user.Id,
		ClientId:      clientId,
		BalanceQuota:  int64(user.Quota),
		BalanceMicros: QuotaToMicros(int64(user.Quota)),
		BalanceAmount: formatMicros(QuotaToMicros(int64(user.Quota))),
		UsedQuota:     int64(user.UsedQuota),
		UsedMicros:    QuotaToMicros(int64(user.UsedQuota)),
		RequestCount:  int64(user.RequestCount),
		Status:        user.Status,
		Group:         user.Group,
		Subscriptions: []HostSummary{},
	}

	var hosts []V10Host
	if err := model.DB.Where("site_id = ? AND user_id = ?", site.Id, account.UserId).Find(&hosts).Error; err != nil {
		return nil, err
	}

	out.Subscriptions, err = summarizeHosts(hosts)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// summarizeHosts 把映射表里的 host 批量补上订阅明细。
func summarizeHosts(hosts []V10Host) ([]HostSummary, error) {
	summaries := make([]HostSummary, 0, len(hosts))
	for i := range hosts {
		host := hosts[i]
		summary := HostSummary{
			HostId:             host.HostId,
			UserSubscriptionId: host.UserSubscriptionId,
			PlanId:             host.PlanId,
			HostStatus:         host.HostStatus,
		}
		if host.UserSubscriptionId > 0 {
			var sub model.UserSubscription
			if err := model.DB.First(&sub, host.UserSubscriptionId).Error; err == nil {
				summary.QuotaTotal = sub.AmountTotal
				summary.QuotaUsed = sub.AmountUsed
				summary.QuotaRemaining = sub.AmountTotal - sub.AmountUsed
				summary.StartTime = sub.StartTime
				summary.EndTime = sub.EndTime
				summary.NextResetTime = sub.NextResetTime
				summary.Status = sub.Status
				summary.PlanId = sub.PlanId
			}
		}
		if host.PlanId > 0 {
			if plan, err := model.GetSubscriptionPlanById(host.PlanId); err == nil && plan != nil {
				summary.PlanTitle = plan.Title
			}
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

// BindHostRequest 是 V10 开通/续费套餐的请求。
//
// 语义：同一个 host_id 重复调用是"续费/对齐"，不是新建。V10 侧订单重推、
// 模块重试都可能重复调用，因此必须幂等。
type BindHostRequest struct {
	ClientId int64 `json:"client_id"`
	HostId   int64 `json:"host_id"`

	// PlanId 是网关侧的套餐 id（V10 插件里配置的商品->套餐映射结果）。
	PlanId int `json:"plan_id"`

	// DurationDays 覆盖套餐时长；0 表示用套餐自带时长。
	DurationDays int `json:"duration_days"`

	// RefType/RefId 是幂等凭证（订单号）。
	RefType string `json:"ref_type"`
	RefId   string `json:"ref_id"`
}

// BindHost 把 V10 的一个 host 绑定到一个订阅。
//
// 已绑定则：把订阅续期（对齐 EndTime）并恢复 host 状态，而不是再开一个订阅。
func BindHost(site *V10Site, req BindHostRequest) (*HostSummary, error) {
	if req.HostId <= 0 {
		return nil, errors.New("host_id must be positive")
	}
	if req.PlanId <= 0 {
		return nil, errors.New("plan_id must be positive")
	}

	account, err := EnsureAccount(site, req.ClientId, "", "")
	if err != nil {
		return nil, err
	}

	plan, err := model.GetSubscriptionPlanById(req.PlanId)
	if err != nil {
		return nil, err
	}
	if plan == nil || plan.Id == 0 {
		return nil, errors.New("subscription plan not found")
	}

	now := time.Now().Unix()
	var host V10Host
	lookupErr := model.DB.Where("site_id = ? AND host_id = ?", site.Id, req.HostId).First(&host).Error
	switch {
	case lookupErr == nil:
		if host.UserId != account.UserId {
			return nil, errors.New("host already bound to another account")
		}
	case errors.Is(lookupErr, gorm.ErrRecordNotFound):
		host = V10Host{
			SiteId:     site.Id,
			HostId:     req.HostId,
			UserId:     account.UserId,
			PlanId:     plan.Id,
			HostStatus: V10HostStatusActive,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
	default:
		return nil, lookupErr
	}

	endUnix := hostEndTime(now, plan, req.DurationDays)

	// replayed 表示这张凭证此前已经落地过：本次请求什么都不做，直接回报当前状态。
	replayed := false

	err = model.DB.Transaction(func(tx *gorm.DB) error {
		// 凭证先行。host 的开通与续期同样必须幂等：
		// V10 的 HTTP 请求超时后会自动重试，而超时的那一次很可能**已经成功**。
		// 若不带凭证去重，重试就会再顺延一个周期——等于白送客户一段时间。
		// 这里复用与入账相同的 (site_id, ref_type, ref_id) 唯一索引。
		if req.RefType != "" && req.RefId != "" {
			ref := V10Ref{
				SiteId:     site.Id,
				RefType:    req.RefType,
				RefId:      req.RefId,
				Kind:       "subscription",
				UserId:     account.UserId,
				DeltaQuota: 0,
				CreatedAt:  now,
			}
			if err := tx.Create(&ref).Error; err != nil {
				if isDuplicateKey(err) {
					replayed = true
					return nil
				}
				return err
			}
		}

		if host.Id == 0 {
			if err := tx.Create(&host).Error; err != nil {
				return err
			}
			sub, err := model.CreateUserSubscriptionFromPlanTx(tx, account.UserId, plan, "admin")
			if err != nil {
				return err
			}
			host.UserSubscriptionId = sub.Id
			return tx.Model(&V10Host{}).Where("id = ?", host.Id).
				Update("user_subscription_id", sub.Id).Error
		}

		// 已绑定：续期并恢复状态。
		if host.UserSubscriptionId > 0 {
			// 续期必须**从现有到期时间往后顺延**，而不是从"现在"重算：
			// 从 now 重算会让第二次续费的 30 天覆盖掉第一次的剩余时间，
			// 客户提前续费反而少拿到时间。已过期则从 now 起算。
			var current model.UserSubscription
			readErr := tx.Where("id = ?", host.UserSubscriptionId).First(&current).Error
			if readErr != nil && !errors.Is(readErr, gorm.ErrRecordNotFound) {
				return readErr
			}
			base := now
			if current.EndTime > now {
				base = current.EndTime
			}
			if base != now {
				endUnix = hostEndTime(base, plan, req.DurationDays)
			}

			updates := map[string]any{
				"status":     "active",
				"end_time":   endUnix,
				"updated_at": now,
			}
			if err := tx.Model(&model.UserSubscription{}).
				Where("id = ?", host.UserSubscriptionId).Updates(updates).Error; err != nil {
				return err
			}
		}
		return tx.Model(&V10Host{}).Where("id = ?", host.Id).Updates(map[string]any{
			"plan_id":     plan.Id,
			"host_status": V10HostStatusActive,
			"updated_at":  now,
		}).Error
	})
	if err != nil {
		return nil, err
	}

	// 重放：不动订阅，也不改状态，直接把 host 的当前样子读出来回报。
	// 读库而不是复用内存里的 host，是因为第一次处理可能来自别的请求。
	if replayed {
		var current V10Host
		if err := model.DB.Where("site_id = ? AND host_id = ?", site.Id, req.HostId).
			First(&current).Error; err != nil {
			return nil, err
		}
		summaries, err := summarizeHosts([]V10Host{current})
		if err != nil {
			return nil, err
		}
		return &summaries[0], nil
	}

	host.PlanId = plan.Id
	host.HostStatus = V10HostStatusActive
	summaries, err := summarizeHosts([]V10Host{host})
	if err != nil {
		return nil, err
	}
	return &summaries[0], nil
}

// hostEndTime 算出订阅应该结束的时间点。
func hostEndTime(now int64, plan *model.SubscriptionPlan, durationDays int) int64 {
	if durationDays > 0 {
		return now + int64(durationDays)*86400
	}
	switch plan.DurationUnit {
	case model.SubscriptionDurationYear:
		return now + int64(plan.DurationValue)*365*86400
	case model.SubscriptionDurationMonth:
		return now + int64(plan.DurationValue)*30*86400
	case model.SubscriptionDurationDay:
		return now + int64(plan.DurationValue)*86400
	case model.SubscriptionDurationHour:
		return now + int64(plan.DurationValue)*3600
	case model.SubscriptionDurationCustom:
		if plan.CustomSeconds > 0 {
			return now + plan.CustomSeconds
		}
		return now + 30*86400
	default:
		return now + 30*86400
	}
}

// SetHostStatus 把 host 及其订阅切到暂停/终止/恢复。
func SetHostStatus(site *V10Site, hostId int64, status, reason string) (*HostSummary, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	switch status {
	case V10HostStatusActive, V10HostStatusSuspended, V10HostStatusTerminated:
	default:
		return nil, errors.New("status must be active/suspended/terminated")
	}

	var host V10Host
	err := model.DB.Where("site_id = ? AND host_id = ?", site.Id, hostId).First(&host).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.New("host not found")
	}
	if err != nil {
		return nil, err
	}

	subStatus := "active"
	switch status {
	case V10HostStatusSuspended:
		subStatus = "cancelled"
	case V10HostStatusTerminated:
		subStatus = "cancelled"
	}

	now := time.Now().Unix()
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		if host.UserSubscriptionId > 0 {
			if err := tx.Model(&model.UserSubscription{}).
				Where("id = ?", host.UserSubscriptionId).
				Updates(map[string]any{"status": subStatus, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		return tx.Model(&V10Host{}).Where("id = ?", host.Id).
			Updates(map[string]any{"host_status": status, "updated_at": now}).Error
	})
	if err != nil {
		return nil, err
	}

	// 暂停/终止时同步处理该客户在该 host 下的 Key，避免"欠费还能用"。
	//
	// 终止与暂停必须区分：暂停是临时的，恢复后 Key 要能继续用，所以置 disabled；
	// 终止是最终态，Key 应该永久作废，置 expired 才算吊销成功。早期两者都按
	// "停用"处理，导致产品删除后客户手里还留着一把能恢复的 Key。
	if status == V10HostStatusTerminated {
		if err := setUserKeysStatus(host.UserId, keyOpRevoke); err != nil {
			return nil, err
		}
	} else if status == V10HostStatusSuspended {
		if err := setUserKeysStatus(host.UserId, keyOpDisable); err != nil {
			return nil, err
		}
	} else {
		if err := setUserKeysStatus(host.UserId, keyOpEnable); err != nil {
			return nil, err
		}
	}

	host.HostStatus = status
	summaries, err := summarizeHosts([]V10Host{host})
	if err != nil {
		return nil, err
	}
	_ = reason
	return &summaries[0], nil
}

// setUserKeysStatus 按操作批量改某用户 Key 的状态。
//
// 恢复时只把"被停用"的 Key 拉回可用，不动已吊销/已过期/已用尽的 Key：
// 否则一次暂停恢复就会让客户早就作废的 Key 全部复活，等于绕过吊销。
func setUserKeysStatus(userId int, op keyOp) error {
	query := model.DB.Model(&model.Token{}).Where("user_id = ?", userId)

	switch op {
	case keyOpEnable:
		return query.Where("status = ?", common.TokenStatusDisabled).
			Update("status", common.TokenStatusEnabled).Error
	case keyOpDisable:
		return query.Where("status = ?", common.TokenStatusEnabled).
			Update("status", common.TokenStatusDisabled).Error
	default:
		return query.Where("status IN ?", []int{common.TokenStatusEnabled, common.TokenStatusDisabled}).
			Update("status", common.TokenStatusExpired).Error
	}
}

// keyOp 是 Key 批量操作的语义，用来区分"临时停用"与"永久吊销"。
type keyOp int

const (
	keyOpEnable keyOp = iota
	keyOpDisable
	keyOpRevoke
)
