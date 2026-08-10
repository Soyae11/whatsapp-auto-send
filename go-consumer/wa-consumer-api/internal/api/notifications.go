package api

// Outbound notifications to consumers: the operator's window onto the delivery queue that
// internal/notify drains. Nothing here talks to wa-gateway — for the events coming the other
// way, see receipts.go.

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"wa-shared/store"
)

type WebhookStore interface {
	ListEvents(ctx context.Context, apiKeyID string, status store.EventStatus, limit int) ([]store.WebhookEvent, error)
	ReplayEvent(ctx context.Context, id string, at time.Time) (*store.WebhookEvent, error)
}
type eventView struct {
	ID          string     `json:"id"`
	WebhookID   string     `json:"webhook_id"`
	APIKeyID    string     `json:"api_key_id"`
	Type        string     `json:"type"`
	MessageID   string     `json:"message_id"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	URL         string     `json:"url"`
	LastStatus  *int       `json:"last_status,omitempty"`
	LastError   *string    `json:"last_error,omitempty"`
	NextAttempt time.Time  `json:"next_attempt_at"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

func toEventView(e store.WebhookEvent) eventView {
	return eventView{
		ID:          e.ID,
		WebhookID:   e.WebhookID,
		APIKeyID:    e.APIKeyID,
		Type:        e.Type,
		MessageID:   e.MessageID,
		Status:      string(e.Status),
		Attempts:    e.Attempts,
		URL:         e.URL,
		LastStatus:  e.LastStatus,
		LastError:   e.LastError,
		NextAttempt: e.NextAttemptAt.UTC(),
		DeliveredAt: e.DeliveredAt,
		CreatedAt:   e.CreatedAt.UTC(),
	}
}

func (s *Server) handleListWebhookEvents(w http.ResponseWriter, r *http.Request) {
	if s.webhooks == nil {
		writeError(w, http.StatusNotImplemented, errorBody{
			ErrorCode: "webhooks_not_configured",
			Message:   "this dispatcher has no webhook store wired in",
		})
		return
	}

	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))

	events, err := s.webhooks.ListEvents(ctx, q.Get("api_key_id"), store.EventStatus(q.Get("status")), limit)
	if err != nil {
		s.log.Error("could not list webhook events", "error", err)
		writeError(w, http.StatusInternalServerError, errorBody{
			ErrorCode: "internal_error",
			Message:   "could not list webhook events",
			Retryable: true,
		})
		return
	}

	out := make([]eventView, 0, len(events))
	for _, e := range events {
		out = append(out, toEventView(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

// handleReplayWebhookEvent puts a dead-lettered event back on the queue. Dead events are kept
// precisely so this is possible — a consumer whose endpoint was down for an hour can be caught
// up rather than told the events are gone.
func (s *Server) handleReplayWebhookEvent(w http.ResponseWriter, r *http.Request) {
	if s.webhooks == nil {
		writeError(w, http.StatusNotImplemented, errorBody{
			ErrorCode: "webhooks_not_configured",
			Message:   "this dispatcher has no webhook store wired in",
		})
		return
	}

	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	id := r.PathValue("id")
	e, err := s.webhooks.ReplayEvent(ctx, id, time.Now().UTC())
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errorBody{
			ErrorCode: "not_found",
			Message:   "no dead-lettered webhook event " + id + "; only dead events can be replayed",
		})
		return
	}
	if err != nil {
		s.log.Error("could not replay a webhook event", "event_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, errorBody{
			ErrorCode: "internal_error",
			Message:   "could not replay webhook event " + id,
			Retryable: true,
		})
		return
	}

	s.log.Info("webhook event queued for replay", "event_id", id, "webhook_id", e.WebhookID)
	writeJSON(w, http.StatusOK, toEventView(*e))
}
