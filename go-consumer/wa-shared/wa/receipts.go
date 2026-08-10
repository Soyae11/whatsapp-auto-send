package wa

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// SignReceipt reproduces wa-gateway's own signature over an event it posts to us.
//
// The timestamp goes in exactly as it arrived on the header, in milliseconds and as a string.
// Reformatting it produces a MAC that never matches, which is the whole reason this takes the
// header value rather than a time.Time. See ARCHITECTURE.md, "Webhook signature contract".
func SignReceipt(secret, timestampHeader string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestampHeader))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifyReceipt checks a gateway event's signature against the raw body bytes. It does not
// look at the timestamp's age — that is the caller's decision, since the acceptable window is
// a policy rather than a property of the signature.
func VerifyReceipt(secret, signature, timestampHeader string, body []byte) bool {
	if secret == "" || signature == "" {
		return false
	}
	return hmac.Equal([]byte(SignReceipt(secret, timestampHeader, body)), []byte(signature))
}
