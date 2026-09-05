package handler

import (
	"encoding/json"
	"net/http"

	"github.com/devstroop/notalk/internal/database"
	"github.com/devstroop/notalk/internal/middleware"
	"github.com/devstroop/notalk/internal/model"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// UserHandler handles user CRUD.
type UserHandler struct {
	db *database.DB
}

// NewUserHandler creates a new user handler.
func NewUserHandler(db *database.DB) *UserHandler {
	return &UserHandler{db: db}
}

// ListUsers returns all users.
func (h *UserHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.db.ListUsers()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to list users"})
		return
	}

	out := make([]model.UserInfo, 0, len(users))
	for _, u := range users {
		out = append(out, userToInfo(u))
	}
	writeJSON(w, http.StatusOK, model.UserListResponse{Users: out, Total: len(out)})
}

// CreateUser creates a new user.
func (h *UserHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req model.CreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "invalid request body"})
		return
	}

	if req.Username == "" || req.Password == "" || req.RoleID == "" {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "username, password, and role_id are required"})
		return
	}
	if len(req.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "password must be at least 8 characters"})
		return
	}

	// Check role exists
	role, err := h.db.GetRole(req.RoleID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "internal error"})
		return
	}
	if role == nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "role not found"})
		return
	}

	// Check username uniqueness
	existing, err := h.db.GetUserByUsername(req.Username)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "internal error"})
		return
	}
	if existing != nil {
		writeJSON(w, http.StatusConflict, model.ErrorResponse{Error: "username already exists"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "internal error"})
		return
	}

	rec := &database.UserRecord{
		ID:           uuid.New().String(),
		Username:     req.Username,
		Email:        req.Email,
		PasswordHash: string(hash),
		RoleID:       req.RoleID,
		Enabled:      true,
	}

	if err := h.db.CreateUser(rec); err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to create user"})
		return
	}

	// Re-read to get joined role name
	user, _ := h.db.GetUser(rec.ID)
	if user != nil {
		writeJSON(w, http.StatusCreated, userToInfo(user))
	} else {
		writeJSON(w, http.StatusCreated, model.UserInfo{ID: rec.ID, Username: rec.Username, RoleID: rec.RoleID, Enabled: true})
	}
}

// GetUser returns a single user.
func (h *UserHandler) GetUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("user_id")

	// Allow non-admin to read their own profile
	identity := middleware.GetIdentity(r)
	if identity != nil && !identity.HasPermission("users:*") && identity.UserID != id {
		writeJSON(w, http.StatusForbidden, model.ErrorResponse{Error: "forbidden"})
		return
	}

	user, err := h.db.GetUser(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "internal error"})
		return
	}
	if user == nil {
		writeJSON(w, http.StatusNotFound, model.ErrorResponse{Error: "user not found"})
		return
	}

	writeJSON(w, http.StatusOK, userToInfo(user))
}

// UpdateUser updates a user's role, enabled status, or password.
func (h *UserHandler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("user_id")

	user, err := h.db.GetUser(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "internal error"})
		return
	}
	if user == nil {
		writeJSON(w, http.StatusNotFound, model.ErrorResponse{Error: "user not found"})
		return
	}

	var req model.UpdateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "invalid request body"})
		return
	}

	if req.Password != nil {
		if len(*req.Password) < 8 {
			writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "password must be at least 8 characters"})
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(*req.Password), bcrypt.DefaultCost)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "internal error"})
			return
		}
		if err := h.db.UpdateUserPassword(id, string(hash)); err != nil {
			writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to update password"})
			return
		}
	}

	if req.Email != nil {
		if err := h.db.UpdateUserEmail(id, *req.Email); err != nil {
			writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to update email"})
			return
		}
	}

	roleID := user.RoleID
	enabled := user.Enabled
	if req.RoleID != nil {
		// Verify role exists
		role, err := h.db.GetRole(*req.RoleID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "internal error"})
			return
		}
		if role == nil {
			writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "role not found"})
			return
		}
		roleID = *req.RoleID
	}
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	if req.RoleID != nil || req.Enabled != nil {
		if err := h.db.UpdateUser(id, roleID, enabled); err != nil {
			writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to update user"})
			return
		}
	}

	updated, _ := h.db.GetUser(id)
	if updated != nil {
		writeJSON(w, http.StatusOK, userToInfo(updated))
	} else {
		writeJSON(w, http.StatusOK, map[string]string{"message": "updated"})
	}
}

// DeleteUser deletes a user.
func (h *UserHandler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("user_id")

	user, err := h.db.GetUser(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "internal error"})
		return
	}
	if user == nil {
		writeJSON(w, http.StatusNotFound, model.ErrorResponse{Error: "user not found"})
		return
	}

	if err := h.db.DeleteUser(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to delete user"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "user deleted", "id": id})
}

func userToInfo(u *database.UserRecord) model.UserInfo {
	return model.UserInfo{
		ID:        u.ID,
		Username:  u.Username,
		Email:     u.Email,
		RoleID:    u.RoleID,
		RoleName:  u.RoleName,
		Enabled:   u.Enabled,
		CreatedAt: u.CreatedAt,
	}
}
