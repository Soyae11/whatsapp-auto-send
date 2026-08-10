package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"wa-shared/auth"
	"wa-shared/message"
	"wa-shared/senders"
	"wa-shared/store"
	"wa-shared/wa"
)

type messageList struct {
	Data       []Message `json:"data"`
	HasMore    bool      `json:"has_more"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

type senderView struct {
	Name                  string `json:"name"`
	Health                string `json:"health"`
	Detail                string `json:"detail"`
	Accepting             bool   `json:"accepting"`
	QueueDepth            int    `json:"queue_depth"`
	EstimatedDelaySeconds *int   `json:"estimated_delay_seconds,omitempty"`
	DryRunOnly            bool   `json:"dry_run_only"`
}

type senderList struct {
	Data []senderView `json:"data"`
}

const (
	healthAvailable   = "available"
	healthDegraded    = "degraded"
	healthUnavailable = "unavailable"
)

func (s *Server) handleGetMessage(w http.ResponseWriter, r *http.Request) {
	key, ok := s.requireKey(w, r)
	if !ok {
		return
	}

	ctx, cancel := contextWithTimeout(r, 5*time.Second)
	defer cancel()

	row, err := s.messages.GetByPublicID(ctx, key.ID, r.PathValue("id"))
	if !s.foundMessage(w, r, err) {
		return
	}

	msg := message.FromRow(*row)

	// Both need a second query, so only the single-message read resolves them — the list
	// endpoint never does, to avoid an N+1.
	if row.FailoverOf != nil {
		if publicID, err := s.messages.FailoverOfPublicID(ctx, key.ID, *row.FailoverOf); err == nil {
			msg.FailoverOf = publicID
		}
	}
	if publicID, err := s.messages.RetriedByPublicID(ctx, key.ID, row.IdempotencyKey); err == nil {
		msg.RetriedBy = publicID
	}

	writeJSON(w, http.StatusOK, msg)
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	key, ok := s.requireKey(w, r)
	if !ok {
		return
	}

	filter, ok := s.parseMessageFilter(w, r)
	if !ok {
		return
	}
	filter.APIKeyID = key.ID

	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	rows, hasMore, err := s.messages.ListMessages(ctx, filter)
	if err != nil {
		s.log.Error("could not list messages",
			"request_id", requestIDFrom(r.Context()), "api_key_id", key.ID, "error", err)
		writeProblem(w, r, CodeInternalError, "The messages could not be listed. It is safe to retry.")
		return
	}

	out := messageList{Data: make([]Message, 0, len(rows)), HasMore: hasMore}
	for _, row := range rows {
		out.Data = append(out.Data, message.FromRow(row))
	}
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		out.NextCursor = store.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}.Encode()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCancelMessage(w http.ResponseWriter, r *http.Request) {
	key, ok := s.requireKey(w, r)
	if !ok {
		return
	}

	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	id := r.PathValue("id")
	row, err := s.messages.CancelByPublicID(ctx, key.ID, id, time.Now().UTC())
	switch {
	case errors.Is(err, store.ErrNotCancellable):
		writeProblem(w, r, CodeMessageNotCancellable, id+" is already '"+message.Status(*row)+
			"'. Only a message still in 'queued' can be cancelled — once the worker has picked it up the send is in flight at WhatsApp and there is nothing left to stop.")
		return

	case !s.foundMessage(w, r, err):
		return
	}

	// The queue is the thing that actually stops the send; the row only records the
	// decision. A task that has already been picked up is gone from the queue anyway, and
	// the store's status check above is what makes that case a 409 rather than a lie.
	if err := s.enqueuer.DeleteQueued(row.SessionID, row.IdempotencyKey); err != nil {
		s.log.Warn("cancelled the record but could not delete the queued task",
			"request_id", requestIDFrom(r.Context()),
			"message_id", id,
			"idempotency_key", row.IdempotencyKey,
			"error", err)
	}

	s.log.Info("message cancelled",
		"request_id", requestIDFrom(r.Context()), "api_key_id", key.ID, "message_id", id)
	writeJSON(w, http.StatusOK, message.FromRow(*row))
}

func (s *Server) handleListSenders(w http.ResponseWriter, r *http.Request) {
	key, ok := s.requireKey(w, r)
	if !ok {
		return
	}

	out := senderList{Data: []senderView{}}
	for _, name := range s.senders.Names() {
		if !key.MaySend(name) {
			continue
		}
		sender, err := s.senders.Get(name)
		if err != nil {
			continue
		}
		out.Data = append(out.Data, s.senderStatus(r, sender))
	}
	writeJSON(w, http.StatusOK, out)
}

// senderStatus answers the two questions a consumer has before a large batch: may I send, and
// how long will it take. Everything it reads is best-effort — a probe that fails reports
// degraded rather than failing the request, because a consumer that cannot read health is
// better off sending than blocked.
func (s *Server) senderStatus(r *http.Request, sender senders.Sender) senderView {
	ctx, cancel := contextWithTimeout(r, 3*time.Second)
	defer cancel()

	view := senderView{
		Name:       sender.Name,
		Health:     healthAvailable,
		Detail:     "Sending normally.",
		Accepting:  true,
		DryRunOnly: sender.DryRun,
	}

	if depth, err := s.enqueuer.QueueDepth(sender.SessionID); err == nil {
		view.QueueDepth = depth
	}

	delay, delayErr := s.enqueuer.Depth(ctx, sender.SessionID)
	if delayErr == nil {
		seconds := int(delay.Round(time.Second).Seconds())
		view.EstimatedDelaySeconds = &seconds
	}

	// A backlog past the horizon means a send would be refused, which is unavailable even
	// though the session itself is fine.
	if delayErr == nil && delay >= s.horizon {
		view.Health = healthUnavailable
		view.Accepting = false
		view.EstimatedDelaySeconds = nil
		view.Detail = fmt.Sprintf(
			"Backlog is %s, past the %s scheduling horizon. Sends are being refused with queue_horizon_exceeded until it drains.",
			delay.Round(time.Second), s.horizon)
		return view
	}

	status, err := s.sessionStatus(ctx, sender)
	switch {
	case err != nil:
		view.Health = healthDegraded
		view.Detail = "Health could not be read just now. Sends are still accepted and queued."

	case status == wa.StatusLoggedOut:
		view.Health = healthUnavailable
		view.Accepting = false
		view.EstimatedDelaySeconds = nil
		view.Detail = "Logged out. A human has to re-pair this sender; sends are refused with sender_unavailable until they do."

	case !status.CanSend():
		view.Health = healthDegraded
		view.Detail = "Reconnecting. Messages are still accepted and will be sent once the connection is back."

	case view.QueueDepth > 0 && view.EstimatedDelaySeconds != nil && *view.EstimatedDelaySeconds > 0:
		view.Detail = fmt.Sprintf("Sending normally, %d waiting.", view.QueueDepth)
	}
	return view
}

func (s *Server) sessionStatus(ctx context.Context, sender senders.Sender) (wa.SessionStatus, error) {
	if s.sessions == nil {
		return wa.StatusConnected, nil
	}
	return s.sessions.Status(ctx, sender.SessionID)
}

func (s *Server) parseMessageFilter(w http.ResponseWriter, r *http.Request) (store.MessageFilter, bool) {
	q := r.URL.Query()
	f := store.MessageFilter{
		Reference: q.Get("reference"),
		Sender:    q.Get("sender"),
	}

	if raw := q.Get("to"); raw != "" {
		to, err := wa.NormalisePhone(raw)
		if err != nil {
			writeFieldProblem(w, r, CodeInvalidRequest,
				"'to' is "+quoteOrEmpty(raw)+"; "+err.Error(),
				[]fieldError{{Field: "to", Detail: "not a valid phone number"}})
			return f, false
		}
		f.To = to
	}

	for _, raw := range q["status"] {
		internal, ok := internalStatuses(raw)
		if !ok {
			writeFieldProblem(w, r, CodeInvalidRequest,
				"'status' is "+quoteOrEmpty(raw)+"; it takes queued, sending, sent, delivered, read, cancelled, or failed.",
				[]fieldError{{Field: "status", Detail: "unknown status"}})
			return f, false
		}
		f.Statuses = append(f.Statuses, internal...)
	}

	for _, spec := range []struct {
		param string
		dst   *time.Time
	}{
		{"created_after", &f.After},
		{"created_before", &f.Before},
	} {
		raw := q.Get(spec.param)
		if raw == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeFieldProblem(w, r, CodeInvalidRequest,
				"'"+spec.param+"' is "+quoteOrEmpty(raw)+"; it takes an RFC 3339 timestamp such as 2026-08-09T10:24:07Z.",
				[]fieldError{{Field: spec.param, Detail: "not an RFC 3339 timestamp"}})
			return f, false
		}
		*spec.dst = t
	}

	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > store.MaxPageLimit {
			writeFieldProblem(w, r, CodeInvalidRequest,
				fmt.Sprintf("'limit' is %s; it takes a whole number from 1 to %d.", quoteOrEmpty(raw), store.MaxPageLimit),
				[]fieldError{{Field: "limit", Detail: "out of range"}})
			return f, false
		}
		f.Limit = n
	}

	cursor, err := store.ParseCursor(q.Get("cursor"))
	if err != nil {
		writeFieldProblem(w, r, CodeInvalidRequest,
			"'cursor' is not one this API issued: "+err.Error()+". Pass back a 'next_cursor' unchanged rather than constructing one.",
			[]fieldError{{Field: "cursor", Detail: "unreadable cursor"}})
		return f, false
	}
	f.Cursor = cursor

	return f, true
}

// internalStatuses expands one public status into the internal ones it covers. `queued` maps
// to two, which is the same collapse Status performs in the other direction.
//
// `sent`, `delivered`, and `read` all share the internal `sent`, so filtering on any of them
// over-selects; the caller gets messages that are at least that far along. Narrowing further
// would need the receipt columns in the WHERE clause, and the coarser answer is the honest one
// given receipts are best-effort.
func internalStatuses(public string) ([]store.Status, bool) {
	switch public {
	case message.StatusQueued:
		return []store.Status{store.StatusScheduled, store.StatusRetrying}, true
	case message.StatusSending:
		return []store.Status{store.StatusSending}, true
	case message.StatusSent, message.StatusDelivered, message.StatusRead:
		return []store.Status{store.StatusSent}, true
	case message.StatusCancelled:
		return []store.Status{store.StatusCancelled}, true
	case message.StatusFailed:
		return []store.Status{store.StatusFailed}, true
	}
	return nil, false
}

func (s *Server) requireKey(w http.ResponseWriter, r *http.Request) (*auth.Key, bool) {
	key := apiKeyFrom(r.Context())
	if key == nil {
		writeProblem(w, r, CodeInternalError, "The request could not be authorised. It is safe to retry.")
		return nil, false
	}
	return key, true
}

// foundMessage turns a store lookup error into the right response, and reports whether the
// caller should carry on. A message belonging to another key reads as missing, never as
// forbidden — whose message it actually is, is not this caller's business.
func (s *Server) foundMessage(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return true

	case errors.Is(err, store.ErrNotFound):
		writeProblem(w, r, CodeMessageNotFound,
			"No message with that id belongs to this key. Dry-run ids are never retrievable, because a dry run stores nothing.")
		return false

	default:
		s.log.Error("could not read a message",
			"request_id", requestIDFrom(r.Context()), "error", err)
		writeProblem(w, r, CodeInternalError, "The message could not be read. It is safe to retry.")
		return false
	}
}
