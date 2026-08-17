package wa

import "testing"

// The failures that are the request's fault: they would repeat identically on any session, so
// neither a circuit breaker nor a sender pool should hold them against the one that reported them.
var requestFaults = []struct {
	code string
	err  error
}{
	{CodeNotOnWhatsApp, ErrNotOnWhatsApp},
	{CodeInvalidPayload, ErrInvalidPayload},
	{CodePayloadTooLarge, ErrPayloadTooLarge},
	{CodeUnsupportedMediaType, ErrUnsupportedMediaType},
}

var sessionFaults = []struct {
	code string
	err  error
}{
	{CodeSessionLoggedOut, ErrSessionLoggedOut},
	{CodeSessionNotConnected, ErrSessionNotConnected},
	{CodeSessionNotFound, ErrSessionNotFound},
	{CodeRateLimitedByWA, ErrRateLimited},
	{CodeUnauthorized, ErrUnauthorized},
	{CodeSendFailed, ErrSendFailed},
	// WhatsApp took the message and then refused it, usually because the sending account is
	// restricted. That indicts the account, so a pooled sender must rotate off this session —
	// moving it to the request-fault list would silently stop rotation on a 463 rejection.
	{CodeMessageRejected, ErrMessageRejected},
	{CodeSendInProgress, ErrSendInProgress},
	{CodeUpstreamTimeout, ErrUpstreamTimeout},
	{CodeInternalError, ErrInternal},
}

func TestFaultsSession(t *testing.T) {
	for _, tc := range requestFaults {
		if FaultsSession(tc.err) {
			t.Errorf("FaultsSession(%v) = true, want false", tc.err)
		}
	}
	for _, tc := range sessionFaults {
		if !FaultsSession(tc.err) {
			t.Errorf("FaultsSession(%v) = false, want true", tc.err)
		}
	}
	if FaultsSession(nil) {
		t.Error("FaultsSession(nil) = true, want false")
	}
}

// An error arriving as an *APIError off the wire must classify the same as its bare sentinel —
// this is the shape callers actually hold.
func TestFaultsSessionThroughAPIError(t *testing.T) {
	for _, tc := range append(append([]struct {
		code string
		err  error
	}{}, requestFaults...), sessionFaults...) {
		apiErr := &APIError{Code: tc.code, sentinel: tc.err}
		if got, want := FaultsSession(apiErr), FaultsSession(tc.err); got != want {
			t.Errorf("FaultsSession(APIError{%s}) = %v, want %v (same as its sentinel)", tc.code, got, want)
		}
	}
}

// FaultsSessionCode exists so the receipt path, which only ever holds a wire code, reaches the
// same verdict as a caller holding the error. If these two ever disagree, a session can be
// disqualified by one rule while another still considers it healthy — the exact drift the shared
// predicate was written to prevent.
func TestFaultsSessionCodeAgreesWithFaultsSession(t *testing.T) {
	for code, sentinel := range sentinelByCode {
		if got, want := FaultsSessionCode(code), FaultsSession(sentinel); got != want {
			t.Errorf("FaultsSessionCode(%q) = %v, but FaultsSession(%v) = %v", code, got, sentinel, want)
		}
	}
}

// An unknown code is treated as the session's fault, matching FaultsSession's default for an
// unrecognised error. An absent code (a receipt that gave no reason) takes the same default.
func TestFaultsSessionCodeUnknownDefaultsToSession(t *testing.T) {
	for _, code := range []string{"", "some_code_added_later"} {
		if !FaultsSessionCode(code) {
			t.Errorf("FaultsSessionCode(%q) = false, want true", code)
		}
	}
}
