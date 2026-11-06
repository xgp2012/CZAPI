package v10

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// app_secret 是 V10 与网关之间唯一的信任根：拿到它就等于可以冒充 V10 加额度。
// 因此它**不以明文落库**——数据库被读走（备份泄露、注入、运维误导出）不等于
// 签名密钥泄露。落库格式：enc:v1:<base64(nonce||ciphertext)>。
//
// 加密密钥来自环境变量 V10_SECRET_ENC_KEY，未配置时回落到 CRYPTO_SECRET。
// 注意：轮换该密钥会让已存库的 secret 全部无法解密，必须同时重配所有站点。

const secretCipherPrefix = "enc:v1:"

var ErrSecretNotConfigured = errors.New("v10 site secret is empty")

// encryptionKey 派生 32 字节 AES-256 密钥。
//
// 用 sha256 而不是直接截断：环境里的密钥长度不固定，散列后既能定长，
// 也让不同用途的密钥彼此无关。
func encryptionKey() []byte {
	raw := strings.TrimSpace(os.Getenv("V10_SECRET_ENC_KEY"))
	if raw == "" {
		raw = common.CryptoSecret
	}
	sum := sha256.Sum256([]byte("v10-site-secret::" + raw))
	return sum[:]
}

// EncryptSecret 加密 app_secret，返回可直接入库的字符串。
func EncryptSecret(secret string) (string, error) {
	if secret == "" {
		return "", ErrSecretNotConfigured
	}

	block, err := aes.NewCipher(encryptionKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(secret), nil)
	return secretCipherPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// DecryptSecret 解密库里的 app_secret。
func DecryptSecret(site *V10Site) (string, error) {
	if site == nil || site.SecretEnc == "" {
		return "", ErrSecretNotConfigured
	}
	encoded, ok := strings.CutPrefix(site.SecretEnc, secretCipherPrefix)
	if !ok {
		return "", fmt.Errorf("v10 site %s: secret is not in %s format", site.AppId, secretCipherPrefix)
	}

	sealed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(encryptionKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(sealed) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}

	nonce, payload := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, payload, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt failed (V10_SECRET_ENC_KEY changed?): %w", err)
	}
	return string(plain), nil
}

// secretPrefix 取 secret 的前 6 位，供后台核对"配的是哪一把"，不泄露完整值。
func secretPrefix(secret string) string {
	if len(secret) <= 6 {
		return secret
	}
	return secret[:6]
}
