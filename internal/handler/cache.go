package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/devstroop/notalk/internal/cache"
	"github.com/devstroop/notalk/internal/middleware"
)

// CacheHandler exposes opt-in Redis cache status and controls.
// When Redis is disabled (default) it no-ops; UI should show disabled state
// with an overlay/backdrop loader only while the click's request is in flight.
type CacheHandler struct {
	cache *cache.Cache
}

func NewCacheHandler(c *cache.Cache) *CacheHandler {
	return &CacheHandler{cache: c}
}

// Status — GET /api/v1/cache/status (admin)
func (h *CacheHandler) Status(w http.ResponseWriter, r *http.Request) {
	identity := middleware.GetIdentity(r)
	if !identity.HasPermission("*") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin required"})
		return
	}
	enabled := h.cache != nil && h.cache.Enabled()
	connected := false
	keys := 0
	if enabled {
		ctx, cancel := context.WithTimeout(r.Context(), 800*time.Millisecond)
		defer cancel()
		if err := h.cache.Ping(ctx); err == nil {
			connected = true
			// best-effort keys count (pattern chat keys)
			if ks, err := h.cache.Keys(ctx, "chats:*"); err == nil {
				keys = len(ks)
			}
		}
	}
	// HTMX fragment support: if Accept is html, return a tiny fragment for overlay use
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if !enabled {
			_, _ = w.Write([]byte(`<span class="inline-flex items-center gap-1.5 text-xs px-2.5 py-1 rounded-full bg-gray-100 text-gray-600">Cache disabled — opt-in via NOTALK_REDIS_ENABLED=true</span>`))
			return
		}
		if !connected {
			_, _ = w.Write([]byte(`<span class="inline-flex items-center gap-1.5 text-xs px-2.5 py-1 rounded-full bg-amber-100 text-amber-800">Cache enabled — redis not reachable</span>`))
			return
		}
		_, _ = w.Write([]byte(`<span class="inline-flex items-center gap-1.5 text-xs px-2.5 py-1 rounded-full bg-green-100 text-green-800"><span class="w-1.5 h-1.5 bg-green-500 rounded-full animate-pulse"></span>Cache active — ` + itoa(keys) + ` chat keys</span>`))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":   enabled,
		"connected": connected,
		"keys":      keys,
	})
}

// Flush — POST /api/v1/cache/flush (admin) — clears cache with overlay feedback
func (h *CacheHandler) Flush(w http.ResponseWriter, r *http.Request) {
	identity := middleware.GetIdentity(r)
	if !identity.HasPermission("*") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin required"})
		return
	}
	if h.cache == nil || !h.cache.Enabled() {
		// still return success so UI overlay can complete (no-op when disabled)
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<span class="text-xs text-gray-500">Cache disabled — nothing to flush</span>`))
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "noop", "message": "cache disabled"})
		return
	}
	// Simulate a brief heavy flush with overlay: keep request until state changes.
	// Real flush is near-instant; we add a tiny delay so backdrop loader is visible.
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	_ = h.cache.FlushDB(ctx)
	// also clear any chat keys explicitly for feedback
	_ = h.cache.Del(ctx, "chats:*")
	time.Sleep(400 * time.Millisecond) // brief perceptible loader (not heavy)
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("HX-Trigger", "cacheFlushed")
		_, _ = w.Write([]byte(`<span class="inline-flex items-center gap-1.5 text-xs text-green-700"><svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24"><path d="m5 13 4 4L19 7"/></svg>Cache flushed</span>`))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "flushed"})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	b := make([]byte, 0, 8)
	// simple itoa
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	_ = b
	return s
}
