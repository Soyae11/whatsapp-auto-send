package dispatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"

	"wa-shared/slots"
	"wa-shared/store"
	"wa-shared/tasks"
	"wa-shared/wa"
)

const (
	MaxRetry = 5

	Retention = 7 * 24 * time.Hour

	MaxTextLen = 65536
)

type SendRequest struct {
	SessionID string
	To        string
	Text      string
	Priority  tasks.Priority
	SourceRef string

	// IdempotencyKey is the caller-supplied Idempotency-Key. APIKeyID scopes it so two
	// projects cannot collide. Together with the normalised recipient they derive the
	// gateway key.
	IdempotencyKey string
	APIKeyID       string

	PublicID string
	Sender   string
	Metadata map[string]string

	// FailoverOf links this send back to the message it's covering for, when it exists
	// because a pool failed over to a different session. Empty for an ordinary send.
	FailoverOf string
}

type Result struct {
	IdempotencyKey string
	PublicID       string
	SessionID      string
	To             string
	Queue          string
	ProcessAt      time.Time
	Duplicate      bool
	Coalesced      bool
}

type ValidationError struct {
	Field  string
	Reason string
	Err    error
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid %s: %s", e.Field, e.Reason)
}

func (e *ValidationError) Unwrap() error { return e.Err }

var ErrValidation = errors.New("validation failed")

func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

type Recorder interface {
	MarkScheduled(ctx context.Context, j store.Job, scheduledFor time.Time) (publicID string, err error)
	MarkFailed(ctx context.Context, j store.Job, errorCode string, permanent bool, at time.Time) error
	MarkCoalesced(ctx context.Context, idempotencyKey, sourceRef string) (publicID string, err error)
	MarkRequeued(ctx context.Context, j store.Job, scheduledFor time.Time) error
	MarkCancelled(ctx context.Context, idempotencyKey string) error
	PublicIDFor(ctx context.Context, idempotencyKey string) (string, error)
}

type NopRecorder struct{}

func (NopRecorder) MarkScheduled(context.Context, store.Job, time.Time) (string, error) {
	return "", nil
}
func (NopRecorder) MarkFailed(context.Context, store.Job, string, bool, time.Time) error {
	return nil
}
func (NopRecorder) MarkCoalesced(context.Context, string, string) (string, error) { return "", nil }
func (NopRecorder) MarkRequeued(context.Context, store.Job, time.Time) error      { return nil }
func (NopRecorder) MarkCancelled(context.Context, string) error                   { return nil }
func (NopRecorder) PublicIDFor(context.Context, string) (string, error)           { return "", nil }

type Enqueuer struct {
	client    *asynq.Client
	inspector *asynq.Inspector
	alloc     *slots.Allocator
	coalescer *Coalescer
	jobs      Recorder
	log       *slog.Logger
}

func New(client *asynq.Client, inspector *asynq.Inspector, alloc *slots.Allocator, coalescer *Coalescer, jobs Recorder, log *slog.Logger) *Enqueuer {
	if jobs == nil {
		jobs = NopRecorder{}
	}
	return &Enqueuer{
		client:    client,
		inspector: inspector,
		alloc:     alloc,
		coalescer: coalescer,
		jobs:      jobs,
		log:       log,
	}
}

// Validate runs every check Enqueue would run, and returns the normalised recipient and lane.
// A dry run calls this and stops, which is what lets it reject exactly what a real send would
// reject without allocating a slot or queueing anything.
func Validate(req SendRequest) (to string, priority tasks.Priority, err error) {
	if req.SessionID == "" {
		return "", "", &ValidationError{Field: "session_id", Reason: "required"}
	}
	if req.IdempotencyKey == "" {
		return "", "", &ValidationError{Field: "idempotency_key", Reason: "required"}
	}
	if err := tasks.ValidCallerKey(req.IdempotencyKey); err != nil {
		return "", "", &ValidationError{Field: "idempotency_key", Reason: err.Error()}
	}
	switch {
	case req.Text == "":
		return "", "", &ValidationError{Field: "text", Reason: "required"}
	case len(req.Text) > MaxTextLen:
		return "", "", &ValidationError{
			Field:  "text",
			Reason: fmt.Sprintf("%d bytes exceeds the %d limit", len(req.Text), MaxTextLen),
		}
	}

	priority = req.Priority
	if priority == "" {
		priority = tasks.PriorityDefault
	}
	if _, err := tasks.ParsePriority(string(priority)); err != nil {
		return "", "", &ValidationError{Field: "priority", Reason: err.Error()}
	}

	to, err = wa.NormalisePhone(req.To)
	if err != nil {
		return "", "", &ValidationError{Field: "to", Reason: err.Error(), Err: err}
	}

	return to, priority, nil
}

func (e *Enqueuer) Enqueue(ctx context.Context, req SendRequest) (*Result, error) {
	to, priority, err := Validate(req)
	if err != nil {
		return nil, err
	}

	key := tasks.IdempotencyKey(req.APIKeyID, req.IdempotencyKey, to)
	queue := tasks.QueueFor(req.SessionID, priority)

	if info, err := e.inspector.GetTaskInfo(queue, key); err == nil {
		return &Result{
			IdempotencyKey: key,
			PublicID:       e.publicIDFor(ctx, key, req.PublicID),
			SessionID:      req.SessionID,
			To:             to,
			Queue:          queue,
			ProcessAt:      info.NextProcessAt,
			Duplicate:      true,
		}, nil
	} else if !errors.Is(err, asynq.ErrTaskNotFound) && !errors.Is(err, asynq.ErrQueueNotFound) {
		return nil, fmt.Errorf("dispatch: check existing task: %w", err)
	}

	if e.coalescer != nil && coalescable(priority) {
		merged, err := e.coalesce(ctx, req, to, key, queue, priority)
		if err != nil {
			return nil, err
		}
		if merged != nil {
			return merged, nil
		}
	}

	processAt, expedited, err := e.alloc.Next(ctx, req.SessionID, priority == tasks.PriorityCritical)
	if err != nil {
		return nil, err
	}

	job := store.Job{
		IdempotencyKey: key,
		SessionID:      req.SessionID,
		To:             to,
		SourceRef:      req.SourceRef,
		PublicID:       req.PublicID,
		APIKeyID:       req.APIKeyID,
		Sender:         req.Sender,
		Priority:       string(priority),
		Metadata:       req.Metadata,
		FailoverOf:     req.FailoverOf,
	}

	// storedID is the public id the row ends up carrying, which is not always the one minted for
	// this request: a row left standing by MarkScheduled keeps the id an earlier caller was given,
	// and that is the row this send moves through. It is "" when the record failed, in which case
	// the minted id is the only one there is.
	storedID, err := e.jobs.MarkScheduled(ctx, job, processAt)
	if err != nil {
		e.log.Error("could not record scheduled job",
			"session_id", req.SessionID, "idempotency_key", key, "error", err)
	}
	publicID := req.PublicID
	if storedID != "" {
		publicID = storedID
	}

	payload, err := tasks.Marshal(tasks.SendMessagePayload{
		IdempotencyKey: key,
		SessionID:      req.SessionID,
		To:             to,
		Type:           tasks.MessageTypeText,
		Text:           req.Text,
		Priority:       string(priority),
		EnqueuedAt:     time.Now().UTC(),
		SourceRef:      req.SourceRef,
		Sender:         req.Sender,
	})
	if err != nil {
		e.markUnqueued(ctx, job, err)
		return nil, err
	}

	_, err = e.client.EnqueueContext(ctx, asynq.NewTask(tasks.TypeSendMessage, payload),
		asynq.ProcessAt(processAt),
		asynq.Queue(queue),
		asynq.TaskID(key),
		asynq.MaxRetry(MaxRetry),
		asynq.Retention(Retention),
	)
	switch {
	case errors.Is(err, asynq.ErrTaskIDConflict):
		e.log.Info("duplicate enqueue ignored",
			"session_id", req.SessionID, "idempotency_key", key, "queue", queue)
		return &Result{
			IdempotencyKey: key,
			PublicID:       publicID,
			SessionID:      req.SessionID,
			To:             to,
			Queue:          queue,
			Duplicate:      true,
		}, nil

	case err != nil:
		e.markUnqueued(ctx, job, err)
		return nil, fmt.Errorf("dispatch: enqueue %s: %w", key, err)
	}

	e.log.Info("send scheduled",
		"session_id", req.SessionID,
		"idempotency_key", key,
		"queue", queue,
		"process_at", processAt.UTC().Format(time.RFC3339Nano),
		"delay", time.Until(processAt).Round(time.Millisecond).String(),
		"expedited", expedited,
		"source_ref", req.SourceRef,
	)

	return &Result{
		IdempotencyKey: key,
		PublicID:       publicID,
		SessionID:      req.SessionID,
		To:             to,
		Queue:          queue,
		ProcessAt:      processAt,
		Duplicate:      false,
	}, nil
}

// publicIDFor answers with the id the first accepted copy of this message was given, so a
// duplicate does not hand the caller an id that addresses nothing. It falls back to the id
// this request minted when no earlier row carries one.
func (e *Enqueuer) publicIDFor(ctx context.Context, key, fallback string) string {
	stored, err := e.jobs.PublicIDFor(ctx, key)
	if err != nil {
		e.log.Warn("could not read the stored public id", "idempotency_key", key, "error", err)
		return fallback
	}
	if stored == "" {
		return fallback
	}
	return stored
}

// Depth reports how far out a session's next slot would fall without allocating one. A dry run
// answers with it, so it can quote a realistic estimated_send_at while moving nothing.
func (e *Enqueuer) Depth(ctx context.Context, sessionID string) (time.Duration, error) {
	return e.alloc.Peek(ctx, sessionID)
}

// QueueDepth counts the messages waiting on a session across all of its priority lanes.
// A queue that does not exist yet has nothing waiting, which is not an error.
func (e *Enqueuer) QueueDepth(sessionID string) (int, error) {
	total := 0
	for _, q := range tasks.QueuesFor(sessionID) {
		info, err := e.inspector.GetQueueInfo(q)
		if errors.Is(err, asynq.ErrQueueNotFound) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("dispatch: queue depth for %s: %w", q, err)
		}
		total += info.Pending + info.Scheduled + info.Retry + info.Active
	}
	return total, nil
}

var ErrCoalesceLostTheOpenTask = errors.New("dispatch: coalesce deleted the open task and could not re-enqueue it")

func (e *Enqueuer) coalesce(ctx context.Context, req SendRequest, to, key, queue string, priority tasks.Priority) (*Result, error) {
	g, err := e.coalescer.claim(ctx, req.SessionID, priority, to, key)
	if err != nil {
		e.log.Warn("coalesce claim failed, sending separately",
			"session_id", req.SessionID, "idempotency_key", key, "error", err)
		return nil, nil
	}
	if g.openKey == key {
		return nil, nil
	}

	processAt, err := e.mergeInto(ctx, queue, g.openKey, req.Text)
	switch {
	case errors.Is(err, ErrCoalesceLostTheOpenTask):
		e.log.Error("coalesce lost a message",
			"session_id", req.SessionID, "idempotency_key", g.openKey, "error", err)
		return nil, err

	case err != nil:
		e.log.Info("coalesce fell back to a separate send",
			"session_id", req.SessionID,
			"into", g.openKey,
			"idempotency_key", key,
			"reason", err)
		if err := e.coalescer.reopen(ctx, req.SessionID, priority, to, key); err != nil {
			e.log.Warn("could not reopen the coalesce group",
				"session_id", req.SessionID, "error", err)
		}
		return nil, nil
	}

	publicID, err := e.jobs.MarkCoalesced(ctx, g.openKey, req.SourceRef)
	if err != nil {
		e.log.Error("could not record a coalesced source_ref",
			"idempotency_key", g.openKey, "source_ref", req.SourceRef, "error", err)
	}
	if publicID == "" {
		publicID = req.PublicID
	}

	e.log.Info("send coalesced",
		"session_id", req.SessionID,
		"idempotency_key", g.openKey,
		"queue", queue,
		"process_at", processAt.UTC().Format(time.RFC3339Nano),
		"messages", g.count,
		"source_ref", req.SourceRef,
	)

	return &Result{
		IdempotencyKey: g.openKey,
		PublicID:       publicID,
		SessionID:      req.SessionID,
		To:             to,
		Queue:          queue,
		ProcessAt:      processAt,
		Coalesced:      true,
	}, nil
}

func (e *Enqueuer) mergeInto(ctx context.Context, queue, openKey, text string) (time.Time, error) {
	info, err := e.inspector.GetTaskInfo(queue, openKey)
	if err != nil {
		return time.Time{}, fmt.Errorf("open task unreadable: %w", err)
	}
	if info.State != asynq.TaskStateScheduled {
		return time.Time{}, fmt.Errorf("open task is %s, not scheduled", info.State)
	}
	if lead := time.Until(info.NextProcessAt); lead < e.coalescer.cfg.MinLead {
		return time.Time{}, fmt.Errorf("open task is due in %s, under the %s lead",
			lead.Round(time.Millisecond), e.coalescer.cfg.MinLead)
	}

	payload, err := tasks.Unmarshal(info.Payload)
	if err != nil {
		return time.Time{}, fmt.Errorf("open task payload: %w", err)
	}

	payload.Text += CoalesceSeparator + text
	if len(payload.Text) > MaxTextLen {
		return time.Time{}, fmt.Errorf("merged text would be %d bytes, over the %d limit",
			len(payload.Text), MaxTextLen)
	}

	body, err := tasks.Marshal(payload)
	if err != nil {
		return time.Time{}, err
	}

	processAt := info.NextProcessAt
	if err := e.inspector.DeleteTask(queue, openKey); err != nil {
		return time.Time{}, fmt.Errorf("could not take the open task: %w", err)
	}

	if _, err := e.client.EnqueueContext(ctx, asynq.NewTask(tasks.TypeSendMessage, body),
		asynq.ProcessAt(processAt),
		asynq.Queue(queue),
		asynq.TaskID(openKey),
		asynq.MaxRetry(MaxRetry),
		asynq.Retention(Retention),
	); err != nil {
		return time.Time{}, fmt.Errorf("%w: %s: %w", ErrCoalesceLostTheOpenTask, openKey, err)
	}

	return processAt, nil
}

func (e *Enqueuer) markUnqueued(ctx context.Context, job store.Job, cause error) {
	recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if err := e.jobs.MarkFailed(recCtx, job, store.EnqueueFailedCode, true, time.Now().UTC()); err != nil {
		e.log.Error("could not record failed enqueue",
			"idempotency_key", job.IdempotencyKey, "cause", cause, "error", err)
	}
}
