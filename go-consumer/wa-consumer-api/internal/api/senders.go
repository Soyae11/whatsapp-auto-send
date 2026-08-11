package api

// Admin routes for which senders exist — see wa-shared/store/senders.go. Operator surface,
// guarded by ADMIN_API_KEY like the rest of /internal/*. wa-console calls these on behalf of
// its own authenticated users, passing that user's id as owner_id on every call; this service
// never authenticates an end user itself — see wa-shared/store/senders.go's package comment.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"wa-shared/senders"
	"wa-shared/store"
)

type senderAdminView struct {
	Name      string `json:"name"`
	Mode      string `json:"mode"`
	SessionID string `json:"session_id,omitempty"`
}

func senderRowView(row store.SenderRow) senderAdminView {
	return senderAdminView{Name: row.Name, Mode: row.Mode, SessionID: row.SessionID}
}

func (s *Server) handleListSendersAdmin(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 5*time.Second)
	defer cancel()

	ownerID := r.URL.Query().Get("owner_id")
	if ownerID == "" {
		writeError(w, http.StatusBadRequest, errorBody{ErrorCode: "invalid_payload", Message: "'owner_id' is required"})
		return
	}

	rows, err := s.senderStore.ListSenders(ctx, ownerID)
	if err != nil {
		s.senderError(w, "list senders", "", err)
		return
	}
	out := make([]senderAdminView, 0, len(rows))
	for _, row := range rows {
		out = append(out, senderRowView(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

type createSenderBody struct {
	OwnerID   string `json:"owner_id"`
	Name      string `json:"name"`
	Mode      string `json:"mode"`
	SessionID string `json:"session_id"`
}

func (s *Server) handleCreateSender(w http.ResponseWriter, r *http.Request) {
	body, ok := s.readBody(w, r)
	if !ok {
		return
	}
	var req createSenderBody
	if err := decodeStrict(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, errorBody{ErrorCode: "invalid_payload", Message: err.Error()})
		return
	}
	if req.OwnerID == "" {
		writeError(w, http.StatusBadRequest, errorBody{ErrorCode: "invalid_payload", Message: "'owner_id' is required"})
		return
	}
	if err := senders.ValidName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, errorBody{ErrorCode: "invalid_payload", Message: err.Error()})
		return
	}
	switch req.Mode {
	case store.SenderModeSingle:
		if req.SessionID == "" {
			writeError(w, http.StatusBadRequest, errorBody{
				ErrorCode: "invalid_payload", Message: "'session_id' is required for a single-mode sender",
			})
			return
		}
	case store.SenderModePool:
		if req.SessionID != "" {
			writeError(w, http.StatusBadRequest, errorBody{
				ErrorCode: "invalid_payload", Message: "'session_id' must be empty for a pool-mode sender",
			})
			return
		}
	default:
		writeError(w, http.StatusBadRequest, errorBody{
			ErrorCode: "invalid_payload", Message: "'mode' must be \"single\" or \"pool\"",
		})
		return
	}

	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	row, err := s.senderStore.CreateSender(ctx, store.SenderInput{
		OwnerID:   req.OwnerID,
		Name:      req.Name,
		Mode:      req.Mode,
		SessionID: req.SessionID,
	})
	if err != nil {
		s.senderError(w, "create sender", req.Name, err)
		return
	}
	s.log.Info("sender created", "name", row.Name, "mode", row.Mode, "owner_id", row.OwnerID)
	s.refreshSenders(ctx)
	writeJSON(w, http.StatusCreated, senderRowView(*row))
}

func (s *Server) handleDeleteSender(w http.ResponseWriter, r *http.Request) {
	ownerID := r.URL.Query().Get("owner_id")
	if ownerID == "" {
		writeError(w, http.StatusBadRequest, errorBody{ErrorCode: "invalid_payload", Message: "'owner_id' is required"})
		return
	}

	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	name := r.PathValue("name")
	if err := s.senderStore.DeleteSender(ctx, name, ownerID); err != nil {
		s.senderError(w, "delete sender", name, err)
		return
	}
	s.log.Warn("sender deleted", "alert", true, "name", name, "owner_id", ownerID)
	s.refreshSenders(ctx)
	w.WriteHeader(http.StatusNoContent)
}

// refreshSenders swaps in a fresh registry snapshot right after an admin mutation, so the
// change is visible immediately rather than waiting for the next poll — see
// wa-shared/senders/cache.go. Best-effort: the poll loop will pick up the change shortly even
// if this particular refresh fails.
func (s *Server) refreshSenders(ctx context.Context) {
	if err := s.senders.Refresh(ctx); err != nil {
		s.log.Warn("could not refresh sender registry after admin change", "error", err)
	}
}

func (s *Server) senderError(w http.ResponseWriter, action, name string, err error) {
	switch {
	case errors.Is(err, store.ErrSenderExists):
		writeError(w, http.StatusConflict, errorBody{
			ErrorCode: "sender_exists",
			Message:   "sender " + name + " already exists",
		})
	case errors.Is(err, store.ErrSenderNotFound):
		writeError(w, http.StatusNotFound, errorBody{
			ErrorCode: "unknown_sender",
			Message:   "no sender named '" + name + "'",
		})
	default:
		s.log.Error("could not "+action, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, errorBody{
			ErrorCode: "internal_error",
			Message:   "could not " + action,
			Retryable: true,
		})
	}
}
