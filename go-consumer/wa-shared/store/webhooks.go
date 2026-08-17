package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type Webhook struct {
	ID         string
	APIKeyID   string
	URL        string
	Secret     string
	Events     []string
	DisabledAt *time.Time
	CreatedAt  time.Time
}

func (w Webhook) Enabled() bool { return w.DisabledAt == nil }

// Wants reports whether this webhook subscribes to an event type. An empty event list means
// every type, so a webhook created without a filter keeps working when new types are added.
func (w Webhook) Wants(eventType string) bool {
	if len(w.Events) == 0 {
		return true
	}
	for _, e := range w.Events {
		if e == eventType {
			return true
		}
	}
	return false
}

type EventStatus string

const (
	EventPending   EventStatus = "pending"
	EventDelivered EventStatus = "delivered"
	EventDead      EventStatus = "dead"
)

type WebhookEvent struct {
	ID            string
	WebhookID     string
	APIKeyID      string
	Type          string
	MessageID     string
	Payload       []byte
	Status        EventStatus
	Attempts      int
	NextAttemptAt time.Time
	LastError     *string
	LastStatus    *int
	DeliveredAt   *time.Time
	CreatedAt     time.Time

	URL    string
	Secret string
}

const webhookColumns = `id, api_key_id, url, secret, events, disabled_at, created_at`

func scanWebhook(sc interface{ Scan(...any) error }) (*Webhook, error) {
	var w Webhook
	if err := sc.Scan(&w.ID, &w.APIKeyID, &w.URL, &w.Secret, &w.Events, &w.DisabledAt, &w.CreatedAt); err != nil {
		return nil, err
	}
	return &w, nil
}

func (s *Store) CreateWebhook(ctx context.Context, w Webhook) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO wa_webhooks (id, api_key_id, url, secret, events)
		VALUES ($1, $2, $3, $4, $5)`,
		w.ID, w.APIKeyID, w.URL, w.Secret, w.Events)
	if err != nil {
		return fmt.Errorf("store: create webhook for %s: %w", w.APIKeyID, err)
	}
	return nil
}

func (s *Store) ListWebhooks(ctx context.Context, apiKeyID string) ([]Webhook, error) {
	q := `SELECT ` + webhookColumns + ` FROM wa_webhooks`
	args := []any{}
	if apiKeyID != "" {
		q += ` WHERE api_key_id = $1`
		args = append(args, apiKeyID)
	}
	q += ` ORDER BY created_at`

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list webhooks: %w", err)
	}
	defer rows.Close()

	var out []Webhook
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list webhooks: %w", err)
		}
		out = append(out, *w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list webhooks: %w", err)
	}
	return out, nil
}

func (s *Store) DeleteWebhook(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM wa_webhooks WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: delete webhook %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: webhook %s", ErrNotFound, id)
	}
	return nil
}

// EnqueueEvent records an event for delivery. The unique index on
// (webhook_id, message_id, type) makes this idempotent: a repeated receipt cannot become a
// second webhook, which matters because wa-gateway's receipts are fire-and-forget and repeat.
func (s *Store) EnqueueEvent(ctx context.Context, e WebhookEvent) (queued bool, err error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO wa_webhook_events (id, webhook_id, api_key_id, type, message_id, payload, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (webhook_id, message_id, type) DO NOTHING`,
		e.ID, e.WebhookID, e.APIKeyID, e.Type, e.MessageID, e.Payload, EventPending)
	if err != nil {
		return false, fmt.Errorf("store: enqueue webhook event %s: %w", e.ID, err)
	}
	return tag.RowsAffected() == 1, nil
}

const eventColumns = `e.id, e.webhook_id, e.api_key_id, e.type, e.message_id, e.payload, e.status,
	       e.attempts, e.next_attempt_at, e.last_error, e.last_status, e.delivered_at, e.created_at,
	       w.url, w.secret`

func scanEvent(sc interface{ Scan(...any) error }) (*WebhookEvent, error) {
	var e WebhookEvent
	if err := sc.Scan(&e.ID, &e.WebhookID, &e.APIKeyID, &e.Type, &e.MessageID, &e.Payload, &e.Status,
		&e.Attempts, &e.NextAttemptAt, &e.LastError, &e.LastStatus, &e.DeliveredAt, &e.CreatedAt,
		&e.URL, &e.Secret); err != nil {
		return nil, err
	}
	return &e, nil
}

// ClaimDueEvents takes ownership of up to limit events that are ready to send, pushing their
// next attempt out so a second delivery worker cannot pick up the same ones.
//
// SKIP LOCKED is what makes two workers safe here. Unlike a send, a duplicate webhook is
// merely rude rather than a message to a real person twice — but the consumer contract says
// events can repeat, so the guarantee only has to be good, not absolute.
//
// Disabled webhooks are filtered in the due set rather than in the final select. Filtering at the
// end still claimed their events — bumping next_attempt_at on rows it then dropped — so they were
// never delivered, never failed, never dead, and came back due every sweep, taking slots out of
// every batch forever. DeadLetterDisabledEvents is what actually retires them.
func (s *Store) ClaimDueEvents(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]WebhookEvent, error) {
	rows, err := s.pool.Query(ctx, `
		WITH due AS (
			SELECT e.id
			  FROM wa_webhook_events e
			  JOIN wa_webhooks w ON w.id = e.webhook_id
			 WHERE e.status = $1 AND e.next_attempt_at <= $2
			   AND w.disabled_at IS NULL
			 ORDER BY e.next_attempt_at
			 LIMIT $3
			 FOR UPDATE OF e SKIP LOCKED
		), claimed AS (
			UPDATE wa_webhook_events
			   SET next_attempt_at = $2 + $4::interval
			 WHERE id IN (SELECT id FROM due)
			RETURNING *
		)
		SELECT `+eventColumns+`
		  FROM claimed e
		  JOIN wa_webhooks w ON w.id = e.webhook_id`,
		EventPending, now, limit, lease.String())
	if err != nil {
		return nil, fmt.Errorf("store: claim due webhook events: %w", err)
	}
	defer rows.Close()

	var out []WebhookEvent
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("store: claim due webhook events: %w", err)
		}
		out = append(out, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: claim due webhook events: %w", err)
	}
	return out, nil
}

func (s *Store) MarkEventDelivered(ctx context.Context, id string, statusCode int, at time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE wa_webhook_events
		   SET status = $2, attempts = attempts + 1, last_status = $3, last_error = NULL, delivered_at = $4
		 WHERE id = $1`,
		id, EventDelivered, statusCode, at)
	if err != nil {
		return fmt.Errorf("store: mark webhook event %s delivered: %w", id, err)
	}
	return nil
}

// MarkEventFailed records a failed attempt, scheduling the next one or dead-lettering when the
// budget is spent. Dead events stay in the table — they are the queue the replay endpoint and
// the admin CLI read.
func (s *Store) MarkEventFailed(ctx context.Context, id string, statusCode *int, reason string, retryAt time.Time, dead bool) error {
	status := EventPending
	if dead {
		status = EventDead
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE wa_webhook_events
		   SET status = $2, attempts = attempts + 1, last_status = $3, last_error = $4, next_attempt_at = $5
		 WHERE id = $1`,
		id, status, statusCode, reason, retryAt)
	if err != nil {
		return fmt.Errorf("store: mark webhook event %s failed: %w", id, err)
	}
	return nil
}

// DisabledWebhookReason is the last_error left on an event retired because its webhook was
// disabled before the event could be delivered. It reads back through the events endpoint and the
// admin CLI, so it says why rather than leaving an operator to guess.
const DisabledWebhookReason = "webhook was disabled before this event could be delivered"

// DeadLetterDisabledEvents retires up to limit pending events whose webhook has since been
// disabled, and returns how many went. Without it those events are never looked at again:
// ClaimDueEvents skips them, nothing else moves them, and they stay pending for good. Dead is the
// honest state and the reversible one — ReplayEvent can put one back if the webhook is turned
// back on. attempts is left alone, since no attempt was ever made.
//
// Only events that are actually due are touched. A deliverer claiming an event pushes its
// next_attempt_at a lease into the future, so that bound is what keeps this sweep from marking
// dead an event another worker is mid-POST on — which MarkEventFailed would then flip back to
// pending anyway, leaving the two to trade the row back and forth.
func (s *Store) DeadLetterDisabledEvents(ctx context.Context, at time.Time, limit int) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		WITH doomed AS (
			SELECT e.id
			  FROM wa_webhook_events e
			  JOIN wa_webhooks w ON w.id = e.webhook_id
			 WHERE e.status = $4 AND e.next_attempt_at <= $3
			   AND w.disabled_at IS NOT NULL
			 ORDER BY e.next_attempt_at
			 LIMIT $5
			 FOR UPDATE OF e SKIP LOCKED
		)
		UPDATE wa_webhook_events
		   SET status = $1, last_error = $2, next_attempt_at = $3
		 WHERE id IN (SELECT id FROM doomed)`,
		EventDead, DisabledWebhookReason, at, EventPending, limit)
	if err != nil {
		return 0, fmt.Errorf("store: dead-letter events of disabled webhooks: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ReplayEvent puts a dead event back on the queue with a fresh attempt budget.
func (s *Store) ReplayEvent(ctx context.Context, id string, at time.Time) (*WebhookEvent, error) {
	e, err := scanEvent(s.pool.QueryRow(ctx, `
		WITH replayed AS (
			UPDATE wa_webhook_events
			   SET status = $2, attempts = 0, next_attempt_at = $3, last_error = NULL, last_status = NULL
			 WHERE id = $1 AND status = $4
			RETURNING *
		)
		SELECT `+eventColumns+`
		  FROM replayed e
		  JOIN wa_webhooks w ON w.id = e.webhook_id`,
		id, EventPending, at, EventDead))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: no dead webhook event %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("store: replay webhook event %s: %w", id, err)
	}
	return e, nil
}

func (s *Store) ListEvents(ctx context.Context, apiKeyID string, status EventStatus, limit int) ([]WebhookEvent, error) {
	if limit <= 0 || limit > MaxPageLimit {
		limit = DefaultPageLimit
	}

	where := []string{"true"}
	args := []any{}
	if apiKeyID != "" {
		args = append(args, apiKeyID)
		where = append(where, fmt.Sprintf("e.api_key_id = $%d", len(args)))
	}
	if status != "" {
		args = append(args, status)
		where = append(where, fmt.Sprintf("e.status = $%d", len(args)))
	}
	args = append(args, limit)

	rows, err := s.pool.Query(ctx, `
		SELECT `+eventColumns+`
		  FROM wa_webhook_events e
		  JOIN wa_webhooks w ON w.id = e.webhook_id
		 WHERE `+joinAnd(where)+`
		 ORDER BY e.created_at DESC
		 LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("store: list webhook events: %w", err)
	}
	defer rows.Close()

	var out []WebhookEvent
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list webhook events: %w", err)
		}
		out = append(out, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list webhook events: %w", err)
	}
	return out, nil
}

func joinAnd(clauses []string) string {
	out := clauses[0]
	for _, c := range clauses[1:] {
		out += " AND " + c
	}
	return out
}
