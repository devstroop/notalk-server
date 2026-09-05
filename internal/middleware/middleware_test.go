package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/devstroop/notalk/internal/config"
)

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
})

// --- Auth tests ---

func TestAuthValidToken(t *testing.T) {
	h := Auth("my-secret", nil, okHandler)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer my-secret")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestAuthMissingHeader(t *testing.T) {
	h := Auth("my-secret", nil, okHandler)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAuthWrongToken(t *testing.T) {
	h := Auth("my-secret", nil, okHandler)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAuthMalformedHeader(t *testing.T) {
	h := Auth("my-secret", nil, okHandler)

	cases := []string{
		"my-secret",            // no Bearer prefix
		"Basic my-secret",      // wrong scheme
		"Bearer",               // no token
	}

	for _, header := range cases {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Authorization", header)
		w := httptest.NewRecorder()

		h.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("header %q: expected 401, got %d", header, w.Code)
		}
	}
}

func TestAuthBearerCaseInsensitive(t *testing.T) {
	h := Auth("my-secret", nil, okHandler)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "bearer my-secret")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for lowercase 'bearer', got %d", w.Code)
	}
}

// --- CORS tests ---

func TestCORSHeaders(t *testing.T) {
	cfg := config.CORSConfig{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{"GET", "POST"},
		AllowHeaders: []string{"authorization", "content-type"},
	}
	h := CORS(cfg, okHandler)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("expected origin *, got %s", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST" {
		t.Errorf("expected methods GET, POST, got %s", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got != "authorization, content-type" {
		t.Errorf("expected headers authorization, content-type, got %s", got)
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestCORSPreflight(t *testing.T) {
	cfg := config.CORSConfig{
		AllowOrigins: []string{"https://example.com"},
		AllowMethods: []string{"GET", "POST", "DELETE"},
		AllowHeaders: []string{"authorization"},
	}
	h := CORS(cfg, okHandler)

	req := httptest.NewRequest("OPTIONS", "/", nil)
	req.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204 for OPTIONS, got %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Errorf("expected origin https://example.com, got %s", got)
	}
	// Should NOT have called the next handler (body should be empty)
	if w.Body.Len() != 0 {
		t.Errorf("expected empty body for preflight, got %s", w.Body.String())
	}
}
