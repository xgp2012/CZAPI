package v10

import (
	"cmp"
	"slices"
	"time"

	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

// 本文件提供 V10 前台需要的两类只读数据：可售模型目录与用量明细。
// 两者都只读能力表/日志表，不参与计费，因此不会与结算逻辑产生耦合。

// ModelInfo 是返回给 V10 的模型条目。
type ModelInfo struct {
	Name string `json:"name"`
}

// ListModels 返回当前可售模型。
//
// 只暴露"已启用且有渠道可用"的模型：V10 前台据此渲染可选模型列表，
// 若把下架模型也列出去，客户选了却调不通，属于把内部状态泄漏成用户投诉。
func ListModels() []ModelInfo {
	names := model.GetEnabledModels()
	slices.Sort(names)

	out := make([]ModelInfo, 0, len(names))
	for _, name := range names {
		out = append(out, ModelInfo{Name: name})
	}
	return out
}

// UsageTotals 是时间窗内的用量合计。
type UsageTotals struct {
	RequestCount     int64  `json:"request_count"`
	Quota            int64  `json:"quota"`
	QuotaMicros      int64  `json:"quota_micros"`
	QuotaAmount      string `json:"quota_amount"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
}

// UsageBucket 是按 day/model/token 分组的用量。V10 账单图表直接用它，
// 不必自己拉明细再聚合。
type UsageBucket struct {
	Key              string `json:"key"`
	RequestCount     int64  `json:"request_count"`
	Quota            int64  `json:"quota"`
	QuotaMicros      int64  `json:"quota_micros"`
	QuotaAmount      string `json:"quota_amount"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
}

// UsageSummary 是某账户在某时间窗内的用量汇总。
type UsageSummary struct {
	UserId   int           `json:"user_id"`
	ClientId int64         `json:"client_id"`
	From     int64         `json:"from"`
	To       int64         `json:"to"`
	GroupBy  string        `json:"group_by"`
	Totals   UsageTotals   `json:"totals"`
	Buckets  []UsageBucket `json:"buckets"`
}

// UsageLogItem 是单条用量明细。
//
// 刻意**不返回** Content / Ip / ChannelId：V10 前台只需要"哪个模型花了多少"，
// 请求正文属于客户隐私，且正文可能很长，回传会把账单页变成数据出口。
type UsageLogItem struct {
	LogId            int    `json:"log_id"`
	CreatedAt        int64  `json:"created_at"`
	ModelName        string `json:"model_name"`
	TokenName        string `json:"token_name"`
	Quota            int64  `json:"quota"`
	QuotaMicros      int64  `json:"quota_micros"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	UseTime          int64  `json:"use_time"`
	IsStream         bool   `json:"is_stream"`
}

// UsageLogs 是明细分页结果。
type UsageLogs struct {
	UserId   int            `json:"user_id"`
	ClientId int64          `json:"client_id"`
	From     int64          `json:"from"`
	To       int64          `json:"to"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
	Total    int64          `json:"total"`
	Items    []UsageLogItem `json:"items"`
}

// usageScope 把"某个 V10 账户 + 时间窗"翻译成日志查询条件。
//
// 单点构造条件，避免汇总、分组、明细分页三处各写一遍而慢慢写歪。
type usageScope struct {
	userId int
	from   int64
	to     int64
}

// resolveUsageScope 解析账户并夹住时间窗。
//
// account 返回 nil 表示账户不存在，调用方要区分"没数据"和"没这个人"。
func resolveUsageScope(site *V10Site, clientId int64, from, to int64) (*usageScope, error) {
	account, err := GetAccount(site, clientId)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, nil
	}

	if to <= 0 {
		to = time.Now().Unix()
	}
	if from <= 0 {
		from = to - 30*24*3600
	}
	if from > to {
		from, to = to, from
	}

	return &usageScope{userId: account.UserId, from: from, to: to}, nil
}

// logs 返回已带上账户与时间窗条件的日志查询。
//
// 每次调用都新建查询：GORM 的链式条件会被就地累积，
// 同一个 *gorm.DB 复用给 Scan 再复用给 Find 会把条件叠加两次。
func (s *usageScope) logs() *gorm.DB {
	return model.LOG_DB.Model(&model.Log{}).
		Where("user_id = ? AND type = ?", s.userId, model.LogTypeConsume).
		Where("created_at >= ? AND created_at <= ?", s.from, s.to)
}

// GetUsageSummary 返回合计与按 groupBy 分组的明细。
//
// groupBy 支持 day / model / token，其余值按 day 处理。
func GetUsageSummary(site *V10Site, clientId int64, from, to int64, groupBy string) (*UsageSummary, error) {
	scope, err := resolveUsageScope(site, clientId, from, to)
	if err != nil {
		return nil, err
	}
	if scope == nil {
		return nil, nil
	}

	if groupBy != "model" && groupBy != "token" {
		groupBy = "day"
	}

	var totals UsageTotals
	if err := scope.logs().Select(
		"COUNT(*) as request_count, " +
			"COALESCE(SUM(quota), 0) as quota, " +
			"COALESCE(SUM(prompt_tokens), 0) as prompt_tokens, " +
			"COALESCE(SUM(completion_tokens), 0) as completion_tokens").
		Scan(&totals).Error; err != nil {
		return nil, err
	}
	totals.QuotaMicros = QuotaToMicros(totals.Quota)
	totals.QuotaAmount = formatMicros(totals.QuotaMicros)

	// 分组键各库写法不同：天的格式化只有 MySQL 有 FROM_UNIXTIME/DATE_FORMAT，
	// 所以不用数据库函数，改为把原始行捞回来在 Go 里聚合。
	// 账单页的窗口通常是一个月，行数可控；这也让三种数据库行为完全一致。
	var rows []struct {
		CreatedAt        int64
		ModelName        string
		TokenName        string
		Quota            int64
		PromptTokens     int64
		CompletionTokens int64
	}
	if err := scope.logs().
		Select("created_at", "model_name", "token_name", "quota", "prompt_tokens", "completion_tokens").
		Find(&rows).Error; err != nil {
		return nil, err
	}

	index := make(map[string]*UsageBucket, len(rows))
	order := make([]string, 0, len(rows))
	for i := range rows {
		row := &rows[i]

		var key string
		switch groupBy {
		case "model":
			key = row.ModelName
		case "token":
			key = row.TokenName
		default:
			// 用 UTC 日期做分组键，避免服务器时区变化把同一天拆成两桶。
			key = time.Unix(row.CreatedAt, 0).UTC().Format("2006-01-02")
		}
		if key == "" {
			key = "unknown"
		}

		bucket, ok := index[key]
		if !ok {
			bucket = &UsageBucket{Key: key}
			index[key] = bucket
			order = append(order, key)
		}
		bucket.RequestCount++
		bucket.Quota += row.Quota
		bucket.PromptTokens += row.PromptTokens
		bucket.CompletionTokens += row.CompletionTokens
	}

	buckets := make([]UsageBucket, 0, len(order))
	for _, key := range order {
		bucket := index[key]
		bucket.QuotaMicros = QuotaToMicros(bucket.Quota)
		bucket.QuotaAmount = formatMicros(bucket.QuotaMicros)
		buckets = append(buckets, *bucket)
	}
	// 天分组按时间正序，模型/Key 分组按花费倒序，各自符合账单页的阅读习惯。
	if groupBy == "day" {
		slices.SortFunc(buckets, func(a, b UsageBucket) int {
			return cmp.Compare(a.Key, b.Key)

		})
	} else {
		slices.SortFunc(buckets, func(a, b UsageBucket) int {
			return cmp.Compare(b.Quota, a.Quota)
		})
	}

	return &UsageSummary{
		UserId:   scope.userId,
		ClientId: clientId,
		From:     scope.from,
		To:       scope.to,
		GroupBy:  groupBy,
		Totals:   totals,
		Buckets:  buckets,
	}, nil
}

// GetUsageLogs 分页返回用量明细。
//
// page 从 1 开始，pageSize 默认 20、上限 200。
func GetUsageLogs(site *V10Site, clientId int64, from, to int64, page, pageSize int, modelName string) (*UsageLogs, error) {
	scope, err := resolveUsageScope(site, clientId, from, to)
	if err != nil {
		return nil, err
	}
	if scope == nil {
		return nil, nil
	}

	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 200 {
		pageSize = 200
	}

	withModel := func() *gorm.DB {
		query := scope.logs()
		if modelName != "" {
			query = query.Where("model_name = ?", modelName)
		}
		return query
	}

	var total int64
	if err := withModel().Count(&total).Error; err != nil {
		return nil, err
	}

	var rows []model.Log
	if err := withModel().
		Select("id", "created_at", "model_name", "token_name", "quota",
			"prompt_tokens", "completion_tokens", "use_time", "is_stream").
		Order("id DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&rows).Error; err != nil {
		return nil, err
	}

	items := make([]UsageLogItem, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		items = append(items, UsageLogItem{
			LogId:            row.Id,
			CreatedAt:        row.CreatedAt,
			ModelName:        row.ModelName,
			TokenName:        row.TokenName,
			Quota:            int64(row.Quota),
			QuotaMicros:      QuotaToMicros(int64(row.Quota)),
			PromptTokens:     int64(row.PromptTokens),
			CompletionTokens: int64(row.CompletionTokens),
			UseTime:          int64(row.UseTime),
			IsStream:         row.IsStream,
		})
	}

	return &UsageLogs{
		UserId:   scope.userId,
		ClientId: clientId,
		From:     scope.from,
		To:       scope.to,
		Page:     page,
		PageSize: pageSize,
		Total:    total,
		Items:    items,
	}, nil
}
