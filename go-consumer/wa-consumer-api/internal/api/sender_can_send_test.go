package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"wa-shared/senders"
	"wa-shared/wa"
)

// fakeLoggedOutSessions always reports a sender's session as logged out, so senderCanSend is
// forced down its pool-rotation branch regardless of sender mode.
type fakeLoggedOutSessions struct{}

func (fakeLoggedOutSessions) Status(ctx context.Context, sessionID string) (wa.SessionStatus, error) {
	return wa.StatusLoggedOut, nil
}

func TestSenderCanSend_SingleMode_NeverRotates(t *testing.T) {
	pools := &fakePools{}
	s := &Server{pools: pools, sessions: fakeLoggedOutSessions{}, log: testLogger()}

	sender := senders.Sender{Name: "hr-notifications", Mode: senders.ModeSingle, SessionID: "static-session"}
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	w := httptest.NewRecorder()

	if ok := s.senderCanSend(w, r, &sender); ok {
		t.Fatal("senderCanSend = true for a logged-out sender, want false")
	}
	if pools.rotateCalled {
		t.Error("Rotate() was called for a single-mode sender; it must never be consulted")
	}
}

func TestSenderCanSend_PoolMode_AttemptsRotation(t *testing.T) {
	pools := &fakePools{}
	s := &Server{pools: pools, sessions: fakeLoggedOutSessions{}, log: testLogger()}

	sender := senders.Sender{Name: "team-b", Mode: senders.ModePool, SessionID: "s1"}
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	w := httptest.NewRecorder()

	s.senderCanSend(w, r, &sender)
	if !pools.rotateCalled {
		t.Fatal("Rotate() was not called for a pool-mode, logged-out sender")
	}
}
