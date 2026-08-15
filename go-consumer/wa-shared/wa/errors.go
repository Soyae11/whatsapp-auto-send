package wa

import (
	"errors"
	"fmt"
	"net/http"
)

var (
	ErrUnauthorized = errors.New("wa: unauthorized")
	ErrNotFound = errors.New("wa: not found")
	ErrSessionNotFound = errors.New("wa: session not found")
	ErrInvalidPayload = errors.New("wa: invalid payload")
	ErrPayloadTooLarge = errors.New("wa: payload too large")
	ErrUnsupportedMediaType = errors.New("wa: unsupported media type")
	ErrSessionNotConnected = errors.New("wa: session not connected")
	ErrSessionLoggedOut = errors.New("wa: session logged out")
	ErrSendInProgress = errors.New("wa: send in progress")
	ErrNotOnWhatsApp = errors.New("wa: recipient not on whatsapp")
	ErrRateLimited = errors.New("wa: rate limited by whatsapp")
	ErrSendFailed = errors.New("wa: send failed")
	ErrPairingFailed = errors.New("wa: pairing failed")
	ErrUpstreamTimeout = errors.New("wa: upstream timeout")
	ErrInternal = errors.New("wa: internal error")
	ErrUnexpectedResponse = errors.New("wa: unexpected response")
)

const (
	CodeUnauthorized         = "unauthorized"
	CodeNotFound             = "not_found"
	CodeSessionNotFound      = "session_not_found"
	CodeInvalidPayload       = "invalid_payload"
	CodePayloadTooLarge      = "payload_too_large"
	CodeUnsupportedMediaType = "unsupported_media_type"
	CodeSessionNotConnected  = "session_not_connected"
	CodeSessionLoggedOut     = "session_logged_out"
	CodeSendInProgress       = "send_in_progress"
	CodeNotOnWhatsApp        = "not_on_whatsapp"
	CodeRateLimitedByWA      = "rate_limited_by_wa"
	CodeSendFailed           = "send_failed"
	CodePairingFailed        = "pairing_failed"
	CodeUpstreamTimeout      = "upstream_timeout"
	CodeInternalError        = "internal_error"
)

var sentinelByCode = map[string]error{
	CodeUnauthorized:         ErrUnauthorized,
	CodeNotFound:             ErrNotFound,
	CodeSessionNotFound:      ErrSessionNotFound,
	CodeInvalidPayload:       ErrInvalidPayload,
	CodePayloadTooLarge:      ErrPayloadTooLarge,
	CodeUnsupportedMediaType: ErrUnsupportedMediaType,
	CodeSessionNotConnected:  ErrSessionNotConnected,
	CodeSessionLoggedOut:     ErrSessionLoggedOut,
	CodeSendInProgress:       ErrSendInProgress,
	CodeNotOnWhatsApp:        ErrNotOnWhatsApp,
	CodeRateLimitedByWA:      ErrRateLimited,
	CodeSendFailed:           ErrSendFailed,
	CodePairingFailed:        ErrPairingFailed,
	CodeUpstreamTimeout:      ErrUpstreamTimeout,
	CodeInternalError:        ErrInternal,
}

type APIError struct {
	Code      string
	Message   string
	Retryable bool
	Status    int

	sentinel error
}

func (e *APIError) Error() string {
	return fmt.Sprintf("wa-gateway: %s (http %d, retryable=%t): %s",
		e.Code, e.Status, e.Retryable, e.Message)
}

func (e *APIError) Unwrap() error { return e.sentinel }

type errorBody struct {
	ErrorCode string `json:"error_code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func newAPIError(status int, b errorBody) *APIError {
	sentinel, known := sentinelByCode[b.ErrorCode]
	if !known {
		sentinel = ErrUnexpectedResponse
	}
	return &APIError{
		Code:      b.ErrorCode,
		Message:   b.Message,
		Retryable: b.Retryable,
		Status:    status,
		sentinel:  sentinel,
	}
}

func unexpectedResponse(status int, body string) *APIError {
	return &APIError{
		Code:      "",
		Message:   fmt.Sprintf("undecodable error response: %s", truncate(body, 512)),
		Retryable: status >= 500 || status == http.StatusTooManyRequests,
		Status:    status,
		sentinel:  ErrUnexpectedResponse,
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func IsRetryable(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable
	}
	return err != nil
}

func ErrorCode(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return ""
}

// FaultsSession reports whether a failure indicts the session that carried it, rather than the
// request it was carrying. The distinction drives three separate decisions — whether to count the
// failure against a session's circuit breaker, whether to fail a sender's pool over to another
// session, and whether a rejection receipt should rotate a pool — so it is defined once here
// instead of once per caller, where the copies would drift and a session could end up disqualified
// by one rule while another still considered it healthy.
//
// Only failures that would repeat identically on any session are the request's fault: a recipient
// who is not on WhatsApp, a payload WhatsApp will not accept. Everything else, including an
// unrecognised error, counts against the session — an unexplained failure is the session's problem
// until something says otherwise, which is the safe direction to be wrong in.
func FaultsSession(err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, ErrNotOnWhatsApp),
		errors.Is(err, ErrInvalidPayload),
		errors.Is(err, ErrPayloadTooLarge),
		errors.Is(err, ErrUnsupportedMediaType):
		return false
	default:
		return true
	}
}

// FaultsSessionCode is FaultsSession for callers holding only a wire error code — a rejection
// receipt, say, where the original error object is long gone. An unknown or empty code takes the
// same "assume the session" default FaultsSession gives an unrecognised error.
func FaultsSessionCode(code string) bool {
	if sentinel, known := sentinelByCode[code]; known {
		return FaultsSession(sentinel)
	}
	return true
}
