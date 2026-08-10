// Package notify signs, queues, and delivers outbound event notifications to consumers.
//
// Concern: extra. Not the API, the queue, or the pacing — it is the notification service that
// tells consumers what became of a message. Named `notify` rather than `webhooks` because four
// other things in this repo are also called webhooks and none of them are this one; see
// ARCHITECTURE.md.
//
// The signature contract is fixed in ARCHITECTURE.md ("Webhook signature contract") and
// documented with a worked example in docs/guides/webhooks.md. Changing anything about how
// Sign builds its input is a breaking change for every consumer that has already implemented
// verification.
package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

const (
	EventMessageSent      = "message.sent"
	EventMessageDelivered = "message.delivered"
	EventMessageRead      = "message.read"
	EventMessageFailed    = "message.failed"

	HeaderSignature = "X-WA-Signature"
	HeaderTimestamp = "X-WA-Timestamp"
	HeaderEventID   = "X-WA-Event-Id"
	HeaderEventType = "X-WA-Event-Type"

	// MaxSkew is how old a timestamp a consumer should accept. Published so the docs and
	// the implementation cannot drift apart.
	MaxSkew = 5 * time.Minute
)

var Types = []string{EventMessageSent, EventMessageDelivered, EventMessageRead, EventMessageFailed}

func ValidType(t string) bool {
	for _, known := range Types {
		if known == t {
			return true
		}
	}
	return false
}

// Envelope is the body of a delivery. It is marshalled once, stored, and signed as those exact
// bytes — re-serialising it later would produce a different signature.
type Envelope struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	CreatedAt time.Time `json:"created_at"`
	Data      any       `json:"data"`
}

// Sign produces the X-WA-Signature value for a body at a timestamp.
//
// The timestamp is inside the signed string so a captured request cannot be replayed later,
// and the body is signed as raw bytes because that is the only form both ends can agree on.
func Sign(secret string, timestamp time.Time, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestamp.Unix(), 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify is the check a consumer performs, kept here so the docs can quote working code and
// so the gateway intake can reuse the same comparison.
func Verify(secret, signature string, timestamp time.Time, body []byte, now time.Time) bool {
	if now.Sub(timestamp) > MaxSkew || timestamp.Sub(now) > MaxSkew {
		return false
	}
	return hmac.Equal([]byte(Sign(secret, timestamp, body)), []byte(signature))
}
