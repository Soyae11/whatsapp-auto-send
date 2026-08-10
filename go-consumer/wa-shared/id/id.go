package id

import (
	"crypto/rand"
	"time"
)

const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func New(prefix string) string {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	for i := range 6 {
		b[i] = byte(ms >> (40 - 8*i))
	}
	rand.Read(b[6:])
	return prefix + "_" + Encode(b[:])
}

func Random(prefix string, n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return prefix + Encode(b)
}

func Encode(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	out := make([]byte, (len(b)*8+4)/5)

	var acc uint32
	var bits uint
	i := len(out) - 1
	for j := len(b) - 1; j >= 0; j-- {
		acc |= uint32(b[j]) << bits
		bits += 8
		for bits >= 5 {
			out[i] = alphabet[acc&31]
			i--
			acc >>= 5
			bits -= 5
		}
	}
	if bits > 0 {
		out[i] = alphabet[acc&31]
		i--
	}
	for ; i >= 0; i-- {
		out[i] = alphabet[0]
	}
	return string(out)
}

func Valid(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := range len(s) {
		if !validChar(s[i]) {
			return false
		}
	}
	return true
}

func validChar(c byte) bool {
	switch {
	case c >= '0' && c <= '9':
		return true
	case c >= 'A' && c <= 'Z':
		return c != 'I' && c != 'L' && c != 'O' && c != 'U'
	default:
		return false
	}
}
