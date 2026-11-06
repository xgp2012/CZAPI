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

// Key 生命周期：创建 / 列表 / 改名与改限 / 吊销。
//
// 设计取舍：**直接复用 new-api 的 token 表**，不另建一套 Key 表。
// token 天然具备"用户归属、额度模式、模型限制、IP 白名单、过期时间"这些字段，
// 重建一套只会带来双写不一致。V10 侧只存 token 的 id 与前缀镜像。

var (
	ErrKeyLimitReached = errors.New("key limit reached")
	ErrKeyNotFound     = errors.New("key not found")
)

// KeyView 是返回给 V10 的 Key 视图。**永不包含明文**，只给前缀。
type KeyView struct {
	KeyId       int      `json:"key_id"`
	KeyPrefix   string   `json:"key_prefix"`
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	Models      []string `json:"models"`
	IpWhitelist []string `json:"ip_whitelist"`
	ExpiresAt   *int64   `json:"expires_at"`
	LastUsedAt  int64    `json:"last_used_at"`
	CreatedAt   int64    `json:"created_at"`
	UsedQuota   int64    `json:"used_quota"`
	RemainQuota int64    `json:"remain_quota"`
}

// 对外的 Key 状态词表。与 new-api 的 token 状态数字解耦，V10 只认这三个。
const (
	KeyStatusActive    = "active"
	KeyStatusSuspended = "suspended"
	KeyStatusRevoked   = "revoked"
)

// CreateKeyRequest 是 V10 建 Key 的请求。
type CreateKeyRequest struct {
	ClientId int64 `json:"client_id"`
	HostId   int64 `json:"host_id"`

	Name        string   `json:"name"`
	Model       []string `json:"models"`
	IpWhitelist []string `json:"ip_whitelist"`

	// ExpiresAt 为 0 表示不过期。
	ExpiresAt int64 `json:"expires_at"`

	// MaxKeys 由 V10 按套餐下发；0 表示用全局默认上限。
	MaxKeys int `json:"max_keys"`
}

// CreatedKey 只在创建时返回一次明文。
type CreatedKey struct {
	KeyView
	// Key 是明文 sk-xxx。重复调用一律为空。
	Key string `json:"key,omitempty"`
}

// CreateKey 为某客户创建一把 Key。
//
// 明文只在本次响应里出现：网关库存哈希与明文的随机部分，V10 只存 id + 前缀，
// 因此任何人（包括管理员）都无法再次取回。
func CreateKey(site *V10Site, req CreateKeyRequest) (*CreatedKey, error) {
	account, err := EnsureAccount(site, req.ClientId, "", "")
	if err != nil {
		return nil, err
	}

	limit := req.MaxKeys
	if limit <= 0 {
		limit = 5
	}
	count, err := model.CountUserTokens(account.UserId)
	if err != nil {
		return nil, err
	}
	if int(count) >= limit {
		return nil, ErrKeyLimitReached
	}

	rawKey, err := common.GenerateKey()
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	token := model.Token{
		UserId:      account.UserId,
		Key:         rawKey,
		Status:      common.TokenStatusEnabled,
		Name:        keyName(req.Name, req.HostId),
		CreatedTime: now,
		ExpiredTime: -1,
		// 额度跟随用户额度：套餐与余额都在用户维度统一扣减，
		// 不在这里切一刀，避免"订阅用尽但 Key 以为自己还有额度"。
		UnlimitedQuota: true,
		Group:          "",
	}
	if req.ExpiresAt > 0 {
		token.ExpiredTime = req.ExpiresAt
	}
	applyKeyRestrictions(&token, req.Model, req.IpWhitelist)

	if err := token.Insert(); err != nil {
		return nil, err
	}

	created := &CreatedKey{Key: "sk-" + rawKey}
	created.KeyView = toKeyView(&token)
	return created, nil
}

func keyName(name string, hostId int64) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		trimmed = "key"
	}
	if hostId > 0 {
		trimmed = trimmed + "#" + strconv.FormatInt(hostId, 10)
	}
	runes := []rune(trimmed)
	if len(runes) > 50 {
		return string(runes[:50])
	}
	return trimmed
}

// applyKeyRestrictions 把模型白名单与 IP 白名单写进 token。
//
// ModelLimits 在 new-api 里是**逗号分隔字符串**（由 Token.GetModelLimits 按 "," 切分），
// 不是 JSON。写成 JSON 会让模型限制静默失效——看起来有限制，实际不生效。
func applyKeyRestrictions(token *model.Token, models []string, ips []string) {
	if len(models) > 0 {
		cleaned := make([]string, 0, len(models))
		for _, name := range models {
			name = strings.TrimSpace(name)
			if name != "" && !strings.Contains(name, ",") {
				cleaned = append(cleaned, name)
			}
		}
		if len(cleaned) > 0 {
			token.ModelLimitsEnabled = true
			token.ModelLimits = strings.Join(cleaned, ",")
		}
	}
	if len(ips) > 0 {
		joined := strings.Join(ips, "\n")
		token.AllowIps = &joined
	}
}

// ListKeys 列出某客户的全部 Key（不含明文）。
func ListKeys(site *V10Site, clientId int64) ([]KeyView, error) {
	account, err := GetAccount(site, clientId)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return []KeyView{}, nil
	}

	var tokens []model.Token
	if err := model.DB.Where("user_id = ?", account.UserId).Order("id desc").Find(&tokens).Error; err != nil {
		return nil, err
	}
	views := make([]KeyView, 0, len(tokens))
	for i := range tokens {
		views = append(views, toKeyView(&tokens[i]))
	}
	return views, nil
}

// UpdateKeyRequest 是部分更新。指针字段为 nil 表示不改。
type UpdateKeyRequest struct {
	ClientId int64 `json:"client_id"`

	Name        *string   `json:"name"`
	Status      *string   `json:"status"`
	Model       *[]string `json:"models"`
	IpWhitelist *[]string `json:"ip_whitelist"`
	ExpiresAt   *int64    `json:"expires_at"`
}

// UpdateKey 改名、启停、改限或改过期时间。
func UpdateKey(site *V10Site, keyId int, req UpdateKeyRequest) (*KeyView, error) {
	account, err := GetAccount(site, req.ClientId)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, ErrKeyNotFound
	}

	token, err := model.GetTokenByIds(keyId, account.UserId)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrKeyNotFound
		}
		return nil, err
	}

	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name != "" {
			runes := []rune(name)
			if len(runes) > 50 {
				name = string(runes[:50])
			}
			token.Name = name
		}
	}
	if req.Status != nil {
		switch *req.Status {
		case KeyStatusActive:
			token.Status = common.TokenStatusEnabled
		case KeyStatusSuspended, KeyStatusRevoked:
			token.Status = common.TokenStatusDisabled
		default:
			return nil, errors.New("status must be active/suspended/revoked")
		}
	}
	if req.Model != nil {
		token.ModelLimitsEnabled = false
		token.ModelLimits = ""
		applyKeyRestrictions(token, *req.Model, nil)
	}
	if req.IpWhitelist != nil {
		if len(*req.IpWhitelist) == 0 {
			empty := ""
			token.AllowIps = &empty
		} else {
			applyKeyRestrictions(token, nil, *req.IpWhitelist)
		}
	}
	if req.ExpiresAt != nil {
		if *req.ExpiresAt <= 0 {
			token.ExpiredTime = -1
		} else {
			token.ExpiredTime = *req.ExpiresAt
		}
	}

	if err := token.Update(); err != nil {
		return nil, err
	}
	view := toKeyView(token)
	return &view, nil
}

// RevokeKey 吊销一把 Key。幂等：已吊销再调仍然成功。
func RevokeKey(site *V10Site, clientId int64, keyId int) error {
	account, err := GetAccount(site, clientId)
	if err != nil {
		return err
	}
	if account == nil {
		return ErrKeyNotFound
	}
	token, err := model.GetTokenByIds(keyId, account.UserId)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrKeyNotFound
		}
		return err
	}
	if token.Status == common.TokenStatusExpired {
		return nil
	}
	// 吊销必须落到 expired 而不是 disabled：disabled 表示"临时停用"，
	// 套餐恢复时会被 setUserKeysStatus 拉回可用，等于吊销无效。
	token.Status = common.TokenStatusExpired
	return token.Update()
}

// toKeyView 把 token 转为对 V10 的视图。**不含明文**。
func toKeyView(token *model.Token) KeyView {
	view := KeyView{
		KeyId:       token.Id,
		KeyPrefix:   token.GetMaskedKey(),
		Name:        token.Name,
		Status:      keyStatusOf(token),
		Models:      token.GetModelLimits(),
		IpWhitelist: token.GetIpLimits(),
		LastUsedAt:  token.AccessedTime,
		CreatedAt:   token.CreatedTime,
		UsedQuota:   int64(token.UsedQuota),
		RemainQuota: int64(token.RemainQuota),
	}
	if view.Models == nil {
		view.Models = []string{}
	}
	if view.IpWhitelist == nil {
		view.IpWhitelist = []string{}
	}
	if token.ExpiredTime > 0 {
		expires := token.ExpiredTime
		view.ExpiresAt = &expires
	}
	return view
}

// keyStatusOf 把网关内部的 token 状态映射为 V10 看到的三种状态。
//
// 映射必须区分"暂停"与"吊销"，两者对客户的意义完全不同：
//   - 暂停（欠费）是可恢复的，恢复后还能继续用；
//   - 吊销是永久的，客户必须重新签发。
//
// 早期把 expired 也映射成 suspended，于是吊销在 V10 界面上显示为"已暂停"，
// 客户会以为交钱就能恢复，实际那把 Key 已经作废。
func keyStatusOf(token *model.Token) string {
	switch token.Status {
	case common.TokenStatusEnabled:
		return KeyStatusActive
	case common.TokenStatusExpired:
		return KeyStatusRevoked
	case common.TokenStatusDisabled, common.TokenStatusExhausted:
		return KeyStatusSuspended
	default:
		return KeyStatusRevoked
	}
}
