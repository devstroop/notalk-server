package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/devstroop/notalk/internal/database"
	"github.com/devstroop/notalk/internal/model"
	"github.com/devstroop/notalk/internal/service"
	qrcode "github.com/skip2/go-qrcode"
)

// DashboardData holds data for the dashboard page.
type DashboardData struct {
	TotalAccounts int
	Connected     int
	Disconnected  int
	TotalUsers    int
	Accounts      []AccountRow

	// Billing
	BillingEnabled bool
	PlanName       string
	PlanID         string
	SubStatus      string
	SubPeriodEnd   string
	DailyUsage     int
	DailyLimit     int
	// Admin-only billing stats
	ActiveSubs    int
	TrialSubs     int
	CanceledSubs  int
	TotalMessages int
}

// AccountRow is a simplified account for display.
type AccountRow struct {
	ID          string
	AccountName string
	PhoneNumber string
	Connected   bool
	CreatedAt   string
}

// infoToRow converts a model.AccountInfo to an AccountRow.
func (h *Handler) infoToRow(a model.AccountInfo) AccountRow {
	phone := ""
	if a.PhoneNumber != nil {
		phone = *a.PhoneNumber
	}
	acct := h.mgr.GetAccount(a.ID)
	isConn := acct != nil && acct.IsLoggedIn()
	return AccountRow{
		ID:          a.ID,
		AccountName: a.AccountName,
		PhoneNumber: phone,
		Connected:   isConn,
		CreatedAt:   a.CreatedAt.Format(time.RFC3339),
	}
}

// Dashboard renders the dashboard page.
func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	list := h.mgr.ListAccounts()
	identity := getIdentity(r)

	connected := 0
	rows := make([]AccountRow, 0, len(list.Accounts))
	for _, a := range list.Accounts {
		row := h.infoToRow(a)
		if row.Connected {
			connected++
		}
		rows = append(rows, row)
	}

	userCount := 0
	if users, err := h.db.ListUsers(); err == nil {
		userCount = len(users)
	}

	data := DashboardData{
		TotalAccounts: list.Total,
		Connected:     connected,
		Disconnected:  list.Total - connected,
		TotalUsers:    userCount,
		Accounts:      rows,
	}

	// Only show last 5 on dashboard
	if len(data.Accounts) > 5 {
		data.Accounts = data.Accounts[:5]
	}

	// Billing data
	data.BillingEnabled = h.db.GetSettingBool("billing.enabled", false)
	if identity != nil && identity.UserID != "system" {
		limits, planID, _ := h.db.GetUserPlanLimits(identity.UserID)
		data.PlanID = planID
		data.DailyLimit = limits.DailyMessages
		if usage, err := h.db.GetDailyUsage(identity.UserID); err == nil {
			data.DailyUsage = usage
		}
		if plan, err := h.db.GetPlan(planID); err == nil && plan != nil {
			data.PlanName = plan.Name
		}
		if sub, err := h.db.GetSubscription(identity.UserID); err == nil && sub != nil {
			data.SubStatus = sub.Status
			data.SubPeriodEnd = sub.CurrentPeriodEnd
		}

		// Admin: aggregate billing stats
		if identity.HasPermission("*") {
			if subs, err := h.db.ListSubscriptions(); err == nil {
				for _, s := range subs {
					switch s.Status {
					case "active":
						data.ActiveSubs++
					case "trialing":
						data.TrialSubs++
					case "canceled":
						data.CanceledSubs++
					}
				}
			}
			if usage, err := h.db.GetAllDailyUsage(); err == nil {
				for _, u := range usage {
					data.TotalMessages += u.Messages
				}
			}
		}
	}

	pd := h.page(w, r, "Dashboard", "dashboard", data)
	h.render.Page(w, http.StatusOK, "dashboard", pd)
}

// AccountsList renders the accounts list page.
func (h *Handler) AccountsList(w http.ResponseWriter, r *http.Request) {
	list := h.mgr.ListAccounts()

	identity := getIdentity(r)
	rows := make([]AccountRow, 0, len(list.Accounts))
	for _, a := range list.Accounts {
		// Non-admin: filter to own accounts
		if identity != nil && !identity.HasPermission("*") && a.UserID != identity.UserID {
			continue
		}
		rows = append(rows, h.infoToRow(a))
	}

	pd := h.page(w, r, "Accounts", "accounts", map[string]any{
		"Accounts": rows,
	})
	h.render.Page(w, http.StatusOK, "accounts", pd)
}

// AccountsCreate handles POST /accounts (create new account).
func (h *Handler) AccountsCreate(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("account_name")
	phone := r.FormValue("phone_number")

	if name == "" || phone == "" {
		setFlash(w, "error", "Account name and phone number are required.")
		http.Redirect(w, r, "/accounts", http.StatusSeeOther)
		return
	}

	identity := getIdentity(r)
	req := model.CreateAccountRequest{
		PhoneNumber: phone,
		AccountName: name,
	}
	if identity != nil && identity.UserID != "system" {
		req.UserID = identity.UserID
	}

	_, err := h.mgr.CreateAccount(req)
	if err != nil {
		setFlash(w, "error", fmt.Sprintf("Failed to create account: %s", err.Error()))
		http.Redirect(w, r, "/accounts", http.StatusSeeOther)
		return
	}

	setFlash(w, "success", "Account created successfully.")
	http.Redirect(w, r, "/accounts", http.StatusSeeOther)
}

// AccountDetail renders the account detail page.
func (h *Handler) AccountDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	acct := h.mgr.GetAccount(id)
	if acct == nil {
		setFlash(w, "error", "Account not found.")
		http.Redirect(w, r, "/accounts", http.StatusSeeOther)
		return
	}

	info := acct.Info()
	phone := ""
	if info.PhoneNumber != nil {
		phone = *info.PhoneNumber
	}

	// Webhook config → JSON for Alpine
	whJSON := "null"
	if cfg, err := h.db.GetWebhookConfig(id); err == nil && cfg != nil {
		var events []string
		if cfg.Events != "" {
			for _, p := range strings.Split(cfg.Events, ",") {
				p = strings.TrimSpace(p)
				if p != "" {
					events = append(events, p)
				}
			}
		}
		if events == nil {
			events = []string{}
		}
		if b, err := json.Marshal(map[string]any{"url": cfg.URL, "events": events, "enabled": cfg.Enabled}); err == nil {
			whJSON = string(b)
		}
	}

	// Proxy config → JSON for Alpine
	pxJSON := "null"
	if cfg, err := h.mgr.GetProxy(id); err == nil && cfg != nil {
		if b, err := json.Marshal(cfg.ToModel()); err == nil {
			pxJSON = string(b)
		}
	}

	pd := h.page(w, r, info.AccountName, "account-detail", map[string]any{
		"Account": map[string]any{
			"ID":          info.ID,
			"AccountName": info.AccountName,
			"PhoneNumber": phone,
			"Connected":   acct.IsLoggedIn(),
		},
		"WebhookJSON": template.JS(whJSON),
		"ProxyJSON":   template.JS(pxJSON),
	})
	h.render.Page(w, http.StatusOK, "account-detail", pd)
}

// AccountUpdate handles POST /accounts/{id}/update.
func (h *Handler) AccountUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := strings.TrimSpace(r.FormValue("account_name"))
	phone := strings.TrimSpace(r.FormValue("phone_number"))

	if name != "" {
		if err := h.mgr.UpdateAccountName(id, name); err != nil {
			setFlash(w, "error", err.Error())
			http.Redirect(w, r, "/accounts/"+id, http.StatusSeeOther)
			return
		}
	}
	if phone != "" {
		if err := h.mgr.UpdatePhoneNumber(id, phone); err != nil {
			setFlash(w, "error", err.Error())
			http.Redirect(w, r, "/accounts/"+id, http.StatusSeeOther)
			return
		}
	}

	setFlash(w, "success", "Account updated.")
	http.Redirect(w, r, "/accounts/"+id, http.StatusSeeOther)
}

// AccountDeletePost handles POST /accounts/{id}/delete (form-based delete).
func (h *Handler) AccountDeletePost(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, err := h.mgr.DeleteAccount(id, true)
	if err != nil {
		setFlash(w, "error", err.Error())
		http.Redirect(w, r, "/accounts/"+id, http.StatusSeeOther)
		return
	}
	setFlash(w, "success", "Account deleted.")
	http.Redirect(w, r, "/accounts", http.StatusSeeOther)
}

// AccountWebhookSet handles PUT /accounts/{id}/webhook.
func (h *Handler) AccountWebhookSet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		URL     string   `json:"url"`
		Secret  string   `json:"secret,omitempty"`
		Events  []string `json:"events,omitempty"`
		Enabled *bool    `json:"enabled,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url is required"})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	rec := &database.WebhookConfigRecord{
		AccountID: id,
		URL:       req.URL,
		Secret:    req.Secret,
		Events:    strings.Join(req.Events, ","),
		Enabled:   enabled,
	}
	if err := h.db.UpsertWebhookConfig(rec); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	events := req.Events
	if events == nil {
		events = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": req.URL, "events": events, "enabled": enabled})
}

// AccountWebhookDelete handles DELETE /accounts/{id}/webhook.
func (h *Handler) AccountWebhookDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.db.DeleteWebhookConfig(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// AccountProxySet handles PUT /accounts/{id}/proxy.
func (h *Handler) AccountProxySet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Protocol string `json:"protocol"`
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Username string `json:"username,omitempty"`
		Password string `json:"password,omitempty"`
		Enabled  *bool  `json:"enabled,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.Host == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host is required"})
		return
	}
	proto := strings.ToLower(req.Protocol)
	switch proto {
	case "http", "https", "socks5":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "protocol must be http, https, or socks5"})
		return
	}
	if req.Port < 1 || req.Port > 65535 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "port must be 1-65535"})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	cfg := &service.ProxyConfig{
		Protocol: proto,
		Host:     req.Host,
		Port:     req.Port,
		Username: req.Username,
		Password: req.Password,
		Enabled:  enabled,
	}
	if err := h.mgr.SetProxy(id, cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, cfg.ToModel())
}

// AccountDelete handles DELETE /accounts/{id}.
func (h *Handler) AccountDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, err := h.mgr.DeleteAccount(id, false)
	if err != nil {
		if isHTMX(r) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		setFlash(w, "error", err.Error())
		http.Redirect(w, r, "/accounts", http.StatusSeeOther)
		return
	}

	if isHTMX(r) {
		hxRedirect(w, "/accounts")
		return
	}
	setFlash(w, "success", "Account deleted.")
	http.Redirect(w, r, "/accounts", http.StatusSeeOther)
}

// AccountProxyDelete handles DELETE /accounts/{id}/proxy.
func (h *Handler) AccountProxyDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.mgr.DeleteProxy(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// AccountSessionStatus returns a badge partial for htmx polling.
func (h *Handler) AccountSessionStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	acct := h.mgr.GetAccount(id)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if acct != nil && acct.IsLoggedIn() {
		_, _ = fmt.Fprint(w, `<span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium bg-green-100 text-green-800">Connected</span>`)
	} else {
		_, _ = fmt.Fprint(w, `<span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium bg-gray-100 text-gray-800">Disconnected</span>`)
	}
}

// AccountSessionTab returns the full session tab content based on connection state.
func (h *Handler) AccountSessionTab(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	acct := h.mgr.GetAccount(id)
	if acct == nil {
		http.Error(w, "account not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if acct.IsLoggedIn() {
		// Connected state — show status info and disconnect button
		_, _ = fmt.Fprintf(w, `<div class="flex flex-col items-center justify-center py-10 space-y-4">
			<div class="w-20 h-20 bg-green-50 rounded-2xl flex items-center justify-center">
				<svg class="w-10 h-10 text-green-600" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24"><path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/><path d="m9 11 3 3L22 4"/></svg>
			</div>
			<div class="text-center">
				<p class="text-lg font-semibold text-green-700">Session Active</p>
				<p class="text-sm text-gray-500 mt-1">WhatsApp is connected and ready to send/receive messages.</p>
			</div>
			<button hx-post="/accounts/%s/disconnect" hx-target="#session-content" hx-swap="innerHTML"
				hx-confirm="Disconnect this session?"
				class="inline-flex items-center gap-2 px-4 py-2 text-sm font-medium text-red-600 border border-red-200 rounded-lg hover:bg-red-50 transition-colors mt-2">
				<svg class="w-4 h-4" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24"><path d="M18.36 6.64A9 9 0 0 1 20.77 15M6.16 6.16a9 9 0 1 0 12.68 12.68"/><path d="m2 2 20 20"/></svg>
				Disconnect
			</button>
		</div>`, id)
		return
	}

	// Disconnected state — show QR / Phone Pairing toggle
	_, _ = fmt.Fprintf(w, `<div x-data="{ mode: 'qr' }" class="space-y-5">
		<div class="flex items-center justify-center">
			<div class="flex bg-gray-100 rounded-lg p-0.5">
				<button @click="mode = 'qr'" :class="mode === 'qr' ? 'bg-white shadow-sm text-gray-900' : 'text-gray-500 hover:text-gray-700'"
					class="flex items-center gap-2 px-4 py-2 rounded-md text-sm font-medium transition-all">
					<svg class="w-4 h-4" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24"><rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><rect x="14" y="14" width="3" height="3"/><path d="M21 14h-3v3h3zM21 19v2h-2M17 21v-6"/></svg>
					QR Code
				</button>
				<button @click="mode = 'phone'" :class="mode === 'phone' ? 'bg-white shadow-sm text-gray-900' : 'text-gray-500 hover:text-gray-700'"
					class="flex items-center gap-2 px-4 py-2 rounded-md text-sm font-medium transition-all">
					<svg class="w-4 h-4" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24"><rect x="5" y="2" width="14" height="20" rx="2"/><path d="M12 18h.01"/></svg>
					Phone Pairing
				</button>
			</div>
		</div>
		<div x-show="mode === 'qr'">
			<div id="qr-area" hx-get="/accounts/%s/qr" hx-trigger="load" hx-swap="innerHTML">
				<div class="flex flex-col items-center justify-center py-12">
					<div class="w-64 h-64 bg-gray-50 rounded-xl border-2 border-dashed border-gray-200 flex items-center justify-center">
						<div class="text-center">
							<svg class="w-8 h-8 text-gray-300 mx-auto mb-2 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"/><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/></svg>
							<p class="text-sm text-gray-400">Loading QR code…</p>
						</div>
					</div>
				</div>
			</div>
		</div>
		<div x-show="mode === 'phone'">
			<div id="pair-area">
				<div class="max-w-sm mx-auto text-center space-y-4 py-6">
					<div class="w-16 h-16 bg-brand-50 rounded-2xl flex items-center justify-center mx-auto">
						<svg class="w-8 h-8 text-brand-600" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24"><rect x="5" y="2" width="14" height="20" rx="2"/><path d="M12 18h.01"/></svg>
					</div>
					<div>
						<p class="text-sm text-gray-600 mb-1">Link using phone number</p>
						<p class="text-xs text-gray-400">A pairing code will appear that you enter in WhatsApp</p>
					</div>
					<button hx-post="/accounts/%s/pair" hx-target="#pair-area" hx-swap="innerHTML"
						class="inline-flex items-center gap-2 px-5 py-2.5 text-sm font-medium text-white bg-brand-600 rounded-lg hover:bg-brand-700 transition-colors">
						<svg class="w-4 h-4" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24"><path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/></svg>
						Generate Pairing Code
					</button>
				</div>
			</div>
		</div>
	</div>`, id, id)
}

// AccountQR returns the QR code image for htmx.
func (h *Handler) AccountQR(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	acct := h.mgr.GetAccount(id)
	if acct == nil {
		http.Error(w, "account not found", http.StatusNotFound)
		return
	}

	if acct.IsLoggedIn() {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<div class="flex flex-col items-center justify-center py-8">
			<div class="w-16 h-16 bg-green-50 rounded-2xl flex items-center justify-center mb-3">
				<svg class="w-8 h-8 text-green-600" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24"><path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/><path d="m9 11 3 3L22 4"/></svg>
			</div>
			<p class="text-sm font-medium text-green-700">Session is already connected</p>
			<p class="text-xs text-gray-400 mt-1">Disconnect first to re-link</p>
		</div>`)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	ch, err := acct.GetQR(ctx)
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		errMsg := template.HTMLEscapeString(err.Error())
		_, _ = fmt.Fprintf(w, `<div class="flex flex-col items-center justify-center py-8">
			<div class="p-3 bg-red-50 border border-red-200 rounded-lg text-sm text-red-700 mb-3">%s</div>
			<button hx-get="/accounts/%s/qr" hx-target="#qr-area" hx-swap="innerHTML"
				class="text-xs text-brand-600 hover:text-brand-700 font-medium">↻ Try again</button>
		</div>`, errMsg, id)
		return
	}

	select {
	case item := <-ch:
		service.DrainQR(ch)
		if item.Error != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			errMsg := template.HTMLEscapeString(item.Error.Error())
			_, _ = fmt.Fprintf(w, `<div class="flex flex-col items-center justify-center py-8">
				<div class="p-3 bg-red-50 border border-red-200 rounded-lg text-sm text-red-700 mb-3">%s</div>
				<button hx-get="/accounts/%s/qr" hx-target="#qr-area" hx-swap="innerHTML"
					class="text-xs text-brand-600 hover:text-brand-700 font-medium">↻ Try again</button>
			</div>`, errMsg, id)
			return
		}
		if item.Event == "code" {
			png, err := qrcode.Encode(item.Code, qrcode.Medium, 512)
			if err != nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = fmt.Fprintf(w, `<div class="flex flex-col items-center justify-center py-8">
					<div class="p-3 bg-red-50 border border-red-200 rounded-lg text-sm text-red-700">Failed to render QR</div>
				</div>`)
				return
			}
			b64 := base64.StdEncoding.EncodeToString(png)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprintf(w, `<div class="flex flex-col items-center justify-center py-4">
				<img src="data:image/png;base64,%s" alt="QR Code" class="w-64 h-64 rounded-xl border border-gray-200 shadow-sm">
				<p class="text-xs text-gray-500 mt-3">Open WhatsApp → Linked Devices → Link a Device</p>
				<button hx-get="/accounts/%s/qr" hx-target="#qr-area" hx-swap="innerHTML"
					class="mt-3 text-xs text-brand-600 hover:text-brand-700 font-medium">↻ Refresh QR</button>
			</div>`, b64, id)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<div class="flex flex-col items-center justify-center py-8">
			<div class="p-3 bg-red-50 border border-red-200 rounded-lg text-sm text-red-700">Unexpected: %s</div>
		</div>`, template.HTMLEscapeString(item.Event))
	case <-ctx.Done():
		service.DrainQR(ch)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<div class="flex flex-col items-center justify-center py-8">
			<div class="p-3 bg-amber-50 border border-amber-200 rounded-lg text-sm text-amber-700 mb-3">QR code timed out</div>
			<button hx-get="/accounts/%s/qr" hx-target="#qr-area" hx-swap="innerHTML"
				class="text-xs text-brand-600 hover:text-brand-700 font-medium">↻ Try again</button>
		</div>`, id)
	}
}

// AccountPair triggers phone pairing and returns the code for htmx.
func (h *Handler) AccountPair(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	acct := h.mgr.GetAccount(id)
	if acct == nil {
		http.Error(w, "account not found", http.StatusNotFound)
		return
	}

	if acct.IsLoggedIn() {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<div class="max-w-sm mx-auto text-center py-6">
			<div class="w-16 h-16 bg-green-50 rounded-2xl flex items-center justify-center mx-auto mb-3">
				<svg class="w-8 h-8 text-green-600" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24"><path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/><path d="m9 11 3 3L22 4"/></svg>
			</div>
			<p class="text-sm font-medium text-green-700">Session is already connected</p>
			<p class="text-xs text-gray-400 mt-1">Disconnect first to re-link</p>
		</div>`)
		return
	}

	code, err := acct.PairPhone(r.Context(), acct.PhoneNumber)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err != nil {
		errMsg := template.HTMLEscapeString(err.Error())
		_, _ = fmt.Fprintf(w, `<div class="max-w-sm mx-auto text-center space-y-4 py-6">
			<div class="p-3 bg-red-50 border border-red-200 rounded-lg text-sm text-red-700">%s</div>
			<button hx-post="/accounts/%s/pair" hx-target="#pair-area" hx-swap="innerHTML"
				class="text-xs text-brand-600 hover:text-brand-700 font-medium">Try again</button>
		</div>`, errMsg, id)
		return
	}

	_, _ = fmt.Fprintf(w, `<div class="max-w-sm mx-auto text-center space-y-4 py-6">
		<div class="p-5 bg-brand-50 border border-brand-200 rounded-xl">
			<p class="text-xs text-gray-500 mb-2 uppercase tracking-wider font-medium">Your Pairing Code</p>
			<p class="text-4xl font-mono font-bold text-brand-700 tracking-[0.3em]">%s</p>
		</div>
		<p class="text-xs text-gray-500">Open WhatsApp → Linked Devices → Link a Device → Link with Phone Number</p>
		<button hx-post="/accounts/%s/pair" hx-target="#pair-area" hx-swap="innerHTML"
			class="text-xs text-brand-600 hover:text-brand-700 font-medium">↻ Generate new code</button>
	</div>`, code, id)
}

// AccountDisconnect disconnects the session and returns refreshed session tab content.
func (h *Handler) AccountDisconnect(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	acct := h.mgr.GetAccount(id)
	if acct != nil {
		_ = acct.Logout()
	}

	// Return the disconnected auth UI by delegating to session tab handler
	h.AccountSessionTab(w, r)
}

// MessageSend handles POST /whatsapp/{id}/send — sends a message via the service layer.
// Returns JSON for the frontend JS to consume.
func (h *Handler) MessageSend(w http.ResponseWriter, r *http.Request) {
	acct, ok := h.requireConnectedAccount(w, r)
	if !ok {
		return
	}

	// Parse multipart for file upload support
	_ = r.ParseMultipartForm(32 << 20) // 32 MB

	chat := r.FormValue("chat")
	phone := r.FormValue("phone")
	text := r.FormValue("text")

	// Determine the target JID
	var jid string
	if chat != "" && strings.Contains(chat, "@") {
		// Full JID provided — use as-is
		jid = chat
	} else if phone != "" {
		jid = phone + "@s.whatsapp.net"
	} else {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "phone or chat is required"})
		return
	}

	// Check for file attachment
	file, header, err := r.FormFile("file")
	if err == nil && header != nil {
		defer func() { _ = file.Close() }()
		data, err := io.ReadAll(file)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to read file"})
			return
		}
		var caption *string
		if text != "" {
			caption = &text
		}
		msgID, err := acct.SendMedia(r.Context(), jid, data, header.Filename, header.Header.Get("Content-Type"), caption)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"message_id": msgID})
		return
	}

	if text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text or file is required"})
		return
	}

	msgID, err := acct.SendMessage(r.Context(), jid, text)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message_id": msgID})
}

// MessagingChats returns a JSON list of chats for an account.
func (h *Handler) MessagingChats(w http.ResponseWriter, r *http.Request) {
	acct, ok := h.requireConnectedAccount(w, r)
	if !ok {
		return
	}
	chats, err := acct.ListChats(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"chats": chats})
}

// MessagingMessages returns a JSON list of messages for a chat.
func (h *Handler) MessagingMessages(w http.ResponseWriter, r *http.Request) {
	acct, ok := h.requireConnectedAccount(w, r)
	if !ok {
		return
	}
	chatJID := r.URL.Query().Get("chat")
	if chatJID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "chat query parameter is required"})
		return
	}
	limit := 50
	before := r.URL.Query().Get("before")
	resp, err := acct.ListMessages(chatJID, limit, before)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// MessagingContacts returns a JSON list of contacts for an account.
func (h *Handler) MessagingContacts(w http.ResponseWriter, r *http.Request) {
	acct, ok := h.requireConnectedAccount(w, r)
	if !ok {
		return
	}
	contacts, err := acct.ListContacts(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, contacts)
}

// MessagingGroups returns a JSON list of groups for an account.
func (h *Handler) MessagingGroups(w http.ResponseWriter, r *http.Request) {
	acct, ok := h.requireConnectedAccount(w, r)
	if !ok {
		return
	}
	groups, err := acct.ListGroups(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

// MessagingNewsletters returns a JSON list of subscribed newsletters/channels.
func (h *Handler) MessagingNewsletters(w http.ResponseWriter, r *http.Request) {
	acct, ok := h.requireConnectedAccount(w, r)
	if !ok {
		return
	}
	newsletters, err := acct.ListNewsletters(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, newsletters)
}

// MessagingNewsletterMessages returns messages from a specific newsletter/channel.
func (h *Handler) MessagingNewsletterMessages(w http.ResponseWriter, r *http.Request) {
	acct, ok := h.requireConnectedAccount(w, r)
	if !ok {
		return
	}
	jid := r.URL.Query().Get("jid")
	if jid == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "jid required"})
		return
	}
	count := 50
	if c := r.URL.Query().Get("count"); c != "" {
		if n, err := strconv.Atoi(c); err == nil && n > 0 {
			count = n
		}
	}
	before := 0
	if b := r.URL.Query().Get("before"); b != "" {
		if n, err := strconv.Atoi(b); err == nil && n > 0 {
			before = n
		}
	}
	resp, err := acct.GetNewsletterMessages(r.Context(), jid, count, before)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// MessagingNewsletterFollow follows (subscribes to) a newsletter/channel.
func (h *Handler) MessagingNewsletterFollow(w http.ResponseWriter, r *http.Request) {
	acct, ok := h.requireConnectedAccount(w, r)
	if !ok {
		return
	}
	var req struct {
		JID string `json:"jid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.JID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "jid required"})
		return
	}
	if err := acct.FollowNewsletter(r.Context(), req.JID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// MessagingNewsletterUnfollow unfollows (unsubscribes from) a newsletter/channel.
func (h *Handler) MessagingNewsletterUnfollow(w http.ResponseWriter, r *http.Request) {
	acct, ok := h.requireConnectedAccount(w, r)
	if !ok {
		return
	}
	var req struct {
		JID string `json:"jid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.JID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "jid required"})
		return
	}
	if err := acct.UnfollowNewsletter(r.Context(), req.JID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// MessagingReact sends a reaction to a message.
func (h *Handler) MessagingReact(w http.ResponseWriter, r *http.Request) {
	acct, ok := h.requireConnectedAccount(w, r)
	if !ok {
		return
	}
	var req struct {
		ChatJID   string `json:"chat_jid"`
		MessageID string `json:"message_id"`
		Emoji     string `json:"emoji"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if err := acct.SendReaction(r.Context(), req.ChatJID, req.MessageID, req.Emoji); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// MessagingMarkRead marks messages as read in a chat.
func (h *Handler) MessagingMarkRead(w http.ResponseWriter, r *http.Request) {
	acct, ok := h.requireConnectedAccount(w, r)
	if !ok {
		return
	}
	var req struct {
		ChatJID    string   `json:"chat_jid"`
		MessageIDs []string `json:"message_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if err := acct.MarkRead(r.Context(), req.ChatJID, req.MessageIDs); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// MessagingRevoke revokes a message.
func (h *Handler) MessagingRevoke(w http.ResponseWriter, r *http.Request) {
	acct, ok := h.requireConnectedAccount(w, r)
	if !ok {
		return
	}
	var req struct {
		ChatJID   string `json:"chat_jid"`
		MessageID string `json:"message_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	resp, err := acct.RevokeMessage(r.Context(), req.ChatJID, req.MessageID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// requireConnectedAccount is a helper that retrieves the account from the URL
// and verifies it is connected. Returns (account, true) or writes an error and returns (nil, false).
func (h *Handler) requireConnectedAccount(w http.ResponseWriter, r *http.Request) (*service.Account, bool) {
	id := r.PathValue("id")
	acct := h.mgr.GetAccount(id)
	if acct == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "account not found"})
		return nil, false
	}
	if !acct.IsLoggedIn() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "account not connected"})
		return nil, false
	}
	return acct, true
}

// writeJSON is a small helper for JSON responses.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
