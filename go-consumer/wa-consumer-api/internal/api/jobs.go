package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"wa-shared/dispatch"
	"wa-shared/slots"
	"wa-shared/store"
)

type jobView struct {
	IdempotencyKey string     `json:"idempotency_key"`
	SessionID      string     `json:"session_id"`
	To             string     `json:"to"`
	SourceRef      string     `json:"source_ref,omitempty"`
	CoalescedRefs  []string   `json:"coalesced_refs,omitempty"`
	Status         string     `json:"status"`
	Attempts       int        `json:"attempts"`
	ScheduledFor   *time.Time `json:"scheduled_for"`
	LastErrorCode  *string    `json:"last_error_code"`
	LastErrorAt    *time.Time `json:"last_error_at"`
	SentAt         *time.Time `json:"sent_at"`
	CreatedAt      time.Time  `json:"created_at"`
}

func viewOf(r store.Row) jobView {
	return jobView{
		IdempotencyKey: r.IdempotencyKey,
		SessionID:      r.SessionID,
		To:             r.To,
		SourceRef:      r.SourceRef,
		CoalescedRefs:  r.CoalescedRefs,
		Status:         string(r.Status),
		Attempts:       r.Attempts,
		ScheduledFor:   r.ScheduledFor,
		LastErrorCode:  r.LastErrorCode,
		LastErrorAt:    r.LastErrorAt,
		SentAt:         r.SentAt,
		CreatedAt:      r.CreatedAt,
	}
}

type listResponse struct {
	Jobs       []jobView `json:"jobs"`
	NextCursor int64     `json:"next_cursor,omitempty"`
}

type attemptView struct {
	Attempt   int       `json:"attempt"`
	Outcome   string    `json:"outcome"`
	ErrorCode *string   `json:"error_code"`
	At        time.Time `json:"at"`
}

type taskView struct {
	Queue         string     `json:"queue"`
	State         string     `json:"state"`
	NextProcessAt *time.Time `json:"next_process_at"`
	Retried       int        `json:"retried"`
	MaxRetry      int        `json:"max_retry"`
	LastError     string     `json:"last_error,omitempty"`
}

type jobDetail struct {
	jobView
	History []attemptView `json:"history"`
	Task    *taskView     `json:"task"`
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	q := r.URL.Query()
	f := store.Filter{
		SessionID: q.Get("session_id"),
		SourceRef: q.Get("source_ref"),
	}

	if raw := q.Get("status"); raw != "" {
		status, err := store.ParseStatus(raw)
		if err != nil {
			badRequest(w, err.Error())
			return
		}
		f.Status = status
	}
	var err error
	if f.Since, err = timeParam(q.Get("since")); err != nil {
		badRequest(w, "since: "+err.Error())
		return
	}
	if f.Until, err = timeParam(q.Get("until")); err != nil {
		badRequest(w, "until: "+err.Error())
		return
	}
	if f.Limit, err = intParam(q.Get("limit")); err != nil {
		badRequest(w, "limit: "+err.Error())
		return
	}
	cursor, err := intParam(q.Get("cursor"))
	if err != nil {
		badRequest(w, "cursor: "+err.Error())
		return
	}
	f.Before = int64(cursor)

	rows, err := s.jobs.List(ctx, f)
	if err != nil {
		s.log.Error("could not list jobs", "error", err)
		writeError(w, http.StatusInternalServerError, errorBody{
			ErrorCode: "internal_error",
			Message:   "could not list jobs",
			Retryable: true,
		})
		return
	}

	body := listResponse{Jobs: make([]jobView, 0, len(rows))}
	for _, row := range rows {
		body.Jobs = append(body.Jobs, viewOf(row))
	}
	if n := len(rows); n > 0 && n == effectiveLimit(f.Limit) {
		body.NextCursor = rows[n-1].ID
	}
	writeJSON(w, http.StatusOK, body)
}

func effectiveLimit(limit int) int {
	switch {
	case limit <= 0:
		return store.DefaultListLimit
	case limit > store.MaxListLimit:
		return store.MaxListLimit
	default:
		return limit
	}
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	key := r.PathValue("key")
	row, err := s.jobs.Get(ctx, key)
	if err != nil {
		s.jobLookupError(w, key, err)
		return
	}

	history, err := s.jobs.Attempts(ctx, key)
	if err != nil {
		s.log.Error("could not read attempt history", "idempotency_key", key, "error", err)
		writeError(w, http.StatusInternalServerError, errorBody{
			ErrorCode: "internal_error",
			Message:   "could not read attempt history",
			Retryable: true,
		})
		return
	}

	body := jobDetail{jobView: viewOf(*row), History: make([]attemptView, 0, len(history))}
	for _, a := range history {
		body.History = append(body.History, attemptView{
			Attempt:   a.Attempt,
			Outcome:   string(a.Outcome),
			ErrorCode: a.ErrorCode,
			At:        a.At,
		})
	}

	switch task, err := s.enqueuer.Locate(row.SessionID, key); {
	case err == nil:
		body.Task = &taskView{
			Queue:     task.Queue,
			State:     task.State,
			Retried:   task.Retried,
			MaxRetry:  task.MaxRetry,
			LastError: task.LastError,
		}
		if !task.NextProcessAt.IsZero() {
			at := task.NextProcessAt.UTC()
			body.Task.NextProcessAt = &at
		}
	case !errors.Is(err, dispatch.ErrJobNotFound):
		s.log.Error("could not read the queued task", "idempotency_key", key, "error", err)
	}

	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleRetryJob(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	key := r.PathValue("key")
	row, err := s.jobs.Get(ctx, key)
	if err != nil {
		s.jobLookupError(w, key, err)
		return
	}

	res, err := s.enqueuer.Requeue(ctx, row.SessionID, key)
	if err != nil {
		s.manualActionError(w, "retry", key, err)
		return
	}

	at := res.ProcessAt.UTC()
	writeJSON(w, http.StatusAccepted, requeueResponse{
		IdempotencyKey: res.IdempotencyKey,
		SessionID:      res.SessionID,
		Queue:          res.Queue,
		ProcessAt:      &at,
	})
}

type requeueResponse struct {
	IdempotencyKey string     `json:"idempotency_key"`
	SessionID      string     `json:"session_id"`
	Queue          string     `json:"queue"`
	ProcessAt      *time.Time `json:"process_at"`
}

type cancelResponse struct {
	IdempotencyKey string `json:"idempotency_key"`
	SessionID      string `json:"session_id"`
	Queue          string `json:"queue"`
	Cancelled      bool   `json:"cancelled"`
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	key := r.PathValue("key")
	row, err := s.jobs.Get(ctx, key)
	if err != nil {
		s.jobLookupError(w, key, err)
		return
	}

	queue, err := s.enqueuer.Cancel(ctx, row.SessionID, key)
	if err != nil {
		s.manualActionError(w, "cancel", key, err)
		return
	}
	writeJSON(w, http.StatusOK, cancelResponse{
		IdempotencyKey: key,
		SessionID:      row.SessionID,
		Queue:          queue,
		Cancelled:      true,
	})
}

func (s *Server) jobLookupError(w http.ResponseWriter, key string, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errorBody{
			ErrorCode: "job_not_found",
			Message:   "no job recorded for idempotency key " + key,
		})
		return
	}
	s.log.Error("could not read job", "idempotency_key", key, "error", err)
	writeError(w, http.StatusInternalServerError, errorBody{
		ErrorCode: "internal_error",
		Message:   "could not read job",
		Retryable: true,
	})
}

func (s *Server) manualActionError(w http.ResponseWriter, what, key string, err error) {
	switch {
	case errors.Is(err, dispatch.ErrJobNotFound):
		writeError(w, http.StatusNotFound, errorBody{
			ErrorCode: "task_not_found",
			Message:   err.Error(),
		})

	case errors.Is(err, dispatch.ErrJobActive):
		writeError(w, http.StatusConflict, errorBody{
			ErrorCode: "job_active",
			Message:   err.Error(),
		})

	case errors.Is(err, dispatch.ErrJobQueued):
		writeError(w, http.StatusConflict, errorBody{
			ErrorCode: "job_already_queued",
			Message:   err.Error(),
		})

	case errors.Is(err, dispatch.ErrJobSent):
		writeError(w, http.StatusConflict, errorBody{
			ErrorCode: "job_already_sent",
			Message:   err.Error(),
		})

	case errors.Is(err, slots.ErrHorizonExceeded):
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, errorBody{
			ErrorCode: "slot_horizon_exceeded",
			Message:   err.Error(),
			Retryable: true,
		})

	default:
		s.log.Error("could not "+what+" job", "idempotency_key", key, "error", err)
		writeError(w, http.StatusInternalServerError, errorBody{
			ErrorCode: "internal_error",
			Message:   "could not " + what + " job",
			Retryable: true,
		})
	}
}

func badRequest(w http.ResponseWriter, message string) {
	writeError(w, http.StatusBadRequest, errorBody{
		ErrorCode: "invalid_payload",
		Message:   message,
	})
}

func timeParam(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, errors.New("want an RFC 3339 timestamp, got " + strconv.Quote(raw))
	}
	return t, nil
}

func intParam(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, errors.New("want a whole number, got " + strconv.Quote(raw))
	}
	return n, nil
}
