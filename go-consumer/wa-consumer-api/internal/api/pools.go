package api

// Admin routes for a sender's main/backup session pool — see wa-shared/store/pools.go. Operator
// surface, guarded by ADMIN_API_KEY like the rest of /internal/*, not the consumer key surface.

import (
	"errors"
	"net/http"
	"time"

	"wa-shared/store"
)

type poolMemberView struct {
	SessionID    string `json:"session_id"`
	IsMain       bool   `json:"is_main"`
	Disqualified bool   `json:"disqualified"`
	Rank         int    `json:"rank"`
}

type poolResponse struct {
	Sender  string           `json:"sender"`
	Members []poolMemberView `json:"members"`
}

func poolView(sender string, members []store.PoolMember) poolResponse {
	out := poolResponse{Sender: sender, Members: make([]poolMemberView, 0, len(members))}
	for _, m := range members {
		out.Members = append(out.Members, poolMemberView{
			SessionID:    m.SessionID,
			IsMain:       m.IsMain,
			Disqualified: m.Disqualified,
			Rank:         m.Rank,
		})
	}
	return out
}

func (s *Server) handleGetPool(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 5*time.Second)
	defer cancel()

	sender := r.PathValue("name")
	if !s.requireOwnedSender(w, r, sender) {
		return
	}
	members, err := s.pools.Pool(ctx, sender)
	if err != nil {
		s.poolError(w, "read pool", sender, err)
		return
	}
	writeJSON(w, http.StatusOK, poolView(sender, members))
}

type createPoolBody struct {
	OwnerID  string   `json:"owner_id"`
	Sessions []string `json:"sessions"`
}

func (s *Server) handleCreatePool(w http.ResponseWriter, r *http.Request) {
	body, ok := s.readBody(w, r)
	if !ok {
		return
	}
	var req createPoolBody
	if err := decodeStrict(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, errorBody{ErrorCode: "invalid_payload", Message: err.Error()})
		return
	}

	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	sender := r.PathValue("name")
	if !s.requirePoolModeSender(w, r, sender, req.OwnerID) {
		return
	}
	if err := s.pools.CreatePool(ctx, sender, req.Sessions); err != nil {
		s.poolError(w, "create pool", sender, err)
		return
	}
	s.log.Info("pool created", "sender", sender, "sessions", req.Sessions)
	s.respondWithPool(w, r, http.StatusCreated, sender)
}

func (s *Server) handleDeletePool(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	sender := r.PathValue("name")
	if !s.requireOwnedSender(w, r, sender) {
		return
	}
	if err := s.pools.DeletePool(ctx, sender); err != nil {
		s.poolError(w, "delete pool", sender, err)
		return
	}
	s.log.Warn("pool deleted", "alert", true, "sender", sender)
	w.WriteHeader(http.StatusNoContent)
}

type poolMemberBody struct {
	OwnerID   string `json:"owner_id"`
	SessionID string `json:"session_id"`
}

func (s *Server) handleAddPoolMember(w http.ResponseWriter, r *http.Request) {
	req, ok := s.readPoolMemberBody(w, r)
	if !ok {
		return
	}

	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	sender := r.PathValue("name")
	if !s.requirePoolModeSender(w, r, sender, req.OwnerID) {
		return
	}
	if err := s.pools.AddMember(ctx, sender, req.SessionID); err != nil {
		s.poolError(w, "add pool member", sender, err)
		return
	}
	s.log.Info("pool member added", "sender", sender, "session_id", req.SessionID)
	s.respondWithPool(w, r, http.StatusOK, sender)
}

func (s *Server) handleRemovePoolMember(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	sender := r.PathValue("name")
	if !s.requireOwnedSender(w, r, sender) {
		return
	}
	sessionID := r.PathValue("sessionId")
	if err := s.pools.RemoveMember(ctx, sender, sessionID); err != nil {
		s.poolError(w, "remove pool member", sender, err)
		return
	}
	s.log.Info("pool member removed", "sender", sender, "session_id", sessionID)
	s.respondWithPool(w, r, http.StatusOK, sender)
}

func (s *Server) handlePromotePoolMember(w http.ResponseWriter, r *http.Request) {
	req, ok := s.readPoolMemberBody(w, r)
	if !ok {
		return
	}

	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	sender := r.PathValue("name")
	if !s.requirePoolModeSender(w, r, sender, req.OwnerID) {
		return
	}
	if err := s.pools.Promote(ctx, sender, req.SessionID); err != nil {
		s.poolError(w, "promote pool member", sender, err)
		return
	}
	s.log.Warn("pool main promoted by hand", "alert", true, "sender", sender, "session_id", req.SessionID)
	s.respondWithPool(w, r, http.StatusOK, sender)
}

func (s *Server) handleReinstatePoolMember(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	sender := r.PathValue("name")
	if !s.requireOwnedSender(w, r, sender) {
		return
	}
	sessionID := r.PathValue("sessionId")
	if err := s.pools.Reinstate(ctx, sender, sessionID); err != nil {
		s.poolError(w, "reinstate pool member", sender, err)
		return
	}
	s.log.Info("pool member reinstated", "sender", sender, "session_id", sessionID)
	s.respondWithPool(w, r, http.StatusOK, sender)
}

func (s *Server) readPoolMemberBody(w http.ResponseWriter, r *http.Request) (poolMemberBody, bool) {
	body, ok := s.readBody(w, r)
	if !ok {
		return poolMemberBody{}, false
	}
	var req poolMemberBody
	if err := decodeStrict(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, errorBody{ErrorCode: "invalid_payload", Message: err.Error()})
		return poolMemberBody{}, false
	}
	if req.SessionID == "" {
		writeError(w, http.StatusBadRequest, errorBody{ErrorCode: "invalid_payload", Message: "'session_id' is required"})
		return poolMemberBody{}, false
	}
	return req, true
}

func (s *Server) respondWithPool(w http.ResponseWriter, r *http.Request, status int, sender string) {
	ctx, cancel := contextWithTimeout(r, 5*time.Second)
	defer cancel()

	members, err := s.pools.Pool(ctx, sender)
	if err != nil {
		s.poolError(w, "read pool", sender, err)
		return
	}
	writeJSON(w, status, poolView(sender, members))
}

// requireOwnedSender rejects a pool operation on a sender that doesn't exist or isn't owned by
// the caller (read from the "owner_id" query parameter) — used by handlers with no JSON body of
// their own to carry it. Existence and ownership failures both read back as unknown_sender, so
// a caller can't distinguish "no such sender" from "someone else's sender".
func (s *Server) requireOwnedSender(w http.ResponseWriter, r *http.Request, sender string) bool {
	_, ok := s.checkOwnedSenderRow(w, r, sender, r.URL.Query().Get("owner_id"))
	return ok
}

// requirePoolModeSender additionally rejects a sender still declared single-mode — otherwise a
// pool would silently start serving traffic the sender's own record claims is single-session.
// ownerID here comes from the caller's own JSON body (create/add/promote all already decode
// one), not the query string.
func (s *Server) requirePoolModeSender(w http.ResponseWriter, r *http.Request, sender, ownerID string) bool {
	known, ok := s.checkOwnedSenderRow(w, r, sender, ownerID)
	if !ok {
		return false
	}
	if known.Mode != store.SenderModePool {
		writeError(w, http.StatusConflict, errorBody{
			ErrorCode: "sender_not_pool_mode",
			Message:   "sender " + sender + " is a single-mode sender; create it as a pool sender before creating or adding to a pool for it",
		})
		return false
	}
	return true
}

func (s *Server) checkOwnedSenderRow(w http.ResponseWriter, r *http.Request, sender, ownerID string) (store.SenderRow, bool) {
	known, err := s.senderStore.GetSender(r.Context(), sender)
	if err != nil || known.OwnerID != ownerID {
		writeError(w, http.StatusNotFound, errorBody{
			ErrorCode: "unknown_sender",
			Message:   "no sender named '" + sender + "'",
		})
		return store.SenderRow{}, false
	}
	return *known, true
}

func (s *Server) poolError(w http.ResponseWriter, action, sender string, err error) {
	switch {
	case errors.Is(err, store.ErrPoolExists):
		writeError(w, http.StatusConflict, errorBody{
			ErrorCode: "pool_exists",
			Message:   "sender " + sender + " already has a pool; use the member routes to change it",
		})
	case errors.Is(err, store.ErrPoolNotFound):
		writeError(w, http.StatusNotFound, errorBody{
			ErrorCode: "pool_member_not_found",
			Message:   err.Error(),
		})
	case errors.Is(err, store.ErrMainMember):
		writeError(w, http.StatusConflict, errorBody{
			ErrorCode: "pool_main_member",
			Message:   err.Error(),
		})
	default:
		s.log.Error("could not "+action, "sender", sender, "error", err)
		writeError(w, http.StatusInternalServerError, errorBody{
			ErrorCode: "internal_error",
			Message:   "could not " + action,
			Retryable: true,
		})
	}
}
