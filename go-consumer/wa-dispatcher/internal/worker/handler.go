package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"

	"wa-shared/store"
	"wa-shared/tasks"
	"wa-shared/wa"
)

type Recorder interface {
	BeginAttempt(ctx context.Context, j store.Job) (attempts int, err error)
	MarkSent(ctx context.Context, j store.Job, sentAt time.Time) error
	MarkFailed(ctx context.Context, j store.Job, errorCode string, permanent bool, at time.Time) error
	Get(ctx context.Context, idempotencyKey string) (*store.Row, error)
}

type NopRecorder struct{}

func (NopRecorder) BeginAttempt(context.Context, store.Job) (int, error) { return 0, nil }
func (NopRecorder) MarkSent(context.Context, store.Job, time.Time) error { return nil }
func (NopRecorder) MarkFailed(context.Context, store.Job, string, bool, time.Time) error {
	return nil
}
func (NopRecorder) Get(context.Context, string) (*store.Row, error) { return nil, nil }

// Emitter queues the webhook a status change should announce. The handler never waits on a
// consumer's endpoint — emitting writes rows, and the deliverer sends them on its own clock.
type Emitter interface {
	EmitForRow(ctx context.Context, row store.Row)
}

type NopEmitter struct{}

func (NopEmitter) EmitForRow(context.Context, store.Row) {}

type CircuitBreaker interface {
	Success(ctx context.Context, sessionID string) error
	Failure(ctx context.Context, sessionID, errorCode string) (opened bool, err error)
}

type NopBreaker struct{}

func (NopBreaker) Success(context.Context, string) error                 { return nil }
func (NopBreaker) Failure(context.Context, string, string) (bool, error) { return false, nil }

type Handler struct {
	wa      *wa.Client
	jobs    Recorder
	circuit CircuitBreaker
	events  Emitter
	log     *slog.Logger
}

type HandlerOption func(*Handler)

func WithBreaker(b CircuitBreaker) HandlerOption {
	return func(h *Handler) {
		if b != nil {
			h.circuit = b
		}
	}
}

func WithEmitter(e Emitter) HandlerOption {
	return func(h *Handler) {
		if e != nil {
			h.events = e
		}
	}
}

func NewHandler(client *wa.Client, jobs Recorder, log *slog.Logger, opts ...HandlerOption) *Handler {
	if jobs == nil {
		jobs = NopRecorder{}
	}
	h := &Handler{wa: client, jobs: jobs, circuit: NopBreaker{}, events: NopEmitter{}, log: log}
	for _, o := range opts {
		o(h)
	}
	return h
}

// announce emits the webhook for whatever status the job now holds. It re-reads the row rather
// than building one from the payload, because the event carries the full public message and
// only the stored row knows its public id, attempts, and timestamps.
func (h *Handler) announce(ctx context.Context, key string) {
	row, err := h.jobs.Get(ctx, key)
	if err != nil || row == nil {
		if err != nil {
			h.log.Warn("could not read a job to announce it", "idempotency_key", key, "error", err)
		}
		return
	}
	h.events.EmitForRow(ctx, *row)
}

func (h *Handler) ProcessTask(ctx context.Context, t *asynq.Task) error {
	p, err := tasks.Unmarshal(t.Payload())
	if err != nil {
		h.log.Error("undecodable task payload", "error", err)
		return fmt.Errorf("%w: %w", err, asynq.SkipRetry)
	}

	job := store.Job{
		IdempotencyKey: p.IdempotencyKey,
		SessionID:      p.SessionID,
		To:             p.To,
		SourceRef:      p.SourceRef,
	}

	retried, haveRetried := asynq.GetRetryCount(ctx)
	maxRetry, haveMax := asynq.GetMaxRetry(ctx)
	lastAttempt := haveRetried && haveMax && retried >= maxRetry

	attempts, err := h.jobs.BeginAttempt(ctx, job)
	if err != nil {
		h.log.Error("could not record attempt",
			"idempotency_key", p.IdempotencyKey, "session_id", p.SessionID, "error", err)
	}

	log := h.log.With(
		"idempotency_key", p.IdempotencyKey,
		"session_id", p.SessionID,
		"source_ref", p.SourceRef,
		"attempt", attempts,
		"queue", queueOf(ctx),
	)

	start := time.Now()
	res, sendErr := h.wa.Send(ctx, p.SessionID, wa.SendRequest{
		IdempotencyKey: p.IdempotencyKey,
		To:             p.To,
		Type:           p.Type,
		Text:           p.Text,
	})
	elapsed := time.Since(start)

	if sendErr != nil {
		return h.recordFailure(ctx, log, job, sendErr, elapsed, lastAttempt)
	}

	job.WAMessageID = derefOr(res.WAMessageID, "")

	sentCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := h.jobs.MarkSent(sentCtx, job, time.Now()); err != nil {
		log.Error("could not record send", "error", err)
	}
	h.announce(sentCtx, job.IdempotencyKey)
	if err := h.circuit.Success(sentCtx, p.SessionID); err != nil {
		log.Error("could not reset the circuit's failure count", "error", err)
	}

	log.Info("sent",
		"duration", elapsed.Round(time.Millisecond).String(),
		"deduplicated", res.Deduplicated,
		"wa_message_id", derefOr(res.WAMessageID, ""),
	)
	return nil
}

func (h *Handler) recordFailure(
	ctx context.Context,
	log *slog.Logger,
	job store.Job,
	sendErr error,
	elapsed time.Duration,
	lastAttempt bool,
) error {
	code := wa.ErrorCode(sendErr)
	v := Classify(sendErr)
	permanent := !v.Retryable || lastAttempt

	recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := h.jobs.MarkFailed(recCtx, job, code, permanent, time.Now()); err != nil {
		log.Error("could not record failure", "error", err)
	}

	// Only a terminal failure is an event. A retryable one leaves the message publicly
	// `queued`, and announcing it would tell a consumer their message failed when it is
	// about to be tried again.
	if permanent {
		h.announce(recCtx, job.IdempotencyKey)
	}

	if v.TripsCircuit {
		if _, err := h.circuit.Failure(recCtx, job.SessionID, code); err != nil {
			log.Error("could not record the failure against the circuit", "error", err)
		}
	}

	attrs := []any{
		"error_code", code,
		"error", sendErr,
		"duration", elapsed.Round(time.Millisecond).String(),
	}
	if v.Alert {
		attrs = append(attrs, "alert", true)
	}

	switch {
	case !v.Retryable:
		log.Error("send failed permanently", attrs...)
		return fmt.Errorf("%w: %w", sendErr, asynq.SkipRetry)

	case lastAttempt:
		log.Error("send failed, retry budget exhausted", attrs...)
		return sendErr

	default:
		log.Warn("send failed, will retry", append(attrs, "backoff_base", v.Backoff.Base.String())...)
		return sendErr
	}
}

func queueOf(ctx context.Context) string {
	q, _ := asynq.GetQueueName(ctx)
	return q
}

func derefOr[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

func IsPermanent(err error) bool { return errors.Is(err, asynq.SkipRetry) }
