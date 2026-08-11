package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"wa-shared/store"
)

func TestHandleCreatePool_RejectsSingleModeSender(t *testing.T) {
	senderStore := &fakeSenderStore{rows: map[string]store.SenderRow{
		"hr-notifications": {Name: "hr-notifications", OwnerID: "owner-1", Mode: store.SenderModeSingle, SessionID: "static-session"},
	}}
	pools := &fakePools{}
	s := &Server{pools: pools, senderStore: senderStore, log: testLogger()}

	r := httptest.NewRequest(http.MethodPost, "/internal/senders/hr-notifications/pool",
		strings.NewReader(`{"owner_id":"owner-1","sessions":["s1","s2"]}`))
	r.Header.Set("Content-Type", "application/json")
	r.SetPathValue("name", "hr-notifications")
	w := httptest.NewRecorder()

	s.handleCreatePool(w, r)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d; body: %s", w.Code, http.StatusConflict, w.Body.String())
	}
	if pools.createPoolCalled {
		t.Error("CreatePool() was called for a sender still configured single-mode")
	}
}

func TestHandleCreatePool_AllowsPoolModeSender(t *testing.T) {
	senderStore := &fakeSenderStore{rows: map[string]store.SenderRow{
		"team-b": {Name: "team-b", OwnerID: "owner-1", Mode: store.SenderModePool},
	}}
	pools := &fakePools{}
	s := &Server{pools: pools, senderStore: senderStore, log: testLogger()}

	r := httptest.NewRequest(http.MethodPost, "/internal/senders/team-b/pool",
		strings.NewReader(`{"owner_id":"owner-1","sessions":["s1","s2"]}`))
	r.Header.Set("Content-Type", "application/json")
	r.SetPathValue("name", "team-b")
	w := httptest.NewRecorder()

	s.handleCreatePool(w, r)

	if !pools.createPoolCalled {
		t.Fatalf("CreatePool() was not called for a pool-mode sender; body: %s", w.Body.String())
	}
}

func TestHandleCreatePool_RejectsUnknownSender(t *testing.T) {
	senderStore := &fakeSenderStore{rows: map[string]store.SenderRow{
		"team-b": {Name: "team-b", OwnerID: "owner-1", Mode: store.SenderModePool},
	}}
	pools := &fakePools{}
	s := &Server{pools: pools, senderStore: senderStore, log: testLogger()}

	r := httptest.NewRequest(http.MethodPost, "/internal/senders/unknown/pool",
		strings.NewReader(`{"owner_id":"owner-1","sessions":["s1"]}`))
	r.Header.Set("Content-Type", "application/json")
	r.SetPathValue("name", "unknown")
	w := httptest.NewRecorder()

	s.handleCreatePool(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d; body: %s", w.Code, http.StatusNotFound, w.Body.String())
	}
	if pools.createPoolCalled {
		t.Error("CreatePool() was called for an unknown sender")
	}
}

func TestHandleCreatePool_RejectsWrongOwner(t *testing.T) {
	senderStore := &fakeSenderStore{rows: map[string]store.SenderRow{
		"team-b": {Name: "team-b", OwnerID: "owner-1", Mode: store.SenderModePool},
	}}
	pools := &fakePools{}
	s := &Server{pools: pools, senderStore: senderStore, log: testLogger()}

	r := httptest.NewRequest(http.MethodPost, "/internal/senders/team-b/pool",
		strings.NewReader(`{"owner_id":"owner-2","sessions":["s1"]}`))
	r.Header.Set("Content-Type", "application/json")
	r.SetPathValue("name", "team-b")
	w := httptest.NewRecorder()

	s.handleCreatePool(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d; body: %s", w.Code, http.StatusNotFound, w.Body.String())
	}
	if pools.createPoolCalled {
		t.Error("CreatePool() was called for a sender owned by someone else")
	}
}
