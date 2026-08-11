package message

import (
	"testing"
	"time"

	"wa-shared/store"
)

func TestFromRow_CopiesSessionID(t *testing.T) {
	row := store.Row{
		PublicID:  "msg_123",
		Status:    store.StatusSent,
		Sender:    "hr-notifications",
		SessionID: "session_abc",
		To:        "62812xxxxxxx",
		CreatedAt: time.Now(),
	}

	got := FromRow(row)

	if got.SessionID != "session_abc" {
		t.Fatalf("SessionID = %q, want %q", got.SessionID, "session_abc")
	}
}
