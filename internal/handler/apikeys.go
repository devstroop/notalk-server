package handler

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/devstroop/notalk/internal/database"
	"github.com/devstroop/notalk/internal/middleware"
	"github.com/devstroop/notalk/internal/model"
	"github.com/google/uuid"
)

// APIKeyHandler handles API key management.
type APIKeyHandler struct {
	db *database.DB
}

// NewAPIKeyHandler creates a new API key handler.
func NewAPIKeyHandler(db *database.DB) *APIKeyHandler {
	return &APIKeyHandler{db: db}
}

// ListAPIKeys — GET /api/v1/api-keys
// Users see their own keys; admins see all keys.
func (h *APIKeyHandler) ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	identity := middleware.GetIdentity(r)

	var keys []*database.APIKeyRecord
	var err error
	if identity.HasPermission("*") {
		keys, err = h.db.ListAllAPIKeys()
	} else {
		keys, err = h.db.ListAPIKeysByUser(identity.UserID)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to list api keys"})
		return
	}

	out := make([]model.APIKeyInfo, 0, len(keys))
	for _, k := range keys {
		out = append(out, model.APIKeyInfo{
			ID:        k.ID,
			Name:      k.Name,
			Prefix:    k.Prefix,
			AccountID: k.AccountID,
			ExpiresAt: k.ExpiresAt,
			LastUsed:  k.LastUsed,
			Enabled:   k.Enabled,
			CreatedAt: k.CreatedAt,
		})
	}

	writeJSON(w, http.StatusOK, model.APIKeyListResponse{Keys: out, Total: len(out)})
}

// CreateAPIKey — POST /api/v1/api-keys
func (h *APIKeyHandler) CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var req model.CreateAPIKeyRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	// Validate expires_at if provided
	if req.ExpiresAt != nil {
		t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, "expires_at must be valid RFC3339")
			return
		}
		if t.Before(time.Now()) {
			writeError(w, http.StatusBadRequest, "expires_at must be in the future")
			return
		}
	}

	identity := middleware.GetIdentity(r)

	// System admin (secret_key) cannot create API keys — they are not a real user.
	if identity.UserID == "system" {
		writeError(w, http.StatusBadRequest, "API keys can only be created by authenticated users, not via secret_key")
		return
	}

	// Validate account_id binding if provided
	if req.AccountID != nil && *req.AccountID != "" {
		acct, err := h.db.GetAccount(*req.AccountID)
		if err != nil || acct == nil {
			writeError(w, http.StatusNotFound, "account not found")
			return
		}
		// Non-admin users can only bind keys to their own accounts
		if !identity.HasPermission("*") && acct.UserID != identity.UserID {
			writeError(w, http.StatusForbidden, "you do not own this account")
			return
		}
	}

	// Generate random API key: notalk_ + 32 random bytes hex = 71 chars
	rawBytes := make([]byte, 32)
	if _, err := rand.Read(rawBytes); err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to generate key"})
		return
	}
	plainKey := "notalk_" + hex.EncodeToString(rawBytes)
	prefix := plainKey[:15] // "notalk_" + first 8 hex chars

	hash := sha256.Sum256([]byte(plainKey))
	keyHash := hex.EncodeToString(hash[:])

	id := uuid.New().String()

	rec := &database.APIKeyRecord{
		ID:        id,
		UserID:    identity.UserID,
		AccountID: req.AccountID,
		Name:      req.Name,
		Prefix:    prefix,
		KeyHash:   keyHash,
		ExpiresAt: req.ExpiresAt,
		Enabled:   true,
	}

	if err := h.db.CreateAPIKey(rec); err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to create api key"})
		return
	}

	// Return the plain key once — never stored, never retrievable again.
	writeJSON(w, http.StatusCreated, model.CreateAPIKeyResponse{
		ID:        id,
		Key:       plainKey,
		Name:      req.Name,
		Prefix:    prefix,
		AccountID: req.AccountID,
		ExpiresAt: req.ExpiresAt,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})
}

// DeleteAPIKey — DELETE /api/v1/api-keys/{key_id}
func (h *APIKeyHandler) DeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	keyID := r.PathValue("key_id")
	if keyID == "" {
		writeError(w, http.StatusBadRequest, "missing key_id")
		return
	}

	rec, err := h.db.GetAPIKey(keyID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "internal error"})
		return
	}
	if rec == nil {
		writeError(w, http.StatusNotFound, "api key not found")
		return
	}

	// Ownership check: non-admin users can only delete their own keys
	identity := middleware.GetIdentity(r)
	if !identity.HasPermission("*") && rec.UserID != identity.UserID {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	if err := h.db.DeleteAPIKey(keyID); err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to delete api key"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
