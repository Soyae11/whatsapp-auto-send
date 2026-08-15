package worker

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"wa-shared/dispatch"
	"wa-shared/store"
	"wa-shared/tasks"
	"wa-shared/wa"
)

// fakePools serves a fixed pool and records what Rotate was asked to do. Rotate's return is set
// per test rather than derived, because the two cases that matter here are precisely the two
// different meanings of its "" (see store.Rotate): "nothing could take over" and "the failed
// session was only a load-spread backup, so main is untouched".
type fakePools struct {
	members     []store.PoolMember
	rotateTo    string
	rotateCalls int
}

func (f *fakePools) Pool(context.Context, string) ([]store.PoolMember, error) {
	return f.members, nil
}

func (f *fakePools) Rotate(context.Context, string, string, func(string) bool) (string, error) {
	f.rotateCalls++
	return f.rotateTo, nil
}

// fakeEnqueuer records the resends failover asks for.
type fakeEnqueuer struct {
	sentTo []string
}

func (f *fakeEnqueuer) Enqueue(_ context.Context, req dispatch.SendRequest) (*dispatch.Result, error) {
	f.sentTo = append(f.sentTo, req.SessionID)
	return &dispatch.Result{}, nil
}

// stubRecorder returns a row from Get so announce has something to emit, which the Nop recorder
// does not do. Everything else is a no-op.
type stubRecorder struct{ NopRecorder }

func (stubRecorder) Get(context.Context, string) (*store.Row, error) {
	return &store.Row{APIKeyID: "key-1"}, nil
}

func testHandler(pools Pools, enq Enqueuer) *Handler {
	return &Handler{
		jobs:     stubRecorder{},
		circuit:  NopBreaker{},
		events:   NopEmitter{},
		pools:    pools,
		enqueuer: enq,
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func testPayload() tasks.SendMessagePayload {
	return tasks.SendMessagePayload{
		Sender:         "team-b",
		SessionID:      "s1",
		To:             "6281234567890",
		Text:           "hello",
		IdempotencyKey: "key-1",
	}
}

// A recipient who is not on WhatsApp fails identically on every session in the pool. Before this
// was gated, each such failure disqualified another member and resent the doomed text through it,
// so a single mistyped number could walk the whole pool to exhaustion.
func TestFailoverPooled_IgnoresFailureThatDoesNotIndictTheSession(t *testing.T) {
	pools := &fakePools{members: []store.PoolMember{
		{Sender: "team-b", SessionID: "s1", IsMain: true},
		{Sender: "team-b", SessionID: "s2"},
	}}
	enq := &fakeEnqueuer{}
	h := testHandler(pools, enq)

	v := Classify(wa.ErrNotOnWhatsApp)
	if v.TripsCircuit {
		t.Fatal("precondition: ErrNotOnWhatsApp must not trip the circuit")
	}

	handled := h.failoverPooled(context.Background(), h.log, store.Job{SessionID: "s1"},
		testPayload(), wa.CodeNotOnWhatsApp, v)

	if handled {
		t.Error("failoverPooled handled a failure that says nothing about the session; it must fall through to the ordinary path")
	}
	if pools.rotateCalls != 0 {
		t.Errorf("Rotate called %d times; a bad recipient must not cost the pool a member", pools.rotateCalls)
	}
	if len(enq.sentTo) != 0 {
		t.Errorf("resent to %v; a message WhatsApp rejected must not be retried on another session", enq.sentTo)
	}
}

// The ordinary case: main failed with something session-scoped, Rotate promoted a backup, and the
// message goes out again through it.
func TestFailoverPooled_ResendsViaPromotedBackup(t *testing.T) {
	pools := &fakePools{
		members: []store.PoolMember{
			{Sender: "team-b", SessionID: "s1", IsMain: true},
			{Sender: "team-b", SessionID: "s2"},
		},
		rotateTo: "s2",
	}
	enq := &fakeEnqueuer{}
	h := testHandler(pools, enq)

	handled := h.failoverPooled(context.Background(), h.log, store.Job{SessionID: "s1"},
		testPayload(), wa.CodeSessionLoggedOut, Classify(wa.ErrSessionLoggedOut))

	if !handled {
		t.Fatal("failoverPooled did not handle a session-scoped failure for a pooled sender")
	}
	if len(enq.sentTo) != 1 || enq.sentTo[0] != "s2" {
		t.Errorf("resent to %v, want exactly [s2]", enq.sentTo)
	}
}

// Load spreading routes some sends to a non-main member. When one of those fails, Rotate
// disqualifies it alone and returns "" — it has no promotion to report — while main is still
// perfectly healthy. The message must go out through main rather than be dropped.
func TestFailoverPooled_ResendsViaMainWhenLoadSpreadBackupFails(t *testing.T) {
	pools := &fakePools{
		members: []store.PoolMember{
			{Sender: "team-b", SessionID: "s1", IsMain: true},
			{Sender: "team-b", SessionID: "s2"},
		},
		rotateTo: "",
	}
	enq := &fakeEnqueuer{}
	h := testHandler(pools, enq)

	p := testPayload()
	p.SessionID = "s2" // load spreading picked the backup for this send

	handled := h.failoverPooled(context.Background(), h.log, store.Job{SessionID: "s2"},
		p, wa.CodeSessionNotConnected, Classify(wa.ErrSessionNotConnected))

	if !handled {
		t.Fatal("failoverPooled did not handle a session-scoped failure for a pooled sender")
	}
	if len(enq.sentTo) != 1 || enq.sentTo[0] != "s1" {
		t.Errorf("resent to %v, want exactly [s1] — main was healthy and the message must not be dropped", enq.sentTo)
	}
}

// The genuinely fatal case: Rotate found no one and nothing is left eligible. Exhaustion must
// stay exhaustion — the re-read must not invent a session that is disqualified or circuit-open.
func TestFailoverPooled_ExhaustedPoolDoesNotResend(t *testing.T) {
	pools := &fakePools{
		members: []store.PoolMember{
			{Sender: "team-b", SessionID: "s1", Disqualified: true},
			{Sender: "team-b", SessionID: "s2", Disqualified: true},
		},
		rotateTo: "",
	}
	enq := &fakeEnqueuer{}
	h := testHandler(pools, enq)

	handled := h.failoverPooled(context.Background(), h.log, store.Job{SessionID: "s1"},
		testPayload(), wa.CodeSessionLoggedOut, Classify(wa.ErrSessionLoggedOut))

	if !handled {
		t.Fatal("failoverPooled did not handle a session-scoped failure for a pooled sender")
	}
	if len(enq.sentTo) != 0 {
		t.Errorf("resent to %v; an exhausted pool has nowhere to send", enq.sentTo)
	}
}

// A sender with no pool at all is untouched by any of this, whatever the failure was.
func TestFailoverPooled_UnpooledSenderFallsThrough(t *testing.T) {
	pools := &fakePools{}
	enq := &fakeEnqueuer{}
	h := testHandler(pools, enq)

	handled := h.failoverPooled(context.Background(), h.log, store.Job{SessionID: "s1"},
		testPayload(), wa.CodeSessionLoggedOut, Classify(wa.ErrSessionLoggedOut))

	if handled {
		t.Error("failoverPooled handled a sender with no pool")
	}
	if pools.rotateCalls != 0 {
		t.Errorf("Rotate called %d times for a sender with no pool", pools.rotateCalls)
	}
}

// Every verdict that reaches failover must be one the ordinary path would also have counted
// against the circuit. This is the invariant the gate rests on: TripsCircuit is the single
// definition of "the session's fault", so failover and the breaker can never disagree.
func TestClassify_FailoverGateMatchesCircuitGate(t *testing.T) {
	sessionScoped := []error{
		wa.ErrSessionLoggedOut, wa.ErrSessionNotConnected, wa.ErrSessionNotFound,
		wa.ErrRateLimited, wa.ErrUnauthorized,
	}
	for _, err := range sessionScoped {
		if !Classify(err).TripsCircuit {
			t.Errorf("Classify(%v).TripsCircuit = false, want true — failover would skip a session-scoped failure", err)
		}
	}

	requestScoped := []error{
		wa.ErrNotOnWhatsApp, wa.ErrInvalidPayload, wa.ErrPayloadTooLarge, wa.ErrUnsupportedMediaType,
	}
	for _, err := range requestScoped {
		if Classify(err).TripsCircuit {
			t.Errorf("Classify(%v).TripsCircuit = true, want false — failover would burn a pool member on a bad request", err)
		}
	}
}
