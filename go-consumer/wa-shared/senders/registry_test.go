package senders

import "testing"

func TestParse_SingleMode(t *testing.T) {
	r, err := Parse("hr-notifications:my-session", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s, err := r.Get("hr-notifications")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.Mode != ModeSingle {
		t.Errorf("Mode = %v, want ModeSingle", s.Mode)
	}
	if s.SessionID != "my-session" {
		t.Errorf("SessionID = %q, want %q", s.SessionID, "my-session")
	}
}

func TestParse_SingleMode_ValidatesAgainstSessions(t *testing.T) {
	if _, err := Parse("hr-notifications:my-session", []string{"other-session"}); err == nil {
		t.Fatal("expected error for a session id not in WA_SESSIONS")
	}
}

func TestParse_PoolMode(t *testing.T) {
	r, err := Parse("team-b:pool", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s, err := r.Get("team-b")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.Mode != ModePool {
		t.Errorf("Mode = %v, want ModePool", s.Mode)
	}
	if s.SessionID != "" {
		t.Errorf("SessionID = %q, want empty for pool mode", s.SessionID)
	}
}

func TestParse_PoolMode_IgnoresSessionsList(t *testing.T) {
	// Pool mode has no static session to validate, so an unrelated WA_SESSIONS list must not
	// cause an error the way it would for single mode.
	if _, err := Parse("team-b:pool", []string{"unrelated-session"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
}

func TestParse_PoolMode_WithPriorityAndDryRun(t *testing.T) {
	r, err := Parse("team-b:pool:critical:dry-run", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s, err := r.Get("team-b")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.Mode != ModePool {
		t.Errorf("Mode = %v, want ModePool", s.Mode)
	}
	if !s.DryRun {
		t.Error("DryRun = false, want true")
	}
}

func TestParse_MultipleSendersMixedModes(t *testing.T) {
	r, err := Parse("hr-notifications:my-session,team-b:pool", []string{"my-session"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	single, err := r.Get("hr-notifications")
	if err != nil {
		t.Fatalf("Get(hr-notifications): %v", err)
	}
	if single.Mode != ModeSingle {
		t.Errorf("hr-notifications Mode = %v, want ModeSingle", single.Mode)
	}

	pool, err := r.Get("team-b")
	if err != nil {
		t.Fatalf("Get(team-b): %v", err)
	}
	if pool.Mode != ModePool {
		t.Errorf("team-b Mode = %v, want ModePool", pool.Mode)
	}
}

func TestParse_EmptySessionField(t *testing.T) {
	if _, err := Parse("hr-notifications:", nil); err == nil {
		t.Fatal("expected error for an empty session field")
	}
}
