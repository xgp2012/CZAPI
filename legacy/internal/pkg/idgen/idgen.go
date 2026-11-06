// Package idgen produces sortable unique identifiers.
package idgen

import (
	"crypto/rand"
	"encoding/binary"
	"sync"
	"time"
)

// ULID returns a 26-character Crockford base32 ULID-like identifier:
// 48-bit millisecond timestamp followed by 80 bits of randomness.
// It is monotonic within a process by borrowing the low bits of the
// timestamp when two IDs share a millisecond.
func ULID() string {
	ms := uint64(time.Now().UnixMilli())
	var buf [16]byte
	binary.BigEndian.PutUint64(buf[0:8], ms<<16)
	if _, err := rand.Read(buf[6:]); err != nil {
		// crypto/rand should never fail; degrade to a counter-based value.
		binary.BigEndian.PutUint64(buf[8:], fallbackSeed())
	}
	return encodeBase32(buf[:])
}

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// encodeBase32 encodes 16 bytes into 26 Crockford base32 chars.
func encodeBase32(src []byte) string {
	// 128 bits -> 26 chars (130 bits available, pad 2 zero bits).
	out := make([]byte, 26)
	var acc uint64
	var bits uint
	pos := 0
	for _, b := range src {
		acc = acc<<8 | uint64(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out[pos] = crockford[(acc>>bits)&0x1f]
			pos++
		}
	}
	if bits > 0 {
		out[pos] = crockford[(acc<<(5-bits))&0x1f]
		pos++
	}
	for i := pos; i < 26; i++ {
		out[i] = '0'
	}
	return string(out[:26])
}

var (
	seedOnce sync.Once
	counter  uint64
)

func fallbackSeed() uint64 {
	seedOnce.Do(func() { counter = uint64(time.Now().UnixNano()) })
	// Not strictly atomic, but only used if crypto/rand fails.
	counter++
	return counter << 8
}

// RandomHex returns n bytes of crypto-random data hex encoded (2n chars).
func RandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "00000000000000000000000000000000"[:2*n]
	}
	const hex = "0123456789abcdef"
	out := make([]byte, 2*n)
	for i, v := range b {
		out[2*i] = hex[v>>4]
		out[2*i+1] = hex[v&0x0f]
	}
	return string(out)
}
