package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"

	"wa-shared/circuit"
	"wa-shared/dispatch"
	"wa-shared/id"
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
	State(ctx context.Context, sessionID string) (circuit.State, error)
}

type NopBreaker struct{}

func (NopBreaker) Success(context.Context, string) error                 { return nil }
func (NopBreaker) Failure(context.Context, string, string) (bool, error) { return false, nil }
func (NopBreaker) State(context.Context, string) (circuit.State, error)  { return circuit.State{}, nil }

// Pools is a sender's main/backup session pool — see wa-shared/store/pools.go. A sender with no
// pool rows is unaffected by any of this.
type Pools interface {
	Pool(ctx context.Context, sender string) ([]store.PoolMember, error)
	Rotate(ctx context.Context, sender, failedSessionID string, isOpen func(sessionID string) bool) (promotedTo string, err error)
}

type NopPools struct{}

func (NopPools) Pool(context.Context, string) ([]store.PoolMember, error) { return nil, nil }
func (NopPools) Rotate(context.Context, string, string, func(string) bool) (string, error) {
	return "", nil
}

// Enqueuer is the one thing a failover resend needs from wa-shared/dispatch — a fresh send
// under a new idempotency key, exactly as any other send is made. wa-dispatcher otherwise only
// ever consumes; this is the one path where it also produces.
type Enqueuer interface {
	Enqueue(ctx context.Context, req dispatch.SendRequest) (*dispatch.Result, error)
}

type Handler struct {
	wa       *wa.Client
	jobs     Recorder
	circuit  CircuitBreaker
	events   Emitter
	pools    Pools
	enqueuer Enqueuer
	log      *slog.Logger
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

func WithPools(p Pools) HandlerOption {
	return func(h *Handler) {
		if p != nil {
			h.pools = p
		}
	}
}

func WithEnqueuer(e Enqueuer) HandlerOption {
	return func(h *Handler) {
		if e != nil {
			h.enqueuer = e
		}
	}
}

func NewHandler(client *wa.Client, jobs Recorder, log *slog.Logger, opts ...HandlerOption) *Handler {
	if jobs == nil {
		jobs = NopRecorder{}
	}
	h := &Handler{
		wa: client, jobs: jobs, circuit: NopBreaker{}, events: NopEmitter{}, pools: NopPools{}, log: log,
	}
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
		return h.recordFailure(ctx, log, job, p, sendErr, elapsed, lastAttempt)
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
	p tasks.SendMessagePayload,
	sendErr error,
	elapsed time.Duration,
	lastAttempt bool,
) error {
	code := wa.ErrorCode(sendErr)
	v := Classify(sendErr)

	recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	// A pooled sender's failures are handled entirely here, on the very first one — not after
	// the normal same-session retry/backoff runs out. An unpooled sender (no pool rows at all)
	// falls straight through to that unchanged, exactly as it always has.
	if h.failoverPooled(recCtx, log, job, p, code, v) {
		return fmt.Errorf("%w: %w", sendErr, asynq.SkipRetry)
	}

	permanent := !v.Retryable || lastAttempt
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

// failoverPooled handles a failure end to end for a sender with a pool: it always records the
// original attempt as a permanent failure (job.SessionID has already proven unfit for this
// message — retrying it on the same session is not on the table once a pool exists), rotates
// the pool (wa-shared/store.Store.Rotate — promotes a healthy backup if job.SessionID was main,
// or just disqualifies it alone if it was a load-spread backup with main untouched), and — the
// one thing this synchronous path can do that the async rejection path in wa-consumer-api
// cannot — resends the same message through whichever session Rotate found, because the text
// is still sitting right here in p, not gone the way it is by the time a receipt arrives.
//
// Reports whether it handled the failure at all. false — no pool for this sender, or a failure
// that does not indict the session — means the caller proceeds exactly as it always has.
func (h *Handler) failoverPooled(
	ctx context.Context,
	log *slog.Logger,
	job store.Job,
	p tasks.SendMessagePayload,
	code string,
	v Verdict,
) bool {
	if p.Sender == "" {
		return false
	}

	// A failure that says nothing about the session must not cost the pool a member. A recipient
	// who is not on WhatsApp, or a payload WhatsApp refuses, fails identically on every session,
	// so resending it through a backup only buys a second failure — and each hop disqualifies
	// another member until one mistyped phone number leaves the pool exhausted. TripsCircuit is
	// already exactly this distinction ("a sick session from a bad request", see Verdict), so
	// failover reuses it rather than keeping a second list of codes that could drift from it.
	// Returning false hands these back to the ordinary path, which records them as the permanent,
	// session-blameless failures they are.
	if !v.TripsCircuit {
		return false
	}

	members, err := h.pools.Pool(ctx, p.Sender)
	if err != nil {
		log.Error("could not read pool after a send failure", "sender", p.Sender, "error", err)
		return false
	}
	if len(members) == 0 {
		return false
	}

	if err := h.jobs.MarkFailed(ctx, job, code, true, time.Now()); err != nil {
		log.Error("could not record failure", "error", err)
	}
	h.announce(ctx, job.IdempotencyKey)
	if v.TripsCircuit {
		if _, err := h.circuit.Failure(ctx, job.SessionID, code); err != nil {
			log.Error("could not record the failure against the circuit", "error", err)
		}
	}

	isOpen := h.isCircuitOpen(ctx)
	backup, err := h.pools.Rotate(ctx, p.Sender, p.SessionID, isOpen)
	if err != nil {
		log.Error("could not rotate pool after a send failure", "sender", p.Sender, "error", err)
		return true
	}

	// Rotate's "" covers two situations that could not be further apart, and only one is fatal.
	// If the failed session was main, "" means nothing was eligible to take over and the pool
	// really is out of options. If it was a load-spread backup, Rotate disqualified it alone and
	// deliberately left main standing — there is somewhere to send, Rotate simply has no
	// promotion to report and says so in its doc comment ("a caller that still wants to try a
	// fresh send picks among the pool's remaining eligible members itself"). Without this, a
	// message routed to a backup by load spreading was dropped with a healthy main right there.
	if backup == "" {
		remaining, err := h.pools.Pool(ctx, p.Sender)
		if err != nil {
			log.Error("could not re-read pool after rotating", "sender", p.Sender, "error", err)
			return true
		}
		backup, _ = store.PickHealthyMember(remaining, p.SessionID, isOpen)
	}
	if backup == "" {
		log.Error("pool has no session eligible to take over", "alert", true, "sender", p.Sender, "error_code", code)
		return true
	}

	h.resendViaBackup(ctx, log, p, backup)
	return true
}

// resendViaBackup sends p's text again through a different session, as a brand-new message —
// new idempotency key, new public id — linked back to the one that failed via FailoverOf,
// exactly like every other "resend after a failure" in this system rather than a mutation of
// the row that failed. APIKeyID and Metadata aren't in the task payload (kept lean; see
// tasks.SendMessagePayload), so this reads them back from the original job row.
func (h *Handler) resendViaBackup(ctx context.Context, log *slog.Logger, p tasks.SendMessagePayload, backup string) {
	if h.enqueuer == nil {
		log.Error("no enqueuer configured, cannot resend via backup", "backup", backup)
		return
	}

	original, err := h.jobs.Get(ctx, p.IdempotencyKey)
	if err != nil || original == nil {
		log.Error("could not read the original job to resend it", "error", err)
		return
	}

	priority, err := tasks.ParsePriority(p.Priority)
	if err != nil {
		priority = tasks.PriorityDefault
	}

	newKey := p.IdempotencyKey + "-fo-" + id.Random("", 6)
	_, err = h.enqueuer.Enqueue(ctx, dispatch.SendRequest{
		SessionID:      backup,
		To:             p.To,
		Text:           p.Text,
		Priority:       priority,
		SourceRef:      p.SourceRef,
		IdempotencyKey: newKey,
		APIKeyID:       original.APIKeyID,
		PublicID:       id.New("msg"),
		Sender:         p.Sender,
		Metadata:       original.Metadata,
		FailoverOf:     p.IdempotencyKey,
	})
	if err != nil {
		log.Error("could not resend via backup session", "backup", backup, "error", err)
		return
	}
	log.Warn("failed over to backup session", "alert", true, "backup", backup, "new_idempotency_key", newKey)
}

// isCircuitOpen adapts the circuit breaker to Rotate's isOpen callback shape.
func (h *Handler) isCircuitOpen(ctx context.Context) func(sessionID string) bool {
	return func(sessionID string) bool {
		state, err := h.circuit.State(ctx, sessionID)
		return err == nil && state.Open
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
