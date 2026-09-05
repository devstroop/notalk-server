package handler

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/devstroop/notalk/internal/database"
	"github.com/devstroop/notalk/internal/model"
	smtpclient "github.com/devstroop/notalk/internal/smtp"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/bcrypt"
)

// AuthHandler handles login, registration, and password reset.
type AuthHandler struct {
	db                  *database.DB
	secretKey           string
	registrationEnabled bool
	smtp                *smtpclient.Client
}

// NewAuthHandler creates a new auth handler.
func NewAuthHandler(db *database.DB, secretKey string, registrationEnabled bool, smtp *smtpclient.Client) *AuthHandler {
	return &AuthHandler{db: db, secretKey: secretKey, registrationEnabled: registrationEnabled, smtp: smtp}
}

// Login authenticates a user and returns a JWT.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req model.LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "invalid request body"})
		return
	}

	if req.Username == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "username and password are required"})
		return
	}

	user, err := h.db.GetUserByUsername(req.Username)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "internal error"})
		return
	}
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, model.ErrorResponse{Error: "invalid credentials"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		writeJSON(w, http.StatusUnauthorized, model.ErrorResponse{Error: "invalid credentials"})
		return
	}

	if !user.Enabled {
		writeJSON(w, http.StatusForbidden, model.ErrorResponse{Error: "account disabled"})
		return
	}

	expiresAt := time.Now().Add(24 * time.Hour)
	claims := &jwt.RegisteredClaims{
		Subject:   user.ID,
		ExpiresAt: jwt.NewNumericDate(expiresAt),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		Issuer:    "notalk",
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(h.secretKey))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to generate token"})
		return
	}

	writeJSON(w, http.StatusOK, model.LoginResponse{
		Token:     signed,
		ExpiresAt: expiresAt.Format(time.RFC3339),
		User: model.UserInfo{
			ID:        user.ID,
			Username:  user.Username,
			Email:     user.Email,
			RoleID:    user.RoleID,
			RoleName:  user.RoleName,
			Enabled:   user.Enabled,
			CreatedAt: user.CreatedAt,
		},
	})
}

// Register creates a new user account with the default user role.
// Only available when registration is enabled in config.
func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	if !h.registrationEnabled {
		writeJSON(w, http.StatusForbidden, model.ErrorResponse{Error: "registration is disabled"})
		return
	}

	var req model.RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "invalid request body"})
		return
	}

	if req.Username == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "username and password are required"})
		return
	}
	if len(req.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "password must be at least 8 characters"})
		return
	}

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
		RoleID:       "builtin-user",
		Enabled:      true,
	}

	if err := h.db.CreateUser(rec); err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to create user"})
		return
	}

	// Create free subscription for new user
	_ = h.db.EnsureUserSubscription(rec.ID, "free")

	user, _ := h.db.GetUser(rec.ID)
	if user != nil {
		writeJSON(w, http.StatusCreated, model.UserInfo{
			ID:        user.ID,
			Username:  user.Username,
			Email:     user.Email,
			RoleID:    user.RoleID,
			RoleName:  user.RoleName,
			Enabled:   user.Enabled,
			CreatedAt: user.CreatedAt,
		})
	} else {
		writeJSON(w, http.StatusCreated, model.UserInfo{ID: rec.ID, Username: rec.Username, Email: rec.Email, RoleID: rec.RoleID, Enabled: true})
	}
}

// ForgotPassword generates a one-time reset token and emails it to the user.
// When SMTP is not configured, the token and reset link are printed to the server console.
func (h *AuthHandler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req model.ForgotPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "invalid request body"})
		return
	}
	if req.Email == "" {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "email is required"})
		return
	}

	// Always return success to prevent email enumeration
	okResp := model.ForgotPasswordResponse{Message: "if an account with that email exists, a reset link has been sent"}

	user, err := h.db.GetUserByEmail(req.Email)
	if err != nil || user == nil {
		// Don't reveal whether the email exists
		writeJSON(w, http.StatusOK, okResp)
		return
	}

	// Generate random token
	rawBytes := make([]byte, 32)
	if _, err := rand.Read(rawBytes); err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to generate token"})
		return
	}
	plainToken := hex.EncodeToString(rawBytes)

	tokenHash := sha256.Sum256([]byte(plainToken))
	hashHex := hex.EncodeToString(tokenHash[:])

	expiresAt := time.Now().Add(1 * time.Hour)

	rec := &database.ResetTokenRecord{
		ID:        uuid.New().String(),
		UserID:    user.ID,
		TokenHash: hashHex,
		ExpiresAt: expiresAt.Format(time.RFC3339),
	}

	if err := h.db.CreateResetToken(rec); err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to create reset token"})
		return
	}

	resetLink := requestBaseURL(r) + "/api/v1/auth/reset-password?token=" + plainToken

	if h.smtp != nil && h.smtp.Enabled() {
		// Send email
		body := "You requested a password reset for your NoTalk account.\n\n" +
			"Use this link to reset your password (valid for 1 hour):\n" +
			resetLink + "\n\n" +
			"Or use this token directly with the API:\n" +
			plainToken + "\n\n" +
			"If you did not request this, ignore this email."

		if err := h.smtp.Send(req.Email, "NoTalk Password Reset", body); err != nil {
			writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to send reset email"})
			return
		}
	} else {
		// No SMTP — print to server console
		log.Warn().
			Str("username", user.Username).
			Str("email", req.Email).
			Str("token", plainToken).
			Str("reset_link", resetLink).
			Str("expires_at", expiresAt.Format(time.RFC3339)).
			Msg("SMTP not configured — password reset token generated (share manually)")
	}

	writeJSON(w, http.StatusOK, okResp)
}

// ResetPassword resets a user's password using a valid reset token.
// Public endpoint — token is the proof of authorization.
func (h *AuthHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var req model.ResetPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "invalid request body"})
		return
	}
	if req.Token == "" || req.NewPassword == "" {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "token and new_password are required"})
		return
	}
	if len(req.NewPassword) < 8 {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "password must be at least 8 characters"})
		return
	}

	tokenHash := sha256.Sum256([]byte(req.Token))
	hashHex := hex.EncodeToString(tokenHash[:])

	rec, err := h.db.GetResetTokenByHash(hashHex)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "internal error"})
		return
	}
	if rec == nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "invalid or expired reset token"})
		return
	}

	// Check expiry
	expiresAt, err := time.Parse(time.RFC3339, rec.ExpiresAt)
	if err != nil || expiresAt.Before(time.Now()) {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "reset token has expired"})
		return
	}

	// Hash new password
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "internal error"})
		return
	}

	if err := h.db.UpdateUserPassword(rec.UserID, string(hash)); err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to update password"})
		return
	}

	// Mark token as used
	_ = h.db.MarkResetTokenUsed(rec.ID)

	writeJSON(w, http.StatusOK, map[string]string{"message": "password reset successfully"})
}

// requestBaseURL derives the public base URL from the incoming request.
func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
