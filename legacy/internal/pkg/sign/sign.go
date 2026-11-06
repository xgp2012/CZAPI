// Package sign implements the V10 <-> gateway HMAC-SHA256 request signing
// protocol defined in plan §8.7 and docs/api.md.
//
// String to sign (all lines joined with "\n"):
//
//	{METHOD}
//	{PATH}              e.g. /internal/v1/keys
//	{SORTED_QUERY}      key1=val1&key2=val2 (keys ascending, URL-encoded)
//	{SHA256_HEX_BODY}   hex sha256 of the raw body (empty body => sha256 of "")
//	{TIMESTAMP}         unix seconds
//	{NONCE}             32 lowercase hex chars
package sign

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// CanonicalQuery renders url.Values as an ASCII-ascending "k=v&k=v" string with
// percent-encoding applied to both key and value. Multiple values for one key
// are sorted too, so both ends agree deterministically.
func CanonicalQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		vals := append([]string(nil), q[k]...)
		sort.Strings(vals)
		for _, v := range vals {
			if b.Len() > 0 {
				b.WriteByte('&')
			}
			b.WriteString(url.QueryEscape(k))
			b.WriteByte('=')
			b.WriteString(url.QueryEscape(v))
		}
	}
	return b.String()
}

// BodyHash returns the lowercase hex sha256 of body.
func BodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// StringToSign builds the canonical signing payload.
func StringToSign(method, path, query string, body []byte, timestamp int64, nonce string) string {
	return strings.Join([]string{
		strings.ToUpper(method),
		path,
		query,
		BodyHash(body),
		strconv.FormatInt(timestamp, 10),
		nonce,
	}, "\n")
}

// Sign returns hex(HMAC-SHA256(secret, stringToSign)).
func Sign(secret, stringToSign string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(stringToSign))
	return hex.EncodeToString(m.Sum(nil))
}

// SignRequest is the convenience entry point used by clients (PHP side mirrors
// this exactly in model/LlmApi.php).
func SignRequest(secret, method, path, query string, body []byte, timestamp int64, nonce string) string {
	return Sign(secret, StringToSign(method, path, query, body, timestamp, nonce))
}

// Verify performs a constant-time comparison of the expected and provided
// signatures. An empty provided signature never verifies.
func Verify(secret, stringToSign, provided string) bool {
	if provided == "" {
		return false
	}
	expected := Sign(secret, stringToSign)
	return hmac.Equal([]byte(expected), []byte(strings.ToLower(provided)))
}

// ResponseSign implements the optional response signature:
// hex(HMAC(secret, sha256hex(body)+timestamp)).
func ResponseSign(secret string, body []byte, timestamp int64) string {
	payload := BodyHash(body) + strconv.FormatInt(timestamp, 10)
	return Sign(secret, payload)
}
