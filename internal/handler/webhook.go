package handler

import (
	"net/http"
	"strings"

	"github.com/devstroop/notalk/internal/database"
	"github.com/devstroop/notalk/internal/model"
)

// GetWebhook — GET /api/v1/accounts/{account_id}/webhook
func (a *API) GetWebhook(w http.ResponseWriter, r *http.Request) {
	acct := a.requireAccount(w, r)
	if acct == nil {
		return
	}

	cfg, err := a.mgr.DB().GetWebhookConfig(acct.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if cfg == nil {
		writeError(w, http.StatusNotFound, "no webhook configured")
		return
	}

	var events []string
	if cfg.Events != "" {
		events = splitCSV(cfg.Events)
	} else {
		events = []string{}
	}

	writeJSON(w, http.StatusOK, model.WebhookConfigResponse{
		URL:     cfg.URL,
		Events:  events,
		Enabled: cfg.Enabled,
	})
}

// SetWebhook — PUT /api/v1/accounts/{account_id}/webhook
func (a *API) SetWebhook(w http.ResponseWriter, r *http.Request) {
	acct := a.requireAccount(w, r)
	if acct == nil {
		return
	}

	var req model.SetWebhookRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if req.URL == "" {
		writeError(w, http.StatusBadRequest, "url required")
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	rec := &database.WebhookConfigRecord{
		AccountID: acct.ID,
		URL:       req.URL,
		Secret:    req.Secret,
		Events:    strings.Join(req.Events, ","),
		Enabled:   enabled,
	}
	if err := a.mgr.DB().UpsertWebhookConfig(rec); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var events []string
	if len(req.Events) > 0 {
		events = req.Events
	} else {
		events = []string{}
	}

	writeJSON(w, http.StatusOK, model.WebhookConfigResponse{
		URL:     req.URL,
		Events:  events,
		Enabled: enabled,
	})
}

// DeleteWebhook — DELETE /api/v1/accounts/{account_id}/webhook
func (a *API) DeleteWebhook(w http.ResponseWriter, r *http.Request) {
	acct := a.requireAccount(w, r)
	if acct == nil {
		return
	}

	if err := a.mgr.DB().DeleteWebhookConfig(acct.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// splitCSV splits a comma-separated string into trimmed parts.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
