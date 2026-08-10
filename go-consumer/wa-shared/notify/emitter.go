package notify

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"wa-shared/id"
	"wa-shared/message"
	"wa-shared/store"
)

const eventIDPrefix = "evt"

type Subscriptions interface {
	ListWebhooks(ctx context.Context, apiKeyID string) ([]store.Webhook, error)
	EnqueueEvent(ctx context.Context, e store.WebhookEvent) (bool, error)
}

// Emitter turns a status change into queued deliveries, one per subscribed webhook.
//
// It never blocks on the network. Emitting writes rows; sending them is the Deliverer's job,
// on its own schedule. A worker that had to wait for a consumer's endpoint would be holding a
// send slot hostage to a third party's uptime.
type Emitter struct {
	subs Subscriptions
	log  *slog.Logger
	now  func() time.Time
}

func NewEmitter(subs Subscriptions, log *slog.Logger) *Emitter {
	return &Emitter{subs: subs, log: log, now: time.Now}
}

// Emit queues eventType for every enabled webhook on the message's API key that wants it.
//
// A message with no api_key_id predates the public API or came from an operator route; there
// is nobody to notify, and that is not an error.
func (e *Emitter) Emit(ctx context.Context, eventType string, row store.Row) {
	if e == nil || row.APIKeyID == "" || row.PublicID == "" {
		return
	}

	hooks, err := e.subs.ListWebhooks(ctx, row.APIKeyID)
	if err != nil {
		e.log.Error("could not read webhooks for an event",
			"event_type", eventType, "message_id", row.PublicID, "api_key_id", row.APIKeyID, "error", err)
		return
	}

	for _, w := range hooks {
		if !w.Enabled() || !w.Wants(eventType) {
			continue
		}

		eventID := id.New(eventIDPrefix)
		payload, err := json.Marshal(Envelope{
			ID:        eventID,
			Type:      eventType,
			CreatedAt: e.now().UTC(),
			Data:      message.FromRow(row),
		})
		if err != nil {
			e.log.Error("could not encode a webhook event",
				"event_type", eventType, "message_id", row.PublicID, "error", err)
			continue
		}

		queued, err := e.subs.EnqueueEvent(ctx, store.WebhookEvent{
			ID:        eventID,
			WebhookID: w.ID,
			APIKeyID:  row.APIKeyID,
			Type:      eventType,
			MessageID: row.PublicID,
			Payload:   payload,
		})
		if err != nil {
			e.log.Error("could not queue a webhook event",
				"event_type", eventType, "message_id", row.PublicID, "webhook_id", w.ID, "error", err)
			continue
		}
		if !queued {
			continue
		}

		e.log.Info("webhook event queued",
			"event_id", eventID,
			"event_type", eventType,
			"message_id", row.PublicID,
			"webhook_id", w.ID)
	}
}

// EventTypeFor names the event a public status transition should announce, or "" when the
// status is not one consumers are notified about. Only terminal-ish transitions are events:
// `queued` and `sending` change too often to be worth a round trip.
func EventTypeFor(status string) string {
	switch status {
	case message.StatusSent:
		return EventMessageSent
	case message.StatusDelivered:
		return EventMessageDelivered
	case message.StatusRead:
		return EventMessageRead
	case message.StatusFailed:
		return EventMessageFailed
	}
	return ""
}

// EmitForRow announces whatever public status the row now carries, if that status is one
// consumers are told about.
func (e *Emitter) EmitForRow(ctx context.Context, row store.Row) {
	if t := EventTypeFor(message.Status(row)); t != "" {
		e.Emit(ctx, t, row)
	}
}
