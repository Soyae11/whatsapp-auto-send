package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"wa-shared/senders"
	"wa-shared/store"
)

func TestCurrentSessionID_SingleMode_UsesStaticSessionID(t *testing.T) {
	s := &Server{pools: &fakePools{}}

	sender := senders.Sender{Name: "hr-notifications", Mode: senders.ModeSingle, SessionID: "static-session"}
	id, err := s.currentSessionID(context.Background(), sender)
	if err != nil {
		t.Fatalf("currentSessionID: %v", err)
	}
	if id != "static-session" {
		t.Errorf("id = %q, want %q", id, "static-session")
	}
}

func TestCurrentSessionID_PoolMode_NoPoolIsMisconfigured(t *testing.T) {
	s := &Server{pools: &fakePools{members: map[string][]store.PoolMember{}}}

	sender := senders.Sender{Name: "team-b", Mode: senders.ModePool}
	_, err := s.currentSessionID(context.Background(), sender)
	if !errors.Is(err, errSenderPoolMisconfigured) {
		t.Fatalf("err = %v, want errSenderPoolMisconfigured", err)
	}
}

func TestCurrentSessionID_PoolMode_NoMainIsExhausted(t *testing.T) {
	pools := &fakePools{members: map[string][]store.PoolMember{
		"team-b": {{Sender: "team-b", SessionID: "s1", Disqualified: true}},
	}}
	s := &Server{pools: pools}

	sender := senders.Sender{Name: "team-b", Mode: senders.ModePool}
	_, err := s.currentSessionID(context.Background(), sender)
	if !errors.Is(err, errSenderPoolExhausted) {
		t.Fatalf("err = %v, want errSenderPoolExhausted", err)
	}
}

func TestCurrentSessionID_PoolMode_ResolvesMain(t *testing.T) {
	pools := &fakePools{members: map[string][]store.PoolMember{
		"team-b": {{Sender: "team-b", SessionID: "s1", IsMain: true}},
	}}
	s := &Server{pools: pools}

	sender := senders.Sender{Name: "team-b", Mode: senders.ModePool}
	id, err := s.currentSessionID(context.Background(), sender)
	if err != nil {
		t.Fatalf("currentSessionID: %v", err)
	}
	if id != "s1" {
		t.Errorf("id = %q, want %q", id, "s1")
	}
}

// The following two tests deliberately leave s.enqueuer nil (it is a concrete *dispatch.Enqueuer
// with no interface seam and no nil-safe methods) to prove senderStatus's misconfigured/exhausted
// short-circuits return before ever touching it — the exact bug this change fixes was reads.go
// dereferencing an empty session id downstream of these checks.

func TestSenderStatus_PoolMode_NoPool_NeverTouchesEnqueuer(t *testing.T) {
	pools := &fakePools{members: map[string][]store.PoolMember{}}
	s := &Server{pools: pools, log: testLogger()}

	sender := senders.Sender{Name: "team-b", Mode: senders.ModePool}
	r := httptest.NewRequest("GET", "/v1/senders", nil)

	view := s.senderStatus(r, sender)
	if view.Health != healthUnavailable {
		t.Errorf("Health = %q, want %q", view.Health, healthUnavailable)
	}
	if view.Accepting {
		t.Error("Accepting = true, want false")
	}
	if view.Mode != senderModePool {
		t.Errorf("Mode = %q, want %q", view.Mode, senderModePool)
	}
}

func TestSenderStatus_PoolMode_Exhausted_NeverTouchesEnqueuer(t *testing.T) {
	pools := &fakePools{members: map[string][]store.PoolMember{
		"team-b": {{Sender: "team-b", SessionID: "s1", Disqualified: true}},
	}}
	s := &Server{pools: pools, log: testLogger()}

	sender := senders.Sender{Name: "team-b", Mode: senders.ModePool}
	r := httptest.NewRequest("GET", "/v1/senders", nil)

	view := s.senderStatus(r, sender)
	if view.Health != healthUnavailable {
		t.Errorf("Health = %q, want %q", view.Health, healthUnavailable)
	}
	if view.Accepting {
		t.Error("Accepting = true, want false")
	}
}

func TestSenderModeString(t *testing.T) {
	if got := senderModeString(senders.ModeSingle); got != senderModeSingle {
		t.Errorf("senderModeString(ModeSingle) = %q, want %q", got, senderModeSingle)
	}
	if got := senderModeString(senders.ModePool); got != senderModePool {
		t.Errorf("senderModeString(ModePool) = %q, want %q", got, senderModePool)
	}
}
