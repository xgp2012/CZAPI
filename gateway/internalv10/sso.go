package v10

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 本文件实现 V10 单点登录。设计前提：V10 才是账号体系的唯一入口，
// 网关不允许用户自己注册或输密码登录，所以必须有一条"V10 证明这个人是谁，
// 网关据此发一个自己的会话"的通道。
//
// 安全要点（对齐 OWASP ASVS 会话管理要求）：
//
//  1. 票据是一次性的。消费即删除，重放拿不到第二个会话。
//  2. 票据只存哈希。数据库泄露也换不来会话（与 refresh token 同思路）。
//  3. 票据带有效期（默认 60 秒），且绑定签名的那个站点，跨站点不可用。
//  4. 票据通过签名接口签发，浏览器拿不到；浏览器只拿到一次性的登录 URL。
//  5. 消费时必须由 V10 服务端签名调用，避免浏览器自己造票据。

const (
	// TicketTTLSeconds 是票据有效期。V10 点完"进入控制台"到浏览器打开页面
	// 要经过一跳网络，60 秒足够，又短到即使泄漏也很难利用。
	TicketTTLSeconds int64 = 60

	// ticketCleanupKeepSeconds 控制过期票据保留多久再清理，
	// 保留一小段时间是为了让"票据过期"和"票据不存在"能给出不同提示。
	ticketCleanupKeepSeconds int64 = 24 * 3600
)

var (
	ErrTicketInvalid = errors.New("v10 login ticket is invalid or expired")
	ErrTicketUsed    = errors.New("v10 login ticket has already been used")
)

// V10LoginTicket 是一次性登录票据。
type V10LoginTicket struct {
	Id int `json:"id" gorm:"primaryKey"`

	// TicketHash 存票据的 SHA-256，明文只在签发响应里出现一次。
	TicketHash string `json:"-" gorm:"type:char(64);uniqueIndex;not null"`

	SiteId    int   `json:"site_id" gorm:"index;not null"`
	UserId    int   `json:"user_id" gorm:"index;not null"`
	ExpiresAt int64 `json:"expires_at" gorm:"bigint;not null;default:0"`

	// UsedAt 非 0 表示已消费，用于区分"重放"与"过期"。
	UsedAt    int64 `json:"used_at" gorm:"bigint;not null;default:0"`
	CreatedAt int64 `json:"created_at" gorm:"bigint;not null;default:0"`
}

func (V10LoginTicket) TableName() string { return "v10_login_tickets" }

// IssueLoginTicket 为某账户签发一张一次性票据，返回明文票据。
//
// 票据明文形如 "<id>.<random>"，id 段让消费端一次查询就能定位，
// 随机段是真正的凭证。这不算信息泄漏：id 本身不是秘密。
func IssueLoginTicket(site *V10Site, clientId int64) (string, int64, error) {
	account, err := GetAccount(site, clientId)
	if err != nil {
		return "", 0, err
	}
	if account == nil {
		return "", 0, ErrAccountNotFound
	}

	random, err := common.GenerateRandomCharsKey(64)
	if err != nil {
		return "", 0, err
	}
	now := time.Now().Unix()
	expiresAt := now + TicketTTLSeconds

	// 顺手清掉早就过期的票据，避免表只增不减。
	model.DB.Where("expires_at < ?", now-ticketCleanupKeepSeconds).Delete(&V10LoginTicket{})

	ticket := V10LoginTicket{
		TicketHash: hashToken(random),
		SiteId:     site.Id,
		UserId:     account.UserId,
		ExpiresAt:  expiresAt,
		CreatedAt:  now,
	}
	if err := model.DB.Create(&ticket).Error; err != nil {
		return "", 0, err
	}
	return strconv.Itoa(ticket.Id) + "." + random, expiresAt, nil
}

// ConsumeLoginTicket 校验并消费票据，返回对应的网关用户。
//
// 消费是原子的：只有一行被标记为已用，并发重复提交只有一个能成功，
// 否则同一张票据可以换出多个会话。
func ConsumeLoginTicket(site *V10Site, raw string) (int, error) {
	idPart, secret, ok := strings.Cut(strings.TrimSpace(raw), ".")
	if !ok || secret == "" {
		return 0, ErrTicketInvalid
	}
	ticketId, err := strconv.Atoi(idPart)
	if err != nil || ticketId <= 0 {
		return 0, ErrTicketInvalid
	}

	var ticket V10LoginTicket
	err = model.DB.Where("id = ? AND site_id = ?", ticketId, site.Id).First(&ticket).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, ErrTicketInvalid
	}
	if err != nil {
		return 0, err
	}
	if ticket.TicketHash != hashToken(secret) {
		return 0, ErrTicketInvalid
	}
	if ticket.UsedAt > 0 {
		return 0, ErrTicketUsed
	}
	if ticket.ExpiresAt < time.Now().Unix() {
		return 0, ErrTicketInvalid
	}

	// 条件更新保证原子消费：只有 used_at 仍为 0 的那一次能改成功。
	result := model.DB.Model(&V10LoginTicket{}).
		Where("id = ? AND used_at = 0", ticket.Id).
		Update("used_at", time.Now().Unix())
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected == 0 {
		return 0, ErrTicketUsed
	}

	return ticket.UserId, nil
}

// LoginURL 拼出浏览器要打开的控制台地址。票据放在 fragment 里，
// 这样它不会进服务端访问日志，也不会随 Referer 外泄。
func LoginURL(consoleBaseUrl, rawTicket string) string {
	base := strings.TrimRight(strings.TrimSpace(consoleBaseUrl), "/")
	if base == "" {
		return ""
	}
	return base + "/v10/login#" + rawTicket
}

// hashToken 用 SHA-256 存票据摘要。票据由 64 位随机字符组成，本身没有
// 可猜测的结构，所以不需要加盐或 HMAC，也不存在彩虹表问题。
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// ConsoleBaseUrl 取本网关对外可访问地址，用于拼登录跳转链接。
// 优先用显式配置，避免"以为自己在 8080 端口"这类反向代理误判。
func ConsoleBaseUrl(c *gin.Context) string {
	if v := strings.TrimSpace(common.GetEnvOrDefaultString("V10_CONSOLE_BASE_URL", "")); v != "" {
		return v
	}
	if c == nil {
		return ""
	}
	scheme := "http"
	if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host
}
