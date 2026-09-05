package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/devstroop/notalk/internal/agent"
	"github.com/devstroop/notalk/internal/database"
)

// ── Mode 1: Personal Assistant ────────────────────────────────────────────────

// AssistantPage renders the Copilot chat UI.
func (h *Handler) AssistantPage(w http.ResponseWriter, r *http.Request) {
	identity := getIdentity(r)
	if identity == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Load stored session history to pre-populate the UI.
	histJSON, _ := h.db.GetAgentSession(identity.UserID)
	var history []agent.Message
	_ = json.Unmarshal([]byte(histJSON), &history)

	// Collect user's accounts for the context display.
	list := h.mgr.ListAccounts()
	type acctOpt struct {
		ID          string
		Name        string
		PhoneNumber string
		Connected   bool
	}
	var accounts []acctOpt
	for _, a := range list.Accounts {
		if !identity.HasPermission("*") && a.UserID != identity.UserID {
			continue
		}
		phone := ""
		if a.PhoneNumber != nil {
			phone = *a.PhoneNumber
		}
		acct := h.mgr.GetAccount(a.ID)
		accounts = append(accounts, acctOpt{
			ID:          a.ID,
			Name:        a.AccountName,
			PhoneNumber: phone,
			Connected:   acct != nil && acct.IsLoggedIn(),
		})
	}

	aiEnabled := h.llmCfg.Enabled
	apiKey := h.llmCfg.APIKey
	provider := h.llmCfg.Provider
	model := h.llmCfg.Model
	if !aiEnabled || (apiKey == "" && provider == "") {
		// Fall back to DB for backwards compatibility.
		aiEnabled = h.db.GetSettingBool("ai.enabled", false)
		apiKey = h.db.GetSetting("ai.api_key", "")
		provider = h.db.GetSetting("ai.provider", "openai")
		model = h.db.GetSetting("ai.model", "gpt-4o-mini")
	} else if model == "" {
		model = h.db.GetSetting("ai.model", "gpt-4o-mini")
	}

	pd := h.page(w, r, "Copilot", "assistant", map[string]any{
		"History":   history,
		"Accounts":  accounts,
		"AIEnabled": aiEnabled,
		"HasAPIKey": apiKey != "",
		"Provider":  provider,
		"Model":     model,
	})
	h.render.Page(w, http.StatusOK, "assistant", pd)
}

// AssistantChat handles POST /assistant/chat — streams the agent response via SSE.
func (h *Handler) AssistantChat(w http.ResponseWriter, r *http.Request) {
	identity := getIdentity(r)
	if identity == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	message := strings.TrimSpace(r.FormValue("message"))
	if message == "" {
		http.Error(w, "message required", http.StatusBadRequest)
		return
	}

	a := agent.New(h.db, h.mgr, identity.UserID, identity.HasPermission("*"), agent.LLMConfig{
		Provider: h.llmCfg.Provider,
		APIKey:   h.llmCfg.APIKey,
		BaseURL:  h.llmCfg.BaseURL,
		Model:    h.llmCfg.Model,
	})
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	a.RunChat(ctx, identity.UserID, message, w)
}

// AssistantClear handles POST /assistant/clear — wipes the stored session history.
func (h *Handler) AssistantClear(w http.ResponseWriter, r *http.Request) {
	identity := getIdentity(r)
	if identity == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	_ = h.db.ClearAgentSession(identity.UserID)
	http.Redirect(w, r, "/assistant", http.StatusSeeOther)
}

// ── Mode 2: Autopilot (per-account auto-reply) ────────────────────────────────

// AutopilotPage renders the autopilot config page for an account.
func (h *Handler) AutopilotPage(w http.ResponseWriter, r *http.Request) {
	identity := getIdentity(r)
	if identity == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	accountID := r.PathValue("id")
	acctRec, err := h.db.GetAccount(accountID)
	if err != nil || acctRec == nil {
		setFlash(w, "error", "Account not found.")
		http.Redirect(w, r, "/accounts", http.StatusSeeOther)
		return
	}
	// Access check — non-admins can only view their own accounts.
	if !identity.HasPermission("*") && acctRec.UserID != identity.UserID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	cfg, _ := h.db.GetAgentConfig(accountID)
	logs, _ := h.db.ListAgentLogs(accountID, 30)

	aiEnabled := h.llmCfg.Enabled
	globalModel := h.llmCfg.Model
	if !aiEnabled || (h.llmCfg.APIKey == "" && h.llmCfg.Provider == "") {
		aiEnabled = h.db.GetSettingBool("ai.enabled", false)
	}
	if globalModel == "" {
		globalModel = h.db.GetSetting("ai.model", "gpt-4o-mini")
	}

	acct := h.mgr.GetAccount(accountID)
	connected := acct != nil && acct.IsLoggedIn()

	pd := h.page(w, r, "Autopilot — "+acctRec.AccountName, "autopilot", map[string]any{
		"Account":     acctRec,
		"Connected":   connected,
		"Config":      cfg,
		"Logs":        logs,
		"AIEnabled":   aiEnabled,
		"GlobalModel": globalModel,
	})
	h.render.Page(w, http.StatusOK, "autopilot", pd)
}

// AutopilotSave handles POST /accounts/{id}/autopilot — saves autopilot config.
func (h *Handler) AutopilotSave(w http.ResponseWriter, r *http.Request) {
	identity := getIdentity(r)
	if identity == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	accountID := r.PathValue("id")
	acctRec, err := h.db.GetAccount(accountID)
	if err != nil || acctRec == nil {
		setFlash(w, "error", "Account not found.")
		http.Redirect(w, r, "/accounts", http.StatusSeeOther)
		return
	}
	if !identity.HasPermission("*") && acctRec.UserID != identity.UserID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		setFlash(w, "error", "Invalid form data.")
		http.Redirect(w, r, "/accounts/"+accountID+"/autopilot", http.StatusSeeOther)
		return
	}

	cfg := database.AgentConfigRecord{
		AccountID:         accountID,
		Enabled:           r.FormValue("enabled") == "1",
		SystemPrompt:      strings.TrimSpace(r.FormValue("system_prompt")),
		Model:             strings.TrimSpace(r.FormValue("model")),
		EscalationEnabled: r.FormValue("escalation_enabled") == "1",
		EscalationMessage: strings.TrimSpace(r.FormValue("escalation_message")),
		Whitelist:         normaliseNumberList(r.FormValue("whitelist")),
		Blacklist:         normaliseNumberList(r.FormValue("blacklist")),
	}
	if err := h.db.SetAgentConfig(cfg); err != nil {
		setFlash(w, "error", "Failed to save config: "+err.Error())
	} else {
		setFlash(w, "success", "Autopilot config saved.")
	}
	http.Redirect(w, r, "/accounts/"+accountID+"/autopilot", http.StatusSeeOther)
}

// AutopilotToggle handles POST /accounts/{id}/autopilot/toggle — flips the enabled flag.
func (h *Handler) AutopilotToggle(w http.ResponseWriter, r *http.Request) {
	identity := getIdentity(r)
	if identity == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	accountID := r.PathValue("id")
	acctRec, err := h.db.GetAccount(accountID)
	if err != nil || acctRec == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if !identity.HasPermission("*") && acctRec.UserID != identity.UserID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	cfg, _ := h.db.GetAgentConfig(accountID)
	cfg.Enabled = !cfg.Enabled
	if err := h.db.SetAgentConfig(*cfg); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}

	status := "disabled"
	if cfg.Enabled {
		status = "enabled"
	}
	setFlash(w, "success", "Autopilot "+status+".")
	http.Redirect(w, r, "/accounts/"+accountID+"/autopilot", http.StatusSeeOther)
}

// ── AI Settings (admin) ───────────────────────────────────────────────────────

// AISettingsPage renders the AI provider settings page.
func (h *Handler) AISettingsPage(w http.ResponseWriter, r *http.Request) {
	identity := getIdentity(r)
	if identity == nil || !identity.HasPermission("*") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Config/env takes priority over DB for LLM settings.
	fromConfig := h.llmCfg.APIKey != "" || h.llmCfg.Provider != "" || h.llmCfg.BaseURL != ""

	enabled := h.llmCfg.Enabled
	provider := h.llmCfg.Provider
	apiKey := h.llmCfg.APIKey
	baseURL := h.llmCfg.BaseURL
	model := h.llmCfg.Model

	if !fromConfig {
		enabled = h.db.GetSettingBool("ai.enabled", false)
		provider = h.db.GetSetting("ai.provider", "openai")
		apiKey = h.db.GetSetting("ai.api_key", "")
		baseURL = h.db.GetSetting("ai.base_url", "")
		model = h.db.GetSetting("ai.model", "gpt-4o-mini")
	} else if model == "" {
		model = h.db.GetSetting("ai.model", "gpt-4o-mini")
	}

	pd := h.page(w, r, "LLM Configuration", "ai-settings", map[string]any{
		"Enabled":    enabled,
		"Provider":   provider,
		"APIKey":     apiKey,
		"BaseURL":    baseURL,
		"Model":      model,
		"FromConfig": fromConfig,
	})
	h.render.Page(w, http.StatusOK, "ai-settings", pd)
}

// AISettingsUpdate handles POST /settings/ai — saves AI provider settings.
func (h *Handler) AISettingsUpdate(w http.ResponseWriter, r *http.Request) {
	identity := getIdentity(r)
	if identity == nil || !identity.HasPermission("*") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// If config/env is active, block web edits to avoid confusion.
	if h.llmCfg.APIKey != "" || h.llmCfg.Provider != "" || h.llmCfg.BaseURL != "" {
		setFlash(w, "error", "LLM settings are managed via config file or environment variables and cannot be edited here.")
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		setFlash(w, "error", "Invalid form data.")
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}

	enabled := r.FormValue("enabled") == "1"
	provider := r.FormValue("provider")
	apiKey := strings.TrimSpace(r.FormValue("api_key"))
	baseURL := strings.TrimSpace(r.FormValue("base_url"))
	model := strings.TrimSpace(r.FormValue("model"))

	validProviders := map[string]bool{"openai": true, "ollama": true}
	if !validProviders[provider] {
		provider = "openai"
	}
	if model == "" {
		model = "gpt-4o-mini"
	}

	_ = h.db.SetSetting("ai.enabled", boolToStr(enabled))
	_ = h.db.SetSetting("ai.provider", provider)
	if apiKey != "" {
		_ = h.db.SetSetting("ai.api_key", apiKey)
	}
	_ = h.db.SetSetting("ai.base_url", baseURL)
	_ = h.db.SetSetting("ai.model", model)

	setFlash(w, "success", "LLM configuration saved.")
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func boolToStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// normaliseNumberList cleans a textarea input of phone numbers (one per line or comma-separated),
// strips leading +, whitespace, and empty lines, then returns a canonical newline-joined string.
func normaliseNumberList(raw string) string {
	raw = strings.ReplaceAll(raw, ",", "\n")
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		num := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "+"))
		if num != "" {
			out = append(out, num)
		}
	}
	return strings.Join(out, "\n")
}

// ── Auto-reply hook (called from AccountManager) ─────────────────────────────

// InitAutoReply wires the autopilot auto-reply logic into the account manager.
// Call this once after startup is complete.
func (h *Handler) InitAutoReply() {
	h.mgr.SetOnMessage(func(accountID, chatJID, senderJID, body string) {
		// Guard: skip if body is empty or too short to be meaningful.
		if len(strings.TrimSpace(body)) < 2 {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		reply, mdl, err := agent.AutoReply(ctx, h.db, h.mgr, accountID, chatJID, senderJID, body, agent.LLMConfig{
			Provider: h.llmCfg.Provider,
			APIKey:   h.llmCfg.APIKey,
			BaseURL:  h.llmCfg.BaseURL,
			Model:    h.llmCfg.Model,
		})
		if err != nil {
			log.Warn().Err(err).Str("account", accountID).Msg("autopilot: llm error")
			return
		}
		if reply == "" {
			return // disabled or escalated
		}

		// Send the reply via the account.
		acct := h.mgr.GetAccount(accountID)
		if acct == nil || !acct.IsLoggedIn() {
			log.Warn().Str("account", accountID).Msg("autopilot: account not connected, cannot send reply")
			return
		}
		if _, err := acct.SendMessage(ctx, chatJID, reply); err != nil {
			log.Warn().Err(err).Str("account", accountID).Msg("autopilot: send reply failed")
			return
		}

		log.Info().Str("account", accountID).Str("chat", chatJID).Str("model", mdl).Msg("autopilot: auto-reply sent")
		_ = h.db.InsertAgentLog(database.AgentLogRecord{
			ID:              uuid.New().String(),
			AccountID:       accountID,
			ChatJID:         chatJID,
			SenderJID:       senderJID,
			IncomingMessage: body,
			OutgoingMessage: reply,
			Model:           mdl,
		})
	})
}
