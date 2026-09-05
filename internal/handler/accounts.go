package handler

import (
	"net/http"

	"github.com/devstroop/notalk/internal/middleware"
	"github.com/devstroop/notalk/internal/model"
)

// ListAccounts — GET /api/v1/accounts
func (a *API) ListAccounts(w http.ResponseWriter, r *http.Request) {
	identity := middleware.GetIdentity(r)
	resp := a.mgr.ListAccounts()

	// Non-admin users only see their own accounts
	if identity != nil && !identity.HasPermission("accounts:write") {
		filtered := make([]model.AccountInfo, 0)
		for _, acct := range resp.Accounts {
			if acct.UserID == identity.UserID {
				filtered = append(filtered, acct)
			}
		}
		resp.Accounts = filtered
		resp.Total = len(filtered)
	}

	writeJSON(w, http.StatusOK, resp)
}

// CreateAccount — POST /api/v1/accounts
func (a *API) CreateAccount(w http.ResponseWriter, r *http.Request) {
	var req model.CreateAccountRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	// If no user_id specified, assign to the caller
	identity := middleware.GetIdentity(r)
	if req.UserID == "" && identity != nil && identity.UserID != "system" {
		req.UserID = identity.UserID
	}

	resp, err := a.mgr.CreateAccount(req)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

// GetAccount — GET /api/v1/accounts/{account_id}
func (a *API) GetAccount(w http.ResponseWriter, r *http.Request) {
	acct := a.requireAccount(w, r)
	if acct == nil {
		return
	}
	writeJSON(w, http.StatusOK, acct.Info())
}

// DeleteAccount — DELETE /api/v1/accounts/{account_id}
func (a *API) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	acct := a.requireAccount(w, r)
	if acct == nil {
		return
	}

	deleteData := r.URL.Query().Get("delete_data") == "true"
	resp, err := a.mgr.DeleteAccount(acct.ID, deleteData)
	if err != nil {
		if err.Error() == "account not found" {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusConflict, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// UpdateAccount — PATCH /api/v1/accounts/{account_id}
func (a *API) UpdateAccount(w http.ResponseWriter, r *http.Request) {
	acct := a.requireAccount(w, r)
	if acct == nil {
		return
	}

	var req model.UpdateAccountRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	if req.AccountName != nil {
		if err := a.mgr.UpdateAccountName(acct.ID, *req.AccountName); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if req.PhoneNumber != nil {
		if err := a.mgr.UpdatePhoneNumber(acct.ID, *req.PhoneNumber); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, acct.Info())
}
