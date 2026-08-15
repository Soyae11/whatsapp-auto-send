package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"wa-shared/senders"
	"wa-shared/store"
	"wa-shared/wa"
)

// fakePools is a minimal, call-recording stand-in for the Pools interface so mode-gating logic
// can be tested without a real Postgres-backed store.Store.
type fakePools struct {
	members map[string][]store.PoolMember

	// rotateTo is what Rotate reports back. Left "" by default, which is both its old fixed
	// behaviour and the interesting case: Rotate says "" for an exhausted pool *and* for a
	// load-spread backup it disqualified without needing a promotion.
	rotateTo string

	poolCalled       bool
	createPoolCalled bool
	rotateCalled     bool
	rotateArgs       struct{ sender, failedSessionID string }
}

func (f *fakePools) Pool(ctx context.Context, sender string) ([]store.PoolMember, error) {
	f.poolCalled = true
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
	got, err := s.resolveSendSession(context.Background(), sender)
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
	_, err := s.resolveSendSession(context.Background(), sender)
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
	_, err := s.resolveSendSession(context.Background(), sender)
	if !errors.Is(err, errSenderPoolExhausted) {
		t.Fatalf("err = %v, want errSenderPoolExhausted", err)
	}
}

// A promotion is reported straight through — nothing needs re-reading.
func TestRotateToHealthyMember_ReportsPromotion(t *testing.T) {
	pools := &fakePools{
		members: map[string][]store.PoolMember{
			"team-b": {{Sender: "team-b", SessionID: "s1", IsMain: true}, {Sender: "team-b", SessionID: "s2"}},
		},
		rotateTo: "s2",
	}
	s := &Server{pools: pools, log: testLogger()}

	got, ok := s.rotateToHealthyMember(context.Background(), "team-b", "s1")
	if !ok || got != "s2" {
		t.Errorf("rotateToHealthyMember = (%q, %v), want (s2, true)", got, ok)
	}
}

// Load spreading may have routed this send to a backup. If that backup is logged out, Rotate
// disqualifies it and returns "" without touching main — so there is still somewhere to send,
// and the request must not be refused with 503.
func TestRotateToHealthyMember_FallsBackToMainWhenBackupFails(t *testing.T) {
	pools := &fakePools{
		members: map[string][]store.PoolMember{
			"team-b": {{Sender: "team-b", SessionID: "s1", IsMain: true}, {Sender: "team-b", SessionID: "s2"}},
		},
		rotateTo: "",
	}
	s := &Server{pools: pools, log: testLogger()}

	got, ok := s.rotateToHealthyMember(context.Background(), "team-b", "s2")
	if !ok || got != "s1" {
		t.Errorf("rotateToHealthyMember = (%q, %v), want (s1, true) — main was healthy", got, ok)
	}
}

// An exhausted pool must stay exhausted: the re-read must not resurrect a disqualified member.
func TestRotateToHealthyMember_ExhaustedPool(t *testing.T) {
	pools := &fakePools{
		members: map[string][]store.PoolMember{
			"team-b": {
				{Sender: "team-b", SessionID: "s1", Disqualified: true},
				{Sender: "team-b", SessionID: "s2", Disqualified: true},
			},
		},
		rotateTo: "",
	}
	s := &Server{pools: pools, log: testLogger()}

	if got, ok := s.rotateToHealthyMember(context.Background(), "team-b", "s1"); ok {
		t.Errorf("rotateToHealthyMember = (%q, true), want ok=false for an exhausted pool", got)
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
