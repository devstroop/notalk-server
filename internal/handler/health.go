package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/devstroop/notalk/internal/middleware"
	"github.com/devstroop/notalk/internal/service"
)

// Health returns a simple health check response.
func Health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	defer func() { _ = r.Body.Close() }()
	return json.NewDecoder(r.Body).Decode(v)
}

// requireAccount resolves the account from the path. Returns nil and writes a
// 404 if the account doesn't exist. Non-admin users can only access their own accounts.
func (a *API) requireAccount(w http.ResponseWriter, r *http.Request) *service.Account {
	acct := a.mgr.GetAccount(r.PathValue("account_id"))
	if acct == nil {
		writeError(w, http.StatusNotFound, "account not found")
		return nil
	}

	// Enforce ownership for non-admin users
	identity := middleware.GetIdentity(r)
	if identity != nil && !identity.HasPermission("accounts:write") && acct.UserID != identity.UserID {
		writeError(w, http.StatusNotFound, "account not found")
		return nil
	}

	return acct
}

// requireConnectedAccount resolves the account from the path and ensures it has
// an active WhatsApp connection. Returns nil and writes an error response if
// the account doesn't exist, isn't authorized, or can't connect.
func (a *API) requireConnectedAccount(w http.ResponseWriter, r *http.Request) *service.Account {
	acct := a.requireAccount(w, r)
	if acct == nil {
		return nil
	}
	if !acct.HasStoredCredentials() {
		writeError(w, http.StatusConflict, "account is not linked to WhatsApp — scan QR or pair first")
		return nil
	}
	if err := acct.EnsureConnected(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "connect: "+err.Error())
		return nil
	}
	return acct
}

// resolveRecipient resolves a JID from chat/jid and phone values.
// Exactly one must be non-empty. If phone is provided it is resolved via
// WhatsApp's IsOnWhatsApp API. Returns "" and writes an error on failure.
func resolveRecipient(w http.ResponseWriter, r *http.Request, acct *service.Account, chatOrJID, phone string) (string, bool) {
	if chatOrJID == "" && phone == "" {
		writeError(w, http.StatusBadRequest, "chat (or jid) or phone required")
		return "", false
	}
	if chatOrJID != "" && phone != "" {
		writeError(w, http.StatusBadRequest, "provide chat/jid or phone, not both")
		return "", false
	}
	if phone != "" {
		resolved, err := acct.ResolvePhone(r.Context(), phone)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return "", false
		}
		return resolved, true
	}
	return chatOrJID, true
}

// resolveRecipientQuery is a convenience for query-param-based endpoints.
// It reads "chat" and "phone" from the query string.
func resolveRecipientQuery(w http.ResponseWriter, r *http.Request, acct *service.Account) (string, bool) {
	q := r.URL.Query()
	return resolveRecipient(w, r, acct, q.Get("chat"), q.Get("phone"))
}

// resolveParticipants takes a mixed slice of JIDs and phone numbers and
// resolves any phone numbers (entries without "@") to JIDs via the WhatsApp
// IsOnWhatsApp API. Returns an error if any phone fails to resolve.
func resolveParticipants(ctx context.Context, acct *service.Account, participants []string) ([]string, error) {
	resolved := make([]string, 0, len(participants))
	for _, p := range participants {
		if strings.Contains(p, "@") {
			// Already a JID
			resolved = append(resolved, p)
		} else {
			jid, err := acct.ResolvePhone(ctx, p)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve phone %q: %w", p, err)
			}
			resolved = append(resolved, jid)
		}
	}
	return resolved, nil
}
