package handler

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/devstroop/notalk/internal/model"
	"github.com/devstroop/notalk/internal/service"
)

// GetProxy — GET /api/v1/accounts/{account_id}/proxy
func (a *API) GetProxy(w http.ResponseWriter, r *http.Request) {
	acct := a.requireAccount(w, r)
	if acct == nil {
		return
	}
	proxy, err := a.mgr.GetProxy(acct.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if proxy == nil {
		writeError(w, http.StatusNotFound, "no proxy configured")
		return
	}
	writeJSON(w, http.StatusOK, proxy.ToModel())
}

// SetProxy — PUT /api/v1/accounts/{account_id}/proxy
func (a *API) SetProxy(w http.ResponseWriter, r *http.Request) {
	acct := a.requireAccount(w, r)
	if acct == nil {
		return
	}
	id := acct.ID

	var req model.SetProxyRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	// Validate
	if err := validateProxyRequest(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	cfg := &service.ProxyConfig{
		Protocol: strings.ToLower(req.Protocol),
		Host:     req.Host,
		Port:     req.Port,
		Username: req.Username,
		Password: req.Password,
		Enabled:  enabled,
	}

	if err := a.mgr.SetProxy(id, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, cfg.ToModel())
}

// DeleteProxy — DELETE /api/v1/accounts/{account_id}/proxy
func (a *API) DeleteProxy(w http.ResponseWriter, r *http.Request) {
	acct := a.requireAccount(w, r)
	if acct == nil {
		return
	}
	if err := a.mgr.DeleteProxy(acct.ID); err != nil {
		if err.Error() == "account not found" {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"message":    "proxy removed",
		"account_id": acct.ID,
	})
}

func validateProxyRequest(req model.SetProxyRequest) error {
	switch strings.ToLower(req.Protocol) {
	case "http", "https", "socks5":
		// ok
	case "":
		return fmt.Errorf("protocol is required (http, https, socks5)")
	default:
		return fmt.Errorf("unsupported protocol %q (use http, https, or socks5)", req.Protocol)
	}
	if req.Host == "" {
		return fmt.Errorf("host is required")
	}
	if req.Port < 1 || req.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	return nil
}
