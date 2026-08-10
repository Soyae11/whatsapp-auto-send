package api

import (
	"encoding/json"
	"net/http"
	"strings"
)

const (
	CodeInvalidRequest        = "invalid_request"
	CodeInvalidPhoneNumber    = "invalid_phone_number"
	CodeUnknownSender         = "unknown_sender"
	CodeIdempotencyKeyReq     = "idempotency_key_required"
	CodeBatchTooLarge         = "batch_too_large"
	CodeSenderNotPermitted    = "sender_not_permitted"
	CodeUnauthenticated       = "unauthenticated"
	CodeNotFound              = "not_found"
	CodeMethodNotAllowed      = "method_not_allowed"
	CodeMessageNotFound       = "message_not_found"
	CodeMessageNotCancellable = "message_not_cancellable"
	CodeIdempotencyConflict   = "idempotency_key_reused_with_different_payload"
	CodePayloadTooLarge       = "payload_too_large"
	CodeUnsupportedMedia      = "unsupported_media_type"
	CodeRateLimited           = "rate_limited"
	CodeQueueHorizonExceeded  = "queue_horizon_exceeded"
	CodeSenderUnavailable     = "sender_unavailable"
	CodeSenderPoolExhausted   = "sender_pool_exhausted"
	CodeInternalError         = "internal_error"
)

const problemTypeBase = "https://wa.internal/errors/"

const problemContentType = "application/problem+json; charset=utf-8"

type problemKind struct {
	status    int
	title     string
	retryable bool
}

var problemKinds = map[string]problemKind{
	CodeInvalidRequest:        {http.StatusBadRequest, "Invalid request", false},
	CodeInvalidPhoneNumber:    {http.StatusBadRequest, "Invalid phone number", false},
	CodeUnknownSender:         {http.StatusBadRequest, "Unknown sender", false},
	CodeIdempotencyKeyReq:     {http.StatusBadRequest, "Idempotency key required", false},
	CodeBatchTooLarge:         {http.StatusBadRequest, "Batch too large", false},
	CodeSenderNotPermitted:    {http.StatusForbidden, "Sender not permitted for this key", false},
	CodeUnauthenticated:       {http.StatusUnauthorized, "Not authenticated", false},
	CodeNotFound:              {http.StatusNotFound, "Not found", false},
	CodeMethodNotAllowed:      {http.StatusMethodNotAllowed, "Method not allowed", false},
	CodeMessageNotFound:       {http.StatusNotFound, "Message not found", false},
	CodeMessageNotCancellable: {http.StatusConflict, "Message can no longer be cancelled", false},
	CodeIdempotencyConflict:   {http.StatusConflict, "Idempotency key reused with a different payload", false},
	CodePayloadTooLarge:       {http.StatusRequestEntityTooLarge, "Payload too large", false},
	CodeUnsupportedMedia:      {http.StatusUnsupportedMediaType, "Unsupported media type", false},
	CodeRateLimited:           {http.StatusTooManyRequests, "Rate limit exceeded", true},
	CodeQueueHorizonExceeded:  {http.StatusServiceUnavailable, "Sender backlog too deep", true},
	CodeSenderUnavailable:     {http.StatusServiceUnavailable, "Sender temporarily unavailable", true},
	CodeSenderPoolExhausted:   {http.StatusServiceUnavailable, "Sender pool exhausted", true},
	CodeInternalError:         {http.StatusInternalServerError, "Internal error", true},
}

type fieldError struct {
	Field  string `json:"field"`
	Detail string `json:"detail"`
}

type problem struct {
	Type      string       `json:"type"`
	Title     string       `json:"title"`
	Status    int          `json:"status"`
	Code      string       `json:"code"`
	Detail    string       `json:"detail"`
	RequestID string       `json:"request_id"`
	Retryable bool         `json:"retryable"`
	Errors    []fieldError `json:"errors,omitempty"`
}

func problemTypeURI(code string) string {
	return problemTypeBase + strings.ReplaceAll(code, "_", "-")
}

func newProblem(r *http.Request, code, detail string) problem {
	kind, ok := problemKinds[code]
	if !ok {
		code = CodeInternalError
		kind = problemKinds[CodeInternalError]
	}
	return problem{
		Type:      problemTypeURI(code),
		Title:     kind.title,
		Status:    kind.status,
		Code:      code,
		Detail:    detail,
		RequestID: requestIDFrom(r.Context()),
		Retryable: kind.retryable,
	}
}

func writeProblem(w http.ResponseWriter, r *http.Request, code, detail string) {
	writeProblemBody(w, newProblem(r, code, detail))
}

func writeFieldProblem(w http.ResponseWriter, r *http.Request, code, detail string, fields []fieldError) {
	p := newProblem(r, code, detail)
	p.Errors = fields
	writeProblemBody(w, p)
}

func writeProblemBody(w http.ResponseWriter, p problem) {
	w.Header().Set("Content-Type", problemContentType)
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}
