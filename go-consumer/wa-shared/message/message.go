// Package message owns the public representation of a message: the status vocabulary
// consumers see, and the JSON shape both the API and outbound notifications render.
//
// Concern: supporting. This is the "message" of ARCHITECTURE.md's glossary — the consumer's
// noun, distinct from a job (a wa_jobs row), a task (an asynq unit) and a slot (a reserved
// send time).
//
// It exists so the mapping in Status lives in exactly one function. wa_jobs keeps its own
// internal vocabulary; nothing outside this package should translate between the two. See
// ARCHITECTURE.md, "The words this codebase uses for almost-the-same thing".
package message

import (
	"time"

	"wa-shared/store"
	"wa-shared/tasks"
)

const (
	StatusQueued    = "queued"
	StatusSending   = "sending"
	StatusSent      = "sent"
	StatusDelivered = "delivered"
	StatusRead      = "read"
	StatusCancelled = "cancelled"
	StatusFailed    = "failed"
)

type Timestamps struct {
	QueuedAt    *time.Time `json:"queued_at,omitempty"`
	SendingAt   *time.Time `json:"sending_at,omitempty"`
	SentAt      *time.Time `json:"sent_at,omitempty"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	ReadAt      *time.Time `json:"read_at,omitempty"`
	CancelledAt *time.Time `json:"cancelled_at,omitempty"`
	FailedAt    *time.Time `json:"failed_at,omitempty"`
}

type Error struct {
	Code      string    `json:"code"`
	Detail    string    `json:"detail"`
	Retryable bool      `json:"retryable"`
	At        time.Time `json:"at"`
}

type Message struct {
	ID              string            `json:"id"`
	Status          string            `json:"status"`
	Sender          string            `json:"sender"`
	To              string            `json:"to"`
	Type            string            `json:"type"`
	Priority        string            `json:"priority"`
	DryRun          bool              `json:"dry_run"`
	EstimatedSendAt *time.Time        `json:"estimated_send_at,omitempty"`
	Reference       string            `json:"reference,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	Attempts        int               `json:"attempts"`
	LastError       *Error            `json:"last_error,omitempty"`
	Timestamps      Timestamps        `json:"timestamps"`
	CreatedAt       time.Time         `json:"created_at"`
}

// Status maps one internal row to the public vocabulary. This is the load-bearing table, and
// the only place the two vocabularies meet. See ARCHITECTURE.md's glossary.
//
// `scheduled` and `retrying` both surface as `queued`: from a consumer's point of view a
// message awaiting a retry is a message awaiting a send. Nothing is lost — attempts and
// last_error say a previous try failed and why.
//
// A `sent` row reports the furthest receipt that arrived, because statuses only move forward.
func Status(r store.Row) string {
	switch r.Status {
	case store.StatusScheduled, store.StatusRetrying:
		return StatusQueued
	case store.StatusSending:
		return StatusSending
	case store.StatusCancelled:
		return StatusCancelled
	case store.StatusFailed:
		return StatusFailed
	case store.StatusSent:
		switch {
		case r.ReadAt != nil:
			return StatusRead
		case r.DeliveredAt != nil:
			return StatusDelivered
		}
		return StatusSent
	}
	// An unrecognised internal status is not a consumer's problem to solve, and reporting
	// it as sent would be a lie in the dangerous direction.
	return StatusQueued
}

// Cancellable reports whether a message is still early enough to stop. Only a message that
// has not been picked up can be — once the worker holds it the send is in flight at WhatsApp.
func Cancellable(r store.Row) bool {
	return r.Status == store.StatusScheduled || r.Status == store.StatusRetrying
}

// FromRow renders the public view of a stored message. Text is never included: wa_jobs does
// not keep it, and the audit trail records that a message went to a number, not what it said.
func FromRow(r store.Row) Message {
	m := Message{
		ID:        r.PublicID,
		Status:    Status(r),
		Sender:    r.Sender,
		To:        r.To,
		Type:      tasks.MessageTypeText,
		Priority:  r.Priority,
		Reference: r.SourceRef,
		Metadata:  r.Metadata,
		Attempts:  r.Attempts,
		CreatedAt: r.CreatedAt.UTC(),
		Timestamps: Timestamps{
			QueuedAt:    utc(&r.CreatedAt),
			SendingAt:   utc(r.SendingAt),
			SentAt:      utc(r.SentAt),
			DeliveredAt: utc(r.DeliveredAt),
			ReadAt:      utc(r.ReadAt),
			CancelledAt: utc(r.CancelledAt),
			FailedAt:    utc(r.FailedAt),
		},
	}
	if m.Priority == "" {
		m.Priority = string(tasks.PriorityDefault)
	}

	// estimated_send_at is an estimate of a future send, so it stops being meaningful the
	// moment the message leaves the queue.
	if Status(r) == StatusQueued {
		m.EstimatedSendAt = utc(r.ScheduledFor)
	}

	if r.Status == store.StatusFailed && r.LastErrorCode != nil {
		at := r.CreatedAt
		if r.LastErrorAt != nil {
			at = *r.LastErrorAt
		}
		m.LastError = &Error{
			Code:      FailureCode(*r.LastErrorCode),
			Detail:    FailureDetail(*r.LastErrorCode, r.To),
			Retryable: false,
			At:        at.UTC(),
		}
	}
	return m
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	out := t.UTC()
	return &out
}
