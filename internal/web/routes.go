package web

import (
	"io/fs"
	"net/http"

	"github.com/devstroop/notalk/internal/config"
	"github.com/devstroop/notalk/internal/database"
	"github.com/devstroop/notalk/internal/service"
	smtpclient "github.com/devstroop/notalk/internal/smtp"
)

// Handler serves the web UI.
type Handler struct {
	mgr        *service.AccountManager
	db         *database.DB
	render     *Renderer
	secret     string
	version    string
	regEnabled bool
	smtp       *smtpclient.Client
	llmCfg     config.LLMConfig
}

// New creates a new web UI handler.
func New(mgr *service.AccountManager, db *database.DB, secret, version string, regEnabled bool, mailer *smtpclient.Client, llmCfg config.LLMConfig) *Handler {
	return &Handler{
		mgr:        mgr,
		db:         db,
		render:     NewRenderer(),
		secret:     secret,
		version:    version,
		regEnabled: regEnabled,
		smtp:       mailer,
		llmCfg:     llmCfg,
	}
}

// RegisterRoutes mounts all web UI routes on the mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// ── Static assets (served directly, no auth) ────
	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))

	// Favicon shortcut
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/static/favicon.svg", http.StatusMovedPermanently)
	})

	// ── All web pages go through a single inner mux ─
	inner := http.NewServeMux()

	// Public pages
	inner.HandleFunc("GET /login", h.LoginPage)
	inner.HandleFunc("POST /login", h.LoginSubmit)
	inner.HandleFunc("POST /logout", h.Logout)
	inner.HandleFunc("GET /about", h.AboutPage)
	inner.HandleFunc("GET /pricing", h.PricingPage)
	inner.HandleFunc("GET /terms", h.TermsPage)
	inner.HandleFunc("GET /privacy", h.PrivacyPage)
	inner.HandleFunc("GET /forgot-password", h.ForgotPasswordPage)
	inner.HandleFunc("POST /forgot-password", h.ForgotPasswordSubmit)
	inner.HandleFunc("GET /reset-password", h.ResetPasswordPage)
	inner.HandleFunc("POST /reset-password", h.ResetPasswordSubmit)
	if h.regEnabled {
		inner.HandleFunc("GET /register", h.RegisterPage)
		inner.HandleFunc("POST /register", h.RegisterSubmit)
	}

	// Dashboard
	inner.HandleFunc("GET /dashboard", h.Dashboard)

	// Accounts
	inner.HandleFunc("GET /accounts", h.AccountsList)
	inner.HandleFunc("POST /accounts", h.AccountsCreate)
	inner.HandleFunc("GET /accounts/{id}", h.AccountDetail)
	inner.HandleFunc("POST /accounts/{id}/update", h.AccountUpdate)
	inner.HandleFunc("POST /accounts/{id}/delete", h.AccountDeletePost)
	inner.HandleFunc("DELETE /accounts/{id}", h.AccountDelete)

	// Account webhook + proxy (JSON)
	inner.HandleFunc("PUT /accounts/{id}/webhook", h.AccountWebhookSet)
	inner.HandleFunc("DELETE /accounts/{id}/webhook", h.AccountWebhookDelete)
	inner.HandleFunc("PUT /accounts/{id}/proxy", h.AccountProxySet)
	inner.HandleFunc("DELETE /accounts/{id}/proxy", h.AccountProxyDelete)

	// Account partials (htmx)
	inner.HandleFunc("GET /accounts/{id}/session-status", h.AccountSessionStatus)
	inner.HandleFunc("GET /accounts/{id}/session-tab", h.AccountSessionTab)
	inner.HandleFunc("GET /accounts/{id}/qr", h.AccountQR)
	inner.HandleFunc("POST /accounts/{id}/pair", h.AccountPair)
	inner.HandleFunc("POST /accounts/{id}/disconnect", h.AccountDisconnect)

	// WhatsApp Web
	inner.HandleFunc("GET /whatsapp", h.Messaging)
	inner.HandleFunc("POST /whatsapp/{id}/send", h.MessageSend)
	inner.HandleFunc("GET /whatsapp/{id}/chats", h.MessagingChats)
	inner.HandleFunc("GET /whatsapp/{id}/messages", h.MessagingMessages)
	inner.HandleFunc("GET /whatsapp/{id}/contacts", h.MessagingContacts)
	inner.HandleFunc("GET /whatsapp/{id}/groups", h.MessagingGroups)
	inner.HandleFunc("GET /whatsapp/{id}/newsletters", h.MessagingNewsletters)
	inner.HandleFunc("GET /whatsapp/{id}/newsletter-messages", h.MessagingNewsletterMessages)
	inner.HandleFunc("POST /whatsapp/{id}/newsletter-follow", h.MessagingNewsletterFollow)
	inner.HandleFunc("POST /whatsapp/{id}/newsletter-unfollow", h.MessagingNewsletterUnfollow)
	inner.HandleFunc("POST /whatsapp/{id}/react", h.MessagingReact)
	inner.HandleFunc("POST /whatsapp/{id}/mark-read", h.MessagingMarkRead)
	inner.HandleFunc("POST /whatsapp/{id}/revoke", h.MessagingRevoke)

	// Admin
	inner.HandleFunc("GET /admin/users", h.UsersList)
	inner.HandleFunc("POST /admin/users", h.UsersCreate)
	inner.HandleFunc("POST /admin/users/{id}/update", h.UsersUpdate)
	inner.HandleFunc("POST /admin/users/{id}/delete", h.UsersDelete)
	inner.HandleFunc("POST /admin/users/{id}/reset-password", h.UsersResetPassword)
	inner.HandleFunc("GET /admin/roles", h.RolesList)
	inner.HandleFunc("POST /admin/roles", h.RolesCreate)
	inner.HandleFunc("POST /admin/roles/{id}/update", h.RolesUpdate)
	inner.HandleFunc("POST /admin/roles/{id}/delete", h.RolesDelete)

	// Admin Billing
	inner.HandleFunc("GET /admin/billing", h.BillingAdmin)                                            // redirects to /admin/billing/plans
	inner.HandleFunc("GET /admin/billing/plans", h.BillingPlansPage)
	inner.HandleFunc("GET /admin/billing/subscriptions", h.BillingSubscriptionsPage)
	inner.HandleFunc("GET /admin/billing/usage", h.BillingUsagePage)
	inner.HandleFunc("POST /admin/billing/stripe", h.PaymentGatewayUpdate)                            // legacy
	inner.HandleFunc("POST /admin/billing/plans", h.BillingPlanCreate)
	inner.HandleFunc("POST /admin/billing/plans/{id}/update", h.BillingPlanUpdate)
	inner.HandleFunc("POST /admin/billing/plans/{id}/delete", h.BillingPlanDelete)
	inner.HandleFunc("POST /admin/billing/subscriptions/{user_id}/assign", h.BillingAssignPlan)
	inner.HandleFunc("POST /admin/billing/subscriptions/{user_id}/delete", h.BillingDeleteSubscription)

	// Admin Configuration
	inner.HandleFunc("GET /admin/configuration", h.ConfigurationPage)

	// AI Settings (admin — separate page for LLM configuration)
	inner.HandleFunc("GET /settings/ai", h.AISettingsPage)
	inner.HandleFunc("POST /settings/ai", h.AISettingsUpdate)

	// AI Assistant (Mode 1 — personal chat)
	inner.HandleFunc("GET /assistant", h.AssistantPage)
	inner.HandleFunc("POST /assistant/chat", h.AssistantChat)
	inner.HandleFunc("POST /assistant/clear", h.AssistantClear)

	// Autopilot (Mode 2 — per-account auto-reply config)
	inner.HandleFunc("GET /accounts/{id}/autopilot", h.AutopilotPage)
	inner.HandleFunc("POST /accounts/{id}/autopilot", h.AutopilotSave)
	inner.HandleFunc("POST /accounts/{id}/autopilot/toggle", h.AutopilotToggle)
	// API Keys & MCP (all authenticated users)
	inner.HandleFunc("GET /api-keys", h.APIKeysList)
	inner.HandleFunc("POST /api-keys", h.APIKeysCreate)
	inner.HandleFunc("POST /api-keys/{id}/delete", h.APIKeysDelete)
	inner.HandleFunc("GET /mcp-server", h.MCPSettings)
	inner.HandleFunc("POST /mcp-server", h.MCPSettingsUpdate)

	// User Subscription
	inner.HandleFunc("GET /subscription", h.SubscriptionPage)

	// Settings
	inner.HandleFunc("GET /settings", h.Settings)
	inner.HandleFunc("POST /settings/password", h.ChangePassword)
	inner.HandleFunc("POST /settings/appearance", h.AppearanceUpdate)
	inner.HandleFunc("POST /settings/localization", h.LocalizationUpdate)
	inner.HandleFunc("POST /settings/payment-gateway", h.PaymentGatewayUpdate)

	// Root redirect
	inner.HandleFunc("GET /{$}", h.Root)

	// Catch-all 404 for unknown web paths
	inner.HandleFunc("/", h.NotFound)

	// Wrap with cookie auth (skips public paths)
	mux.Handle("/", WebAuth(h.secret, h.db, h.regEnabled, inner))
}

// page builds a PageData with common fields filled in.
func (h *Handler) page(w http.ResponseWriter, r *http.Request, title, activePage string, data any) PageData {
	return PageData{
		Title:    title + " — NoTalk",
		Heading:  title,
		Page:     activePage,
		Version:  h.version,
		Identity: getIdentity(r),
		Flash:    getFlash(w, r),
		Data:     data,
	}
}
