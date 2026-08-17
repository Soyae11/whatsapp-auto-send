package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"wa-shared/dispatch"
	"wa-shared/id"
	"wa-shared/message"
	"wa-shared/senders"
	"wa-shared/slots"
	"wa-shared/store"
	"wa-shared/tasks"
	"wa-shared/wa"
)

const (
	HeaderIdempotencyKey = "Idempotency-Key"
	HeaderIdempotentRepl = "X-Idempotent-Replay"
	HeaderDryRun         = "X-Dry-Run"

	messageIDPrefix = "msg"
	dryRunIDPrefix  = "msg_dry"

	MaxBatchSize = 100

	MaxMetadataKeys     = 16
	MaxMetadataKeyLen   = 64
	MaxMetadataValueLen = 256
)

// The public message shape lives in internal/message, so the API and outbound webhooks render
// the same thing from the same status mapping.
type (
	Message    = message.Message
	Timestamps = message.Timestamps
)

type sendFields struct {
	Sender    string            `json:"sender"`
	To        string            `json:"to"`
	Type      string            `json:"type"`
	Text      string            `json:"text"`
	Priority  string            `json:"priority"`
	Reference string            `json:"reference"`
	Metadata  map[string]string `json:"metadata"`
}

type batchItem struct {
	sendFields
	IdempotencyKey string `json:"idempotency_key"`
}

type batchRequest struct {
	Messages []batchItem `json:"messages"`
}

type batchResult struct {
	Index   int      `json:"index"`
	Status  string   `json:"status"`
	Message *Message `json:"message,omitempty"`
	Error   *problem `json:"error,omitempty"`
}

type batchResponse struct {
	Accepted int           `json:"accepted"`
	Rejected int           `json:"rejected"`
	Results  []batchResult `json:"results"`
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	body, ok := s.readBody(w, r)
	if !ok {
		return
	}

	// The header is checked before the body is parsed: a caller who forgot it needs to hear
	// that, not a JSON complaint that sends them looking in the wrong place.
	callerKey, ok := s.idempotencyKeyOf(w, r)
	if !ok {
		return
	}

	var req sendFields
	if err := decodeStrict(body, &req); err != nil {
		writeProblem(w, r, CodeInvalidRequest, err.Error())
		return
	}

	// Everything is validated before the key is claimed, so a request that never had a
	// chance of being accepted does not leave a claim behind for its own retry to trip on.
	plan, ok := s.planSend(w, r, req, callerKey, nil)
	if !ok {
		return
	}

	if plan.dryRun {
		writeJSON(w, http.StatusAccepted, s.dryRunMessage(r.Context(), plan))
		return
	}

	s.underIdempotency(w, r, callerKey, body, func() (int, any, bool) {
		msg, ok := s.enqueue(w, r, plan)
		if !ok {
			return 0, nil, false
		}
		return http.StatusAccepted, msg, true
	})
}

func (s *Server) handleSendBatch(w http.ResponseWriter, r *http.Request) {
	body, ok := s.readBody(w, r)
	if !ok {
		return
	}

	callerKey, ok := s.idempotencyKeyOf(w, r)
	if !ok {
		return
	}

	var req batchRequest
	if err := decodeStrict(body, &req); err != nil {
		writeProblem(w, r, CodeInvalidRequest, err.Error())
		return
	}

	switch {
	case len(req.Messages) == 0:
		writeFieldProblem(w, r, CodeInvalidRequest,
			"'messages' is empty. A batch must carry at least one message.",
			[]fieldError{{Field: "/messages", Detail: "at least one message required"}})
		return
	case len(req.Messages) > MaxBatchSize:
		writeProblem(w, r, CodeBatchTooLarge, fmt.Sprintf(
			"This batch has %d messages, over the %d limit. Split it — a batch of %d is not faster than %d single sends, it is one round trip.",
			len(req.Messages), MaxBatchSize, MaxBatchSize, MaxBatchSize))
		return
	}

	if dup, at := firstDuplicateKey(req.Messages); dup != "" {
		writeFieldProblem(w, r, CodeInvalidRequest, fmt.Sprintf(
			"Two items share the idempotency key %q. Two items with one key are one message, so the second would be silently dropped — give each item its own key.", dup),
			[]fieldError{{Field: fmt.Sprintf("/messages/%d/idempotency_key", at), Detail: "duplicated within this batch"}})
		return
	}

	s.underIdempotency(w, r, callerKey, body, func() (int, any, bool) {
		return http.StatusMultiStatus, s.runBatch(w, r, req.Messages), true
	})
}

func (s *Server) runBatch(w http.ResponseWriter, r *http.Request, items []batchItem) batchResponse {
	out := batchResponse{Results: make([]batchResult, 0, len(items))}

	// One memo for the whole batch: items naming the same sender share its routing and its
	// health verdict instead of each re-deriving them. See sendMemo.
	memo := newSendMemo()

	for i, item := range items {
		rec := &problemRecorder{header: http.Header{}}

		plan, ok := s.planSend(rec, r, item.sendFields, item.IdempotencyKey, memo)
		if ok && plan.dryRun {
			out.Accepted++
			msg := s.dryRunMessage(r.Context(), plan)
			out.Results = append(out.Results, batchResult{Index: i, Status: "accepted", Message: &msg})
			continue
		}

		var msg Message
		if ok {
			msg, ok = s.enqueue(rec, r, plan)
		}
		if !ok {
			out.Rejected++
			carryRetryAfter(w, rec)
			out.Results = append(out.Results, batchResult{Index: i, Status: "rejected", Error: rec.problem(r)})
			continue
		}

		out.Accepted++
		out.Results = append(out.Results, batchResult{Index: i, Status: "accepted", Message: &msg})
	}

	return out
}

// carryRetryAfter lifts a per-item Retry-After onto the batch response. An item rejected for a
// backlogged or logged-out sender is the one case where the whole batch should back off, and a
// 207 gives the caller nowhere else to read that from.
func carryRetryAfter(w http.ResponseWriter, rec *problemRecorder) {
	if after := rec.header.Get("Retry-After"); after != "" && w.Header().Get("Retry-After") == "" {
		w.Header().Set("Retry-After", after)
	}
}

func firstDuplicateKey(items []batchItem) (string, int) {
	seen := make(map[string]bool, len(items))
	for i, item := range items {
		if seen[item.IdempotencyKey] {
			return item.IdempotencyKey, i
		}
		seen[item.IdempotencyKey] = true
	}
	return "", 0
}

// sendMemo caches the per-sender decisions planSend makes, for the length of one request.
//
// A batch may name the same sender a hundred times, and each item asked independently: a pool
// read, a queue-depth read, a status read, and — when that session is logged out — a probe walk
// across every pool member and a rotation attempt that deliberately writes nothing when the pool
// has nowhere to go. None of it varies between two items naming the same sender, so a hundred-item
// batch paid for the same answer a hundred times: up to 4.5s of I/O per item behind a 60s write
// timeout, which a caller could only respond to by retrying the whole batch onto a claim that had
// since expired — and being sent twice.
//
// A nil *sendMemo caches nothing and every method still works, which is what a single send wants.
// A memo must not outlive its request: the answers are only as fresh as the moment it was made.
type sendMemo struct {
	routing  map[string]poolRouting
	verdicts map[string]senderVerdict
}

func newSendMemo() *sendMemo {
	return &sendMemo{
		routing:  map[string]poolRouting{},
		verdicts: map[string]senderVerdict{},
	}
}

// routingFor answers with the pool read for this sender earlier in the same request, or reads it.
// Errors are cached too: a misconfigured pool is misconfigured for every item of the batch, and
// re-reading to rediscover that is the same wasted query a hundred times over.
func (m *sendMemo) routingFor(sender string, read func() poolRouting) poolRouting {
	if m == nil {
		return read()
	}
	if got, ok := m.routing[sender]; ok {
		return got
	}
	got := read()
	m.routing[sender] = got
	return got
}

// verdictFor is keyed by session id, not by sender: load spreading routes the items of one batch
// across several members, and "is this session logged out, and where should it go instead" is a
// question about the session that was chosen. Keying it by sender would answer for a member that
// was never asked about. A batch that is not spreading names one session throughout and so pays
// for one status read, which is the case worth collapsing.
func (m *sendMemo) verdictFor(sessionID string, decide func() senderVerdict) senderVerdict {
	if m == nil {
		return decide()
	}
	if got, ok := m.verdicts[sessionID]; ok {
		return got
	}
	v := decide()
	m.verdicts[sessionID] = v
	return v
}

// sendPlan is a request that has passed every check and is ready to be queued, or to be
// answered as a dry run.
type sendPlan struct {
	fields    sendFields
	sender    senders.Sender
	priority  tasks.Priority
	to        string
	callerKey string
	apiKeyID  string
	publicID  string
	dryRun    bool
}

func (s *Server) planSend(w http.ResponseWriter, r *http.Request, req sendFields, callerKey string, memo *sendMemo) (sendPlan, bool) {
	if req.Type != tasks.MessageTypeText {
		writeFieldProblem(w, r, CodeInvalidRequest,
			`'type' is required and the only value it takes is "text". It is required rather than defaulted so that adding media types later cannot change what an existing request means.`,
			[]fieldError{{Field: "/type", Detail: `want "text", got ` + quoteOrEmpty(req.Type)}})
		return sendPlan{}, false
	}

	if err := validateMetadata(req.Metadata); err != nil {
		writeFieldProblem(w, r, CodeInvalidRequest, err.Error(),
			[]fieldError{{Field: "/metadata", Detail: err.Error()}})
		return sendPlan{}, false
	}

	sender, ok := s.resolveSender(w, r, req.Sender)
	if !ok {
		return sendPlan{}, false
	}

	sender, members, err := s.resolveSendSession(r.Context(), sender, memo)
	if err != nil {
		if errors.Is(err, errSenderPoolExhausted) {
			writeProblem(w, r, CodeSenderPoolExhausted, "Sender "+quoteOrEmpty(sender.Name)+
				" has no session currently eligible to send: every session in its pool has "+
				"failed and been disqualified. A human has to reinstate one on the Pools page "+
				"before this sender can send again — queueing would only delay the same failure.")
			return sendPlan{}, false
		}
		if errors.Is(err, errSenderPoolMisconfigured) {
			writeProblem(w, r, CodeSenderPoolMisconfigured, "Sender "+quoteOrEmpty(sender.Name)+
				" is configured for pool mode but has no pool yet. An operator has to create one "+
				"via POST /internal/senders/"+sender.Name+"/pool before this sender can send.")
			return sendPlan{}, false
		}
		s.log.Error("could not resolve a session for sender",
			"request_id", requestIDFrom(r.Context()), "sender", sender.Name, "error", err)
		writeProblem(w, r, CodeInternalError, "The message could not be queued. It is safe to retry.")
		return sendPlan{}, false
	}

	priority := sender.DefaultPriority
	if req.Priority != "" {
		p, err := tasks.ParsePriority(req.Priority)
		if err != nil {
			writeFieldProblem(w, r, CodeInvalidRequest, err.Error(),
				[]fieldError{{Field: "/priority", Detail: err.Error()}})
			return sendPlan{}, false
		}
		priority = p
	}

	key := apiKeyFrom(r.Context())
	if key == nil {
		writeProblem(w, r, CodeInternalError, "The request could not be authorised. It is safe to retry.")
		return sendPlan{}, false
	}

	dryRun, ok := s.dryRunRequested(w, r, sender)
	if !ok {
		return sendPlan{}, false
	}

	if !dryRun && !s.senderCanSend(w, r, &sender, members, memo) {
		return sendPlan{}, false
	}

	plan := sendPlan{
		fields:    req,
		sender:    sender,
		priority:  priority,
		callerKey: callerKey,
		apiKeyID:  key.ID,
		dryRun:    dryRun,
	}

	to, _, err := dispatch.Validate(plan.sendRequest())
	if err != nil {
		s.sendError(w, r, sender, err)
		return sendPlan{}, false
	}
	plan.to = to

	plan.publicID = id.New(messageIDPrefix)
	if dryRun {
		plan.publicID = id.New(dryRunIDPrefix)
	}
	return plan, true
}

func (p sendPlan) sendRequest() dispatch.SendRequest {
	return dispatch.SendRequest{
		SessionID:      p.sender.SessionID,
		To:             p.fields.To,
		Text:           p.fields.Text,
		Priority:       p.priority,
		SourceRef:      p.fields.Reference,
		IdempotencyKey: p.callerKey,
		APIKeyID:       p.apiKeyID,
		PublicID:       p.publicID,
		Sender:         p.sender.Name,
		Metadata:       p.fields.Metadata,
	}
}

func (s *Server) enqueue(w http.ResponseWriter, r *http.Request, plan sendPlan) (Message, bool) {
	res, err := s.enqueuer.Enqueue(r.Context(), plan.sendRequest())
	if err != nil {
		s.sendError(w, r, plan.sender, err)
		return Message{}, false
	}

	now := time.Now().UTC()
	msg := Message{
		ID:         firstNonEmpty(res.PublicID, plan.publicID),
		Status:     message.StatusQueued,
		Sender:     plan.sender.Name,
		To:         res.To,
		Type:       tasks.MessageTypeText,
		Priority:   string(plan.priority),
		Reference:  plan.fields.Reference,
		Metadata:   plan.fields.Metadata,
		Timestamps: Timestamps{QueuedAt: &now},
		CreatedAt:  now,
	}
	if !res.ProcessAt.IsZero() {
		at := res.ProcessAt.UTC()
		msg.EstimatedSendAt = &at
	}
	return msg, true
}

func (s *Server) dryRunMessage(ctx context.Context, plan sendPlan) Message {
	now := time.Now().UTC()
	msg := Message{
		ID:         plan.publicID,
		Status:     message.StatusQueued,
		Sender:     plan.sender.Name,
		To:         plan.to,
		Type:       tasks.MessageTypeText,
		Priority:   string(plan.priority),
		DryRun:     true,
		Reference:  plan.fields.Reference,
		Metadata:   plan.fields.Metadata,
		Timestamps: Timestamps{QueuedAt: &now},
		CreatedAt:  now,
	}

	// Peek rather than allocate: a dry run must not move the session's tail, or a staging
	// environment would pace production's sends.
	if depth, err := s.enqueuer.Depth(ctx, plan.sender.SessionID); err == nil {
		at := now.Add(depth)
		msg.EstimatedSendAt = &at
	}
	return msg
}

// underIdempotency runs work exactly once per caller key, replaying the first response for
// every repeat and refusing a key reused with different content.
func (s *Server) underIdempotency(w http.ResponseWriter, r *http.Request, callerKey string, body []byte, work func() (int, any, bool)) {
	key := apiKeyFrom(r.Context())
	if key == nil {
		writeProblem(w, r, CodeInternalError, "The request could not be authorised. It is safe to retry.")
		return
	}
	if s.idempotency == nil {
		s.runAndWrite(w, work)
		return
	}

	// Scoped to the claim alone, and deliberately not reused afterwards: work() runs between the
	// claim and the record, and a batch of a hundred messages takes far longer than this budget.
	claimCtx, cancel := contextWithTimeout(r, 5*time.Second)
	defer cancel()

	hash := store.HashRequest(body)
	claimID, replay, err := s.idempotency.ClaimIdempotencyKey(claimCtx, key.ID, callerKey, hash)
	switch {
	case errors.Is(err, store.ErrIdempotencyConflict):
		s.log.Warn("idempotency key reused with a different payload",
			"request_id", requestIDFrom(r.Context()), "api_key_id", key.ID, "idempotency_key", callerKey)
		writeProblem(w, r, CodeIdempotencyConflict, "The key "+quoteOrEmpty(callerKey)+
			" was already used for a different message. This is almost always a bug: either the key is built from something that changes between retries, or two different sends share one key. Use a new key for a new message, and the original key to retry the original one.")
		return

	case errors.Is(err, store.ErrIdempotencyInFlight):
		w.Header().Set("Retry-After", "1")
		writeProblem(w, r, CodeInternalError, "An earlier request with the key "+quoteOrEmpty(callerKey)+
			" is still being processed. Retry with the same key in a moment; it will replay that request's response rather than send twice.")
		return

	case err != nil:
		s.log.Error("could not claim the idempotency key",
			"request_id", requestIDFrom(r.Context()), "api_key_id", key.ID, "error", err)
		writeProblem(w, r, CodeInternalError, "The request could not be made idempotent, so it was not accepted. It is safe to retry with the same key.")
		return

	case replay != nil:
		w.Header().Set(HeaderIdempotentRepl, "true")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(replay.StatusCode)
		_, _ = w.Write(replay.Body)
		return
	}

	status, payload, ok := work()
	if !ok {
		// Nothing replayable happened, so the claim must go — otherwise the caller's own
		// retry would collide with a key that recorded no outcome.
		s.releaseClaim(r, key.ID, callerKey, claimID)
		return
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		s.log.Error("could not encode the response for replay",
			"request_id", requestIDFrom(r.Context()), "error", err)
		s.releaseClaim(r, key.ID, callerKey, claimID)
		writeProblem(w, r, CodeInternalError, "The message was accepted but the response could not be encoded. Report request id "+requestIDFrom(r.Context())+".")
		return
	}

	// A context of its own, on two counts: claimCtx's budget was spent on the claim and work(),
	// and a caller that hung up still needs its key to end up replayable. Recording nothing
	// strands the claim with no response — the one state that answers every retry with "still
	// being processed".
	recCtx, recCancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer recCancel()

	switch err := s.idempotency.RecordIdempotentResponse(recCtx, key.ID, callerKey, claimID, status, encoded); {
	case errors.Is(err, store.ErrIdempotencyClaimTaken):
		// This request outran the in-flight timeout and a retry took the key over, so the response
		// stored under it is that retry's and must stay. Answer this caller normally: the work
		// really did happen. Worth an alert, though — it means a send took over five minutes, and
		// the takeover sent the message a second time.
		s.log.Warn("finished after the idempotency claim was taken over; the response was not recorded",
			"alert", true,
			"request_id", requestIDFrom(r.Context()), "api_key_id", key.ID, "idempotency_key", callerKey)

	case err != nil:
		// The work is done and the message is queued, so answer normally rather than inviting a
		// retry that sends nothing new. The claim is left behind deliberately — it stops a retry
		// re-sending while this request is in the air, and store.IdempotencyInFlightTimeout caps
		// how long an unrecorded one can block the key.
		s.log.Error("could not record the idempotent response",
			"request_id", requestIDFrom(r.Context()), "api_key_id", key.ID, "idempotency_key", callerKey, "error", err)
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}

func (s *Server) runAndWrite(w http.ResponseWriter, work func() (int, any, bool)) {
	if status, payload, ok := work(); ok {
		writeJSON(w, status, payload)
	}
}

func (s *Server) releaseClaim(r *http.Request, apiKeyID, callerKey, claimID string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()

	if err := s.idempotency.ReleaseIdempotencyKey(ctx, apiKeyID, callerKey, claimID); err != nil {
		s.log.Error("could not release the idempotency claim",
			"request_id", requestIDFrom(r.Context()), "api_key_id", apiKeyID, "idempotency_key", callerKey, "error", err)
	}
}

func (s *Server) idempotencyKeyOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get(HeaderIdempotencyKey))
	if key == "" {
		writeProblem(w, r, CodeIdempotencyKeyReq,
			"Every send needs an 'Idempotency-Key' header. Derive it from something stable in your domain, such as 'leave-8842-notify-approver' — not a UUID generated at call time, because a retry would generate a different one and message the recipient twice.")
		return "", false
	}
	if err := tasks.ValidCallerKey(key); err != nil {
		writeProblem(w, r, CodeInvalidRequest, "The 'Idempotency-Key' header is not usable: "+err.Error()+".")
		return "", false
	}
	return key, true
}

func (s *Server) dryRunRequested(w http.ResponseWriter, r *http.Request, sender senders.Sender) (bool, bool) {
	if sender.DryRun {
		return true, true
	}
	raw := strings.TrimSpace(r.Header.Get(HeaderDryRun))
	if raw == "" {
		return false, true
	}
	on, err := strconv.ParseBool(raw)
	if err != nil {
		writeProblem(w, r, CodeInvalidRequest,
			"The 'X-Dry-Run' header is "+quoteOrEmpty(raw)+"; it takes 'true' or 'false'. It is rejected rather than read as false, because a dry run silently becoming a real send is the expensive way to find out.")
		return false, false
	}
	return on, true
}

// senderCanSend refuses only the case where queueing cannot help: a logged-out sender needs a
// human to re-pair it. A reconnecting or briefly disconnected sender still gets 202, because the
// message waits in the queue and goes out on recovery.
//
// A logged-out session is itself a failure signal, so a pooled sender rotates on it exactly as it
// would on a failed send — finding the failure before queueing rather than after a doomed
// attempt. When the pool still has a usable session, sender.SessionID is updated in place.
//
// members is the pool resolveSendSession already read for this sender, threaded down rather than
// read again; it is nil for a single-mode sender, which never reaches the rotation branch.
//
// The decision and the response are separate on purpose: the decision costs a status read and
// possibly a whole probe walk, so a batch caches it per sender, while the refusal has to be
// written once per item that it rejects.
func (s *Server) senderCanSend(w http.ResponseWriter, r *http.Request, sender *senders.Sender, members []store.PoolMember, memo *sendMemo) bool {
	v := memo.verdictFor(sender.SessionID, func() senderVerdict {
		return s.senderVerdict(r, *sender, members)
	})
	if !v.ok {
		s.refuseLoggedOutSender(w, r, sender)
		return false
	}
	sender.SessionID = v.sessionID
	return true
}

// senderVerdict is senderCanSend's decision on its own: may this sender send, and through which
// session. Cacheable for the length of a request precisely because it writes nothing.
type senderVerdict struct {
	sessionID string
	ok        bool
}

func (s *Server) senderVerdict(r *http.Request, sender senders.Sender, members []store.PoolMember) senderVerdict {
	if s.sessions == nil {
		return senderVerdict{sessionID: sender.SessionID, ok: true}
	}

	ctx, cancel := contextWithTimeout(r, 3*time.Second)
	defer cancel()

	view, err := s.sessions.Status(ctx, sender.SessionID)
	if err != nil {
		s.log.Warn("could not read sender status, accepting the send",
			"request_id", requestIDFrom(r.Context()), "sender", sender.Name, "error", err)
		return senderVerdict{sessionID: sender.SessionID, ok: true}
	}
	if view != wa.StatusLoggedOut {
		return senderVerdict{sessionID: sender.SessionID, ok: true}
	}

	if s.pools != nil && sender.Mode == senders.ModePool && len(members) > 0 {
		if next, ok := s.rotateToConnectedMember(ctx, sender.Name, sender.SessionID, members); ok {
			s.log.Warn("sender logged out at send time, pool rotated",
				"alert", true,
				"request_id", requestIDFrom(r.Context()),
				"sender", sender.Name, "session_id", sender.SessionID, "rotated_to", next)
			return senderVerdict{sessionID: next, ok: true}
		}
	}

	s.log.Warn("send refused, sender is logged out",
		"alert", true,
		"request_id", requestIDFrom(r.Context()),
		"sender", sender.Name,
		"session_id", sender.SessionID)
	return senderVerdict{sessionID: sender.SessionID, ok: false}
}

func (s *Server) refuseLoggedOutSender(w http.ResponseWriter, r *http.Request, sender *senders.Sender) {
	w.Header().Set("Retry-After", "300")
	writeProblem(w, r, CodeSenderUnavailable, "Sender "+quoteOrEmpty(sender.Name)+
		" is logged out and a human has to re-pair it, so queueing this message would not help — it was refused rather than left to expire in a queue. The platform team is being alerted. Retry with the same idempotency key once the sender is back.")
}

func validateMetadata(m map[string]string) error {
	if len(m) > MaxMetadataKeys {
		return fmt.Errorf("'metadata' has %d keys, over the %d limit", len(m), MaxMetadataKeys)
	}
	for k, v := range m {
		if len(k) > MaxMetadataKeyLen {
			return fmt.Errorf("the metadata key %q is %d characters, over the %d limit", k, len(k), MaxMetadataKeyLen)
		}
		if len(v) > MaxMetadataValueLen {
			return fmt.Errorf("the metadata value for %q is %d characters, over the %d limit", k, len(v), MaxMetadataValueLen)
		}
	}
	return nil
}

func (s *Server) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		if media, _, err := mime.ParseMediaType(ct); err != nil || media != "application/json" {
			writeProblem(w, r, CodeUnsupportedMedia,
				"This endpoint reads 'Content-Type: application/json', not "+quoteOrEmpty(ct)+".")
			return nil, false
		}
	}

	body, err := readAll(w, r, maxBodyBytes)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeProblem(w, r, CodePayloadTooLarge, "The request body is over the 1 MiB limit.")
			return nil, false
		}
		writeProblem(w, r, CodeInvalidRequest, "The request body could not be read: "+err.Error())
		return nil, false
	}
	return body, true
}

func readAll(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	return io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
}

func decodeStrict(body []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return errors.New("The request body could not be read as JSON: " + err.Error())
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return errors.New("The request body must contain exactly one JSON object.")
	}
	return nil
}

func (s *Server) sendError(w http.ResponseWriter, r *http.Request, sender senders.Sender, err error) {
	var ve *dispatch.ValidationError
	switch {
	case errors.As(err, &ve):
		code := CodeInvalidRequest
		if ve.Field == "to" {
			code = CodeInvalidPhoneNumber
		}
		writeFieldProblem(w, r, code, ve.Reason,
			[]fieldError{{Field: "/" + publicFieldName(ve.Field), Detail: ve.Reason}})

	case errors.Is(err, slots.ErrHorizonExceeded):
		var he *slots.HorizonError
		errors.As(err, &he)
		s.log.Error("slot horizon exceeded",
			"alert", true,
			"request_id", requestIDFrom(r.Context()),
			"sender", sender.Name,
			"session_id", sender.SessionID,
			"depth", he.Depth.String(),
			"horizon", he.Horizon.String())
		w.Header().Set("Retry-After", "60")
		writeProblem(w, r, CodeQueueHorizonExceeded,
			"Sender "+quoteOrEmpty(sender.Name)+" has a backlog of "+he.Depth.Round(time.Second).String()+
				", past the "+he.Horizon.String()+" limit. A message accepted now would go out too late to be useful, so it was refused rather than queued. This is alerting the platform team. Retry in 60 seconds.")

	default:
		s.log.Error("enqueue failed",
			"request_id", requestIDFrom(r.Context()),
			"sender", sender.Name,
			"session_id", sender.SessionID,
			"error", err)
		writeProblem(w, r, CodeInternalError,
			"The message could not be queued. It is safe to retry. If this persists, report request id "+requestIDFrom(r.Context())+".")
	}
}

func publicFieldName(field string) string {
	switch field {
	case "source_ref":
		return "reference"
	case "idempotency_key":
		return "header/Idempotency-Key"
	}
	return field
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
