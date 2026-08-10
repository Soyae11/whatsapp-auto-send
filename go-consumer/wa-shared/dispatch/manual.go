package dispatch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hibiken/asynq"

	"wa-shared/store"
	"wa-shared/tasks"
)

var (
	ErrJobNotFound = errors.New("dispatch: no task in the queue for that idempotency key")

	ErrJobActive = errors.New("dispatch: the task is being sent right now")

	ErrJobQueued = errors.New("dispatch: the task is already queued to run")

	ErrJobSent = errors.New("dispatch: the task already sent")

	ErrRequeueLostTheTask = errors.New("dispatch: requeue deleted the task and could not re-enqueue it")
)

type QueuedTask struct {
	Queue         string
	State         string
	NextProcessAt time.Time
	Retried       int
	MaxRetry      int
	LastError     string
}

func (e *Enqueuer) Locate(sessionID, key string) (*QueuedTask, error) {
	info, queue, err := e.locate(sessionID, key)
	if err != nil {
		return nil, err
	}
	return &QueuedTask{
		Queue:         queue,
		State:         info.State.String(),
		NextProcessAt: info.NextProcessAt,
		Retried:       info.Retried,
		MaxRetry:      info.MaxRetry,
		LastError:     info.LastErr,
	}, nil
}

func (e *Enqueuer) locate(sessionID, key string) (*asynq.TaskInfo, string, error) {
	for _, q := range tasks.QueuesFor(sessionID) {
		info, err := e.inspector.GetTaskInfo(q, key)
		switch {
		case err == nil:
			return info, q, nil
		case errors.Is(err, asynq.ErrTaskNotFound), errors.Is(err, asynq.ErrQueueNotFound):
			continue
		default:
			return nil, "", fmt.Errorf("dispatch: look up %s in %s: %w", key, q, err)
		}
	}
	return nil, "", fmt.Errorf("%w: %s", ErrJobNotFound, key)
}

func (e *Enqueuer) Requeue(ctx context.Context, sessionID, key string) (*Result, error) {
	info, queue, err := e.locate(sessionID, key)
	if err != nil {
		return nil, err
	}
	switch info.State {
	case asynq.TaskStateActive:
		return nil, fmt.Errorf("%w: %s", ErrJobActive, key)
	case asynq.TaskStatePending, asynq.TaskStateScheduled, asynq.TaskStateAggregating:
		return nil, fmt.Errorf("%w: %s is %s", ErrJobQueued, key, info.State)
	}

	payload, err := tasks.Unmarshal(info.Payload)
	if err != nil {
		return nil, fmt.Errorf("dispatch: requeue %s: %w", key, err)
	}
	_, lane, err := tasks.ParseQueue(queue)
	if err != nil {
		return nil, fmt.Errorf("dispatch: requeue %s: %w", key, err)
	}

	processAt, expedited, err := e.alloc.Next(ctx, sessionID, lane == tasks.PriorityCritical)
	if err != nil {
		return nil, err
	}

	if err := e.inspector.DeleteTask(queue, key); err != nil {
		return nil, fmt.Errorf("dispatch: requeue %s: could not take the task: %w", key, err)
	}

	if _, err := e.client.EnqueueContext(ctx, asynq.NewTask(tasks.TypeSendMessage, info.Payload),
		asynq.ProcessAt(processAt),
		asynq.Queue(queue),
		asynq.TaskID(key),
		asynq.MaxRetry(MaxRetry),
		asynq.Retention(Retention),
	); err != nil {
		e.log.Error("requeue lost a message",
			"alert", true, "session_id", sessionID, "idempotency_key", key, "error", err)
		return nil, fmt.Errorf("%w: %s: %w", ErrRequeueLostTheTask, key, err)
	}

	job := store.Job{
		IdempotencyKey: key,
		SessionID:      sessionID,
		To:             payload.To,
		SourceRef:      payload.SourceRef,
	}
	if err := e.jobs.MarkRequeued(ctx, job, processAt); err != nil {
		e.log.Error("could not record a requeued job",
			"session_id", sessionID, "idempotency_key", key, "error", err)
	}

	e.log.Info("send requeued by hand",
		"session_id", sessionID,
		"idempotency_key", key,
		"queue", queue,
		"was", info.State.String(),
		"process_at", processAt.UTC().Format(time.RFC3339Nano),
		"delay", time.Until(processAt).Round(time.Millisecond).String(),
		"expedited", expedited,
		"source_ref", payload.SourceRef,
	)

	return &Result{
		IdempotencyKey: key,
		SessionID:      sessionID,
		Queue:          queue,
		ProcessAt:      processAt,
	}, nil
}

// DeleteQueued removes a task from its queue without touching the wa_jobs record.
//
// It exists for the public cancel endpoint, which has already recorded the decision under its
// own status guard. Cancel below is the operator's version: it owns the record too, and logs
// an alert because a human reaching for it means something went wrong.
func (e *Enqueuer) DeleteQueued(sessionID, key string) error {
	_, queue, err := e.locate(sessionID, key)
	if err != nil {
		return err
	}
	if err := e.inspector.DeleteTask(queue, key); err != nil {
		return fmt.Errorf("dispatch: delete queued %s: %w", key, err)
	}
	return nil
}

func (e *Enqueuer) Cancel(ctx context.Context, sessionID, key string) (queue string, err error) {
	info, queue, err := e.locate(sessionID, key)
	if err != nil {
		return "", err
	}
	switch info.State {
	case asynq.TaskStateActive:
		return "", fmt.Errorf("%w: %s", ErrJobActive, key)
	case asynq.TaskStateCompleted:
		return "", fmt.Errorf("%w: %s", ErrJobSent, key)
	}

	if err := e.inspector.DeleteTask(queue, key); err != nil {
		return "", fmt.Errorf("dispatch: cancel %s: %w", key, err)
	}

	if err := e.jobs.MarkCancelled(ctx, key); err != nil {
		e.log.Error("could not record a cancelled job",
			"session_id", sessionID, "idempotency_key", key, "error", err)
	}

	e.log.Warn("send cancelled by hand",
		"alert", true,
		"session_id", sessionID,
		"idempotency_key", key,
		"queue", queue,
		"was", info.State.String(),
	)
	return queue, nil
}
