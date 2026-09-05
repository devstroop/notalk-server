package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/devstroop/notalk/internal/database"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// UsersList renders the users admin page.
func (h *Handler) UsersList(w http.ResponseWriter, r *http.Request) {
	users, _ := h.db.ListUsers()
	roles, _ := h.db.ListRoles()

	pd := h.page(w, r, "Users", "users", map[string]any{
		"Users": users,
		"Roles": roles,
	})
	h.render.Page(w, http.StatusOK, "users", pd)
}

// UsersCreate handles POST /admin/users.
func (h *Handler) UsersCreate(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")
	roleID := r.FormValue("role_id")

	if username == "" || password == "" || roleID == "" {
		setFlash(w, "error", "Username, password, and role are required.")
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		return
	}
	if len(password) < 8 {
		setFlash(w, "error", "Password must be at least 8 characters.")
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		setFlash(w, "error", "Failed to hash password.")
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		return
	}

	rec := &database.UserRecord{
		ID:           uuid.NewString(),
		Username:     username,
		Email:        email,
		PasswordHash: string(hash),
		RoleID:       roleID,
		Enabled:      true,
	}
	if err := h.db.CreateUser(rec); err != nil {
		setFlash(w, "error", fmt.Sprintf("Failed to create user: %s", err.Error()))
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		return
	}

	setFlash(w, "success", "User created.")
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// UsersUpdate handles POST /admin/users/{id}/update.
func (h *Handler) UsersUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	username := strings.TrimSpace(r.FormValue("username"))
	email := strings.TrimSpace(r.FormValue("email"))
	roleID := r.FormValue("role_id")
	enabled := r.FormValue("enabled") == "on"

	if username == "" {
		setFlash(w, "error", "Username cannot be empty.")
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		return
	}

	// Update role + enabled
	if err := h.db.UpdateUser(id, roleID, enabled); err != nil {
		setFlash(w, "error", fmt.Sprintf("Failed to update user: %s", err.Error()))
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		return
	}
	// Update username
	if err := h.db.UpdateUserUsername(id, username); err != nil {
		setFlash(w, "error", fmt.Sprintf("Failed to update username: %s", err.Error()))
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		return
	}
	// Update email
	if err := h.db.UpdateUserEmail(id, email); err != nil {
		setFlash(w, "error", fmt.Sprintf("Failed to update email: %s", err.Error()))
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		return
	}

	setFlash(w, "success", "User updated.")
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// UsersDelete handles POST /admin/users/{id}/delete.
func (h *Handler) UsersDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	identity := getIdentity(r)
	if identity != nil && identity.UserID == id {
		setFlash(w, "error", "You cannot delete your own account.")
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		return
	}

	if err := h.db.DeleteUser(id); err != nil {
		setFlash(w, "error", fmt.Sprintf("Failed to delete user: %s", err.Error()))
	} else {
		setFlash(w, "success", "User deleted.")
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// UsersResetPassword handles POST /admin/users/{id}/reset-password.
func (h *Handler) UsersResetPassword(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	newPw := r.FormValue("new_password")

	if len(newPw) < 8 {
		setFlash(w, "error", "Password must be at least 8 characters.")
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPw), bcrypt.DefaultCost)
	if err != nil {
		setFlash(w, "error", "Failed to hash password.")
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		return
	}

	if err := h.db.UpdateUserPassword(id, string(hash)); err != nil {
		setFlash(w, "error", "Failed to reset password.")
	} else {
		setFlash(w, "success", "Password reset successfully.")
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// RolesList renders the roles admin page.
func (h *Handler) RolesList(w http.ResponseWriter, r *http.Request) {
	roles, _ := h.db.ListRoles()

	// Attach permissions to each role for display
	type roleWithPerms struct {
		database.RoleRecord
		Permissions []string
		UserCount   int
	}
	enriched := make([]roleWithPerms, 0, len(roles))
	for _, role := range roles {
		perms, _ := h.db.GetRolePermissions(role.ID)
		count, _ := h.db.CountUsersByRole(role.ID)
		enriched = append(enriched, roleWithPerms{
			RoleRecord:  *role,
			Permissions: perms,
			UserCount:   count,
		})
	}

	pd := h.page(w, r, "Roles", "roles", map[string]any{
		"Roles": enriched,
	})
	h.render.Page(w, http.StatusOK, "roles", pd)
}

// RolesCreate handles POST /admin/roles.
func (h *Handler) RolesCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	permsRaw := strings.TrimSpace(r.FormValue("permissions"))

	if name == "" {
		setFlash(w, "error", "Role name is required.")
		http.Redirect(w, r, "/admin/roles", http.StatusSeeOther)
		return
	}

	rec := &database.RoleRecord{
		ID:          uuid.NewString(),
		Name:        name,
		Description: description,
	}
	if err := h.db.CreateRole(rec); err != nil {
		setFlash(w, "error", fmt.Sprintf("Failed to create role: %s", err.Error()))
		http.Redirect(w, r, "/admin/roles", http.StatusSeeOther)
		return
	}

	if permsRaw != "" {
		perms := splitPerms(permsRaw)
		_ = h.db.SetRolePermissions(rec.ID, perms)
	}

	setFlash(w, "success", "Role created.")
	http.Redirect(w, r, "/admin/roles", http.StatusSeeOther)
}

// RolesUpdate handles POST /admin/roles/{id}/update.
func (h *Handler) RolesUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	permsRaw := strings.TrimSpace(r.FormValue("permissions"))

	if err := h.db.UpdateRole(id, name, description); err != nil {
		setFlash(w, "error", fmt.Sprintf("Failed to update role: %s", err.Error()))
		http.Redirect(w, r, "/admin/roles", http.StatusSeeOther)
		return
	}

	perms := splitPerms(permsRaw)
	if err := h.db.SetRolePermissions(id, perms); err != nil {
		setFlash(w, "error", fmt.Sprintf("Failed to update permissions: %s", err.Error()))
	} else {
		setFlash(w, "success", "Role updated.")
	}
	http.Redirect(w, r, "/admin/roles", http.StatusSeeOther)
}

// RolesDelete handles POST /admin/roles/{id}/delete.
func (h *Handler) RolesDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	role, err := h.db.GetRole(id)
	if err != nil || role == nil {
		setFlash(w, "error", "Role not found.")
		http.Redirect(w, r, "/admin/roles", http.StatusSeeOther)
		return
	}
	if role.IsBuiltin {
		setFlash(w, "error", "Built-in roles cannot be deleted.")
		http.Redirect(w, r, "/admin/roles", http.StatusSeeOther)
		return
	}

	count, _ := h.db.CountUsersByRole(id)
	if count > 0 {
		setFlash(w, "error", fmt.Sprintf("Cannot delete: %d user(s) still assigned to this role.", count))
		http.Redirect(w, r, "/admin/roles", http.StatusSeeOther)
		return
	}

	if err := h.db.DeleteRole(id); err != nil {
		setFlash(w, "error", fmt.Sprintf("Failed to delete role: %s", err.Error()))
	} else {
		setFlash(w, "success", "Role deleted.")
	}
	http.Redirect(w, r, "/admin/roles", http.StatusSeeOther)
}

// splitPerms splits a comma/newline-separated permission string into a clean slice.
func splitPerms(raw string) []string {
	raw = strings.ReplaceAll(raw, "\n", ",")
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// APIKeysList renders the API keys admin page.
func (h *Handler) APIKeysList(w http.ResponseWriter, r *http.Request) {
	identity := getIdentity(r)
	var keys interface{}
	if identity != nil && identity.HasPermission("*") {
		keys, _ = h.db.ListAllAPIKeys()
	} else if identity != nil {
		keys, _ = h.db.ListAPIKeysByUser(identity.UserID)
	}

	// Also load accounts for the create dropdown
	list := h.mgr.ListAccounts()
	rows := make([]AccountRow, 0, len(list.Accounts))
	for _, a := range list.Accounts {
		if identity != nil && !identity.HasPermission("*") && a.UserID != identity.UserID {
			continue
		}
		rows = append(rows, h.infoToRow(a))
	}

	pd := h.page(w, r, "API Keys", "api-keys", map[string]any{
		"Keys":     keys,
		"Accounts": rows,
		"IsAdmin":  identity != nil && identity.HasPermission("*"),
	})
	h.render.Page(w, http.StatusOK, "api-keys", pd)
}

// APIKeysCreate handles POST /api-keys — creates a key and returns JSON with the plain key.
func (h *Handler) APIKeysCreate(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseMultipartForm(1 << 20)
	name := strings.TrimSpace(r.FormValue("name"))
	accountID := strings.TrimSpace(r.FormValue("account_id"))
	expiresAt := strings.TrimSpace(r.FormValue("expires_at"))

	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}

	identity := getIdentity(r)
	if identity == nil || identity.UserID == "system" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot create keys as system"})
		return
	}

	var accPtr *string
	if accountID != "" {
		accPtr = &accountID
	}
	var expPtr *string
	if expiresAt != "" {
		// Parse HTML datetime-local format → RFC3339
		t, err := time.Parse("2006-01-02T15:04", expiresAt)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid expiry date"})
			return
		}
		if t.Before(time.Now()) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expiry must be in the future"})
			return
		}
		s := t.UTC().Format(time.RFC3339)
		expPtr = &s
	}

	// Generate key
	rawBytes := make([]byte, 32)
	if _, err := rand.Read(rawBytes); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "key generation failed"})
		return
	}
	plainKey := "notalk_" + hex.EncodeToString(rawBytes)
	prefix := plainKey[:15]
	hash := sha256.Sum256([]byte(plainKey))
	keyHash := hex.EncodeToString(hash[:])

	rec := &database.APIKeyRecord{
		ID:        uuid.NewString(),
		UserID:    identity.UserID,
		AccountID: accPtr,
		Name:      name,
		Prefix:    prefix,
		KeyHash:   keyHash,
		ExpiresAt: expPtr,
		Enabled:   true,
	}
	if err := h.db.CreateAPIKey(rec); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create key"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"key": plainKey, "id": rec.ID, "name": name, "prefix": prefix})
}

// APIKeysDelete handles POST /api-keys/{id}/delete.
func (h *Handler) APIKeysDelete(w http.ResponseWriter, r *http.Request) {
	keyID := r.PathValue("id")
	if keyID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing key id"})
		return
	}

	rec, err := h.db.GetAPIKey(keyID)
	if err != nil || rec == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "key not found"})
		return
	}

	// Ownership check
	identity := getIdentity(r)
	if identity != nil && !identity.HasPermission("*") && rec.UserID != identity.UserID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}

	if err := h.db.DeleteAPIKey(keyID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to delete"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ─── MCP Settings ────────────────────────────────────────────

// MCPSettings renders the MCP configuration page.
func (h *Handler) MCPSettings(w http.ResponseWriter, r *http.Request) {
	enabled := h.db.GetSettingBool("mcp.enabled", true)

	identity := getIdentity(r)
	isAdmin := identity != nil && identity.HasPermission("*")

	// Count API keys — admins see all, regular users see their own
	allKeys, _ := h.db.ListAllAPIKeys()
	keyCount := len(allKeys)
	if !isAdmin && identity != nil {
		keyCount = 0
		for _, k := range allKeys {
			if k.UserID == identity.UserID {
				keyCount++
			}
		}
	}

	pd := h.page(w, r, "MCP Server", "mcp", map[string]any{
		"Enabled":     enabled,
		"Path":        "/mcp",
		"APIKeyCount": keyCount,
		"IsAdmin":     isAdmin,
	})
	h.render.Page(w, http.StatusOK, "mcp", pd)
}

// MCPSettingsUpdate handles POST /mcp-server (admin only).
func (h *Handler) MCPSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	identity := getIdentity(r)
	if identity == nil || !identity.HasPermission("*") {
		setFlash(w, "error", "Admin access required.")
		http.Redirect(w, r, "/mcp-server", http.StatusSeeOther)
		return
	}

	enabled := r.FormValue("enabled") == "on" || r.FormValue("enabled") == "true"

	val := "false"
	if enabled {
		val = "true"
	}
	if err := h.db.SetSetting("mcp.enabled", val); err != nil {
		setFlash(w, "error", "Failed to update MCP settings.")
		http.Redirect(w, r, "/mcp-server", http.StatusSeeOther)
		return
	}

	if enabled {
		setFlash(w, "success", "MCP server enabled.")
	} else {
		setFlash(w, "success", "MCP server disabled.")
	}
	http.Redirect(w, r, "/mcp-server", http.StatusSeeOther)
}

// Messaging renders the messaging page with sender account selection.
func (h *Handler) Messaging(w http.ResponseWriter, r *http.Request) {
	list := h.mgr.ListAccounts()

	identity := getIdentity(r)
	rows := make([]AccountRow, 0, len(list.Accounts))
	for _, a := range list.Accounts {
		if identity != nil && !identity.HasPermission("*") && a.UserID != identity.UserID {
			continue
		}
		rows = append(rows, h.infoToRow(a))
	}

	pd := h.page(w, r, "WhatsApp Web", "messaging", map[string]any{
		"Accounts": rows,
	})
	h.render.Page(w, http.StatusOK, "messaging", pd)
}

// Settings renders the settings page.
func (h *Handler) Settings(w http.ResponseWriter, r *http.Request) {
	identity := getIdentity(r)
	var user interface{}
	if identity != nil && identity.UserID != "system" {
		user, _ = h.db.GetUser(identity.UserID)
	}

	currency := h.db.GetSetting("localization.currency", "USD")
	timezone := h.db.GetSetting("localization.timezone", "UTC")
	appName := h.db.GetSetting("appearance.app_name", "")
	appTagline := h.db.GetSetting("appearance.app_tagline", "")

	stripeKey := h.db.GetSetting("billing.stripe_secret_key", "")
	stripeWebhook := h.db.GetSetting("billing.stripe_webhook_secret", "")
	razorpayKey := h.db.GetSetting("billing.razorpay_key_id", "")
	razorpaySecret := h.db.GetSetting("billing.razorpay_key_secret", "")
	payuKey := h.db.GetSetting("billing.payu_merchant_key", "")
	payuSalt := h.db.GetSetting("billing.payu_merchant_salt", "")
	activeGateway := h.db.GetSetting("billing.active_gateway", "")
	billingEnabled := h.db.GetSettingBool("billing.enabled", false)

	data := map[string]any{
		"User":             user,
		"Currency":         currency,
		"Timezone":         timezone,
		"AppName":          appName,
		"AppTagline":       appTagline,
		"BillingEnabled":   billingEnabled,
		"ActiveGateway":    activeGateway,
		"StripeKeySet":     stripeKey != "",
		"StripeWebhookSet": stripeWebhook != "",
		"RazorpayKeySet":   razorpayKey != "",
		"RazorpaySecretSet": razorpaySecret != "",
		"PayUKeySet":       payuKey != "",
		"PayUSaltSet":      payuSalt != "",
	}

	pd := h.page(w, r, "Settings", "settings", data)
	h.render.Page(w, http.StatusOK, "settings", pd)
}

// AppearanceUpdate handles POST /settings/appearance.
func (h *Handler) AppearanceUpdate(w http.ResponseWriter, r *http.Request) {
	appName := strings.TrimSpace(r.FormValue("app_name"))
	appTagline := strings.TrimSpace(r.FormValue("app_tagline"))

	_ = h.db.SetSetting("appearance.app_name", appName)
	_ = h.db.SetSetting("appearance.app_tagline", appTagline)

	setFlash(w, "success", "Appearance settings saved.")
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// LocalizationUpdate handles POST /settings/localization.
func (h *Handler) LocalizationUpdate(w http.ResponseWriter, r *http.Request) {
	validCurrencies := map[string]bool{
		"USD": true, "INR": true, "EUR": true, "GBP": true,
		"AED": true, "SGD": true, "AUD": true, "CAD": true, "JPY": true,
	}
	validTimezones := map[string]bool{
		"UTC": true, "Asia/Kolkata": true, "America/New_York": true,
		"America/Los_Angeles": true, "America/Chicago": true,
		"Europe/London": true, "Europe/Berlin": true,
		"Asia/Dubai": true, "Asia/Singapore": true,
		"Asia/Tokyo": true, "Australia/Sydney": true,
	}

	currency := r.FormValue("currency")
	if !validCurrencies[currency] {
		currency = "USD"
	}
	timezone := r.FormValue("timezone")
	if !validTimezones[timezone] {
		timezone = "UTC"
	}

	_ = h.db.SetSetting("localization.currency", currency)
	_ = h.db.SetSetting("localization.timezone", timezone)

	setFlash(w, "success", "Localization settings saved.")
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// ChangePassword handles POST /settings/password.
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	identity := getIdentity(r)
	if identity == nil || identity.UserID == "system" {
		setFlash(w, "error", "Password change is not available for this account.")
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}

	current := r.FormValue("current_password")
	newPw := r.FormValue("new_password")
	confirm := r.FormValue("confirm_password")

	if newPw != confirm {
		setFlash(w, "error", "New passwords do not match.")
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	if len(newPw) < 8 {
		setFlash(w, "error", "New password must be at least 8 characters.")
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}

	user, err := h.db.GetUser(identity.UserID)
	if err != nil || user == nil {
		setFlash(w, "error", "User not found.")
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(current)); err != nil {
		setFlash(w, "error", "Current password is incorrect.")
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPw), bcrypt.DefaultCost)
	if err != nil {
		setFlash(w, "error", "Failed to process new password.")
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}

	if err := h.db.UpdateUserPassword(identity.UserID, string(hash)); err != nil {
		setFlash(w, "error", "Failed to update password.")
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}

	setFlash(w, "success", "Password updated successfully.")
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// ── Billing Admin ────────────────────────────────────

// requireAdmin checks the caller has the wildcard permission and redirects otherwise.
func requireAdmin(w http.ResponseWriter, r *http.Request, dest string) bool {
	id := getIdentity(r)
	if id == nil || !id.HasPermission("*") {
		setFlash(w, "error", "Admin access required.")
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return false
	}
	return true
}

// BillingAdmin redirects to the Plans page (the primary billing section).
func (h *Handler) BillingAdmin(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, "/dashboard") {
		return
	}
	http.Redirect(w, r, "/admin/billing/plans", http.StatusSeeOther)
}

// shared row types used by the separate billing pages.
type subRow struct {
	UserID   string
	Username string
	PlanID   string
	PlanName string
	Status   string
	Period   string
}

type usageRow struct {
	UserID   string
	Username string
	Messages int
}

// BillingPlansPage renders GET /admin/billing/plans.
func (h *Handler) BillingPlansPage(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, "/dashboard") {
		return
	}
	plans, _ := h.db.ListPlans()
	pd := h.page(w, r, "Plans", "billing-plans", map[string]any{
		"Plans": plans,
	})
	h.render.Page(w, http.StatusOK, "billing-plans", pd)
}

// BillingSubscriptionsPage renders GET /admin/billing/subscriptions.
func (h *Handler) BillingSubscriptionsPage(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, "/dashboard") {
		return
	}
	plans, _ := h.db.ListPlans()
	subs, _ := h.db.ListSubscriptions()

	planMap := make(map[string]string, len(plans))
	for _, p := range plans {
		planMap[p.ID] = p.Name
	}
	rows := make([]subRow, 0, len(subs))
	for _, s := range subs {
		uname := s.UserID
		if u, err := h.db.GetUser(s.UserID); err == nil {
			uname = u.Username
		}
		pname := planMap[s.PlanID]
		if pname == "" {
			pname = s.PlanID
		}
		rows = append(rows, subRow{
			UserID: s.UserID, Username: uname,
			PlanID: s.PlanID, PlanName: pname,
			Status: s.Status, Period: s.CurrentPeriodEnd,
		})
	}

	var active, trial, canceled int
	for _, r := range rows {
		switch r.Status {
		case "active":
			active++
		case "trialing":
			trial++
		case "canceled":
			canceled++
		}
	}

	pd := h.page(w, r, "Subscriptions", "billing-subscriptions", map[string]any{
		"Subscriptions": rows,
		"Plans":         plans,
		"ActiveCount":   active,
		"TrialCount":    trial,
		"CanceledCount": canceled,
	})
	h.render.Page(w, http.StatusOK, "billing-subscriptions", pd)
}

// BillingUsagePage renders GET /admin/billing/usage.
func (h *Handler) BillingUsagePage(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, "/dashboard") {
		return
	}
	usage, _ := h.db.GetAllDailyUsage()
	rows := make([]usageRow, 0, len(usage))
	for _, u := range usage {
		uname := u.UserID
		if usr, err := h.db.GetUser(u.UserID); err == nil {
			uname = usr.Username
		}
		rows = append(rows, usageRow{UserID: u.UserID, Username: uname, Messages: u.Messages})
	}

	total := 0
	for _, r := range rows {
		total += r.Messages
	}
	avg := 0
	if len(rows) > 0 {
		avg = total / len(rows)
	}

	pd := h.page(w, r, "Usage", "billing-usage", map[string]any{
		"Usage":         rows,
		"TotalMessages": total,
		"AvgMessages":   avg,
	})
	h.render.Page(w, http.StatusOK, "billing-usage", pd)
}

// ConfigurationPage renders GET /admin/configuration (payment gateway config).
func (h *Handler) ConfigurationPage(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, "/dashboard") {
		return
	}
	stripeKey := h.db.GetSetting("billing.stripe_secret_key", "")
	stripeWebhook := h.db.GetSetting("billing.stripe_webhook_secret", "")
	razorpayKey := h.db.GetSetting("billing.razorpay_key_id", "")
	razorpaySecret := h.db.GetSetting("billing.razorpay_key_secret", "")
	payuKey := h.db.GetSetting("billing.payu_merchant_key", "")
	payuSalt := h.db.GetSetting("billing.payu_merchant_salt", "")
	activeGateway := h.db.GetSetting("billing.active_gateway", "")
	billingEnabled := h.db.GetSettingBool("billing.enabled", false)

	pd := h.page(w, r, "Configuration", "admin-config", map[string]any{
		"BillingEnabled":  billingEnabled,
		"ActiveGateway":   activeGateway,
		"StripeKeySet":    stripeKey != "",
		"StripeWebhookSet": stripeWebhook != "",
		"RazorpayKeySet":  razorpayKey != "",
		"RazorpaySecretSet": razorpaySecret != "",
		"PayUKeySet":      payuKey != "",
		"PayUSaltSet":     payuSalt != "",
	})
	h.render.Page(w, http.StatusOK, "admin-config", pd)
}

// PaymentGatewayUpdate handles POST /settings/payment-gateway.
func (h *Handler) PaymentGatewayUpdate(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, "/settings") {
		return
	}

	// Billing enabled toggle.
	if r.FormValue("billing_enabled") == "on" {
		_ = h.db.SetSetting("billing.enabled", "true")
	} else {
		_ = h.db.SetSetting("billing.enabled", "false")
	}

	// Active gateway.
	if gw := strings.TrimSpace(r.FormValue("active_gateway")); gw != "" {
		_ = h.db.SetSetting("billing.active_gateway", gw)
	}

	// Stripe.
	if key := strings.TrimSpace(r.FormValue("stripe_secret_key")); key != "" {
		_ = h.db.SetSetting("billing.stripe_secret_key", key)
	}
	if secret := strings.TrimSpace(r.FormValue("stripe_webhook_secret")); secret != "" {
		_ = h.db.SetSetting("billing.stripe_webhook_secret", secret)
	}

	// Razorpay.
	if key := strings.TrimSpace(r.FormValue("razorpay_key_id")); key != "" {
		_ = h.db.SetSetting("billing.razorpay_key_id", key)
	}
	if secret := strings.TrimSpace(r.FormValue("razorpay_key_secret")); secret != "" {
		_ = h.db.SetSetting("billing.razorpay_key_secret", secret)
	}

	// PayU Money.
	if key := strings.TrimSpace(r.FormValue("payu_merchant_key")); key != "" {
		_ = h.db.SetSetting("billing.payu_merchant_key", key)
	}
	if salt := strings.TrimSpace(r.FormValue("payu_merchant_salt")); salt != "" {
		_ = h.db.SetSetting("billing.payu_merchant_salt", salt)
	}

	setFlash(w, "success", "Payment gateway settings updated.")
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// BillingPlanCreate handles POST /admin/billing/plans.
func (h *Handler) BillingPlanCreate(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, "/admin/billing") {
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		setFlash(w, "error", "Plan name is required.")
		http.Redirect(w, r, "/admin/billing", http.StatusSeeOther)
		return
	}

	price := 0
	if v := r.FormValue("price_cents"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &price); err != nil {
			price = 0
		}
	}
	daily := 0
	if v := r.FormValue("daily_messages"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &daily); err != nil {
			daily = 0
		}
	}
	maxAcct := 0
	if v := r.FormValue("max_accounts"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &maxAcct); err != nil {
			maxAcct = 0
		}
	}

	limits := fmt.Sprintf(`{"daily_messages":%d,"max_accounts":%d,"api_access":%t,"mcp_access":%t,"webhooks":%t,"copilot":%t,"autopilot":%t}`,
		daily, maxAcct,
		r.FormValue("api_access") == "on",
		r.FormValue("mcp_access") == "on",
		r.FormValue("webhooks") == "on",
		r.FormValue("copilot") == "on",
		r.FormValue("autopilot") == "on",
	)

	rec := &database.PlanRecord{
		ID:          strings.TrimSpace(r.FormValue("id")),
		Name:        name,
		Description: strings.TrimSpace(r.FormValue("description")),
		PriceCents:  price,
		Interval:    "month",
		Limits:      limits,
		IsDefault:   r.FormValue("is_default") == "on",
	}
	if rec.ID == "" {
		rec.ID = database.GenerateID()
	}

	if err := h.db.CreatePlan(rec); err != nil {
		setFlash(w, "error", fmt.Sprintf("Failed to create plan: %s", err.Error()))
	} else {
		setFlash(w, "success", "Plan created.")
	}
	http.Redirect(w, r, "/admin/billing", http.StatusSeeOther)
}

// BillingPlanUpdate handles POST /admin/billing/plans/{id}/update.
func (h *Handler) BillingPlanUpdate(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, "/admin/billing") {
		return
	}
	id := r.PathValue("id")
	existing, err := h.db.GetPlan(id)
	if err != nil {
		setFlash(w, "error", "Plan not found.")
		http.Redirect(w, r, "/admin/billing", http.StatusSeeOther)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = existing.Name
	}

	price := existing.PriceCents
	if v := r.FormValue("price_cents"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &price); err != nil {
			price = existing.PriceCents
		}
	}
	daily := 0
	if v := r.FormValue("daily_messages"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &daily); err != nil {
			daily = 0
		}
	}
	maxAcct := 0
	if v := r.FormValue("max_accounts"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &maxAcct); err != nil {
			maxAcct = 0
		}
	}

	limits := fmt.Sprintf(`{"daily_messages":%d,"max_accounts":%d,"api_access":%t,"mcp_access":%t,"webhooks":%t,"copilot":%t,"autopilot":%t}`,
		daily, maxAcct,
		r.FormValue("api_access") == "on",
		r.FormValue("mcp_access") == "on",
		r.FormValue("webhooks") == "on",
		r.FormValue("copilot") == "on",
		r.FormValue("autopilot") == "on",
	)

	rec := &database.PlanRecord{
		ID:          id,
		Name:        name,
		Description: strings.TrimSpace(r.FormValue("description")),
		PriceCents:  price,
		Interval:    "month",
		Limits:      limits,
		IsDefault:   r.FormValue("is_default") == "on",
	}
	if err := h.db.UpdatePlan(rec); err != nil {
		setFlash(w, "error", fmt.Sprintf("Failed to update plan: %s", err.Error()))
	} else {
		setFlash(w, "success", "Plan updated.")
	}
	http.Redirect(w, r, "/admin/billing", http.StatusSeeOther)
}

// BillingPlanDelete handles POST /admin/billing/plans/{id}/delete.
func (h *Handler) BillingPlanDelete(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, "/admin/billing") {
		return
	}
	id := r.PathValue("id")
	if err := h.db.DeletePlan(id); err != nil {
		setFlash(w, "error", fmt.Sprintf("Cannot delete plan: %s", err.Error()))
	} else {
		setFlash(w, "success", "Plan deleted.")
	}
	http.Redirect(w, r, "/admin/billing", http.StatusSeeOther)
}

// BillingAssignPlan handles POST /admin/billing/subscriptions/{user_id}/assign.
func (h *Handler) BillingAssignPlan(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, "/admin/billing") {
		return
	}
	userID := r.PathValue("user_id")
	planID := r.FormValue("plan_id")
	if planID == "" {
		setFlash(w, "error", "Plan is required.")
		http.Redirect(w, r, "/admin/billing", http.StatusSeeOther)
		return
	}

	now := time.Now().UTC()
	rec := &database.SubscriptionRecord{
		ID:                 database.GenerateID(),
		UserID:             userID,
		PlanID:             planID,
		Status:             "active",
		CurrentPeriodStart: now.Format(time.RFC3339),
		CurrentPeriodEnd:   now.AddDate(0, 1, 0).Format(time.RFC3339),
		CreatedAt:          now.Format(time.RFC3339),
	}
	if err := h.db.UpsertSubscription(rec); err != nil {
		setFlash(w, "error", fmt.Sprintf("Failed to assign plan: %s", err.Error()))
	} else {
		setFlash(w, "success", "Plan assigned.")
	}
	http.Redirect(w, r, "/admin/billing", http.StatusSeeOther)
}

// BillingDeleteSubscription handles POST /admin/billing/subscriptions/{user_id}/delete.
func (h *Handler) BillingDeleteSubscription(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, "/admin/billing") {
		return
	}
	userID := r.PathValue("user_id")
	if err := h.db.DeleteSubscription(userID); err != nil {
		setFlash(w, "error", fmt.Sprintf("Failed: %s", err.Error()))
	} else {
		setFlash(w, "success", "Subscription removed.")
	}
	http.Redirect(w, r, "/admin/billing", http.StatusSeeOther)
}

// ── User Subscription Page ──────────────────────────

// SubscriptionPage renders the user-facing subscription view.
func (h *Handler) SubscriptionPage(w http.ResponseWriter, r *http.Request) {
	identity := getIdentity(r)
	if identity == nil || identity.UserID == "system" {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}

	userID := identity.UserID

	// Current plan & limits.
	limits, planID, _ := h.db.GetUserPlanLimits(userID)
	plan, _ := h.db.GetPlan(planID)

	// Subscription record (may not exist).
	sub, _ := h.db.GetSubscription(userID)

	// Usage.
	dailyUsage, _ := h.db.GetDailyUsage(userID)
	accountCount, _ := h.db.CountUserAccounts(userID)

	// Available plans for comparison.
	allPlans, _ := h.db.ListPlans()

	// Build view-friendly plan info.
	type PlanView struct {
		ID            string
		Name          string
		Description   string
		PriceCents    int
		DailyMessages int
		MaxAccounts   int
		APIAccess     bool
		MCPAccess     bool
		Webhooks      bool
		Copilot       bool
		Autopilot     bool
		IsCurrent     bool
	}
	planViews := make([]PlanView, 0, len(allPlans))
	for _, p := range allPlans {
		lim := p.PlanLimits()
		planViews = append(planViews, PlanView{
			ID:            p.ID,
			Name:          p.Name,
			Description:   p.Description,
			PriceCents:    p.PriceCents,
			DailyMessages: lim.DailyMessages,
			MaxAccounts:   lim.MaxAccounts,
			APIAccess:     lim.APIAccess,
			MCPAccess:     lim.MCPAccess,
			Webhooks:      lim.Webhooks,
			Copilot:       lim.Copilot,
			Autopilot:     lim.Autopilot,
			IsCurrent:     p.ID == planID,
		})
	}

	// Subscription status.
	subStatus := "none"
	subPeriodEnd := ""
	if sub != nil {
		subStatus = sub.Status
		subPeriodEnd = sub.CurrentPeriodEnd
	}

	pd := h.page(w, r, "Subscription", "subscription", map[string]any{
		"PlanID":        planID,
		"PlanName":      "",
		"Limits":        limits,
		"DailyUsage":    dailyUsage,
		"AccountCount":  accountCount,
		"SubStatus":     subStatus,
		"SubPeriodEnd":  subPeriodEnd,
		"Plans":         planViews,
	})
	if plan != nil {
		pd.Data.(map[string]any)["PlanName"] = plan.Name
	}
	h.render.Page(w, http.StatusOK, "subscription", pd)
}

// NotFound renders the styled 404 error page.
func (h *Handler) NotFound(w http.ResponseWriter, r *http.Request) {
	pd := PageData{
		Title: "Not Found — NoTalk",
		Page:  "error",
		Data: map[string]any{
			"Code":    "404",
			"Title":   "Page not found",
			"Message": "The page you're looking for doesn't exist or has been moved.",
		},
	}
	h.render.Page(w, http.StatusNotFound, "error", pd)
}
