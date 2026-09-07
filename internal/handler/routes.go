package handler

import (
	"net/http"

	"github.com/devstroop/notalk/internal/database"
	"github.com/devstroop/notalk/internal/middleware"
	"github.com/devstroop/notalk/internal/service"
)

// API groups all route handlers.
type API struct {
	mgr            *service.AccountManager
	db             *database.DB
}

// NewAPI creates a new API handler group.
func NewAPI(mgr *service.AccountManager, db *database.DB) *API {
	return &API{mgr: mgr, db: db}
}


// RegisterRoutes wires every endpoint into the mux.
// Core paths are under /api/v1/accounts; see also RegisterRBACRoutes.
func (a *API) RegisterRoutes(mux *http.ServeMux) {
	perm := middleware.RequirePermission
	base := "/api/v1/accounts"
	acct := base + "/{account_id}"

	// ── Account CRUD ────────────────────────────────
	mux.HandleFunc("GET "+base, perm("accounts:read", a.ListAccounts))
	mux.HandleFunc("POST "+base, perm("accounts:write", a.CreateAccount))
	mux.HandleFunc("GET "+acct, perm("accounts:read", a.GetAccount))
	mux.HandleFunc("PATCH "+acct, perm("accounts:write", a.UpdateAccount))
	mux.HandleFunc("DELETE "+acct, perm("accounts:write", a.DeleteAccount))

	// ── Session (auth/linking lifecycle) ────────────
	mux.HandleFunc("GET "+acct+"/session", perm("session:read", a.GetSession))
	mux.HandleFunc("GET "+acct+"/session/qr", perm("session:write", a.GetQR))
	mux.HandleFunc("POST "+acct+"/session/pair", perm("session:write", a.PairPhone))
	mux.HandleFunc("DELETE "+acct+"/session", perm("session:write", a.DeleteSession))

	// ── Proxy ───────────────────────────────────────
	mux.HandleFunc("GET "+acct+"/proxy", perm("proxy:read", a.GetProxy))
	mux.HandleFunc("PUT "+acct+"/proxy", perm("proxy:write", a.SetProxy))
	mux.HandleFunc("DELETE "+acct+"/proxy", perm("proxy:write", a.DeleteProxy))

	// ── Messaging ───────────────────────────────────
	mux.HandleFunc("GET "+acct+"/messages", perm("messages:read", a.GetMessages))
	mux.HandleFunc("POST "+acct+"/messages", perm("messages:write", a.SendMessage))
	mux.HandleFunc("POST "+acct+"/messages/reactions", perm("messages:write", a.ReactMessage))
	mux.HandleFunc("POST "+acct+"/messages/mark-read", perm("messages:write", a.MarkRead))
	mux.HandleFunc("DELETE "+acct+"/messages/{message_id}", perm("messages:write", a.RevokeMessage))

	// ── Webhook ─────────────────────────────────────
	mux.HandleFunc("GET "+acct+"/webhook", perm("webhook:read", a.GetWebhook))
	mux.HandleFunc("PUT "+acct+"/webhook", perm("webhook:write", a.SetWebhook))
	mux.HandleFunc("DELETE "+acct+"/webhook", perm("webhook:write", a.DeleteWebhook))

	// ── Chats ───────────────────────────────────────
	mux.HandleFunc("GET "+acct+"/chats", perm("chats:read", a.ListChats))

	// ── Contacts ────────────────────────────────────
	mux.HandleFunc("GET "+acct+"/contacts", perm("contacts:read", a.ListContacts))
	mux.HandleFunc("POST "+acct+"/contacts/check", perm("contacts:write", a.CheckContacts))
	mux.HandleFunc("GET "+acct+"/contacts/{jid}", perm("contacts:read", a.GetContact))

	// ── Groups ──────────────────────────────────────
	mux.HandleFunc("GET "+acct+"/groups", perm("groups:read", a.ListGroups))
	mux.HandleFunc("POST "+acct+"/groups", perm("groups:write", a.CreateGroup))
	mux.HandleFunc("GET "+acct+"/groups/{jid}", perm("groups:read", a.GetGroup))
	mux.HandleFunc("PATCH "+acct+"/groups/{jid}", perm("groups:write", a.UpdateGroup))
	mux.HandleFunc("DELETE "+acct+"/groups/{jid}", perm("groups:write", a.LeaveGroup))
	mux.HandleFunc("GET "+acct+"/groups/{jid}/invite", perm("groups:read", a.GetGroupInvite))
	mux.HandleFunc("POST "+acct+"/groups/{jid}/participants", perm("groups:write", a.UpdateGroupParticipants))

	// ── Newsletters (Channels) ──────────────────────
	mux.HandleFunc("GET "+acct+"/newsletters", perm("newsletters:read", a.ListNewsletters))
	mux.HandleFunc("POST "+acct+"/newsletters/follow", perm("newsletters:write", a.FollowNewsletter))
	mux.HandleFunc("POST "+acct+"/newsletters/unfollow", perm("newsletters:write", a.UnfollowNewsletter))
	mux.HandleFunc("GET "+acct+"/newsletters/{jid}", perm("newsletters:read", a.GetNewsletter))
	mux.HandleFunc("GET "+acct+"/newsletters/{jid}/messages", perm("newsletters:read", a.GetNewsletterMessages))
	mux.HandleFunc("POST "+acct+"/newsletters/{jid}/mute", perm("newsletters:write", a.MuteNewsletter))

	// ── Presence ────────────────────────────────────
	mux.HandleFunc("POST "+acct+"/presence", perm("presence:write", a.SendPresence))

	// ── Profile ─────────────────────────────────────
	mux.HandleFunc("GET "+acct+"/profile", perm("profile:read", a.GetProfile))
	mux.HandleFunc("PATCH "+acct+"/profile", perm("profile:write", a.UpdateProfile))
}

// RegisterRBACRoutes wires user and role management endpoints.
func RegisterRBACRoutes(mux *http.ServeMux, db *database.DB) {
	perm := middleware.RequirePermission

	// Users
	users := NewUserHandler(db)
	mux.HandleFunc("GET /api/v1/users", perm("users:*", users.ListUsers))
	mux.HandleFunc("POST /api/v1/users", perm("users:*", users.CreateUser))
	mux.HandleFunc("GET /api/v1/users/{user_id}", users.GetUser) // self-access handled inside
	mux.HandleFunc("PATCH /api/v1/users/{user_id}", perm("users:*", users.UpdateUser))
	mux.HandleFunc("DELETE /api/v1/users/{user_id}", perm("users:*", users.DeleteUser))

	// Roles
	roles := NewRoleHandler(db)
	mux.HandleFunc("GET /api/v1/roles", perm("roles:read", roles.ListRoles))
	mux.HandleFunc("POST /api/v1/roles", perm("roles:write", roles.CreateRole))
	mux.HandleFunc("GET /api/v1/roles/{role_id}", perm("roles:read", roles.GetRole))
	mux.HandleFunc("PATCH /api/v1/roles/{role_id}", perm("roles:write", roles.UpdateRole))
	mux.HandleFunc("DELETE /api/v1/roles/{role_id}", perm("roles:write", roles.DeleteRole))

	// API Keys
	apiKeys := NewAPIKeyHandler(db)
	mux.HandleFunc("GET /api/v1/api-keys", perm("api-keys:read", apiKeys.ListAPIKeys))
	mux.HandleFunc("POST /api/v1/api-keys", perm("api-keys:write", apiKeys.CreateAPIKey))
	mux.HandleFunc("DELETE /api/v1/api-keys/{key_id}", perm("api-keys:write", apiKeys.DeleteAPIKey))

	// MCP Settings (read for all, write for admin)
	mcpH := NewMCPHandler(db)
	mux.HandleFunc("GET /api/v1/mcp", perm("mcp:read", mcpH.GetMCPSettings))
	mux.HandleFunc("PATCH /api/v1/mcp", perm("*", mcpH.UpdateMCPSettings))

	// Billing (admin only)
	billing := NewBillingHandler(db)
	mux.HandleFunc("GET /api/v1/billing/plans", perm("*", billing.ListPlans))
	mux.HandleFunc("POST /api/v1/billing/plans", perm("*", billing.CreatePlan))
	mux.HandleFunc("PATCH /api/v1/billing/plans/{id}", perm("*", billing.UpdatePlan))
	mux.HandleFunc("PUT /api/v1/billing/plans/{id}", perm("*", billing.UpdatePlan))
	mux.HandleFunc("DELETE /api/v1/billing/plans/{id}", perm("*", billing.DeletePlan))
	mux.HandleFunc("GET /api/v1/billing/subscriptions", perm("*", billing.ListSubscriptions))
	mux.HandleFunc("POST /api/v1/billing/subscriptions/{user_id}/assign", perm("*", billing.AssignPlan))
	mux.HandleFunc("DELETE /api/v1/billing/subscriptions/{user_id}", perm("*", billing.DeleteSubscription))
	mux.HandleFunc("GET /api/v1/billing/usage", perm("*", billing.GetUsage))
	mux.HandleFunc("GET /api/v1/billing/config", perm("*", billing.GetBillingConfig))
}

// RegisterCacheRoutes wires opt-in cache endpoints (overlay loader).
// No special board — just status + flush with backdrop feedback.
func RegisterCacheRoutes(mux *http.ServeMux, h *CacheHandler) {
	perm := middleware.RequirePermission
	mux.HandleFunc("GET /api/v1/cache/status", perm("*", h.Status))
	mux.HandleFunc("POST /api/v1/cache/flush", perm("*", h.Flush))
}
