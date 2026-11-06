package v10

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// 请求头约定。
const (
	HeaderAppId     = "X-V10-App-Id"
	HeaderTimestamp = "X-V10-Timestamp"
	HeaderNonce     = "X-V10-Nonce"
	HeaderSignature = "X-V10-Signature"
)

// 时间窗。超过窗口的请求直接拒绝，防止旧请求被重放。
const signatureWindow = 5 * time.Minute

// 业务码。与 V10 插件 LlmApi.php 里的 CODE_* 常量一一对应。
const (
	CodeOK              = 0
	CodeParamError      = 40000
	CodeMissingRef      = 40001
	CodeSignInvalid     = 40100
	CodeTimestampWrong  = 40101
	CodeNonceReplay     = 40102
	CodeSiteDisabled    = 40300
	CodeNotFound        = 40400
	CodeRefConflict     = 40900
	CodeInsufficient    = 40200
	CodeRateLimited     = 42900
	CodeInternal        = 50000
	CodeFeatureDisabled = 50300
)

// envelope 是 /internal/v10 统一的响应信封。与 /v1 的 OpenAI 错误格式刻意不同：
// 这一层只给 V10 服务端看，稳定压倒兼容。
type envelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data"`
}

func replyOK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, envelope{Code: CodeOK, Msg: "ok", Data: data})
}

func replyError(c *gin.Context, httpStatus, code int, msg string) {
	c.JSON(httpStatus, envelope{Code: code, Msg: msg, Data: nil})
}

// nonceStore 记录近期用过的 nonce，拒绝重放。
//
// 为什么不用 Redis：签名校验必须在 Redis 挂掉时依然生效，否则缓存故障会连带
// 让对接层失效。进程内 + 定时清理足够——多副本场景下每个副本各自校验，
// 最坏情况是同一 nonce 在不同副本各通过一次，而幂等账本仍然挡住重复入账。
type nonceStore struct {
	mu     sync.Mutex
	seen   map[string]time.Time
	lastGC time.Time
}

var nonces = &nonceStore{seen: make(map[string]time.Time)}

func (s *nonceStore) claim(nonce string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if now.Sub(s.lastGC) > signatureWindow {
		for key, at := range s.seen {
			if now.Sub(at) > signatureWindow {
				delete(s.seen, key)
			}
		}
		s.lastGC = now
	}
	if _, exists := s.seen[nonce]; exists {
		return false
	}
	s.seen[nonce] = now
	return true
}

// SiteAuth 是签名校验中间件。校验通过后把 *V10Site 放进 context。
//
// 校验顺序（任一失败即 401，不泄露具体原因给调用方，但日志里区分）：
//  1. 头齐全且格式合法
//  2. 站点存在且启用
//  3. 时间戳在窗口内
//  4. nonce 未被用过
//  5. 签名常数时间比较一致
func SiteAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		appId := c.GetHeader(HeaderAppId)
		timestampRaw := c.GetHeader(HeaderTimestamp)
		nonce := c.GetHeader(HeaderNonce)
		signature := c.GetHeader(HeaderSignature)

		if appId == "" || timestampRaw == "" || nonce == "" || signature == "" {
			replyError(c, http.StatusUnauthorized, CodeSignInvalid, "missing signature headers")
			c.Abort()
			return
		}

		timestamp, err := strconv.ParseInt(timestampRaw, 10, 64)
		if err != nil {
			replyError(c, http.StatusUnauthorized, CodeTimestampWrong, "invalid timestamp")
			c.Abort()
			return
		}

		site, err := GetSiteByAppId(appId)
		if err != nil {
			if errors.Is(err, ErrSiteNotFound) {
				replyError(c, http.StatusUnauthorized, CodeSignInvalid, "unknown app_id")
			} else {
				common.SysError(common.LogText("v10: lookup site %s failed: %s", appId, err.Error()))
				replyError(c, http.StatusInternalServerError, CodeInternal, "internal error")
			}
			c.Abort()
			return
		}
		if site.Status != V10SiteStatusEnabled {
			replyError(c, http.StatusForbidden, CodeSiteDisabled, "app_id disabled")
			c.Abort()
			return
		}

		now := time.Now()
		skew := now.Sub(time.Unix(timestamp, 0))
		if skew < 0 {
			skew = -skew
		}
		if skew > signatureWindow {
			replyError(c, http.StatusUnauthorized, CodeTimestampWrong, "timestamp out of window")
			c.Abort()
			return
		}

		body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBodyBytes))
		if err != nil {
			replyError(c, http.StatusBadRequest, CodeParamError, "cannot read body")
			c.Abort()
			return
		}
		// 读完必须放回，后续 handler 还要用它解析 JSON。
		c.Request.Body = io.NopCloser(bytes.NewReader(body))

		secret, err := DecryptSecret(site)
		if err != nil {
			common.SysError(common.LogText("v10: decrypt secret for site %s failed: %s", appId, err.Error()))
			replyError(c, http.StatusInternalServerError, CodeInternal, "internal error")
			c.Abort()
			return
		}

		expected := SignRequest(
			secret,
			c.Request.Method,
			c.Request.URL.Path,
			CanonicalQuery(c.Request.URL.Query()),
			body,
			timestamp,
			nonce,
		)
		if !EqualSignature(expected, strings.ToLower(signature)) {
			common.SysLog(common.LogText("v10: signature mismatch app_id=%s method=%s path=%s",
				appId, c.Request.Method, c.Request.URL.Path))
			replyError(c, http.StatusUnauthorized, CodeSignInvalid, "signature mismatch")
			c.Abort()
			return
		}

		// 签名合法后才占用 nonce：避免攻击者用伪造签名刷满 nonce 表。
		if !nonces.claim(nonce, now) {
			replyError(c, http.StatusUnauthorized, CodeNonceReplay, "nonce already used")
			c.Abort()
			return
		}

		c.Set(ctxSiteKey, site)
		c.Next()
	}
}

const (
	// ctxSiteKey 是签名中间件放进 context 的站点对象键。
	ctxSiteKey = "v10_site"

	// maxBodyBytes 限制内部请求体大小。这一层只接受小 JSON，不需要大的。
	maxBodyBytes = 1 << 20
)

// SiteFrom 从 context 取回已鉴权的站点。只在 SiteAuth 之后调用。
func SiteFrom(c *gin.Context) *V10Site {
	value, exists := c.Get(ctxSiteKey)
	if !exists {
		return nil
	}
	site, _ := value.(*V10Site)
	return site
}

// newNonce 生成 32 位小写十六进制随机串，供客户端参考/测试使用。
func newNonce() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buf)
}
