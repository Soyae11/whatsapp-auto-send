package api

// Admin routes for self-service API key issuance — see wa-shared/store/keys.go, whose
// CreateAPIKey/ListAPIKeysByOwner/RevokeAPIKeyByOwner already existed but had no HTTP path to
// them before this file. Guarded by ADMIN_API_KEY like the rest of /internal/*; wa-console
// calls these on behalf of its own authenticated users, passing that user's id as owner_id.

import (
	"errors"
	"net/http"
	"time"

	"wa-shared/auth"
	"wa-shared/store"
)

type keyView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Project     string   `json:"project"`
	Environment string   `json:"environment"`
	Hint        string   `json:"hint"`
	Senders     []string `json:"senders"`
	RateLimit   int      `json:"rate_limit"`
	LastUsedAt  *string  `json:"last_used_at,omitempty"`
	RevokedAt   *string  `json:"revoked_at,omitempty"`
	ExpiresAt   *string  `json:"expires_at,omitempty"`
	CreatedAt   string   `json:"created_at"`
}

func keyRowView(k auth.Key) keyView {
	return keyView{
		ID:          k.ID,
		Name:        k.Name,
		Project:     k.Project,
		Environment: string(k.Environment),
		Hint:        k.Hint,
		Senders:     k.Senders,
		RateLimit:   k.RateLimit,
		LastUsedAt:  formatTimePtr(k.LastUsedAt),
		RevokedAt:   formatTimePtr(k.RevokedAt),
		ExpiresAt:   formatTimePtr(k.ExpiresAt),
		CreatedAt:   k.CreatedAt.Format(time.RFC3339),
	}
}

func formatTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(time.RFC3339)
	return &s
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 5*time.Second)
	defer cancel()

	ownerID := r.URL.Query().Get("owner_id")
	if ownerID == "" {
		writeError(w, http.StatusBadRequest, errorBody{ErrorCode: "invalid_payload", Message: "'owner_id' is required"})
		return
	}

	keys, err := s.keys.ListAPIKeysByOwner(ctx, ownerID)
	if err != nil {
		s.keyError(w, "list api keys", err)
		return
	}
	out := make([]keyView, 0, len(keys))
	for _, k := range keys {
		out = append(out, keyRowView(k))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

type createKeyBody struct {
	OwnerID     string   `json:"owner_id"`
	Name        string   `json:"name"`
	Project     string   `json:"project"`
	Environment string   `json:"environment"`
	Senders     []string `json:"senders"`
	RateLimit   int      `json:"rate_limit"`
}

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	body, ok := s.readBody(w, r)
	if !ok {
		return
	}
	var req createKeyBody
	if err := decodeStrict(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, errorBody{ErrorCode: "invalid_payload", Message: err.Error()})
		return
	}
	if req.OwnerID == "" {
		writeError(w, http.StatusBadRequest, errorBody{ErrorCode: "invalid_payload", Message: "'owner_id' is required"})
		return
	}
	env, err := auth.ParseEnvironment(req.Environment)
	if err != nil {
		writeError(w, http.StatusBadRequest, errorBody{ErrorCode: "invalid_payload", Message: err.Error()})
		return
	}
	rateLimit := req.RateLimit
	if rateLimit <= 0 {
		rateLimit = auth.DefaultRateLimit
	}

	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	// A key can only be scoped to senders its own creator owns — otherwise self-service
	// issuance would let a user hand themselves a key that sends through someone else's
	// WhatsApp session.
	for _, name := range req.Senders {
		row, err := s.senderStore.GetSender(ctx, name)
		if err != nil || row.OwnerID != req.OwnerID {
			writeError(w, http.StatusBadRequest, errorBody{
				ErrorCode: "invalid_payload",
				Message:   "sender '" + name + "' is not one of your senders",
			})
			return
		}
	}

	key, secret, err := s.keys.CreateAPIKey(ctx, store.APIKeyInput{
		OwnerID:     req.OwnerID,
		Name:        req.Name,
		Project:     req.Project,
		Environment: env,
		Senders:     req.Senders,
		RateLimit:   rateLimit,
	})
	if err != nil {
		s.keyError(w, "create api key", err)
		return
	}
	s.log.Info("api key created", "id", key.ID, "owner_id", key.OwnerID, "project", key.Project)

	view := keyRowView(*key)
	writeJSON(w, http.StatusCreated, map[string]any{
		"key":    view,
		"secret": secret.Plaintext,
	})
}

func (s *Server) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	ownerID := r.URL.Query().Get("owner_id")
	if ownerID == "" {
		writeError(w, http.StatusBadRequest, errorBody{ErrorCode: "invalid_payload", Message: "'owner_id' is required"})
		return
	}

	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	id := r.PathValue("id")
	key, _, err := s.keys.RevokeAPIKeyByOwner(ctx, id, ownerID)
	if err != nil {
		s.keyError(w, "revoke api key", err)
		return
	}
	s.log.Warn("api key revoked", "alert", true, "id", key.ID, "owner_id", ownerID)
	writeJSON(w, http.StatusOK, keyRowView(*key))
}

func (s *Server) keyError(w http.ResponseWriter, action string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, errorBody{ErrorCode: "unknown_key", Message: "no such api key"})
	default:
		s.log.Error("could not "+action, "error", err)
		writeError(w, http.StatusInternalServerError, errorBody{
			ErrorCode: "internal_error",
			Message:   "could not " + action,
			Retryable: true,
		})
	}
}
