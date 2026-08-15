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
