package worker

import (
	"errors"
	"testing"

	"wa-shared/wa"
)

// TripsCircuit moved out of classify's table and into wa.FaultsSession so the receipt path in
// wa-consumer-api could reach the same verdict from a bare error code. This pins the values the
// table used to return, so that move cannot have quietly changed which failures pause a session.
func TestClassifyTripsCircuitMatchesTheOriginalTable(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{wa.ErrSessionLoggedOut, true},
		{wa.ErrNotOnWhatsApp, false},
		{wa.ErrInvalidPayload, false},
		{wa.ErrPayloadTooLarge, false},
		{wa.ErrUnsupportedMediaType, false},
		{wa.ErrUnauthorized, true},
		{wa.ErrSessionNotFound, true},
		{wa.ErrSessionNotConnected, true},
		{wa.ErrRateLimited, true},
		{wa.ErrSendFailed, true},
		{wa.ErrSendInProgress, true},
		{wa.ErrUpstreamTimeout, true},
		{wa.ErrInternal, true},

		// Fell through to the table's default before, and to FaultsSession's default now.
		{wa.ErrNotFound, true},
		{wa.ErrPairingFailed, true},
		{wa.ErrUnexpectedResponse, true},
		{errors.New("something nobody has seen before"), true},
	}

	for _, tc := range cases {
		if got := Classify(tc.err).TripsCircuit; got != tc.want {
			t.Errorf("Classify(%v).TripsCircuit = %v, want %v", tc.err, got, tc.want)
		}
	}
}

// The rest of the verdict is still the table's business, and must be unaffected by that move.
func TestClassifyKeepsBackoffAndRetryability(t *testing.T) {
	if v := Classify(wa.ErrRateLimited); !v.Retryable || v.Backoff != backoffRateLimited {
		t.Errorf("Classify(ErrRateLimited) = %+v, want retryable with the rate-limit backoff", v)
	}
	if v := Classify(wa.ErrSessionNotConnected); !v.Retryable || v.Backoff != backoffDisconnected {
		t.Errorf("Classify(ErrSessionNotConnected) = %+v, want retryable with the disconnected backoff", v)
	}
	if v := Classify(wa.ErrNotOnWhatsApp); v.Retryable {
		t.Errorf("Classify(ErrNotOnWhatsApp) = %+v, want not retryable", v)
	}
	if v := Classify(wa.ErrSessionLoggedOut); !v.Alert {
		t.Errorf("Classify(ErrSessionLoggedOut) = %+v, want an alert", v)
	}
}

// Every retryable verdict must also trip the circuit. failoverPooled's gate depends on it: a
// verdict that was retryable but did not trip would be handed back to the ordinary path and
// retried on the very session that just failed, with no pool rotation to rescue it.
func TestNoRetryableVerdictEscapesTheFailoverGate(t *testing.T) {
	for _, err := range []error{
		wa.ErrSessionLoggedOut, wa.ErrNotOnWhatsApp, wa.ErrInvalidPayload, wa.ErrPayloadTooLarge,
		wa.ErrUnsupportedMediaType, wa.ErrUnauthorized, wa.ErrSessionNotFound,
		wa.ErrSessionNotConnected, wa.ErrRateLimited, wa.ErrSendFailed, wa.ErrSendInProgress,
		wa.ErrUpstreamTimeout, wa.ErrInternal, wa.ErrNotFound, wa.ErrPairingFailed,
		wa.ErrUnexpectedResponse, errors.New("unknown"),
	} {
		if v := Classify(err); v.Retryable && !v.TripsCircuit {
			t.Errorf("Classify(%v) is retryable but does not trip the circuit", err)
		}
	}
}
