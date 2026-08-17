package store

import "testing"

func members() []PoolMember {
	return []PoolMember{
		{Sender: "team-b", SessionID: "s1", IsMain: true},
		{Sender: "team-b", SessionID: "s2"},
		{Sender: "team-b", SessionID: "s3"},
	}
}

// The one difference between the two pickers: a promotion candidate cannot be the session that is
// already main, but a session to send through very much can be.
func TestPickHealthyMemberIncludesMain(t *testing.T) {
	got, ok := PickHealthyMember(members(), "s2", nil)
	if !ok || got != "s1" {
		t.Errorf("PickHealthyMember = (%q, %v), want (s1, true)", got, ok)
	}

	got, ok = PickHealthyBackup(members(), "s2", nil)
	if !ok || got != "s3" {
		t.Errorf("PickHealthyBackup = (%q, %v), want (s3, true) — main is not a promotion candidate", got, ok)
	}
}

// Both pickers share every other eligibility rule, so they are checked together.
func TestPickHealthySkipsIneligible(t *testing.T) {
	all := []PoolMember{
		{Sender: "team-b", SessionID: "s1", IsMain: true, Disqualified: true},
		{Sender: "team-b", SessionID: "s2", Disqualified: true},
		{Sender: "team-b", SessionID: "s3"},
	}

	if got, ok := PickHealthyMember(all, "", nil); !ok || got != "s3" {
		t.Errorf("PickHealthyMember = (%q, %v), want (s3, true) — disqualified members are skipped", got, ok)
	}

	openCircuit := func(sessionID string) bool { return sessionID == "s3" }
	if got, ok := PickHealthyMember(all, "", openCircuit); ok {
		t.Errorf("PickHealthyMember = (%q, true), want ok=false — every member is disqualified or circuit-open", got)
	}

	if got, ok := PickHealthyMember(all, "s3", nil); ok {
		t.Errorf("PickHealthyMember = (%q, true), want ok=false — the excluded session is the only eligible one", got)
	}
}

// Rank order decides, not the order eligibility happens to be evaluated in.
func TestPickHealthyPrefersLowestRank(t *testing.T) {
	if got, ok := PickHealthyMember(members(), "", nil); !ok || got != "s1" {
		t.Errorf("PickHealthyMember = (%q, %v), want (s1, true)", got, ok)
	}
}

func TestPickHealthyEmptyPool(t *testing.T) {
	if got, ok := PickHealthyMember(nil, "", nil); ok {
		t.Errorf("PickHealthyMember = (%q, true), want ok=false for an empty pool", got)
	}
}

// MainOf separates "this pool has no main" from "main is this member". CurrentMain, Rotate and
// wa-consumer-api's routing all used to scan for IsMain themselves and disagreed on the empty
// case; one answer here is what keeps them from drifting apart again.
func TestMainOf(t *testing.T) {
	got, ok := MainOf(members())
	if !ok || got.SessionID != "s1" {
		t.Errorf("MainOf = (%+v, %v), want s1", got, ok)
	}

	noMain := []PoolMember{
		{Sender: "team-b", SessionID: "s1", Disqualified: true},
		{Sender: "team-b", SessionID: "s2", Disqualified: true},
	}
	if got, ok := MainOf(noMain); ok {
		t.Errorf("MainOf = (%+v, true), want ok=false — an exhausted pool has no main", got)
	}
	if got, ok := MainOf(nil); ok {
		t.Errorf("MainOf = (%+v, true), want ok=false for an empty pool", got)
	}
}

// IsMain is false for a backup and false for a session the pool has never heard of. A caller that
// must tell those apart wants MainOf; a caller deciding whether to move the crown does not.
func TestIsMain(t *testing.T) {
	all := members()
	if !IsMain(all, "s1") {
		t.Error("IsMain(s1) = false, want true")
	}
	if IsMain(all, "s2") {
		t.Error("IsMain(s2) = true, want false — s2 is a backup")
	}
	if IsMain(all, "nobody") {
		t.Error("IsMain(nobody) = true, want false — that session is not in the pool")
	}
	if IsMain(nil, "s1") {
		t.Error("IsMain on an empty pool = true, want false")
	}
}

// EligibleToServe is the rule pickHealthy applies per member, exported so callers outside this
// package filter by the same one rather than restating it and drifting.
func TestEligibleToServe(t *testing.T) {
	main := PoolMember{Sender: "team-b", SessionID: "s1", IsMain: true}
	backup := PoolMember{Sender: "team-b", SessionID: "s2"}
	retired := PoolMember{Sender: "team-b", SessionID: "s3", Disqualified: true}

	if !EligibleToServe(main, "", nil) {
		t.Error("main is not eligible to serve; only a promotion has reason to exclude it")
	}
	if EligibleToServe(backup, "s2", nil) {
		t.Error("the excluded session is eligible")
	}
	if EligibleToServe(retired, "", nil) {
		t.Error("a disqualified member is eligible")
	}

	open := func(sessionID string) bool { return sessionID == "s2" }
	if EligibleToServe(backup, "", open) {
		t.Error("a circuit-open member is eligible")
	}
	// A nil isOpen means no circuit information, which must not be read as "every circuit is
	// open" — refusing every member on a missing health check is the worse way to be wrong.
	if !EligibleToServe(backup, "", nil) {
		t.Error("a member is ineligible when no circuit information is available")
	}
}
