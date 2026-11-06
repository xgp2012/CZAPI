// Package crypto provides API-key material generation, hashing and
// AES-256-GCM encryption for upstream channel secrets.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	keyPrefix = "sk-"
	// keyRandomBytes yields a 43-character base62 body (256 bits of entropy).
	keyRandomBytes = 32
	base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

// GenerateAPIKey returns a fresh "sk-" prefixed key plus its display prefix.
// The plaintext is returned to the caller exactly once and never stored.
func GenerateAPIKey() (plain string, prefix string) {
	body := randomBase62(keyRandomBytes)
	plain = keyPrefix + body
	return plain, Prefix(plain)
}

// Prefix returns the first 8 chars of the key body prefixed by "sk-",
// e.g. "sk-a1b2c3d4", which is what the V10 plugin stores for display.
func Prefix(plain string) string {
	body := strings.TrimPrefix(plain, keyPrefix)
	if len(body) > 8 {
		body = body[:8]
	}
	return keyPrefix + body
}

// HashKey returns hex(sha256(salt||key)) — the only form persisted.
func HashKey(salt, plain string) string {
	h := sha256.New()
	h.Write([]byte(salt))
	h.Write([]byte(plain))
	return hex.EncodeToString(h.Sum(nil))
}

// LookupHash is the deterministic, unkeyed hash used for cache lookups so that
// the salted hash never needs to be enumerated.
func LookupHash(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// LooksLikeAPIKey reports whether s has the shape of a gateway key.
func LooksLikeAPIKey(s string) bool {
	if !strings.HasPrefix(s, keyPrefix) || len(s) < 12 {
		return false
	}
	for _, r := range s[len(keyPrefix):] {
		if !strings.ContainsRune(base62Alphabet, r) {
			return false
		}
	}
	return true
}

func randomBase62(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = base62Alphabet[int(b)%len(base62Alphabet)]
	}
	return string(out)
}

// Sealer encrypts/decrypts channel credentials with AES-256-GCM.
type Sealer struct{ aead cipher.AEAD }

// NewSealer derives a 32-byte key from masterKey (sha256) and returns a Sealer.
func NewSealer(masterKey string) (*Sealer, error) {
	if masterKey == "" {
		return nil, errors.New("crypto: empty master key")
	}
	sum := sha256.Sum256([]byte(masterKey))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

// Seal returns base64(nonce||ciphertext).
func (s *Sealer) Seal(plaintext string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	out := s.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Open decrypts a value produced by Seal.
func (s *Sealer) Open(sealed string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", fmt.Errorf("crypto: bad base64: %w", err)
	}
	ns := s.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("crypto: ciphertext too short")
	}
	plain, err := s.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("crypto: decrypt: %w", err)
	}
	return string(plain), nil
}

// MaskSecret keeps the last 4 characters visible, for admin display.
func MaskSecret(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return "****" + s[len(s)-4:]
}
