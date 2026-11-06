package v10

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

// 站点管理。这里只做"注册一个 V10 站点"和"改 secret"这类运维动作，
// 不做完整的 CRUD 后台——站点数量是个位数，用命令行/环境变量足够。

var ErrSiteExists = errors.New("v10 site with this app_id already exists")

// CreateSite 注册一个 V10 站点。
//
// secret 只在创建时由调用方持有一次；库中存密文与前缀，后台只能核对前缀。
func CreateSite(appId, name, secret string) (*V10Site, error) {
	appId = strings.TrimSpace(appId)
	if appId == "" {
		return nil, errors.New("app_id is required")
	}
	if strings.TrimSpace(secret) == "" {
		return nil, ErrSecretNotConfigured
	}

	encrypted, err := EncryptSecret(secret)
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	site := &V10Site{
		AppId:        appId,
		Name:         strings.TrimSpace(name),
		SecretEnc:    encrypted,
		SecretPrefix: secretPrefix(secret),
		Status:       V10SiteStatusEnabled,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := model.DB.Create(site).Error; err != nil {
		if isDuplicateKey(err) {
			return nil, ErrSiteExists
		}
		return nil, err
	}
	return site, nil
}

// RotateSecret 更换站点密钥。轮换后 V10 插件必须同步改成新值，
// 否则它的全部调用都会 401。
func RotateSecret(siteId int, newSecret string) error {
	if strings.TrimSpace(newSecret) == "" {
		return ErrSecretNotConfigured
	}
	encrypted, err := EncryptSecret(newSecret)
	if err != nil {
		return err
	}
	result := model.DB.Model(&V10Site{}).Where("id = ?", siteId).Updates(map[string]any{
		"secret_enc":    encrypted,
		"secret_prefix": secretPrefix(newSecret),
		"updated_at":    time.Now().Unix(),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrSiteNotFound
	}
	return nil
}

// SetSiteStatus 启用/停用站点。停用后该站点的所有签名请求一律 403。
func SetSiteStatus(siteId int, enabled bool) error {
	status := V10SiteStatusDisabled
	if enabled {
		status = V10SiteStatusEnabled
	}
	result := model.DB.Model(&V10Site{}).Where("id = ?", siteId).Updates(map[string]any{
		"status":     status,
		"updated_at": time.Now().Unix(),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrSiteNotFound
	}
	return nil
}

// ListSites 列出全部站点（不含 secret 明文）。
func ListSites() ([]V10Site, error) {
	var sites []V10Site
	err := model.DB.Order("id asc").Find(&sites).Error
	return sites, err
}

// GetSiteById 按主键取站点。后台按站点操作时用得到。
func GetSiteById(siteId int) (*V10Site, error) {
	if siteId <= 0 {
		return nil, ErrSiteNotFound
	}
	var site V10Site
	if err := model.DB.First(&site, siteId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSiteNotFound
		}
		return nil, err
	}
	return &site, nil
}

// GenerateSiteSecret 生成一个新的站点密钥（32 字节随机数的 hex，64 字符）。
//
// 后台新建站点时用：调用方拿到明文写进 V10 插件，库里只留密文。
func GenerateSiteSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

var _ = gorm.ErrRecordNotFound
