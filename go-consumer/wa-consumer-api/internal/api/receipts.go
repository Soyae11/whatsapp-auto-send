package api

// Inbound receipts from wa-gateway. This is the only source of `delivered` and `read`, and the
// only route on this service authenticated by an HMAC signature rather than by a key — see
// ARCHITECTURE.md on the four surfaces. For notifications going out to consumers, see
// notifications.go.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"wa-shared/notify"
	"wa-shared/store"
	"wa-shared/wa"
)

// Receipts is the half of the gateway contract this service consumes: wa-gateway posts
// message.status events, and they are the only source of delivered, read, and a post-sent
// failure.
type Receipts interface {
	ApplyReceipt(ctx context.Context, waMessageID string, kind store.Receipt, at time.Time, errorCode string) (bool, *store.Row, error)
}

type Emitter interface {
	EmitForRow(ctx context.Context, row store.Row)
}

// gatewayEvent is the envelope wa-gateway posts. Its optional fields are absent keys rather
// than nulls, which is the opposite convention from its REST bodies, so they are pointers.
type gatewayEvent struct {
	Type      string  `json:"type"`
	SessionID string  `json:"sessionId"`
	MessageID string  `json:"messageId"`
	Status    string  `json:"status"`
	Timestamp *int64  `json:"timestamp"`
	At        *string `json:"at"`
	ErrorCode *string `json:"errorCode"`
}

const gatewayStatusEvent = "message.status"

// handleGatewayEvent takes wa-gateway's fire-and-forget receipts and turns the ones that matter
// into public status transitions.
//
// It answers 200 to anything well-formed and authentic, including events it ignores. The
// gateway does not retry, so a non-2xx buys nothing and only fills its logs.
func (s *Server) handleGatewayEvent(w http.ResponseWriter, r *http.Request) {
	body, ok := s.readGatewayBody(w, r)
	if !ok {
		return
	}

	var ev gatewayEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		writeError(w, http.StatusBadRequest, errorBody{
			ErrorCode: "invalid_payload",
			Message:   "could not read the event body as JSON",
		})
		return
	}

	if ev.Type != gatewayStatusEvent || ev.MessageID == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ignored": true})
		return
	}

	kind, wanted := receiptFor(ev.Status)
	if !wanted {
		writeJSON(w, http.StatusOK, map[string]any{"ignored": true, "status": ev.Status})
		return
	}

	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	errorCode := ""
	if ev.ErrorCode != nil {
		errorCode = *ev.ErrorCode
	}

	changed, row, err := s.receipts.ApplyReceipt(ctx, ev.MessageID, kind, receiptTime(ev), errorCode)
	if errors.Is(err, store.ErrNotFound) {
		// A receipt for a message this dispatcher never sent. Normal when a session is
		// shared, and nothing to do about it.
		writeJSON(w, http.StatusOK, map[string]any{"ignored": true, "reason": "unknown message"})
		return
	}
	if err != nil {
		s.log.Error("could not apply a receipt",
			"wa_message_id", ev.MessageID, "receipt", kind, "error", err)
		writeError(w, http.StatusInternalServerError, errorBody{
			ErrorCode: "internal_error",
			Message:   "could not apply receipt",
			Retryable: true,
		})
		return
	}

	if changed && s.emitter != nil {
		s.emitter.EmitForRow(ctx, *row)
	}

	// A rejection is exactly the case `sent` was a lie about — it may mean the session that sent
	// this message just failed, so its pool (if it has one) rotates. Only if the rejection is the
	// session's fault, though; rotatePoolAfterFailure weighs errorCode for that. No resend: the
	// text isn't kept anywhere by the time a receipt arrives, only the pool moves for future
	// sends. See the dispatcher's synchronous path for the one case that does resend.
	if changed && kind == store.ReceiptFailed && row.Sender != "" {
		s.rotatePoolAfterFailure(ctx, row.Sender, row.SessionID, errorCode)
	}

	s.log.Info("receipt applied",
		"wa_message_id", ev.MessageID,
		"receipt", kind,
		"message_id", row.PublicID,
		"changed", changed)
	writeJSON(w, http.StatusOK, map[string]any{"applied": changed})
}

// receiptFor maps wa-gateway's Baileys-flavoured statuses onto the transitions consumers are
// told about. `server_ack` is deliberately ignored: it means WhatsApp's server took the
// message, which `sent` already covers. `error` is not ignored — it means the server took the
// message and then rejected it, which is the one case `sent` is a lie about.
func receiptFor(status string) (store.Receipt, bool) {
	switch status {
	case "delivery_ack":
		return store.ReceiptDelivered, true
	case "read", "played":
		return store.ReceiptRead, true
	case "error":
		return store.ReceiptFailed, true
	}
	return "", false
}

func receiptTime(ev gatewayEvent) time.Time {
	if ev.At != nil {
		if t, err := time.Parse(time.RFC3339, *ev.At); err == nil {
			return t.UTC()
		}
	}
	if ev.Timestamp != nil {
		return time.Unix(*ev.Timestamp, 0).UTC()
	}
	return time.Now().UTC()
}

// readGatewayBody verifies wa-gateway's own signature over the raw bytes before anything else
// looks at them. Without this the endpoint would let anyone who can reach it mark any message
// delivered.
func (s *Server) readGatewayBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if s.gatewaySecret == "" {
		writeError(w, http.StatusNotImplemented, errorBody{
			ErrorCode: "gateway_webhook_not_configured",
			Message:   "set WA_GATEWAY_WEBHOOK_SECRET to accept receipts from wa-gateway",
		})
		return nil, false
	}

	body, err := readAll(w, r, maxBodyBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, errorBody{
			ErrorCode: "invalid_payload",
			Message:   "could not read the event body",
		})
		return nil, false
	}

	raw := r.Header.Get("x-baileys-timestamp")
	millis, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		writeError(w, http.StatusUnauthorized, errorBody{
			ErrorCode: "unauthorized",
			Message:   "x-baileys-timestamp is missing or unreadable",
		})
		return nil, false
	}

	// wa-gateway signs the timestamp header verbatim in milliseconds, so the string it sent
	// is what has to go into the MAC — not a re-rendered version of it.
	if !wa.VerifyReceipt(s.gatewaySecret, r.Header.Get("x-baileys-signature"), raw, body) {
		s.log.Warn("rejected an unsigned or mis-signed gateway event",
			"request_id", requestIDFrom(r.Context()), "remote", r.RemoteAddr)
		writeError(w, http.StatusUnauthorized, errorBody{
			ErrorCode: "unauthorized",
			Message:   "x-baileys-signature did not match",
		})
		return nil, false
	}

	if age := time.Since(time.UnixMilli(millis)); age > notify.MaxSkew || age < -notify.MaxSkew {
		writeError(w, http.StatusUnauthorized, errorBody{
			ErrorCode: "unauthorized",
			Message:   "x-baileys-timestamp is outside the accepted window",
		})
		return nil, false
	}

	return body, true
}
