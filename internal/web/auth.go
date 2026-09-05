package web

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/devstroop/notalk/internal/database"
	"github.com/devstroop/notalk/internal/model"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/bcrypt"
)

// LoginPage renders the login form.
func (h *Handler) LoginPage(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title: "Sign in — NoTalk",
		Page:  "login",
		Flash: getFlash(w, r),
		Data: map[string]any{
			"SecretKeyHint":       true,
			"RegistrationEnabled": h.regEnabled,
		},
	}
	h.render.Page(w, http.StatusOK, "login", data)
}

// LoginSubmit processes the login form.
func (h *Handler) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	if username == "" || password == "" {
		setFlash(w, "error", "Username and password are required.")
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Path 1: system admin via secret key
	if username == "system" && subtle.ConstantTimeCompare([]byte(password), []byte(h.secret)) == 1 {
		setSession(w, h.secret)
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}

	// Path 2: user auth
	user, err := h.db.GetUserByUsername(username)
	if err != nil || user == nil {
		setFlash(w, "error", "Invalid credentials.")
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		setFlash(w, "error", "Invalid credentials.")
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if !user.Enabled {
		setFlash(w, "error", "Account disabled.")
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Issue JWT
	claims := &jwt.RegisteredClaims{
		Subject:   user.ID,
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		Issuer:    "notalk",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(h.secret))
	if err != nil {
		setFlash(w, "error", "Failed to create session.")
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	setSession(w, signed)
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

// Logout clears the session and redirects to login.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	clearSession(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// RegisterPage renders the registration form.
func (h *Handler) RegisterPage(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title: "Register — NoTalk",
		Page:  "register",
		Flash: getFlash(w, r),
	}
	h.render.Page(w, http.StatusOK, "register", data)
}

// RegisterSubmit processes the registration form.
func (h *Handler) RegisterSubmit(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")

	if username == "" || password == "" {
		setFlash(w, "error", "Username and password are required.")
		http.Redirect(w, r, "/register", http.StatusSeeOther)
		return
	}
	if len(password) < 8 {
		setFlash(w, "error", "Password must be at least 8 characters.")
		http.Redirect(w, r, "/register", http.StatusSeeOther)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		setFlash(w, "error", "Failed to process registration.")
		http.Redirect(w, r, "/register", http.StatusSeeOther)
		return
	}

	// Look up the default user role
	role, err := h.db.GetRoleByName("user")
	if err != nil || role == nil {
		setFlash(w, "error", "Registration not available.")
		http.Redirect(w, r, "/register", http.StatusSeeOther)
		return
	}

	rec := &database.UserRecord{
		ID:           uuid.New().String(),
		Username:     username,
		Email:        email,
		PasswordHash: string(hash),
		RoleID:       role.ID,
		Enabled:      true,
	}
	if err := h.db.CreateUser(rec); err != nil {
		setFlash(w, "error", "Username already taken.")
		http.Redirect(w, r, "/register", http.StatusSeeOther)
		return
	}

	setFlash(w, "success", "Account created. Please sign in.")
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// Root serves the public landing page, or redirects to dashboard if
// the user is already authenticated.
func (h *Handler) Root(w http.ResponseWriter, r *http.Request) {
	if id := getIdentity(r); id != nil {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	data := PageData{
		Title:   "NoTalk — WhatsApp API Gateway",
		Page:    "home",
		Version: h.version,
		Data: map[string]any{
			"RegistrationEnabled": h.regEnabled,
		},
	}
	h.render.Page(w, http.StatusOK, "home", data)
}

// AboutPage renders the public about page.
func (h *Handler) AboutPage(w http.ResponseWriter, r *http.Request) {
	data := PageData{Title: "About — NoTalk", Page: "about", Version: h.version,
		Data: map[string]any{"RegistrationEnabled": h.regEnabled}}
	h.render.Page(w, http.StatusOK, "about", data)
}

// PricingPage renders the public pricing page with plan data from the database.
func (h *Handler) PricingPage(w http.ResponseWriter, r *http.Request) {
	recs, _ := h.db.ListPlans()
	var plans []model.PlanInfo
	for _, rec := range recs {
		plans = append(plans, model.PlanInfo{
			ID:          rec.ID,
			Name:        rec.Name,
			Description: rec.Description,
			PriceCents:  rec.PriceCents,
			Interval:    rec.Interval,
			Limits:      rec.PlanLimits(),
			IsDefault:   rec.IsDefault,
		})
	}
	data := PageData{
		Title:   "Pricing — NoTalk",
		Page:    "pricing",
		Version: h.version,
		Data: map[string]any{
			"Plans":               plans,
			"RegistrationEnabled": h.regEnabled,
		},
	}
	h.render.Page(w, http.StatusOK, "pricing", data)
}

// TermsPage renders the public terms of service page.
func (h *Handler) TermsPage(w http.ResponseWriter, r *http.Request) {
	data := PageData{Title: "Terms of Service — NoTalk", Page: "terms", Version: h.version,
		Data: map[string]any{"RegistrationEnabled": h.regEnabled}}
	h.render.Page(w, http.StatusOK, "terms", data)
}

// PrivacyPage renders the public privacy policy page.
func (h *Handler) PrivacyPage(w http.ResponseWriter, r *http.Request) {
	data := PageData{Title: "Privacy Policy — NoTalk", Page: "privacy", Version: h.version,
		Data: map[string]any{"RegistrationEnabled": h.regEnabled}}
	h.render.Page(w, http.StatusOK, "privacy", data)
}

// ForgotPasswordPage renders the forgot-password form.
func (h *Handler) ForgotPasswordPage(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title: "Forgot Password — NoTalk",
		Page:  "forgot-password",
		Flash: getFlash(w, r),
	}
	h.render.Page(w, http.StatusOK, "forgot-password", data)
}

// ForgotPasswordSubmit processes the forgot-password form.
func (h *Handler) ForgotPasswordSubmit(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.FormValue("email"))
	if email == "" {
		setFlash(w, "error", "Email is required.")
		http.Redirect(w, r, "/forgot-password", http.StatusSeeOther)
		return
	}

	// Always show success to prevent email enumeration
	successMsg := "If an account with that email exists, a reset link has been sent."

	user, err := h.db.GetUserByEmail(email)
	if err != nil || user == nil {
		setFlash(w, "success", successMsg)
		http.Redirect(w, r, "/forgot-password", http.StatusSeeOther)
		return
	}

	rawBytes := make([]byte, 32)
	if _, err := rand.Read(rawBytes); err != nil {
		setFlash(w, "error", "Something went wrong. Please try again.")
		http.Redirect(w, r, "/forgot-password", http.StatusSeeOther)
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
		setFlash(w, "error", "Something went wrong. Please try again.")
		http.Redirect(w, r, "/forgot-password", http.StatusSeeOther)
		return
	}

	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	resetLink := scheme + "://" + r.Host + "/reset-password?token=" + plainToken

	if h.smtp != nil && h.smtp.Enabled() {
		body := "You requested a password reset for your NoTalk account.\n\n" +
			"Use this link to reset your password (valid for 1 hour):\n" +
			resetLink + "\n\n" +
			"If you did not request this, ignore this email."
		if err := h.smtp.Send(email, "NoTalk Password Reset", body); err != nil {
			log.Error().Err(err).Msg("failed to send reset email")
		}
	} else {
		log.Warn().
			Str("username", user.Username).
			Str("email", email).
			Str("token", plainToken).
			Str("reset_link", resetLink).
			Str("expires_at", expiresAt.Format(time.RFC3339)).
			Msg("SMTP not configured — password reset token generated (check server logs)")
	}

	setFlash(w, "success", successMsg)
	http.Redirect(w, r, "/forgot-password", http.StatusSeeOther)
}

// ResetPasswordPage renders the reset-password form.
func (h *Handler) ResetPasswordPage(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		setFlash(w, "error", "Invalid or missing reset token.")
		http.Redirect(w, r, "/forgot-password", http.StatusSeeOther)
		return
	}

	// Pre-validate the token so the user gets immediate feedback instead of
	// filling the form and only seeing the error after submission.
	tokenHash := sha256.Sum256([]byte(token))
	hashHex := hex.EncodeToString(tokenHash[:])
	rec, err := h.db.GetResetTokenByHash(hashHex)
	if err != nil || rec == nil {
		setFlash(w, "error", "This reset link is invalid. Please request a new one.")
		http.Redirect(w, r, "/forgot-password", http.StatusSeeOther)
		return
	}
	expiresAt, parseErr := time.Parse(time.RFC3339, rec.ExpiresAt)
	if parseErr != nil || expiresAt.Before(time.Now()) {
		setFlash(w, "error", "This reset link has expired. Please request a new one.")
		http.Redirect(w, r, "/forgot-password", http.StatusSeeOther)
		return
	}

	data := PageData{
		Title: "Reset Password — NoTalk",
		Page:  "reset-password",
		Flash: getFlash(w, r),
		Data: map[string]any{
			"Token": token,
		},
	}
	h.render.Page(w, http.StatusOK, "reset-password", data)
}

// ResetPasswordSubmit processes the reset-password form.
func (h *Handler) ResetPasswordSubmit(w http.ResponseWriter, r *http.Request) {
	token := r.FormValue("token")
	password := r.FormValue("password")
	confirm := r.FormValue("confirm_password")

	if token == "" {
		setFlash(w, "error", "Invalid reset token.")
		http.Redirect(w, r, "/forgot-password", http.StatusSeeOther)
		return
	}
	if password == "" || len(password) < 8 {
		setFlash(w, "error", "Password must be at least 8 characters.")
		http.Redirect(w, r, "/reset-password?token="+token, http.StatusSeeOther)
		return
	}
	if password != confirm {
		setFlash(w, "error", "Passwords do not match.")
		http.Redirect(w, r, "/reset-password?token="+token, http.StatusSeeOther)
		return
	}

	tokenHash := sha256.Sum256([]byte(token))
	hashHex := hex.EncodeToString(tokenHash[:])

	rec, err := h.db.GetResetTokenByHash(hashHex)
	if err != nil || rec == nil {
		setFlash(w, "error", "This reset link is invalid. Please request a new one.")
		http.Redirect(w, r, "/forgot-password", http.StatusSeeOther)
		return
	}

	expiresAt, err := time.Parse(time.RFC3339, rec.ExpiresAt)
	if err != nil || expiresAt.Before(time.Now()) {
		setFlash(w, "error", "This reset link has expired. Please request a new one.")
		http.Redirect(w, r, "/forgot-password", http.StatusSeeOther)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		setFlash(w, "error", "Something went wrong. Please try again.")
		http.Redirect(w, r, "/reset-password?token="+token, http.StatusSeeOther)
		return
	}

	if err := h.db.UpdateUserPassword(rec.UserID, string(hash)); err != nil {
		setFlash(w, "error", "Failed to update password.")
		http.Redirect(w, r, "/reset-password?token="+token, http.StatusSeeOther)
		return
	}

	_ = h.db.MarkResetTokenUsed(rec.ID)

	setFlash(w, "success", "Password reset successfully. Please sign in.")
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
