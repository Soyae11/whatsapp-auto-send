package notify

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"wa-shared/store"
)

const (
	// MaxAttempts is the delivery budget from API_PLAN Phase 3. After it the event is
	// dead-lettered rather than retried forever — a consumer endpoint that has been down
	// for hours is not coming back within this event's usefulness.
	MaxAttempts = 5

	// DeliveryTimeout matches what the docs promise consumers: a response slower than this
	// counts as a failure. Consumers must acknowledge fast and do their work afterwards.
	DeliveryTimeout = 10 * time.Second

	// PollInterval is how often the deliverer looks for due events. Webhooks are a
	// notification channel, not a control path, so a few seconds of latency is fine and a
	// tight loop would be pure database load.
	PollInterval = 5 * time.Second

	// ClaimLease pushes a claimed event's next attempt out far enough that a second
	// deliverer will not pick it up while this one is still trying.
	ClaimLease = 2 * DeliveryTimeout

	BatchSize = 20

	backoffBase = 30 * time.Second
	backoffCap  = 30 * time.Minute
)

// Backoff is the delay before attempt n+1, given n attempts have failed. Exponential from 30
// seconds, capped at 30 minutes: five attempts span roughly half an hour, which is long enough
// to ride out a deploy and short enough that the event still means something on arrival.
func Backoff(attempts int) time.Duration {
	d := backoffBase
	for range attempts - 1 {
		d *= 2
		if d >= backoffCap {
			return backoffCap
		}
	}
	if d > backoffCap {
		return backoffCap
	}
	return d
}

type Queue interface {
	ClaimDueEvents(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]store.WebhookEvent, error)
	MarkEventDelivered(ctx context.Context, id string, statusCode int, at time.Time) error
	MarkEventFailed(ctx context.Context, id string, statusCode *int, reason string, retryAt time.Time, dead bool) error
}

type Deliverer struct {
	queue Queue
	hc    *http.Client
	log   *slog.Logger
	now   func() time.Time
}

func NewDeliverer(queue Queue, log *slog.Logger) *Deliverer {
	return &Deliverer{
		queue: queue,
		hc:    &http.Client{Timeout: DeliveryTimeout},
		log:   log,
		now:   time.Now,
	}
}

// Run polls until the context is cancelled. It is safe to run more than one.
func (d *Deliverer) Run(ctx context.Context) {
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()

	d.log.Info("webhook deliverer started",
		"poll_interval", PollInterval.String(),
		"max_attempts", MaxAttempts,
		"delivery_timeout", DeliveryTimeout.String())

	for {
		select {
		case <-ctx.Done():
			d.log.Info("webhook deliverer stopped")
			return
		case <-ticker.C:
		}

		if err := d.Tick(ctx); err != nil {
			d.log.Error("webhook delivery sweep failed", "error", err)
		}
	}
}

// Tick delivers one batch of due events.
func (d *Deliverer) Tick(ctx context.Context) error {
	events, err := d.queue.ClaimDueEvents(ctx, d.now(), ClaimLease, BatchSize)
	if err != nil {
		return err
	}
	for _, e := range events {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		d.deliver(ctx, e)
	}
	return nil
}

func (d *Deliverer) deliver(ctx context.Context, e store.WebhookEvent) {
	status, err := d.post(ctx, e)
	attempts := e.Attempts + 1

	log := d.log.With(
		"event_id", e.ID,
		"event_type", e.Type,
		"message_id", e.MessageID,
		"webhook_id", e.WebhookID,
		"attempt", attempts,
	)

	// Record against a context that outlives cancellation: an event delivered but not
	// recorded would be delivered again, and the record is cheap insurance against that.
	recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if err == nil {
		if err := d.queue.MarkEventDelivered(recCtx, e.ID, status, d.now()); err != nil {
			log.Error("could not record a delivered webhook", "error", err)
		}
		log.Info("webhook delivered", "status", status)
		return
	}

	var statusCode *int
	if status != 0 {
		statusCode = &status
	}
	dead := attempts >= MaxAttempts
	retryAt := d.now().Add(Backoff(attempts))

	if err := d.queue.MarkEventFailed(recCtx, e.ID, statusCode, err.Error(), retryAt, dead); err != nil {
		log.Error("could not record a failed webhook", "error", err)
	}

	if dead {
		log.Error("webhook dead-lettered after exhausting its attempts",
			"alert", true, "error", err, "url", e.URL)
		return
	}
	log.Warn("webhook delivery failed, will retry",
		"error", err, "retry_in", Backoff(attempts).String())
}

func (d *Deliverer) post(ctx context.Context, e store.WebhookEvent) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, DeliveryTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.URL, bytes.NewReader(e.Payload))
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}

	at := d.now()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "wa-dispatcher-webhooks/1")
	req.Header.Set(HeaderTimestamp, fmt.Sprint(at.Unix()))
	req.Header.Set(HeaderSignature, Sign(e.Secret, at, e.Payload))
	req.Header.Set(HeaderEventID, e.ID)
	req.Header.Set(HeaderEventType, e.Type)

	resp, err := d.hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("post to %s: %w", e.URL, err)
	}
	defer resp.Body.Close()

	// Read and discard a bounded amount so the connection can be reused. A consumer's
	// response body is not part of the contract and is never interpreted.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("%s returned %d", e.URL, resp.StatusCode)
	}
	return resp.StatusCode, nil
}
