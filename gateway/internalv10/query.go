package v10

import (
	"github.com/QuantumNous/new-api/model"
)

// 供控制器层使用的小查询集中在这里，避免在 router.go 里直接铺 SQL。

// dbHealth 判断数据库连接是否可用。
func model_DBIsReady() bool {
	if model.DB == nil {
		return false
	}
	sqlDB, err := model.DB.DB()
	if err != nil {
		return false
	}
	return sqlDB.Ping() == nil
}

// userBrief 是账户同步接口返回给 V10 的最小用户信息。
type userBrief struct {
	UserId       int    `json:"user_id"`
	ClientId     int64  `json:"client_id"`
	Username     string `json:"username"`
	DisplayName  string `json:"display_name"`
	Email        string `json:"email"`
	Status       int    `json:"status"`
	Group        string `json:"group"`
	Quota        int64  `json:"quota"`
	QuotaMicros  int64  `json:"quota_micros"`
	QuotaAmount  string `json:"quota_amount"`
	UsedQuota    int64  `json:"used_quota"`
	RequestCount int64  `json:"request_count"`
}

// getUserBrief 读取用户的对外快照。
func getUserBrief(userId int) (*userBrief, error) {
	user, err := model.GetUserById(userId, true)
	if err != nil {
		return nil, err
	}
	return &userBrief{
		UserId:       user.Id,
		Username:     user.Username,
		DisplayName:  user.DisplayName,
		Email:        user.Email,
		Status:       user.Status,
		Group:        user.Group,
		Quota:        int64(user.Quota),
		QuotaMicros:  QuotaToMicros(int64(user.Quota)),
		QuotaAmount:  formatMicros(QuotaToMicros(int64(user.Quota))),
		UsedQuota:    int64(user.UsedQuota),
		RequestCount: int64(user.RequestCount),
	}, nil
}
