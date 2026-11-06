package model

import (
	"github.com/QuantumNous/new-api/common"
)

// SyncUserQuotaCacheDelta 把一次已提交的额度增量补进用户额度缓存。
//
// 背景：额度缓存是预扣/鉴权的第一权威（Redis 命中期间），余额类入账在数据库
// 事务提交后必须把增量同步进去，否则新到账的额度在缓存过期前不可用。
//
// 该函数专供 V10 对接层使用：它对 delta 的正负都成立，与只会加正数的
// syncCreditUserQuotaCache 不同——V10 的退款与管理员调账都需要扣减。
func SyncUserQuotaCacheDelta(userId int, delta int64) {
	if userId <= 0 || delta == 0 {
		return
	}
	if err := cacheIncrUserQuota(userId, delta); err != nil {
		common.SysLog(common.LogText("failed to sync quota delta %d to user %d cache: %s", delta, userId, err.Error()))
	}
}
