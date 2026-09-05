// Package tests contains integration tests that exercise the full HTTP API
// stack: router → middleware (auth, CORS, rate-limit) → handler → service → DB.
//
// No WhatsApp connection is established; these tests verify every endpoint that
// does NOT require a live session (account CRUD, messages, webhooks, proxy,
// health, OpenAPI, error responses, rate limiting).
package tests

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/devstroop/notalk/internal/config"
	"github.com/devstroop/notalk/internal/database"
	"github.com/devstroop/notalk/internal/handler"
	"github.com/devstroop/notalk/internal/middleware"
	"github.com/devstroop/notalk/internal/service"
	_ "github.com/lib/pq"
)

const testSecret = "test-secret-key"

// testServer builds the full middleware chain and returns an httptest.Server
// plus the account manager for direct DB seeding.
func testServer(t *testing.T) (*httptest.Server, *service.AccountManager) {
	srv, mgr, _ := testServerWithDB(t)
	return srv, mgr
}

// testServerWithDB is like testServer but also returns the database handle so
// tests can verify DB state directly (e.g. RBAC / API key tests).
func testServerWithDB(t *testing.T) (*httptest.Server, *service.AccountManager, *database.DB) {
	t.Helper()
	dsn := os.Getenv("NOTALK_TEST_DSN")
	if dsn == "" {
		t.Skip("NOTALK_TEST_DSN not set, skipping integration tests")
	}
	dir := t.TempDir()

	db, err := database.Open(dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Clean mutable tables for test isolation (preserve builtin roles).
	raw, _ := sql.Open("postgres", dsn)
	if raw != nil {
		for _, table := range []string{
			"password_reset_token", "api_key", "agent_log", "agent_config", "agent_session",
			"message", "webhook_config", "proxy_config",
			"account", "app_user",
		} {
			_, _ = raw.Exec("DELETE FROM " + table + " WHERE TRUE")
		}
		_ = raw.Close()
	}

	cfg := &config.Config{
		Auth:    config.AuthConfig{SecretKey: testSecret},
		Limits:  config.LimitsConfig{MaxConcurrentRequests: 50},
		Swagger: config.SwaggerConfig{Enabled: true, Path: "/api-docs"},
		CORS: config.CORSConfig{
			AllowOrigins: []string{"*"},
			AllowMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			AllowHeaders: []string{"authorization", "content-type"},
		},
		Accounts: config.AccountsConfig{BaseDirectory: filepath.Join(dir, "accounts")},
		Database: config.DatabaseConfig{DSN: dsn},
	}

	mgr, err := service.NewAccountManager(cfg, db)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", handler.Health)

	// Public auth endpoints (no auth required)
	authH := handler.NewAuthHandler(db, cfg.Auth.SecretKey, false, nil)
	mux.HandleFunc("POST /api/v1/auth/login", authH.Login)

	api := handler.NewAPI(mgr, db)
	apiMux := http.NewServeMux()
	api.RegisterRoutes(apiMux)
	handler.RegisterRBACRoutes(apiMux, db)

	authed := middleware.Auth(cfg.Auth.SecretKey, db, apiMux)
	limited := middleware.RateLimit(cfg.Limits.MaxConcurrentRequests, authed)
	mux.Handle("/api/v1/", limited)

	if cfg.Swagger.Enabled {
		mux.Handle(cfg.Swagger.Path+"/", handler.SwaggerUI(cfg.Swagger.Path))
		mux.Handle(cfg.Swagger.Path, handler.SwaggerUI(cfg.Swagger.Path))
	}

	root := middleware.CORS(cfg.CORS, mux)
	srv := httptest.NewServer(root)
	t.Cleanup(srv.Close)

	return srv, mgr, db
}

// authGet performs an authenticated GET request.
func authGet(t *testing.T, srv *httptest.Server, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+testSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// authReq performs an authenticated request with JSON body.
func authReq(t *testing.T, srv *httptest.Server, method, path, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testSecret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// decodeJSON reads and decodes the response body.
func decodeJSON(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode json: %v", err)
	}
}

// ─── Health ─────────────────────────────────────────

func TestHealthEndpoint(t *testing.T) {
	srv, _ := testServer(t)
	// No auth required
	resp, err := http.Get(srv.URL + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body map[string]string
	decodeJSON(t, resp, &body)
	if body["status"] != "ok" {
		t.Errorf("expected status=ok, got %s", body["status"])
	}
}

// ─── Auth middleware ────────────────────────────────

func TestAuthRequired(t *testing.T) {
	srv, _ := testServer(t)

	// No Authorization header
	resp, err := http.Get(srv.URL + "/api/v1/accounts")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 401 {
		t.Errorf("expected 401 for missing auth, got %d", resp.StatusCode)
	}
}

func TestAuthWrongToken(t *testing.T) {
	srv, _ := testServer(t)

	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/accounts", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 401 {
		t.Errorf("expected 401 for wrong token, got %d", resp.StatusCode)
	}
}

// ─── CORS ───────────────────────────────────────────

func TestCORSPreflight(t *testing.T) {
	srv, _ := testServer(t)

	req, _ := http.NewRequest("OPTIONS", srv.URL+"/api/v1/accounts", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 204 {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("expected ACAO *, got %s", got)
	}
}

// ─── OpenAPI / Swagger ──────────────────────────────

func TestSwaggerUI(t *testing.T) {
	srv, _ := testServer(t)

	resp, err := http.Get(srv.URL + "/api-docs/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 for /api-docs/, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("expected text/html, got %s", ct)
	}
}

func TestOpenAPISpec(t *testing.T) {
	srv, _ := testServer(t)

	resp, err := http.Get(srv.URL + "/api-docs/openapi.json")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 for openapi.json, got %d", resp.StatusCode)
	}

	var spec map[string]any
	decodeJSON(t, resp, &spec)
	if spec["openapi"] == nil {
		t.Error("expected openapi field in spec")
	}
}

// ─── Account CRUD ───────────────────────────────────

func TestAccountLifecycle(t *testing.T) {
	srv, _ := testServer(t)

	// 1. List — should be empty
	resp := authGet(t, srv, "/api/v1/accounts")
	var listResp struct {
		Accounts []any `json:"accounts"`
		Total    int   `json:"total"`
	}
	decodeJSON(t, resp, &listResp)
	if listResp.Total != 0 {
		t.Fatalf("expected 0 accounts, got %d", listResp.Total)
	}

	// 2. Create
	resp = authReq(t, srv, "POST", "/api/v1/accounts",
		`{"phone_number":"+919876543210","account_name":"integration"}`)
	if resp.StatusCode != 201 {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var createResp struct {
		ID          string `json:"id"`
		PhoneNumber string `json:"phone_number"`
		AccountName string `json:"account_name"`
	}
	decodeJSON(t, resp, &createResp)
	if createResp.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if createResp.PhoneNumber != "919876543210" {
		t.Errorf("expected normalized phone, got %s", createResp.PhoneNumber)
	}
	accountID := createResp.ID

	// 3. Get
	resp = authGet(t, srv, "/api/v1/accounts/"+accountID)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var acctInfo struct {
		ID          string `json:"id"`
		AccountName string `json:"account_name"`
		Authorized  bool   `json:"authorized"`
	}
	decodeJSON(t, resp, &acctInfo)
	if acctInfo.ID != accountID {
		t.Errorf("expected %s, got %s", accountID, acctInfo.ID)
	}
	if acctInfo.Authorized {
		t.Error("expected Authorized=false for new account")
	}

	// 4. Update name
	resp = authReq(t, srv, "PATCH", "/api/v1/accounts/"+accountID,
		`{"account_name":"updated-name"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var updatedInfo struct {
		AccountName string `json:"account_name"`
	}
	decodeJSON(t, resp, &updatedInfo)
	if updatedInfo.AccountName != "updated-name" {
		t.Errorf("expected updated-name, got %s", updatedInfo.AccountName)
	}

	// 5. List — should have 1
	resp = authGet(t, srv, "/api/v1/accounts")
	decodeJSON(t, resp, &listResp)
	if listResp.Total != 1 {
		t.Errorf("expected 1 account, got %d", listResp.Total)
	}

	// 6. Delete
	resp = authReq(t, srv, "DELETE", "/api/v1/accounts/"+accountID+"?delete_data=true", "")
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// 7. Verify gone
	resp = authGet(t, srv, "/api/v1/accounts/"+accountID)
	if resp.StatusCode != 404 {
		t.Errorf("expected 404 after delete, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestCreateAccountDuplicate(t *testing.T) {
	srv, _ := testServer(t)

	_ = authReq(t, srv, "POST", "/api/v1/accounts",
		`{"phone_number":"1234567890","account_name":"first"}`).Body.Close()

	resp := authReq(t, srv, "POST", "/api/v1/accounts",
		`{"phone_number":"1234567890","account_name":"second"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 409 {
		t.Errorf("expected 409 for duplicate phone, got %d", resp.StatusCode)
	}
}

func TestCreateAccountInvalidPhone(t *testing.T) {
	srv, _ := testServer(t)

	resp := authReq(t, srv, "POST", "/api/v1/accounts",
		`{"phone_number":"123","account_name":"bad"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 409 {
		t.Errorf("expected 409 for too-short phone, got %d", resp.StatusCode)
	}
}

func TestGetAccountNotFound(t *testing.T) {
	srv, _ := testServer(t)

	resp := authGet(t, srv, "/api/v1/accounts/nonexistent-id")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestCreateAccountInvalidJSON(t *testing.T) {
	srv, _ := testServer(t)

	resp := authReq(t, srv, "POST", "/api/v1/accounts", "not json")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 400 {
		t.Errorf("expected 400 for bad JSON, got %d", resp.StatusCode)
	}
}

// ─── Session (without live WhatsApp) ────────────────

func TestSessionUnlinked(t *testing.T) {
	srv, _ := testServer(t)

	resp := authReq(t, srv, "POST", "/api/v1/accounts",
		`{"phone_number":"5551234567","account_name":"sess"}`)
	var cr struct{ ID string `json:"id"` }
	decodeJSON(t, resp, &cr)

	resp = authGet(t, srv, "/api/v1/accounts/"+cr.ID+"/session")
	var sr struct {
		Authorized bool `json:"authorized"`
	}
	decodeJSON(t, resp, &sr)
	if sr.Authorized {
		t.Error("expected authorized=false for new account")
	}
}

func TestSessionNotFound(t *testing.T) {
	srv, _ := testServer(t)

	resp := authGet(t, srv, "/api/v1/accounts/nonexistent/session")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// ─── Messages (requires account but NOT connection for GET) ──

func TestGetMessagesNoChatParam(t *testing.T) {
	srv, _ := testServer(t)

	resp := authReq(t, srv, "POST", "/api/v1/accounts",
		`{"phone_number":"7771111111","account_name":"msg"}`)
	var cr struct{ ID string `json:"id"` }
	decodeJSON(t, resp, &cr)

	// Missing chat param → 400
	resp = authGet(t, srv, "/api/v1/accounts/"+cr.ID+"/messages")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 400 {
		t.Errorf("expected 400 for missing chat param, got %d", resp.StatusCode)
	}
}

func TestGetMessagesEmpty(t *testing.T) {
	srv, _ := testServer(t)

	resp := authReq(t, srv, "POST", "/api/v1/accounts",
		`{"phone_number":"7772222222","account_name":"msg2"}`)
	var cr struct{ ID string `json:"id"` }
	decodeJSON(t, resp, &cr)

	resp = authGet(t, srv, "/api/v1/accounts/"+cr.ID+"/messages?chat=123@s.whatsapp.net")
	var mr struct {
		Messages []any `json:"messages"`
		Count    int   `json:"count"`
	}
	decodeJSON(t, resp, &mr)
	if mr.Count != 0 {
		t.Errorf("expected 0 messages, got %d", mr.Count)
	}
}

func TestGetMessagesWithData(t *testing.T) {
	srv, mgr := testServer(t)

	resp := authReq(t, srv, "POST", "/api/v1/accounts",
		`{"phone_number":"7773333333","account_name":"msg3"}`)
	var cr struct{ ID string `json:"id"` }
	decodeJSON(t, resp, &cr)

	// Seed messages directly via DB
	db := mgr.DB()
	chat := "919999999999@s.whatsapp.net"
	for i := 0; i < 5; i++ {
		if err := db.InsertMessage(&database.MessageRecord{
			ID:        fmt.Sprintf("msg-%d", i),
			AccountID: cr.ID,
			ChatJID:   chat,
			SenderJID: "919999999999@s.whatsapp.net",
			FromMe:    false,
			Type:      "text",
			Body:      fmt.Sprintf("hello %d", i),
			Timestamp: fmt.Sprintf("2026-01-01T00:00:0%dZ", i),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Default limit
	resp = authGet(t, srv, "/api/v1/accounts/"+cr.ID+"/messages?chat="+chat)
	var mr struct {
		Messages []struct {
			ID   string `json:"id"`
			Body string `json:"body"`
		} `json:"messages"`
		Count int `json:"count"`
	}
	decodeJSON(t, resp, &mr)
	if mr.Count != 5 {
		t.Errorf("expected 5 messages, got %d", mr.Count)
	}
	// Should be newest-first
	if mr.Messages[0].ID != "msg-4" {
		t.Errorf("expected newest first (msg-4), got %s", mr.Messages[0].ID)
	}

	// Limit
	resp = authGet(t, srv, "/api/v1/accounts/"+cr.ID+"/messages?chat="+chat+"&limit=2")
	decodeJSON(t, resp, &mr)
	if mr.Count != 2 {
		t.Errorf("expected 2 messages with limit=2, got %d", mr.Count)
	}

	// Before cursor
	resp = authGet(t, srv, "/api/v1/accounts/"+cr.ID+"/messages?chat="+chat+"&before=2026-01-01T00:00:03Z")
	decodeJSON(t, resp, &mr)
	if mr.Count != 3 {
		t.Errorf("expected 3 messages before T03, got %d", mr.Count)
	}
}

func TestGetMessagesNotFound(t *testing.T) {
	srv, _ := testServer(t)

	resp := authGet(t, srv, "/api/v1/accounts/nonexistent/messages?chat=x")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// ─── Webhook config CRUD ────────────────────────────

func TestWebhookLifecycle(t *testing.T) {
	srv, _ := testServer(t)

	// Create account
	resp := authReq(t, srv, "POST", "/api/v1/accounts",
		`{"phone_number":"8881111111","account_name":"wh"}`)
	var cr struct{ ID string `json:"id"` }
	decodeJSON(t, resp, &cr)

	base := "/api/v1/accounts/" + cr.ID + "/webhook"

	// 1. Get — not configured yet → 404
	resp = authGet(t, srv, base)
	if resp.StatusCode != 404 {
		t.Errorf("expected 404 for no webhook, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 2. Set webhook
	resp = authReq(t, srv, "PUT", base,
		`{"url":"https://example.com/hook","secret":"s3cr3t","events":["message"],"enabled":true}`)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var whResp struct {
		URL     string   `json:"url"`
		Events  []string `json:"events"`
		Enabled bool     `json:"enabled"`
	}
	decodeJSON(t, resp, &whResp)
	if whResp.URL != "https://example.com/hook" {
		t.Errorf("expected URL, got %s", whResp.URL)
	}
	if len(whResp.Events) != 1 || whResp.Events[0] != "message" {
		t.Errorf("expected [message], got %v", whResp.Events)
	}
	if !whResp.Enabled {
		t.Error("expected enabled=true")
	}

	// 3. Get — should return config (without secret)
	resp = authGet(t, srv, base)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	decodeJSON(t, resp, &whResp)
	if whResp.URL != "https://example.com/hook" {
		t.Errorf("expected URL, got %s", whResp.URL)
	}

	// 4. Update webhook
	resp = authReq(t, srv, "PUT", base,
		`{"url":"https://other.com/hook","events":["message","receipt"]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	decodeJSON(t, resp, &whResp)
	if whResp.URL != "https://other.com/hook" {
		t.Errorf("expected updated URL, got %s", whResp.URL)
	}
	if len(whResp.Events) != 2 {
		t.Errorf("expected 2 events, got %d", len(whResp.Events))
	}

	// 5. Delete webhook
	resp = authReq(t, srv, "DELETE", base, "")
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 6. Verify gone
	resp = authGet(t, srv, base)
	if resp.StatusCode != 404 {
		t.Errorf("expected 404 after delete, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestWebhookMissingURL(t *testing.T) {
	srv, _ := testServer(t)

	resp := authReq(t, srv, "POST", "/api/v1/accounts",
		`{"phone_number":"8882222222","account_name":"wh2"}`)
	var cr struct{ ID string `json:"id"` }
	decodeJSON(t, resp, &cr)

	resp = authReq(t, srv, "PUT", "/api/v1/accounts/"+cr.ID+"/webhook",
		`{"events":["message"]}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 400 {
		t.Errorf("expected 400 for missing url, got %d", resp.StatusCode)
	}
}

// ─── Proxy config CRUD ─────────────────────────────

func TestProxyLifecycle(t *testing.T) {
	srv, _ := testServer(t)

	resp := authReq(t, srv, "POST", "/api/v1/accounts",
		`{"phone_number":"9991111111","account_name":"px"}`)
	var cr struct{ ID string `json:"id"` }
	decodeJSON(t, resp, &cr)

	base := "/api/v1/accounts/" + cr.ID + "/proxy"

	// 1. Get — no proxy → 404
	resp = authGet(t, srv, base)
	if resp.StatusCode != 404 {
		t.Errorf("expected 404 for no proxy, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 2. Set proxy
	resp = authReq(t, srv, "PUT", base,
		`{"protocol":"http","host":"proxy.example.com","port":8080,"username":"user","password":"pass"}`)
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	_ = resp.Body.Close()

	// 3. Get — should return config (no password)
	resp = authGet(t, srv, base)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var pxResp struct {
		Protocol string `json:"protocol"`
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Username string `json:"username"`
		Enabled  bool   `json:"enabled"`
	}
	decodeJSON(t, resp, &pxResp)
	if pxResp.Host != "proxy.example.com" {
		t.Errorf("expected host, got %s", pxResp.Host)
	}
	if pxResp.Port != 8080 {
		t.Errorf("expected port 8080, got %d", pxResp.Port)
	}

	// 4. Delete proxy
	resp = authReq(t, srv, "DELETE", base, "")
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 5. Verify gone
	resp = authGet(t, srv, base)
	if resp.StatusCode != 404 {
		t.Errorf("expected 404 after delete, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

// ─── Endpoints requiring connection → error ────────

// blockDataDir places a regular file where the account expects a directory,
// causing Connect() → prepareClient() → os.MkdirAll to fail without hitting
// real WhatsApp servers.
func blockDataDir(t *testing.T, mgr *service.AccountManager, id string) {
	t.Helper()
	acct := mgr.GetAccount(id)
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o444); err != nil {
		t.Fatalf("create blocker file: %v", err)
	}
	acct.DataDir = blocker
}

func TestSendMessageWithoutConnection(t *testing.T) {
	srv, mgr := testServer(t)

	resp := authReq(t, srv, "POST", "/api/v1/accounts",
		`{"phone_number":"6661111111","account_name":"nc"}`)
	var cr struct{ ID string `json:"id"` }
	decodeJSON(t, resp, &cr)

	blockDataDir(t, mgr, cr.ID)

	resp = authReq(t, srv, "POST", "/api/v1/accounts/"+cr.ID+"/messages?phone=1234567890&text=hello",
		`{}`)
	defer func() { _ = resp.Body.Close() }()
	// Should fail because account is not linked (409)
	if resp.StatusCode != 409 {
		t.Errorf("expected 409 for unlinked send, got %d", resp.StatusCode)
	}
}

func TestListChatsWithoutConnection(t *testing.T) {
	srv, mgr := testServer(t)

	resp := authReq(t, srv, "POST", "/api/v1/accounts",
		`{"phone_number":"6662222222","account_name":"nc2"}`)
	var cr struct{ ID string `json:"id"` }
	decodeJSON(t, resp, &cr)

	blockDataDir(t, mgr, cr.ID)

	resp = authGet(t, srv, "/api/v1/accounts/"+cr.ID+"/chats")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 409 {
		t.Errorf("expected 409 for unlinked chats, got %d", resp.StatusCode)
	}
}

func TestListContactsWithoutConnection(t *testing.T) {
	srv, mgr := testServer(t)

	resp := authReq(t, srv, "POST", "/api/v1/accounts",
		`{"phone_number":"6663333333","account_name":"nc3"}`)
	var cr struct{ ID string `json:"id"` }
	decodeJSON(t, resp, &cr)

	blockDataDir(t, mgr, cr.ID)

	resp = authGet(t, srv, "/api/v1/accounts/"+cr.ID+"/contacts")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 409 {
		t.Errorf("expected 409 for unlinked contacts, got %d", resp.StatusCode)
	}
}

// TestPresenceWithoutConnection verifies that presence endpoints return 409
// when the account is not linked. (State validation happens after connection
// check in the handler, so invalid-state → 400 can only be tested with a live
// session.)
func TestPresenceWithoutConnection(t *testing.T) {
	srv, mgr := testServer(t)

	resp := authReq(t, srv, "POST", "/api/v1/accounts",
		`{"phone_number":"6664444444","account_name":"nc4"}`)
	var cr struct{ ID string `json:"id"` }
	decodeJSON(t, resp, &cr)

	blockDataDir(t, mgr, cr.ID)

	resp = authReq(t, srv, "POST", "/api/v1/accounts/"+cr.ID+"/presence",
		`{"state":"invalid"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 409 {
		t.Errorf("expected 409 for unlinked presence, got %d", resp.StatusCode)
	}
}

// ─── POST /messages endpoint ─────────────────────────

func TestSendEndpointMissingPhoneAndJID(t *testing.T) {
	srv, _ := testServer(t)

	resp := authReq(t, srv, "POST", "/api/v1/accounts",
		`{"phone_number":"7771111111","account_name":"send1"}`)
	var cr struct{ ID string `json:"id"` }
	decodeJSON(t, resp, &cr)

	// Neither phone nor jid → 400
	resp = authReq(t, srv, "POST", "/api/v1/accounts/"+cr.ID+"/messages?text=Hello", "")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 400 {
		t.Errorf("expected 400 missing phone/jid, got %d", resp.StatusCode)
	}
	var errBody map[string]string
	decodeJSON(t, resp, &errBody)
	if !strings.Contains(errBody["error"], "phone or jid") {
		t.Errorf("expected 'phone or jid' in error, got %s", errBody["error"])
	}
}

func TestSendEndpointBothPhoneAndJID(t *testing.T) {
	srv, _ := testServer(t)

	resp := authReq(t, srv, "POST", "/api/v1/accounts",
		`{"phone_number":"7772222222","account_name":"send2"}`)
	var cr struct{ ID string `json:"id"` }
	decodeJSON(t, resp, &cr)

	// Both phone and jid → 400
	resp = authReq(t, srv, "POST",
		"/api/v1/accounts/"+cr.ID+"/messages?phone=1234567890&jid=x@s.whatsapp.net&text=Hello", "")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 400 {
		t.Errorf("expected 400 for both phone+jid, got %d", resp.StatusCode)
	}
}

func TestSendEndpointUnconnectedWithJID(t *testing.T) {
	srv, mgr := testServer(t)

	resp := authReq(t, srv, "POST", "/api/v1/accounts",
		`{"phone_number":"7773333333","account_name":"send3"}`)
	var cr struct{ ID string `json:"id"` }
	decodeJSON(t, resp, &cr)

	blockDataDir(t, mgr, cr.ID)

	// jid provided, but account is not linked → 409
	resp = authReq(t, srv, "POST",
		"/api/v1/accounts/"+cr.ID+"/messages?jid=123456@s.whatsapp.net&text=Hello", "")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 409 {
		t.Errorf("expected 409 for unlinked send, got %d", resp.StatusCode)
	}
}

func TestSendEndpointMissingText(t *testing.T) {
	srv, _ := testServer(t)

	resp := authReq(t, srv, "POST", "/api/v1/accounts",
		`{"phone_number":"7774444444","account_name":"send4"}`)
	var cr struct{ ID string `json:"id"` }
	decodeJSON(t, resp, &cr)

	// jid given but no text and no file → 400
	resp = authReq(t, srv, "POST",
		"/api/v1/accounts/"+cr.ID+"/messages?jid=123456@s.whatsapp.net", "")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 400 {
		t.Errorf("expected 400 for missing text, got %d", resp.StatusCode)
	}
}

func TestSendEndpointTextInBody(t *testing.T) {
	srv, mgr := testServer(t)

	resp := authReq(t, srv, "POST", "/api/v1/accounts",
		`{"phone_number":"7775555555","account_name":"send5"}`)
	var cr struct{ ID string `json:"id"` }
	decodeJSON(t, resp, &cr)

	blockDataDir(t, mgr, cr.ID)

	// text in JSON body, jid in query → 409 (not linked, but proves routing works)
	resp = authReq(t, srv, "POST",
		"/api/v1/accounts/"+cr.ID+"/messages?jid=123456@s.whatsapp.net",
		`{"text":"from body"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 409 {
		t.Errorf("expected 409 (unlinked) for body text, got %d", resp.StatusCode)
	}
}

// ─── Rate Limiting ──────────────────────────────────

func TestRateLimitEnforced(t *testing.T) {
	dsn := os.Getenv("NOTALK_TEST_DSN")
	if dsn == "" {
		t.Skip("NOTALK_TEST_DSN not set, skipping integration tests")
	}
	dir := t.TempDir()
	db, err := database.Open(dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	cfg := &config.Config{
		Auth:     config.AuthConfig{SecretKey: testSecret},
		Limits:   config.LimitsConfig{MaxConcurrentRequests: 2}, // very low limit
		Accounts: config.AccountsConfig{BaseDirectory: filepath.Join(dir, "accounts")},
		Database: config.DatabaseConfig{DSN: dsn},
		CORS: config.CORSConfig{
			AllowOrigins: []string{"*"},
			AllowMethods: []string{"GET"},
			AllowHeaders: []string{"authorization"},
		},
	}
	mgr, _ := service.NewAccountManager(cfg, db)

	mux := http.NewServeMux()
	apiMux := http.NewServeMux()

	// done is closed after the 429 is verified, unblocking the slow handlers
	// so the server can shut down cleanly.
	done := make(chan struct{})

	// Slow handler to hold semaphore slots.
	var inFlight int32
	apiMux.HandleFunc("GET /api/v1/accounts", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&inFlight, 1)
		defer atomic.AddInt32(&inFlight, -1)
		select {
		case <-r.Context().Done():
		case <-done:
		}
	})
	_ = mgr

	authed := middleware.Auth(cfg.Auth.SecretKey, db, apiMux)
	limited := middleware.RateLimit(cfg.Limits.MaxConcurrentRequests, authed)
	mux.Handle("/api/v1/", limited)
	root := middleware.CORS(cfg.CORS, mux)
	srv := httptest.NewServer(root)
	defer srv.Close()

	// Fill up the 2 slots
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("GET", srv.URL+"/api/v1/accounts", nil)
			req.Header.Set("Authorization", "Bearer "+testSecret)
			resp, _ := http.DefaultClient.Do(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
		}()
	}

	// Wait for them to be in flight
	for atomic.LoadInt32(&inFlight) < 2 {
		// spin
	}

	// Next request should get 429
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/accounts", nil)
	req.Header.Set("Authorization", "Bearer "+testSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("rate limit request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 429 {
		t.Errorf("expected 429, got %d", resp.StatusCode)
	}
	if ra := resp.Header.Get("Retry-After"); ra != "1" {
		t.Errorf("expected Retry-After: 1, got %s", ra)
	}

	// Unblock the slow handlers and wait for goroutines to finish.
	close(done)
	wg.Wait()
}

// ─── Error response format ──────────────────────────

func TestErrorResponseFormat(t *testing.T) {
	srv, _ := testServer(t)

	resp := authGet(t, srv, "/api/v1/accounts/nonexistent")
	var errResp struct {
		Error string `json:"error"`
	}
	decodeJSON(t, resp, &errResp)
	if errResp.Error == "" {
		t.Error("expected non-empty error field")
	}
	if errResp.Error != "account not found" {
		t.Errorf("expected 'account not found', got %s", errResp.Error)
	}
}

func TestContentTypeJSON(t *testing.T) {
	srv, _ := testServer(t)

	resp := authGet(t, srv, "/api/v1/accounts")
	defer func() { _ = resp.Body.Close() }()
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected application/json, got %s", ct)
	}
}

// ─── Multiple accounts ──────────────────────────────

func TestMultipleAccounts(t *testing.T) {
	srv, _ := testServer(t)

	phones := []string{"4441111111", "4442222222", "4443333333"}
	ids := make([]string, 3)

	for i, phone := range phones {
		resp := authReq(t, srv, "POST", "/api/v1/accounts",
			fmt.Sprintf(`{"phone_number":"%s","account_name":"acct-%d"}`, phone, i))
		var cr struct{ ID string `json:"id"` }
		decodeJSON(t, resp, &cr)
		ids[i] = cr.ID
	}

	resp := authGet(t, srv, "/api/v1/accounts")
	var listResp struct {
		Total int `json:"total"`
	}
	decodeJSON(t, resp, &listResp)
	if listResp.Total != 3 {
		t.Errorf("expected 3 accounts, got %d", listResp.Total)
	}

	// Messages are account-scoped
	mgr := srv // not needed, we use DB through the first server
	_ = mgr
	// Seed messages for account 0
	resp = authGet(t, srv, "/api/v1/accounts/"+ids[0]+"/messages?chat=test@s.whatsapp.net")
	var mr struct{ Count int `json:"count"` }
	decodeJSON(t, resp, &mr)
	if mr.Count != 0 {
		t.Errorf("expected 0 messages for new account, got %d", mr.Count)
	}
}

// ─── Update phone number ────────────────────────────

func TestUpdatePhoneNumber(t *testing.T) {
	srv, _ := testServer(t)

	resp := authReq(t, srv, "POST", "/api/v1/accounts",
		`{"phone_number":"5550001111","account_name":"phone-test"}`)
	var cr struct{ ID string `json:"id"` }
	decodeJSON(t, resp, &cr)

	resp = authReq(t, srv, "PATCH", "/api/v1/accounts/"+cr.ID,
		`{"phone_number":"5550002222"}`)
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	_ = resp.Body.Close()
}

func TestUpdatePhoneNumberConflict(t *testing.T) {
	srv, _ := testServer(t)

	_ = authReq(t, srv, "POST", "/api/v1/accounts",
		`{"phone_number":"5550003333","account_name":"p1"}`).Body.Close()
	resp := authReq(t, srv, "POST", "/api/v1/accounts",
		`{"phone_number":"5550004444","account_name":"p2"}`)
	var cr struct{ ID string `json:"id"` }
	decodeJSON(t, resp, &cr)

	// Try to change p2's phone to p1's
	resp = authReq(t, srv, "PATCH", "/api/v1/accounts/"+cr.ID,
		`{"phone_number":"5550003333"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 409 {
		t.Errorf("expected 409 for phone conflict, got %d", resp.StatusCode)
	}
}

// ─── Delete session (no connection) ─────────────────

func TestDeleteSessionNoSession(t *testing.T) {
	srv, _ := testServer(t)

	resp := authReq(t, srv, "POST", "/api/v1/accounts",
		`{"phone_number":"5550005555","account_name":"ds"}`)
	var cr struct{ ID string `json:"id"` }
	decodeJSON(t, resp, &cr)

	resp = authReq(t, srv, "DELETE", "/api/v1/accounts/"+cr.ID+"/session", "")
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	_ = resp.Body.Close()
}
