package tasks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const TypeSendMessage = "wa:send_message"

const MessageTypeText = "text"

type SendMessagePayload struct {
	IdempotencyKey string    `json:"idempotency_key"`
	SessionID      string    `json:"session_id"`
	To             string    `json:"to"`
	Type           string    `json:"type"`
	Text           string    `json:"text"`
	Priority       string    `json:"priority"`
	EnqueuedAt     time.Time `json:"enqueued_at"`
	SourceRef      string    `json:"source_ref"`

	// Sender is the pool this session belongs to, if any. The worker needs it on failure to
	// look up backups — see wa_sender_pools.
	Sender string `json:"sender,omitempty"`
}

type Priority string

const (
	PriorityCritical Priority = "critical"
	PriorityDefault Priority = "default"
	PriorityBulk Priority = "bulk"
)

var Priorities = []Priority{PriorityCritical, PriorityDefault, PriorityBulk}

func ParsePriority(s string) (Priority, error) {
	if s == "" {
		return PriorityDefault, nil
	}
	for _, p := range Priorities {
		if string(p) == s {
			return p, nil
		}
	}
	return "", fmt.Errorf("unknown priority %q (want one of critical, default, bulk)", s)
}

const QueuePrefix = "wa"

func QueueFor(sessionID string, p Priority) string {
	return QueuePrefix + ":" + sessionID + ":" + string(p)
}

func QueuesFor(sessionID string) []string {
	out := make([]string, 0, len(Priorities))
	for _, p := range Priorities {
		out = append(out, QueueFor(sessionID, p))
	}
	return out
}

func ParseQueue(queue string) (sessionID string, p Priority, err error) {
	parts := strings.Split(queue, ":")
	if len(parts) != 3 || parts[0] != QueuePrefix || parts[1] == "" {
		return "", "", fmt.Errorf("malformed queue name %q", queue)
	}
	lane, err := ParsePriority(parts[2])
	if err != nil {
		return "", "", fmt.Errorf("malformed queue name %q: %w", queue, err)
	}
	// ParsePriority maps "" to default; a queue name must name its lane explicitly.
	if parts[2] == "" {
		return "", "", fmt.Errorf("malformed queue name %q: empty priority", queue)
	}
	return parts[1], lane, nil
}

const MaxIdempotencyKeyLen = 255

// IdempotencyKey derives the gateway key every downstream system uses — the Asynq task id,
// the wa_jobs primary reference, the coalesce group member. It is deterministic in its three
// inputs, computed once at enqueue, and never regenerated on retry.
//
// The caller-supplied key is scoped by apiKeyID so two projects cannot collide, and by the
// normalised recipient so one caller key cannot address two people.
func IdempotencyKey(apiKeyID, callerKey, to string) string {
	h := sha256.New()
	for _, part := range []string{apiKeyID, callerKey, to} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ValidCallerKey reports whether a caller-supplied Idempotency-Key is well formed: printable
// ASCII, non-empty, within the documented length.
func ValidCallerKey(k string) error {
	if k == "" {
		return errors.New("idempotency key is empty")
	}
	if len(k) > MaxIdempotencyKeyLen {
		return fmt.Errorf("idempotency key is %d characters, over the %d limit", len(k), MaxIdempotencyKeyLen)
	}
	for i := 0; i < len(k); i++ {
		if k[i] < 0x21 || k[i] > 0x7E {
			return fmt.Errorf("idempotency key contains a character that is not printable ASCII at position %d", i)
		}
	}
	return nil
}

func Marshal(p SendMessagePayload) ([]byte, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("marshal send payload: %w", err)
	}
	return b, nil
}

func Unmarshal(b []byte) (SendMessagePayload, error) {
	var p SendMessagePayload
	if err := json.Unmarshal(b, &p); err != nil {
		return SendMessagePayload{}, fmt.Errorf("unmarshal send payload: %w", err)
	}
	return p, nil
}
