package handler

import (
	"encoding/json"
	"net/http"

	"github.com/devstroop/notalk/internal/database"
)

// MCPHandler manages MCP server settings via REST API.
type MCPHandler struct {
	db *database.DB
}

// NewMCPHandler creates a new MCP settings handler.
func NewMCPHandler(db *database.DB) *MCPHandler {
	return &MCPHandler{db: db}
}

// GetMCPSettings returns the current MCP server configuration.
func (h *MCPHandler) GetMCPSettings(w http.ResponseWriter, r *http.Request) {
	enabled := h.db.GetSettingBool("mcp.enabled", true)

	resp := map[string]any{
		"enabled": enabled,
		"path":    "/mcp",
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// UpdateMCPSettings updates MCP server configuration (only enabled is mutable; path is fixed at /mcp).
func (h *MCPHandler) UpdateMCPSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	if req.Enabled != nil {
		val := "false"
		if *req.Enabled {
			val = "true"
		}
		if err := h.db.SetSetting("mcp.enabled", val); err != nil {
			http.Error(w, `{"error":"failed to update setting"}`, http.StatusInternalServerError)
			return
		}
	}

	// Return updated state
	h.GetMCPSettings(w, r)
}
