package handler

import (
	"encoding/json"
	"net/http"

	"github.com/devstroop/notalk/internal/database"
	"github.com/devstroop/notalk/internal/model"
	"github.com/google/uuid"
)

// RoleHandler handles role CRUD.
type RoleHandler struct {
	db *database.DB
}

// NewRoleHandler creates a new role handler.
func NewRoleHandler(db *database.DB) *RoleHandler {
	return &RoleHandler{db: db}
}

// ListRoles returns all roles with their permissions.
func (h *RoleHandler) ListRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := h.db.ListRoles()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to list roles"})
		return
	}

	out := make([]model.RoleInfo, 0, len(roles))
	for _, role := range roles {
		perms, _ := h.db.GetRolePermissions(role.ID)
		if perms == nil {
			perms = []string{}
		}
		out = append(out, model.RoleInfo{
			ID:          role.ID,
			Name:        role.Name,
			Description: role.Description,
			IsBuiltin:   role.IsBuiltin,
			Permissions: perms,
			CreatedAt:   role.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, model.RoleListResponse{Roles: out, Total: len(out)})
}

// CreateRole creates a new custom role.
func (h *RoleHandler) CreateRole(w http.ResponseWriter, r *http.Request) {
	var req model.CreateRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "invalid request body"})
		return
	}

	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "name is required"})
		return
	}

	// Check name uniqueness
	existing, err := h.db.GetRoleByName(req.Name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "internal error"})
		return
	}
	if existing != nil {
		writeJSON(w, http.StatusConflict, model.ErrorResponse{Error: "role name already exists"})
		return
	}

	rec := &database.RoleRecord{
		ID:          uuid.New().String(),
		Name:        req.Name,
		Description: req.Description,
		IsBuiltin:   false,
	}

	if err := h.db.CreateRole(rec); err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to create role"})
		return
	}

	if len(req.Permissions) > 0 {
		if err := h.db.SetRolePermissions(rec.ID, req.Permissions); err != nil {
			writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to set permissions"})
			return
		}
	}

	// Re-read to get DB-generated created_at
	created, _ := h.db.GetRole(rec.ID)
	perms, _ := h.db.GetRolePermissions(rec.ID)
	if perms == nil {
		perms = []string{}
	}

	createdAt := ""
	if created != nil {
		createdAt = created.CreatedAt
	}

	writeJSON(w, http.StatusCreated, model.RoleInfo{
		ID:          rec.ID,
		Name:        rec.Name,
		Description: rec.Description,
		IsBuiltin:   false,
		Permissions: perms,
		CreatedAt:   createdAt,
	})
}

// GetRole returns a single role.
func (h *RoleHandler) GetRole(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("role_id")

	role, err := h.db.GetRole(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "internal error"})
		return
	}
	if role == nil {
		writeJSON(w, http.StatusNotFound, model.ErrorResponse{Error: "role not found"})
		return
	}

	perms, _ := h.db.GetRolePermissions(role.ID)
	if perms == nil {
		perms = []string{}
	}

	writeJSON(w, http.StatusOK, model.RoleInfo{
		ID:          role.ID,
		Name:        role.Name,
		Description: role.Description,
		IsBuiltin:   role.IsBuiltin,
		Permissions: perms,
		CreatedAt:   role.CreatedAt,
	})
}

// UpdateRole updates a role's name, description, and/or permissions.
func (h *RoleHandler) UpdateRole(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("role_id")

	role, err := h.db.GetRole(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "internal error"})
		return
	}
	if role == nil {
		writeJSON(w, http.StatusNotFound, model.ErrorResponse{Error: "role not found"})
		return
	}

	var req model.UpdateRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "invalid request body"})
		return
	}

	name := role.Name
	desc := role.Description
	if req.Name != nil {
		name = *req.Name
	}
	if req.Description != nil {
		desc = *req.Description
	}

	if err := h.db.UpdateRole(id, name, desc); err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to update role"})
		return
	}

	if req.Permissions != nil {
		if err := h.db.SetRolePermissions(id, req.Permissions); err != nil {
			writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to update permissions"})
			return
		}
	}

	perms, _ := h.db.GetRolePermissions(id)
	if perms == nil {
		perms = []string{}
	}

	writeJSON(w, http.StatusOK, model.RoleInfo{
		ID:          id,
		Name:        name,
		Description: desc,
		IsBuiltin:   role.IsBuiltin,
		Permissions: perms,
	})
}

// DeleteRole deletes a custom role. Built-in roles cannot be deleted.
func (h *RoleHandler) DeleteRole(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("role_id")

	role, err := h.db.GetRole(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "internal error"})
		return
	}
	if role == nil {
		writeJSON(w, http.StatusNotFound, model.ErrorResponse{Error: "role not found"})
		return
	}

	if role.IsBuiltin {
		writeJSON(w, http.StatusForbidden, model.ErrorResponse{Error: "cannot delete built-in role"})
		return
	}

	// Check no users are assigned
	count, err := h.db.CountUsersByRole(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "internal error"})
		return
	}
	if count > 0 {
		writeJSON(w, http.StatusConflict, model.ErrorResponse{Error: "role has assigned users, reassign them first"})
		return
	}

	if err := h.db.DeleteRole(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to delete role"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "role deleted", "id": id})
}
