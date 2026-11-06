package v10

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// 待签串由 6 行组成，行间以 "\n" 连接，**无尾随换行**：
//
//	{METHOD}            大写
//	{PATH}              含前缀，如 /internal/v10/credit
//	{SORTED_QUERY}      key 升序的 canonical query；无查询串时为空行
//	{SHA256_HEX_BODY}   原始请求体字节的 sha256 hex；空体为 sha256("")
//	{TIMESTAMP}         unix 秒
//	{NONCE}             32 位小写十六进制
//
// 签名 = hex(HMAC-SHA256(app_secret, 待签串))。
// PHP 侧实现见 v10/idcsmart_llm_gateway/model/LlmApi.php::sign，两边必须逐字节一致。

// CanonicalQuery 把查询串渲染成 ASCII 升序的 k=v&k=v，key 与 value 都做百分号编码。
// 同一个 key 的多值也排序，保证两端结果确定。
//
// 编码规则用 %20 而非 '+'：PHP 的 rawurlencode 与它一致，避免空格场景下两端签名不同。
func CanonicalQuery(values url.Values) string {
	if len(values) == 0 {
		return ""
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	var b strings.Builder
	for _, key := range keys {
		items := slices.Clone(values[key])
		slices.Sort(items)
		for _, item := range items {
			if b.Len() > 0 {
				b.WriteByte('&')
			}
			b.WriteString(escape(key))
			b.WriteByte('=')
			b.WriteString(escape(item))
		}
	}
	return b.String()
}

// escape 是 url.QueryEscape 的 %20 变体：url.QueryEscape 把空格编成 '+'，
// 而 PHP 的 rawurlencode 编成 %20，两者在含空格的参数上会产生不同待签串。
func escape(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}

func bodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// StringToSign 组装 6 行待签串。
func StringToSign(method, path, sortedQuery string, body []byte, timestamp int64, nonce string) string {
	return strings.Join([]string{
		strings.ToUpper(method),
		path,
		sortedQuery,
		bodyHash(body),
		strconv.FormatInt(timestamp, 10),
		nonce,
	}, "\n")
}

// Sign 返回 hex(HMAC-SHA256(secret, stringToSign))，小写。
func Sign(secret, stringToSign string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stringToSign))
	return hex.EncodeToString(mac.Sum(nil))
}

// SignRequest 是给调用方（含测试夹具）用的便捷入口。
func SignRequest(secret, method, path, sortedQuery string, body []byte, timestamp int64, nonce string) string {
	return Sign(secret, StringToSign(method, path, sortedQuery, body, timestamp, nonce))
}

// EqualSignature 以常数时间比较两个签名，避免时序侧信道。
func EqualSignature(want, got string) bool {
	return hmac.Equal([]byte(want), []byte(got))
}
