package api

import (
	"context"
	"testing"

	"wa-shared/senders"
	"wa-shared/store"
)

// A batch of a hundred messages to one sender is one question about that sender's pool, not a
// hundred. Asking per item is what turned a large batch into minutes of database work behind a
// sixty-second write timeout.
func TestSendMemo_ReadsThePoolOncePerSender(t *testing.T) {
	pools := &fakePools{
		members: map[string][]store.PoolMember{
			"team-b": {{Sender: "team-b", SessionID: "s1", IsMain: true}, {Sender: "team-b", SessionID: "s2"}},
		},
	}
	s := &Server{pools: pools, log: testLogger()}
	memo := newSendMemo()

	sender := senders.Sender{Name: "team-b", Mode: senders.ModePool}
	for range 50 {
		got, members, err := s.resolveSendSession(context.Background(), sender, memo)
		if err != nil {
			t.Fatalf("resolveSendSession: %v", err)
		}
		if got.SessionID != "s1" {
			t.Fatalf("SessionID = %q, want s1", got.SessionID)
		}
		if len(members) != 2 {
			t.Fatalf("members = %d, want 2 — the pool must come back with the sender", len(members))
		}
	}

	if pools.poolCalls != 1 {
		t.Errorf("Pool() read %d times for 50 items naming one sender, want 1", pools.poolCalls)
	}
}

// A nil memo is the single-send case: nothing is shared, and every call does its own work.
func TestSendMemo_NilCachesNothing(t *testing.T) {
	pools := &fakePools{
		members: map[string][]store.PoolMember{
			"team-b": {{Sender: "team-b", SessionID: "s1", IsMain: true}},
		},
	}
	s := &Server{pools: pools, log: testLogger()}

	sender := senders.Sender{Name: "team-b", Mode: senders.ModePool}
	for range 3 {
		if _, _, err := s.resolveSendSession(context.Background(), sender, nil); err != nil {
			t.Fatalf("resolveSendSession: %v", err)
		}
	}
	if pools.poolCalls != 3 {
		t.Errorf("Pool() read %d times without a memo, want 3", pools.poolCalls)
	}
}

// A pool that is misconfigured is misconfigured for every item, so the error is cached too rather
// than rediscovered with the same failing query a hundred times.
func TestSendMemo_CachesTheRoutingError(t *testing.T) {
	pools := &fakePools{members: map[string][]store.PoolMember{}}
	s := &Server{pools: pools, log: testLogger()}
	memo := newSendMemo()

	sender := senders.Sender{Name: "team-b", Mode: senders.ModePool}
	for range 5 {
		if _, _, err := s.resolveSendSession(context.Background(), sender, memo); err == nil {
			t.Fatal("resolveSendSession = nil error, want errSenderPoolMisconfigured")
		}
	}
	if pools.poolCalls != 1 {
		t.Errorf("Pool() read %d times for a pool already known to be misconfigured, want 1", pools.poolCalls)
	}
}

// The health verdict is keyed by session id because load spreading routes the items of one batch
// across several members. Keyed by sender, an item routed to s2 would be answered with whatever
// was decided about s1 — a member it never asked about.
func TestSendMemo_VerdictIsKeyedBySessionNotSender(t *testing.T) {
	memo := newSendMemo()

	asked := map[string]int{}
	decide := func(id string) func() senderVerdict {
		return func() senderVerdict {
			asked[id]++
			return senderVerdict{sessionID: id, ok: true}
		}
	}

	for range 10 {
		if got := memo.verdictFor("s1", decide("s1")); got.sessionID != "s1" {
			t.Fatalf("verdictFor(s1) = %+v", got)
		}
		if got := memo.verdictFor("s2", decide("s2")); got.sessionID != "s2" {
			t.Fatalf("verdictFor(s2) = %+v", got)
		}
	}

	if asked["s1"] != 1 || asked["s2"] != 1 {
		t.Errorf("decided s1 %d times and s2 %d times, want 1 each", asked["s1"], asked["s2"])
	}
}
