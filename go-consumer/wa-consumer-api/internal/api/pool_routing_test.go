package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"wa-shared/senders"
	"wa-shared/store"
	"wa-shared/wa"
)

// fakePools is a minimal, call-recording stand-in for the Pools interface so mode-gating logic
// can be tested without a real Postgres-backed store.Store.
type fakePools struct {
	members map[string][]store.PoolMember

	// rotateTo is what Rotate reports back, "" by default — which is both an exhausted pool and a
	// load-spread backup it disqualified without needing a promotion.
	rotateTo string

	poolCalled       bool
	poolCalls        int
	createPoolCalled bool
	rotateCalled     bool
	rotateArgs       struct{ sender, failedSessionID string }
	handoverCalled   bool
	handoverArgs     struct{ sender, newMainSessionID string }
}

func (f *fakePools) Pool(ctx context.Context, sender string) ([]store.PoolMember, error) {
	f.poolCalled = true
	f.poolCalls++
	return f.members[sender], nil
}

func (f *fakePools) CurrentMain(ctx context.Context, sender string) (string, bool, error) {
	members := f.members[sender]
	if len(members) == 0 {
		return "", false, nil
	}
	for _, m := range members {
		if m.IsMain {
			return m.SessionID, true, nil
		}
	}
	return "", true, nil
}

func (f *fakePools) CreatePool(ctx context.Context, sender string, sessionIDs []string) error {
	f.createPoolCalled = true
	return errors.New("not implemented")
}

func (f *fakePools) DeletePool(ctx context.Context, sender string) error {
	return errors.New("not implemented")
}

func (f *fakePools) Promote(ctx context.Context, sender, newMainSessionID string) error {
	return errors.New("not implemented")
}

// Handover mirrors store.Handover: the crown moves and nothing else changes — in particular the
// old main keeps whatever disqualified flag it already had, which is what keeps a status read from
// retiring a member.
func (f *fakePools) Handover(ctx context.Context, sender, newMainSessionID string) error {
	f.handoverCalled = true
	f.handoverArgs.sender = sender
	f.handoverArgs.newMainSessionID = newMainSessionID

	members := f.members[sender]
	for i, m := range members {
		switch {
		case m.SessionID == newMainSessionID:
			members[i].IsMain = true
		case m.IsMain:
			members[i].IsMain = false
		}
	}
	return nil
}

func (f *fakePools) Disqualify(ctx context.Context, sender, sessionID string) error {
	return errors.New("not implemented")
}

func (f *fakePools) Reinstate(ctx context.Context, sender, sessionID string) error {
	return errors.New("not implemented")
}

func (f *fakePools) AddMember(ctx context.Context, sender, sessionID string) error {
	return errors.New("not implemented")
}

func (f *fakePools) RemoveMember(ctx context.Context, sender, sessionID string) error {
	return errors.New("not implemented")
}

// Rotate records the call and reports rotateTo. The send path does not call it at all any more —
// rotateToConnectedMember hands over instead, precisely so a status read cannot disqualify anyone —
// so its only callers are the rotatePoolAfterFailure tests, and they assert which arguments it was
// given rather than what it did to the pool. Mirroring store.Rotate's promotion and disqualify
// loops here would be logic no test reaches, kept in sync with the real one by hand forever.
func (f *fakePools) Rotate(ctx context.Context, sender, failedSessionID string, isOpen func(sessionID string) bool) (string, error) {
	f.rotateCalled = true
	f.rotateArgs.sender = sender
	f.rotateArgs.failedSessionID = failedSessionID
	return f.rotateTo, nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testSenderCache wraps an already-built Registry as a Cache whose loader just returns the
// same Registry again — none of these tests call Refresh/Watch, so the loader is never
// actually invoked, but Cache requires one.
func testSenderCache(r *senders.Registry) *senders.Cache {
	return senders.NewCache(r, func(context.Context) (*senders.Registry, error) { return r, nil })
}

// fakeSenderStore is a minimal, in-memory stand-in for the SenderStore interface so ownership
// gating can be tested without a real Postgres-backed store.Store.
type fakeSenderStore struct {
	rows map[string]store.SenderRow
}

func (f *fakeSenderStore) ListAllSenders(ctx context.Context) ([]store.SenderRow, error) {
	out := make([]store.SenderRow, 0, len(f.rows))
	for _, row := range f.rows {
		out = append(out, row)
	}
	return out, nil
}

func (f *fakeSenderStore) ListSenders(ctx context.Context, ownerID string) ([]store.SenderRow, error) {
	var out []store.SenderRow
	for _, row := range f.rows {
		if row.OwnerID == ownerID {
			out = append(out, row)
		}
	}
	return out, nil
}

func (f *fakeSenderStore) CreateSender(ctx context.Context, in store.SenderInput) (*store.SenderRow, error) {
	if f.rows == nil {
		f.rows = map[string]store.SenderRow{}
	}
	if _, exists := f.rows[in.Name]; exists {
		return nil, store.ErrSenderExists
	}
	row := store.SenderRow{Name: in.Name, OwnerID: in.OwnerID, Mode: in.Mode, SessionID: in.SessionID}
	f.rows[in.Name] = row
	return &row, nil
}

func (f *fakeSenderStore) GetSender(ctx context.Context, name string) (*store.SenderRow, error) {
	row, ok := f.rows[name]
	if !ok {
		return nil, store.ErrSenderNotFound
	}
	return &row, nil
}

func (f *fakeSenderStore) DeleteSender(ctx context.Context, name, ownerID string) error {
	row, ok := f.rows[name]
	if !ok || row.OwnerID != ownerID {
		return store.ErrSenderNotFound
	}
	delete(f.rows, name)
	return nil
}

func TestResolveSendSession_SingleMode_NeverTouchesPool(t *testing.T) {
	pools := &fakePools{}
	s := &Server{pools: pools, log: testLogger()}

	sender := senders.Sender{Name: "hr-notifications", Mode: senders.ModeSingle, SessionID: "static-session"}
	got, _, err := s.resolveSendSession(context.Background(), sender, nil)
	if err != nil {
		t.Fatalf("resolveSendSession: %v", err)
	}
	if got.SessionID != "static-session" {
		t.Errorf("SessionID = %q, want unchanged %q", got.SessionID, "static-session")
	}
	if pools.poolCalled {
		t.Error("Pool() was called for a single-mode sender; it must never be consulted")
	}
}

func TestResolveSendSession_PoolMode_EmptyPoolIsMisconfigured(t *testing.T) {
	pools := &fakePools{members: map[string][]store.PoolMember{}}
	s := &Server{pools: pools, log: testLogger()}

	sender := senders.Sender{Name: "team-b", Mode: senders.ModePool}
	_, _, err := s.resolveSendSession(context.Background(), sender, nil)
	if !errors.Is(err, errSenderPoolMisconfigured) {
		t.Fatalf("err = %v, want errSenderPoolMisconfigured", err)
	}
}

func TestResolveSendSession_PoolMode_ExhaustedWhenNoMain(t *testing.T) {
	pools := &fakePools{members: map[string][]store.PoolMember{
		"team-b": {{Sender: "team-b", SessionID: "s1", Disqualified: true}},
	}}
	s := &Server{pools: pools, log: testLogger()}

	sender := senders.Sender{Name: "team-b", Mode: senders.ModePool}
	_, _, err := s.resolveSendSession(context.Background(), sender, nil)
	if !errors.Is(err, errSenderPoolExhausted) {
		t.Fatalf("err = %v, want errSenderPoolExhausted", err)
	}
}

// A failed main hands over to the backup that gets promoted in its place.
func TestRotateToConnectedMember_ReportsPromotion(t *testing.T) {
	pools := &fakePools{
		members: map[string][]store.PoolMember{
			"team-b": {{Sender: "team-b", SessionID: "s1", IsMain: true}, {Sender: "team-b", SessionID: "s2"}},
		},
	}
	s := &Server{pools: pools, sessions: fakeSessions{}, log: testLogger()}

	got, ok := s.rotateToConnectedMember(context.Background(), "team-b", "s1", pools.members["team-b"])
	if !ok || got != "s2" {
		t.Fatalf("rotateToConnectedMember = (%q, %v), want (s2, true)", got, ok)
	}
	if !pools.members["team-b"][1].IsMain {
		t.Error("s2 was routed to but never promoted; the next send would go back to the dead main")
	}
}

// Load spreading may have routed this send to a backup. If that backup is logged out, Rotate
// disqualifies it and leaves main standing — so there is still somewhere to send, and the request
// must not be refused with 503.
func TestRotateToConnectedMember_FallsBackToMainWhenBackupFails(t *testing.T) {
	pools := &fakePools{
		members: map[string][]store.PoolMember{
			"team-b": {{Sender: "team-b", SessionID: "s1", IsMain: true}, {Sender: "team-b", SessionID: "s2"}},
		},
	}
	s := &Server{pools: pools, sessions: fakeSessions{}, log: testLogger()}

	got, ok := s.rotateToConnectedMember(context.Background(), "team-b", "s2", pools.members["team-b"])
	if !ok || got != "s1" {
		t.Fatalf("rotateToConnectedMember = (%q, %v), want (s1, true) — main was healthy", got, ok)
	}
	if !pools.members["team-b"][0].IsMain {
		t.Error("main was dethroned by a backup's failure")
	}
}

// An exhausted pool must stay exhausted: nothing here may resurrect a disqualified member.
func TestRotateToConnectedMember_ExhaustedPool(t *testing.T) {
	pools := &fakePools{
		members: map[string][]store.PoolMember{
			"team-b": {
				{Sender: "team-b", SessionID: "s1", Disqualified: true},
				{Sender: "team-b", SessionID: "s2", Disqualified: true},
			},
		},
	}
	s := &Server{pools: pools, sessions: fakeSessions{}, log: testLogger()}

	if got, ok := s.rotateToConnectedMember(context.Background(), "team-b", "s1", pools.members["team-b"]); ok {
		t.Errorf("rotateToConnectedMember = (%q, true), want ok=false for an exhausted pool", got)
	}
}

// Being logged out is not grounds for retiring anyone, the session this send was routed to
// included: disqualified is permanent until an operator calls Reinstate, and a status read taken
// while wa-gateway is restarting — every session reading logged_out for a few seconds — is far too
// weak to spend it on. The crown moves; nothing is retired.
func TestRotateToConnectedMember_DoesNotDisqualifyMembersThatAreMerelyLoggedOut(t *testing.T) {
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

	got, ok := s.rotateToConnectedMember(context.Background(), "team-b", "s1", pools.members["team-b"])
	if !ok || got != "s3" {
		t.Fatalf("rotateToConnectedMember = (%q, %v), want (s3, true)", got, ok)
	}

	members := pools.members["team-b"]
	for _, m := range members {
		if m.Disqualified {
			t.Errorf("%s was disqualified on a status read; only a real serving failure may retire a member", m.SessionID)
		}
	}
	if members[0].IsMain {
		t.Error("s1 read logged out and kept main; the send would route back to it next time")
	}
	if !members[2].IsMain {
		t.Errorf("s3 = %+v, want the new main", members[2])
	}
}

// The failure mode this whole path exists to avoid: a gateway blip where every member reads
// logged_out must leave the pool exactly as it was. Dethroning main with nobody to hand over to
// empties the pool of mains, and resolveSendSession answers sender_pool_exhausted for every send
// after it — a permanent outage, needing a manual promotion, over sessions that came back on
// their own seconds later.
func TestRotateToConnectedMember_LeavesThePoolAloneWhenNothingIsConnected(t *testing.T) {
	pools := &fakePools{
		members: map[string][]store.PoolMember{
			"team-b": {
				{Sender: "team-b", SessionID: "s1", IsMain: true},
				{Sender: "team-b", SessionID: "s2"},
				{Sender: "team-b", SessionID: "s3"},
			},
		},
	}
	s := &Server{pools: pools, sessions: fakeLoggedOutSessions{}, log: testLogger()}

	if got, ok := s.rotateToConnectedMember(context.Background(), "team-b", "s1", pools.members["team-b"]); ok {
		t.Fatalf("rotateToConnectedMember = (%q, true), want ok=false; every member is logged out", got)
	}

	members := pools.members["team-b"]
	if !members[0].IsMain {
		t.Error("s1 was dethroned on a status read with nowhere to hand over to; the pool now has no main")
	}
	for _, m := range members {
		if m.Disqualified {
			t.Errorf("%s was disqualified during a gateway blip", m.SessionID)
		}
	}
}

// A logged-out load-spread backup is routed around, not retired: the same weak signal, and main
// still holds the crown.
func TestRotateToConnectedMember_DoesNotRetireALoggedOutBackup(t *testing.T) {
	pools := &fakePools{
		members: map[string][]store.PoolMember{
			"team-b": {{Sender: "team-b", SessionID: "s1", IsMain: true}, {Sender: "team-b", SessionID: "s2"}},
		},
	}
	sessions := fakeSessions{"s2": wa.StatusLoggedOut}
	s := &Server{pools: pools, sessions: sessions, log: testLogger()}

	got, ok := s.rotateToConnectedMember(context.Background(), "team-b", "s2", pools.members["team-b"])
	if !ok || got != "s1" {
		t.Fatalf("rotateToConnectedMember = (%q, %v), want (s1, true)", got, ok)
	}
	if pools.handoverCalled {
		t.Error("main changed hands over a backup's status read")
	}
	if pools.members["team-b"][1].Disqualified {
		t.Error("s2 was disqualified for reading logged out")
	}
}

// A probe budget that runs out is not evidence of anything. A Status call cut short by the budget
// fails knowing nothing about that member, and reading those failures as "usable" hands back a
// session that may well be logged out — the hole the probing exists to close. A walk that learned
// nothing refuses the send, and the caller's retry gets to probe again.
func TestFirstConnectedMember_SpentBudgetIsNotAnAnswer(t *testing.T) {
	members := []store.PoolMember{
		{Sender: "team-b", SessionID: "s1", IsMain: true},
		{Sender: "team-b", SessionID: "s2"},
		{Sender: "team-b", SessionID: "s3"},
	}
	sessions := slowSessions{delay: rotationProbeBudget * 2}
	s := &Server{sessions: sessions, log: testLogger()}

	if got, ok := s.firstConnectedMember(context.Background(), members, "s1", func(string) bool { return false }); ok {
		t.Fatalf("firstConnectedMember = (%q, true), want ok=false; no probe finished inside the budget", got)
	}
}

// The budget covers the whole walk, so the walk has to ask every member at once. Asked one at a
// time, a pool this size against a gateway this slow could never reach the member that is actually
// connected: six probes at 40% of the budget each is 2.4 budgets of waiting, so the send would be
// refused with s6 logged in and available the whole time.
func TestFirstConnectedMember_ProbesTheWholePoolWithinOneBudget(t *testing.T) {
	members := make([]store.PoolMember, 0, 6)
	statuses := map[string]wa.SessionStatus{}
	for _, id := range []string{"s1", "s2", "s3", "s4", "s5"} {
		members = append(members, store.PoolMember{Sender: "team-b", SessionID: id})
		statuses[id] = wa.StatusLoggedOut
	}
	members = append(members, store.PoolMember{Sender: "team-b", SessionID: "s6"})
	members[0].IsMain = true

	sessions := slowSessions{delay: rotationProbeBudget * 2 / 5, statuses: statuses}
	s := &Server{sessions: sessions, log: testLogger()}

	got, ok := s.firstConnectedMember(context.Background(), members, "", func(string) bool { return false })
	if !ok || got != "s6" {
		t.Fatalf("firstConnectedMember = (%q, %v), want (s6, true)", got, ok)
	}
}

// Concurrency must not change which member is chosen: the answer is still the lowest-rank
// connected one, however the probes happen to finish. s4 answers first here and s2 last.
func TestFirstConnectedMember_AnswersInRankOrderNotCompletionOrder(t *testing.T) {
	members := []store.PoolMember{
		{Sender: "team-b", SessionID: "s1", IsMain: true},
		{Sender: "team-b", SessionID: "s2"},
		{Sender: "team-b", SessionID: "s3"},
		{Sender: "team-b", SessionID: "s4"},
	}
	sessions := perSessionDelaySessions{
		delays: map[string]time.Duration{
			"s2": rotationProbeBudget / 2,
			"s3": rotationProbeBudget / 4,
			"s4": 0,
		},
		statuses: map[string]wa.SessionStatus{"s3": wa.StatusLoggedOut},
	}
	s := &Server{sessions: sessions, log: testLogger()}

	got, ok := s.firstConnectedMember(context.Background(), members, "s1", func(string) bool { return false })
	if !ok || got != "s2" {
		t.Fatalf("firstConnectedMember = (%q, %v), want (s2, true) — the lowest-rank connected member", got, ok)
	}
}

// A circuit-open member is not probed at all: the pool store already ruled it out, and asking
// wa-gateway about it would spend budget on an answer that cannot be used.
func TestFirstConnectedMember_SkipsMembersRuledOutByTheStore(t *testing.T) {
	members := []store.PoolMember{
		{Sender: "team-b", SessionID: "s1", IsMain: true},
		{Sender: "team-b", SessionID: "s2", Disqualified: true},
		{Sender: "team-b", SessionID: "s3"},
	}
	sessions := countingSessions{fakeSessions: fakeSessions{}}
	s := &Server{sessions: &sessions, log: testLogger()}

	got, ok := s.firstConnectedMember(context.Background(), members, "s1", func(id string) bool { return id == "s3" })
	if ok {
		t.Fatalf("firstConnectedMember = (%q, true), want ok=false; s2 is disqualified and s3 circuit-open", got)
	}
	if n := sessions.calls.Load(); n != 0 {
		t.Errorf("probed %d members, want 0 — none of them was eligible", n)
	}
}

func TestRotatePoolAfterFailure_SkipsSingleModeSender(t *testing.T) {
	registry, err := senders.Parse("hr-notifications:static-session", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	pools := &fakePools{}
	s := &Server{pools: pools, senders: testSenderCache(registry), log: testLogger()}

	s.rotatePoolAfterFailure(context.Background(), "hr-notifications", "static-session", wa.CodeSendFailed)
	if pools.rotateCalled {
		t.Error("Rotate() was called for a single-mode sender; it must never be consulted")
	}
}

func TestRotatePoolAfterFailure_CallsRotateForPoolModeSender(t *testing.T) {
	registry, err := senders.Parse("team-b:pool", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	pools := &fakePools{}
	s := &Server{pools: pools, senders: testSenderCache(registry), log: testLogger()}

	s.rotatePoolAfterFailure(context.Background(), "team-b", "s1", wa.CodeSendFailed)
	if !pools.rotateCalled {
		t.Fatal("Rotate() was not called for a pool-mode sender")
	}
	if pools.rotateArgs.sender != "team-b" || pools.rotateArgs.failedSessionID != "s1" {
		t.Errorf("Rotate called with %+v, want sender=team-b failedSessionID=s1", pools.rotateArgs)
	}
}

// A rejection receipt saying the recipient is not on WhatsApp is about that phone number, not the
// session that carried it. Rotating on one would cost the pool a healthy member per bad number.
func TestRotatePoolAfterFailure_SkipsRejectionsThatAreNotTheSessionsFault(t *testing.T) {
	registry, err := senders.Parse("team-b:pool", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	for _, code := range []string{
		wa.CodeNotOnWhatsApp,
		wa.CodeInvalidPayload,
		wa.CodePayloadTooLarge,
		wa.CodeUnsupportedMediaType,
	} {
		pools := &fakePools{}
		s := &Server{pools: pools, senders: testSenderCache(registry), log: testLogger()}

		s.rotatePoolAfterFailure(context.Background(), "team-b", "s1", code)
		if pools.rotateCalled {
			t.Errorf("Rotate() was called for a %q rejection; it says nothing about the session", code)
		}
	}
}

// The session-scoped reasons must still rotate, and so must an unrecognised or absent one — an
// unexplained failure is the session's problem until something says otherwise.
func TestRotatePoolAfterFailure_RotatesOnSessionFaults(t *testing.T) {
	registry, err := senders.Parse("team-b:pool", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	for _, code := range []string{
		wa.CodeSessionLoggedOut,
		wa.CodeSessionNotConnected,
		wa.CodeSessionNotFound,
		wa.CodeRateLimitedByWA,
		wa.CodeUnauthorized,
		wa.CodeSendFailed,
		wa.CodeMessageRejected,
		"some_code_added_later",
		"",
	} {
		pools := &fakePools{}
		s := &Server{pools: pools, senders: testSenderCache(registry), log: testLogger()}

		s.rotatePoolAfterFailure(context.Background(), "team-b", "s1", code)
		if !pools.rotateCalled {
			t.Errorf("Rotate() was not called for a %q rejection", code)
		}
	}
}

func TestRotatePoolAfterFailure_SkipsUnknownSender(t *testing.T) {
	registry, err := senders.Parse("team-b:pool", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	pools := &fakePools{}
	s := &Server{pools: pools, senders: testSenderCache(registry), log: testLogger()}

	s.rotatePoolAfterFailure(context.Background(), "unknown-sender", "s1", wa.CodeSendFailed)
	if pools.rotateCalled {
		t.Error("Rotate() was called for an unknown sender")
	}
}
