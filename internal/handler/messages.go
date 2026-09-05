package handler

import (
	"net/http"
	"strconv"

	"github.com/devstroop/notalk/internal/service"
)

// GetMessages — GET /api/v1/accounts/{account_id}/messages?chat=...&limit=...&before=...
// Also accepts ?phone=... instead of ?chat=...
func (a *API) GetMessages(w http.ResponseWriter, r *http.Request) {
	acct := a.requireAccount(w, r)
	if acct == nil {
		return
	}

	q := r.URL.Query()
	chat := q.Get("chat")
	phone := q.Get("phone")

	// If phone is given, convert to JID without full IsOnWhatsApp lookup
	// (message history is local — no connection needed).
	if chat == "" && phone != "" {
		chat = service.PhoneToJID(phone)
	} else if chat == "" {
		writeError(w, http.StatusBadRequest, "chat or phone query parameter required")
		return
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 500 {
		limit = 500
	}

	before := r.URL.Query().Get("before") // RFC3339 cursor

	resp, err := acct.ListMessages(chat, limit, before)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, resp)
}
