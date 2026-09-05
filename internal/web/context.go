package web

import (
	"net/http"

	"github.com/devstroop/notalk/internal/middleware"
)

// PageData is the top-level data passed to every page template.
type PageData struct {
	Title    string              // Browser tab title (e.g. "Dashboard — NoTalk")
	Heading  string              // Page heading (e.g. "Dashboard")
	Page     string              // Active sidebar item: "dashboard", "accounts", etc.
	Version  string              // App version for footer
	Identity *middleware.Identity // Authenticated user (nil on auth pages)
	Flash    *Flash              // One-shot notification
	Data     any                 // Page-specific payload
}

// Flash is a one-shot notification shown after redirects.
type Flash struct {
	Type    string // "success", "error", "info"
	Message string
}

// isHTMX returns true if the request was made by htmx.
func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// hxRedirect sends an HX-Redirect header (htmx client-side redirect).
func hxRedirect(w http.ResponseWriter, url string) {
	w.Header().Set("HX-Redirect", url)
	w.WriteHeader(http.StatusOK)
}
