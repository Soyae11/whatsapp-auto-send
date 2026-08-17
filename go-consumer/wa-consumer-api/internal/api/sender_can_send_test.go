package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"wa-shared/senders"
	"wa-shared/store"
	"wa-shared/wa"
)

// fakeLoggedOutSessions always reports a sender's session as logged out, so senderCanSend is
// forced down its pool-rotation branch regardless of sender mode.
type fakeLoggedOutSessions struct{}

func (fakeLoggedOutSessions) Status(ctx context.Context, sessionID string) (wa.SessionStatus, error) {
	return wa.StatusLoggedOut, nil
}

// fakeSessions reports a status per session, so a pool can hold a mix of live and dead members.
// Anything unnamed is connected.
type fakeSessions map[string]wa.SessionStatus

func (f fakeSessions) Status(ctx context.Context, sessionID string) (wa.SessionStatus, error) {
	if status, ok := f[sessionID]; ok {
		return status, nil
	}
	return wa.StatusConnected, nil
}

// slowSessions answers like fakeSessions, but only after delay — a wa-gateway degraded enough
// that a probe walk can run out of budget. A call cut short returns the context's own error,
// exactly as the real client does.
type slowSessions struct {
	delay    time.Duration
	statuses map[string]wa.SessionStatus
}

func (f slowSessions) Status(ctx context.Context, sessionID string) (wa.SessionStatus, error) {
	return answerAfter(ctx, f.delay, f.statuses, sessionID)
}

// perSessionDelaySessions gives each session its own latency, so probes finish in an order that
// has nothing to do with rank. Unnamed sessions answer at once.
type perSessionDelaySessions struct {
	delays   map[string]time.Duration
	statuses map[string]wa.SessionStatus
}

func (f perSessionDelaySessions) Status(ctx context.Context, sessionID string) (wa.SessionStatus, error) {
	return answerAfter(ctx, f.delays[sessionID], f.statuses, sessionID)
}

// countingSessions records how many probes were actually made, for asserting that a member the
// store already ruled out is never asked about.
type countingSessions struct {
	fakeSessions
	calls atomic.Int64
}

func (f *countingSessions) Status(ctx context.Context, sessionID string) (wa.SessionStatus, error) {
	f.calls.Add(1)
	return f.fakeSessions.Status(ctx, sessionID)
}

func answerAfter(ctx context.Context, delay time.Duration, statuses map[string]wa.SessionStatus, sessionID string) (wa.SessionStatus, error) {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
		if status, ok := statuses[sessionID]; ok {
			return status, nil
		}
		return wa.StatusConnected, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestSenderCanSend_SingleMode_NeverRotates(t *testing.T) {
	pools := &fakePools{}
	s := &Server{pools: pools, sessions: fakeLoggedOutSessions{}, log: testLogger()}

	sender := senders.Sender{Name: "hr-notifications", Mode: senders.ModeSingle, SessionID: "static-session"}
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	w := httptest.NewRecorder()

	// A single-mode sender has no pool, so resolveSendSession hands down no members.
	if ok := s.senderCanSend(w, r, &sender, nil, nil); ok {
		t.Fatal("senderCanSend = true for a logged-out sender, want false")
	}
	if pools.handoverCalled || pools.rotateCalled {
		t.Error("the pool was moved for a single-mode sender; it must never be consulted")
	}
}

// A pool-mode sender whose main is logged out rotates: the crown moves to a connected member and
// the send goes out through it.
func TestSenderCanSend_PoolMode_AttemptsRotation(t *testing.T) {
	pools := &fakePools{
		members: map[string][]store.PoolMember{
			"team-b": {{Sender: "team-b", SessionID: "s1", IsMain: true}, {Sender: "team-b", SessionID: "s2"}},
		},
	}
	sessions := fakeSessions{"s1": wa.StatusLoggedOut}
	s := &Server{pools: pools, sessions: sessions, log: testLogger()}

	sender := senders.Sender{Name: "team-b", Mode: senders.ModePool, SessionID: "s1"}
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	w := httptest.NewRecorder()

	if ok := s.senderCanSend(w, r, &sender, pools.members["team-b"], nil); !ok {
		t.Fatal("senderCanSend = false, want true; s2 was connected")
	}
	if !pools.handoverCalled {
		t.Error("main was never handed over for a pool-mode, logged-out sender")
	}
	if pools.handoverArgs.sender != "team-b" || pools.handoverArgs.newMainSessionID != "s2" {
		t.Errorf("Handover called with %+v, want sender=team-b newMainSessionID=s2", pools.handoverArgs)
	}
}

// senderCanSend must not re-read the pool: resolveSendSession read it microseconds earlier in the
// same request and hands it down. Reading again is a query per send that was already paid for, and
// a concurrent handover between the two reads would leave the rotation deciding against a snapshot
// that no longer matches the session it was given.
func TestSenderCanSend_PoolMode_DoesNotReReadThePool(t *testing.T) {
	pools := &fakePools{
		members: map[string][]store.PoolMember{
			"team-b": {{Sender: "team-b", SessionID: "s1", IsMain: true}, {Sender: "team-b", SessionID: "s2"}},
		},
	}
	sessions := fakeSessions{"s1": wa.StatusLoggedOut}
	s := &Server{pools: pools, sessions: sessions, log: testLogger()}

	sender := senders.Sender{Name: "team-b", Mode: senders.ModePool, SessionID: "s1"}
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	w := httptest.NewRecorder()

	s.senderCanSend(w, r, &sender, pools.members["team-b"], nil)
	if pools.poolCalled {
		t.Error("Pool() was read again inside senderCanSend; the caller already passed the members down")
	}
}

// The pool store knows about disqualified and circuit-open members, not logged-out ones: a member
// that was never asked to send is neither. Rotating onto one and answering 202 accepts a send
// into exactly the hole this refusal exists to close.
func TestSenderCanSend_PoolMode_RefusesWhenEveryMemberIsLoggedOut(t *testing.T) {
	pools := &fakePools{
		members: map[string][]store.PoolMember{
			"team-b": {{Sender: "team-b", SessionID: "s1", IsMain: true}, {Sender: "team-b", SessionID: "s2"}},
		},
	}
	s := &Server{pools: pools, sessions: fakeLoggedOutSessions{}, log: testLogger()}

	sender := senders.Sender{Name: "team-b", Mode: senders.ModePool, SessionID: "s1"}
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	w := httptest.NewRecorder()

	if ok := s.senderCanSend(w, r, &sender, pools.members["team-b"], nil); ok {
		t.Fatalf("senderCanSend = true, want false; it rotated onto %q, which is logged out too", sender.SessionID)
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

// Rotation must still walk past a dead member to a live one rather than giving up at the first.
func TestSenderCanSend_PoolMode_RotatesPastLoggedOutMembers(t *testing.T) {
	pools := &fakePools{
		members: map[string][]store.PoolMember{
			"team-b": {
				{Sender: "team-b", SessionID: "s1", IsMain: true},
				{Sender: "team-b", SessionID: "s2"},
				{Sender: "team-b", SessionID: "s3"},
			},
		},
	}
	sessions := fakeSessions{"s1": wa.StatusLoggedOut, "s2": wa.StatusLoggedOut}
	s := &Server{pools: pools, sessions: sessions, log: testLogger()}

	sender := senders.Sender{Name: "team-b", Mode: senders.ModePool, SessionID: "s1"}
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	w := httptest.NewRecorder()

	if ok := s.senderCanSend(w, r, &sender, pools.members["team-b"], nil); !ok {
		t.Fatal("senderCanSend = false, want true; s3 was connected")
	}
	if sender.SessionID != "s3" {
		t.Errorf("rotated to %q, want s3 — the only member still logged in", sender.SessionID)
	}
}
