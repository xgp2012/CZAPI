package v10

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// 后台各业务模块的查询与操作实现。
//
// 原则：后台不自己写一套业务逻辑，全部复用 internalv10 已有函数
// （ApplyCredit / RotateSiteSecret / SetHostStatus 等），保证"命令行能做对的事，
// 点按钮也一定做对"，不会出现两条路径行为不一致。

const consolePageSizeMax = 200

// pageParams 统一解析分页参数。
func pageParams(c *gin.Context) (page, size int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ = strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > consolePageSizeMax {
		size = consolePageSizeMax
	}
	return page, size
}

// ---------------------------------------------------------------- 站点

func handleConsoleListSites(c *gin.Context) {
	sites, err := ListSites()
	if err != nil {
		common.ApiError(c, err)
		return
	}

	items := make([]siteOption, 0, len(sites))
	for i := range sites {
		s := &sites[i]
		items = append(items, siteOption{
			Id:           s.Id,
			AppId:        s.AppId,
			Name:         s.Name,
			SecretPrefix: s.SecretPrefix,
			Status:       s.Status,
			CreatedAt:    s.CreatedAt,
		})
	}
	common.ApiSuccess(c, gin.H{"items": items, "total": len(items)})
}

type consoleCreateSiteRequest struct {
	AppId string `json:"app_id"`
	Name  string `json:"name"`
}

func handleConsoleCreateSite(c *gin.Context) {
	var req consoleCreateSiteRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorMsg(c, "请求格式不正确")
		return
	}

	appId := strings.TrimSpace(req.AppId)
	if appId == "" {
		common.ApiErrorMsg(c, "app_id 不能为空")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = appId
	}

	secret, err := GenerateSiteSecret()
	if err != nil {
		common.ApiError(c, err)
		return
	}

	site, err := CreateSite(appId, name, secret)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{
		"id":      site.Id,
		"app_id":  site.AppId,
		"name":    site.Name,
		"secret":  secret,
		"warning": "请立即保存此 secret，之后无法再次查看",
	})
}

func handleConsoleRotateSecret(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiErrorMsg(c, "站点 ID 不正确")
		return
	}

	secret, err := GenerateSiteSecret()
	if err != nil {
		common.ApiError(c, err)
		return
	}

	if err := RotateSecret(id, secret); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{
		"secret":  secret,
		"warning": "旧 secret 已立即失效，请同步更新 V10 插件配置",
	})
}

type consoleSiteStatusRequest struct {
	Enabled bool `json:"enabled"`
}

func handleConsoleSetSiteStatus(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiErrorMsg(c, "站点 ID 不正确")
		return
	}

	var req consoleSiteStatusRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorMsg(c, "请求格式不正确")
		return
	}

	if err := SetSiteStatus(id, req.Enabled); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

// ---------------------------------------------------------------- 账户与额度

// consoleAccountRow 把 V10 账户与网关用户信息拼在一起返回。
type consoleAccountRow struct {
	UserId    int    `json:"user_id"`
	ClientId  int64  `json:"client_id"`
	SiteId    int    `json:"site_id"`
	Username  string `json:"username"`
	Quota     int64  `json:"quota"`
	UsedQuota int64  `json:"used_quota"`
	CreatedAt int64  `json:"created_at"`
}

func handleConsoleListAccounts(c *gin.Context) {
	page, size := pageParams(c)
	query := model.DB.Model(&V10Account{})

	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		if id, err := strconv.ParseInt(kw, 10, 64); err == nil {
			// 纯数字时无法判断运维想找哪个：V10 的 client_id 与网关 user_id
			// 都是数字。同时匹配两者，否则"按用户 ID 搜不到"会让人困惑。
			query = query.Where("client_id = ? OR user_id = ?", id, id)
		} else {
			var userIds []int
			model.DB.Model(&model.User{}).Where("username LIKE ?", "%"+kw+"%").
				Limit(500).Pluck("id", &userIds)
			if len(userIds) == 0 {
				common.ApiSuccess(c, gin.H{"items": []consoleAccountRow{}, "total": 0})
				return
			}
			query = query.Where("user_id IN ?", userIds)
		}
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		common.ApiError(c, err)
		return
	}

	var accounts []V10Account
	if err := query.Order("id DESC").Offset((page - 1) * size).Limit(size).
		Find(&accounts).Error; err != nil {
		common.ApiError(c, err)
		return
	}

	// 批量取用户，避免每行一次 SQL（翻页时 N+1 会明显拖慢）。
	userIds := make([]int, 0, len(accounts))
	for i := range accounts {
		userIds = append(userIds, accounts[i].UserId)
	}
	userByID := map[int]model.User{}
	if len(userIds) > 0 {
		var users []model.User
		if err := model.DB.Where("id IN ?", userIds).Find(&users).Error; err == nil {
			for i := range users {
				userByID[users[i].Id] = users[i]
			}
		}
	}

	rows := make([]consoleAccountRow, 0, len(accounts))
	for i := range accounts {
		a := &accounts[i]
		row := consoleAccountRow{
			UserId:    a.UserId,
			ClientId:  a.ClientId,
			SiteId:    a.SiteId,
			CreatedAt: a.CreatedAt,
		}
		if user, ok := userByID[a.UserId]; ok {
			row.Username = user.Username
			row.Quota = int64(user.Quota)
			row.UsedQuota = int64(user.UsedQuota)
		}
		rows = append(rows, row)
	}

	common.ApiSuccess(c, gin.H{"items": rows, "total": total, "page": page, "page_size": size})
}

type consoleCreditRequest struct {
	// AmountYuan 是人民币金额，正数加额、负数扣额。前端按元填写，
	// 后端换算成微元，避免让运维直接算 quota。
	AmountYuan float64 `json:"amount_yuan"`
	Remark     string  `json:"remark"`
	AllowNeg   bool    `json:"allow_negative"`
}

func handleConsoleManualCredit(c *gin.Context) {
	userId, err := strconv.Atoi(c.Param("userId"))
	if err != nil || userId <= 0 {
		common.ApiErrorMsg(c, "用户 ID 不正确")
		return
	}

	var req consoleCreditRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorMsg(c, "请求格式不正确")
		return
	}
	if req.AmountYuan == 0 {
		common.ApiErrorMsg(c, "金额不能为 0")
		return
	}

	// 元 → 微元。四舍五入到整数微元，避免浮点残留。
	micros := int64(req.AmountYuan*1_000_000 + sign(req.AmountYuan)*0.5)
	if micros == 0 {
		common.ApiErrorMsg(c, "金额不能为 0")
		return
	}

	// 单笔上限来自 users.quota 的 32 位存贮上限。超了要让运维看到明确指引，
	// 而不是把 driving 层的数据库错误原样抛出去。
	maxYuan := float64(maxSingleCreditQuota) / common.QuotaPerUnit
	if math.Abs(req.AmountYuan) > maxYuan {
		common.ApiErrorMsg(c, fmt.Sprintf(
			"单笔调额上限约 %.2f 元（受 quota 字段 32 位上限制约）；请拆成多笔，每笔都会独立记账",
			maxYuan))
		return
	}

	var account V10Account
	if err := model.DB.Where("user_id = ?", userId).First(&account).Error; err != nil {
		common.ApiErrorMsg(c, "该用户不是 V10 托管账户，不能通过后台调额")
		return
	}
	site, err := GetSiteById(account.SiteId)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	// 操作人用于账本备注留痕。双凭据下可能是 new-api 管理员，
	// 也可能是旧 czapi_admin 会话，统一由 ConsoleOperator 归一。
	operator := ConsoleOperator(c)

	// ref_id 用时间戳 + 操作人，保证每次手工调额都是独立凭证，不会被幂等挡掉。
	refId := "ADMIN-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	remark := strings.TrimSpace(req.Remark)
	if remark == "" {
		remark = "后台手工调额"
	}

	result, err := ApplyCredit(site, CreditRequest{
		ClientId:      account.ClientId,
		Kind:          "admin_adjust",
		AmountMicros:  micros,
		RefType:       "admin_adjust",
		RefId:         refId,
		Remark:        remark + " by " + operator,
		AllowNegative: req.AllowNeg,
	})
	if err != nil {
		common.ApiError(c, err)
		return
	}

	common.ApiSuccess(c, result)
}

func sign(f float64) float64 {
	if f < 0 {
		return -1
	}
	return 1
}

// ---------------------------------------------------------------- 账本

func handleConsoleListLedger(c *gin.Context) {
	page, size := pageParams(c)
	query := model.DB.Model(&V10Ref{})

	if siteId := c.Query("site_id"); siteId != "" {
		if v, err := strconv.Atoi(siteId); err == nil && v > 0 {
			query = query.Where("site_id = ?", v)
		}
	}
	if kind := strings.TrimSpace(c.Query("kind")); kind != "" {
		query = query.Where("kind = ?", kind)
	}
	if refType := strings.TrimSpace(c.Query("ref_type")); refType != "" {
		query = query.Where("ref_type = ?", refType)
	}
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		query = query.Where("ref_id LIKE ?", "%"+kw+"%")
	}
	// 时间范围按秒级时间戳，前端传 Unix 秒。
	if from := c.Query("from"); from != "" {
		if v, err := strconv.ParseInt(from, 10, 64); err == nil && v > 0 {
			query = query.Where("created_at >= ?", v)
		}
	}
	if to := c.Query("to"); to != "" {
		if v, err := strconv.ParseInt(to, 10, 64); err == nil && v > 0 {
			query = query.Where("created_at <= ?", v)
		}
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		common.ApiError(c, err)
		return
	}

	var refs []V10Ref
	if err := query.Order("id DESC").Offset((page - 1) * size).Limit(size).
		Find(&refs).Error; err != nil {
		common.ApiError(c, err)
		return
	}

	common.ApiSuccess(c, gin.H{
		"items":     refs,
		"total":     total,
		"page":      page,
		"page_size": size,
	})
}

// ---------------------------------------------------------------- Key

type consoleKeyRow struct {
	Id          int    `json:"id"`
	UserId      int    `json:"user_id"`
	Username    string `json:"username"`
	ClientId    int64  `json:"client_id"`
	Name        string `json:"name"`
	Status      int    `json:"status"`
	UsedQuota   int64  `json:"used_quota"`
	ExpiredTime int64  `json:"expired_time"`
	CreatedTime int64  `json:"created_time"`
	Unlimited   bool   `json:"unlimited_quota"`
}

func handleConsoleListKeys(c *gin.Context) {
	page, size := pageParams(c)
	query := model.DB.Model(&model.Token{})

	// 只列 V10 托管账户的 Key，避免后台看到 new-api 自带的其它 Key。
	accountQuery := model.DB.Model(&V10Account{}).Select("user_id")
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		query = query.Where("name LIKE ?", "%"+kw+"%")
	}
	if userId := c.Query("user_id"); userId != "" {
		if v, err := strconv.Atoi(userId); err == nil && v > 0 {
			query = query.Where("user_id = ?", v)
		}
	} else {
		query = query.Where("user_id IN (?)", accountQuery)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		common.ApiError(c, err)
		return
	}

	var tokens []model.Token
	if err := query.Order("id DESC").Offset((page - 1) * size).Limit(size).
		Find(&tokens).Error; err != nil {
		common.ApiError(c, err)
		return
	}

	// 批量取客户号与用户名，避免每行两次 SQL。
	userIds := make([]int, 0, len(tokens))
	for i := range tokens {
		userIds = append(userIds, tokens[i].UserId)
	}
	accountByUser := map[int]V10Account{}
	userByID := map[int]model.User{}
	if len(userIds) > 0 {
		var accounts []V10Account
		if err := model.DB.Where("user_id IN ?", userIds).Find(&accounts).Error; err == nil {
			for i := range accounts {
				accountByUser[accounts[i].UserId] = accounts[i]
			}
		}
		var users []model.User
		if err := model.DB.Where("id IN ?", userIds).Find(&users).Error; err == nil {
			for i := range users {
				userByID[users[i].Id] = users[i]
			}
		}
	}

	rows := make([]consoleKeyRow, 0, len(tokens))
	for i := range tokens {
		t := &tokens[i]
		row := consoleKeyRow{
			Id:          t.Id,
			UserId:      t.UserId,
			Name:        t.Name,
			Status:      t.Status,
			UsedQuota:   int64(t.UsedQuota),
			ExpiredTime: t.ExpiredTime,
			CreatedTime: t.CreatedTime,
			Unlimited:   t.UnlimitedQuota,
		}
		row.ClientId = accountByUser[t.UserId].ClientId
		if user, ok := userByID[t.UserId]; ok {
			row.Username = user.Username
		}
		rows = append(rows, row)
	}

	common.ApiSuccess(c, gin.H{"items": rows, "total": total, "page": page, "page_size": size})
}

func handleConsoleRevokeKey(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiErrorMsg(c, "Key ID 不正确")
		return
	}

	// 只允许吊销 V10 托管账户的 Key，防止误删运维自建的测试 Key。
	var token model.Token
	if err := model.DB.First(&token, id).Error; err != nil {
		common.ApiErrorMsg(c, "Key 不存在")
		return
	}
	var count int64
	model.DB.Model(&V10Account{}).Where("user_id = ?", token.UserId).Count(&count)
	if count == 0 {
		common.ApiErrorMsg(c, "该 Key 不属于 V10 托管账户")
		return
	}

	// 置为禁用而非物理删除：日志里还引用着 token_name，删掉会让历史记录失去归属。
	if err := model.DB.Model(&model.Token{}).Where("id = ?", id).
		Update("status", 2).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

// ---------------------------------------------------------------- host / 订阅

func handleConsoleListHosts(c *gin.Context) {
	page, size := pageParams(c)
	query := model.DB.Model(&V10Host{})

	if status := strings.TrimSpace(c.Query("status")); status != "" {
		query = query.Where("host_status = ?", status)
	}
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		if v, err := strconv.ParseInt(kw, 10, 64); err == nil {
			// v10_hosts 没有 client_id 列，客户号在 v10_accounts 上。
			// 按客户号过滤需要先换算成 user_id 集合，不能直接比较。
			var userIds []int
			model.DB.Model(&V10Account{}).Where("client_id = ?", v).
				Limit(500).Pluck("user_id", &userIds)
			if len(userIds) == 0 {
				query = query.Where("host_id = ?", v)
			} else {
				query = query.Where("host_id = ? OR user_id IN ?", v, userIds)
			}
		}
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		common.ApiError(c, err)
		return
	}

	var hosts []V10Host
	if err := query.Order("id DESC").Offset((page - 1) * size).Limit(size).
		Find(&hosts).Error; err != nil {
		common.ApiError(c, err)
		return
	}

	// 补 client_id 与用户名：host 表本身没有客户号，要经 v10_accounts 反查。
	// 一次批量查询而不是逐行查，避免 20 行就产生 40 次 SQL。
	userIds := make([]int, 0, len(hosts))
	for i := range hosts {
		userIds = append(userIds, hosts[i].UserId)
	}
	accountByUser := map[int]V10Account{}
	if len(userIds) > 0 {
		var accounts []V10Account
		if err := model.DB.Where("user_id IN ?", userIds).Find(&accounts).Error; err == nil {
			for i := range accounts {
				accountByUser[accounts[i].UserId] = accounts[i]
			}
		}
	}

	items := make([]gin.H, 0, len(hosts))
	for i := range hosts {
		h := &hosts[i]
		acc := accountByUser[h.UserId]
		items = append(items, gin.H{
			"id":                   h.Id,
			"site_id":              h.SiteId,
			"host_id":              h.HostId,
			"user_id":              h.UserId,
			"client_id":            acc.ClientId,
			"user_subscription_id": h.UserSubscriptionId,
			"plan_id":              h.PlanId,
			"host_status":          h.HostStatus,
			"created_at":           h.CreatedAt,
			"updated_at":           h.UpdatedAt,
		})
	}

	common.ApiSuccess(c, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}

type consoleHostStatusRequest struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func handleConsoleSetHostStatus(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiErrorMsg(c, "host ID 不正确")
		return
	}

	var req consoleHostStatusRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorMsg(c, "请求格式不正确")
		return
	}

	var host V10Host
	if err := model.DB.First(&host, id).Error; err != nil {
		common.ApiErrorMsg(c, "host 不存在")
		return
	}

	site, err := GetSiteById(host.SiteId)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	result, err := SetHostStatus(site, host.HostId, req.Status, req.Reason)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, result)
}

// ---------------------------------------------------------------- 用量

// handleConsoleUsageSummary 按客户聚合区间内的消费。
func handleConsoleUsageSummary(c *gin.Context) {
	from, to := usageRange(c)

	type row struct {
		UserId       int    `json:"user_id"`
		ClientId     int64  `json:"client_id"`
		Username     string `json:"username"`
		Quota        int64  `json:"quota"`
		RequestCount int64  `json:"request_count"`
		PromptTokens int64  `json:"prompt_tokens"`
		CompTokens   int64  `json:"completion_tokens"`
	}

	var rows []row
	err := model.DB.Model(&model.Log{}).
		Select("user_id, COALESCE(SUM(quota),0) as quota, COUNT(*) as request_count, "+
			"COALESCE(SUM(prompt_tokens),0) as prompt_tokens, "+
			"COALESCE(SUM(completion_tokens),0) as comp_tokens").
		Where("created_at >= ? AND created_at <= ? AND type = ?", from, to, model.LogTypeConsume).
		Group("user_id").
		Order("quota DESC").
		Limit(100).
		Scan(&rows).Error
	if err != nil {
		common.ApiError(c, err)
		return
	}

	// 补上 client_id 与用户名，前端表格才看得懂。
	// 批量查而不是逐行查：最多 100 行，逐行就是 200 次 SQL。
	userIds := make([]int, 0, len(rows))
	for i := range rows {
		userIds = append(userIds, rows[i].UserId)
	}
	accountByUser := map[int]V10Account{}
	userByID := map[int]model.User{}
	if len(userIds) > 0 {
		var accounts []V10Account
		if err := model.DB.Where("user_id IN ?", userIds).Find(&accounts).Error; err == nil {
			for i := range accounts {
				accountByUser[accounts[i].UserId] = accounts[i]
			}
		}
		var users []model.User
		if err := model.DB.Where("id IN ?", userIds).Find(&users).Error; err == nil {
			for i := range users {
				userByID[users[i].Id] = users[i]
			}
		}
	}
	for i := range rows {
		rows[i].ClientId = accountByUser[rows[i].UserId].ClientId
		if user, ok := userByID[rows[i].UserId]; ok {
			rows[i].Username = user.Username
		}
	}

	common.ApiSuccess(c, gin.H{"items": rows, "from": from, "to": to})
}

// handleConsoleUsageByModel 按模型聚合，用于看哪个模型最烧钱。
func handleConsoleUsageByModel(c *gin.Context) {
	from, to := usageRange(c)

	type row struct {
		ModelName    string `json:"model_name"`
		Quota        int64  `json:"quota"`
		RequestCount int64  `json:"request_count"`
		PromptTokens int64  `json:"prompt_tokens"`
		CompTokens   int64  `json:"completion_tokens"`
	}

	var rows []row
	err := model.DB.Model(&model.Log{}).
		Select("model_name, COALESCE(SUM(quota),0) as quota, COUNT(*) as request_count, "+
			"COALESCE(SUM(prompt_tokens),0) as prompt_tokens, "+
			"COALESCE(SUM(completion_tokens),0) as comp_tokens").
		Where("created_at >= ? AND created_at <= ? AND type = ?", from, to, model.LogTypeConsume).
		Group("model_name").
		Order("quota DESC").
		Limit(100).
		Scan(&rows).Error
	if err != nil {
		common.ApiError(c, err)
		return
	}

	common.ApiSuccess(c, gin.H{"items": rows, "from": from, "to": to})
}

// usageRange 默认取最近 7 天。
func usageRange(c *gin.Context) (from, to int64) {
	now := time.Now()
	to = now.Unix()
	from = now.AddDate(0, 0, -7).Unix()

	if v := c.Query("from"); v != "" {
		if parsed, err := strconv.ParseInt(v, 10, 64); err == nil && parsed > 0 {
			from = parsed
		}
	}
	if v := c.Query("to"); v != "" {
		if parsed, err := strconv.ParseInt(v, 10, 64); err == nil && parsed > 0 {
			to = parsed
		}
	}
	return from, to
}
