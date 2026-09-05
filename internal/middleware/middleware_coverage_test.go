package middleware

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/devstroop/notalk/internal/database"
	"github.com/golang-jwt/jwt/v5"
	_ "github.com/lib/pq"
)

func cleanDB(t *testing.T) {
	t.Helper()
	dsn := os.Getenv("NOTALK_TEST_DSN")
	if dsn == "" {
		return
	}
	raw, _ := sql.Open("postgres", dsn)
	if raw == nil {
		return
	}
	defer func() { _ = raw.Close() }()
	for _, table := range []string{
		"password_reset_token", "api_key", "agent_log", "agent_config", "agent_session",
		"usage", "subscription", "message", "webhook_config", "proxy_config",
		"account", "role_permission", "app_user",
	} {
		_, _ = raw.Exec("DELETE FROM " + table + " WHERE TRUE")
	}
	// re-seed builtin roles if needed (database.Open will have seeded, but we deleted role_permission)
	// Re-seed via opening DB which calls migrate
	db, _ := database.Open(dsn)
	if db != nil {
		_ = db.Close()
	}
}

func TestHasPermission(t *testing.T) {
	admin := &Identity{Permissions: []string{"*"}}
	if !admin.HasPermission("anything:read") {
		t.Error("admin should have all")
	}
	user := &Identity{Permissions: []string{"messages:*", "accounts:read"}}
	if !user.HasPermission("messages:read") {
		t.Error("should match messages:*")
	}
	if !user.HasPermission("messages:write") {
		t.Error("should match messages:write")
	}
	if user.HasPermission("accounts:write") {
		t.Error("should not match")
	}
	if !user.HasPermission("accounts:read") {
		t.Error("exact match failed")
	}
	empty := &Identity{Permissions: []string{}}
	if empty.HasPermission("messages:read") {
		t.Error("empty should not have")
	}
}

func TestGetIdentityAndFromContext(t *testing.T) {
	id := &Identity{UserID: "123", Username: "bob", RoleName: "user", Permissions: []string{"messages:read"}}
	req := httptest.NewRequest("GET", "/", nil)
	ctx := context.WithValue(req.Context(), identityKey, id)
	req = req.WithContext(ctx)
	got := GetIdentity(req)
	if got == nil || got.UserID != "123" {
		t.Error("GetIdentity failed")
	}
	got2 := GetIdentityFromContext(ctx)
	if got2 == nil || got2.Username != "bob" {
		t.Error("GetIdentityFromContext failed")
	}
	if GetIdentity(httptest.NewRequest("GET", "/", nil)) != nil {
		t.Error("expected nil for missing")
	}
	if GetIdentityFromContext(context.Background()) != nil {
		t.Error("expected nil")
	}
	if GetScopedAccountID(context.Background()) != "" {
		t.Error("expected empty")
	}
	ctx2 := context.WithValue(context.Background(), scopedAccountKey, "acc123")
	if GetScopedAccountID(ctx2) != "acc123" {
		t.Error("scoped failed")
	}
}

func TestRequirePermission(t *testing.T) {
	handler := RequirePermission("messages:read", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	// no identity
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != 401 {
		t.Errorf("expected 401 got %d", w.Code)
	}
	// with identity lacking permission
	id := &Identity{Permissions: []string{"accounts:read"}}
	req = httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), identityKey, id))
	w = httptest.NewRecorder()
	handler(w, req)
	if w.Code != 403 {
		t.Errorf("expected 403 got %d", w.Code)
	}
	// with permission
	id.Permissions = []string{"messages:read"}
	req = httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), identityKey, id))
	w = httptest.NewRecorder()
	handler(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200 got %d", w.Code)
	}
	// wildcard
	id.Permissions = []string{"*"}
	req = httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), identityKey, id))
	w = httptest.NewRecorder()
	handler(w, req)
	if w.Code != 200 {
		t.Error("wildcard failed")
	}
}

func TestRateLimit(t *testing.T) {
	// disabled
	h := RateLimit(0, okHandler)
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Error("disabled should pass")
	}
	// limited - handler that blocks until context done
	_ = RateLimit(1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// hold
		<-r.Context().Done()
	}))
	// We test the default case (429) via the integration test already; here test the success path
	h2 := RateLimit(1, okHandler)
	w = httptest.NewRecorder()
	h2.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200 got %d", w.Code)
	}
}

func TestBillingEnforcerDisabled(t *testing.T) {
	h := BillingEnforcer(nil, false)(okHandler)
	req := httptest.NewRequest("GET", "/api/v1/accounts", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Error("disabled should pass")
	}
}

func TestBillingEnforcerSystemBypass(t *testing.T) {
	cleanDB(t)
	dsn := os.Getenv("NOTALK_TEST_DSN")
	if dsn == "" {
		t.Skip("NOTALK_TEST_DSN not set")
	}
	db, err := database.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	h := BillingEnforcer(db, true)(okHandler)
	// system admin bypass
	id := &Identity{UserID: "system", RoleName: "admin", Permissions: []string{"*"}}
	req := httptest.NewRequest("GET", "/api/v1/accounts", nil)
	req = req.WithContext(context.WithValue(req.Context(), identityKey, id))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("system should bypass got %d", w.Code)
	}
	// no identity
	req = httptest.NewRequest("GET", "/api/v1/accounts", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Error("no identity should pass to next (auth will handle)")
	}
}

func TestBillingEnforcerQuotaAndGates(t *testing.T) {
	cleanDB(t)
	dsn := os.Getenv("NOTALK_TEST_DSN")
	if dsn == "" {
		t.Skip("NOTALK_TEST_DSN not set")
	}
	db, err := database.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	// create user with free plan (20 msgs)
	uid := "bill-" + database.GenerateID()
	// create user
	if err := db.CreateUser(&database.UserRecord{ID: uid, Username: "billuser-" + uid, Email: "b@e.com", PasswordHash: "h", RoleID: "builtin-user", Enabled: true}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := db.EnsureUserSubscription(uid, "free"); err != nil {
		t.Fatalf("EnsureUserSubscription: %v", err)
	}
	// ensure we have a user with free plan
	h := BillingEnforcer(db, true)(okHandler)

	id := &Identity{UserID: uid, RoleName: "user", Permissions: []string{"messages:*"}}

	// Test message quota not exceeded (should pass)
	req := httptest.NewRequest("POST", "/api/v1/accounts/123/messages", nil)
	req = req.WithContext(context.WithValue(req.Context(), identityKey, id))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200 for under quota got %d", w.Code)
	}

	// Increment usage to limit
	for i := 0; i < 20; i++ {
		if _, err := db.IncrementUsage(uid); err != nil {
			t.Fatalf("IncrementUsage %d: %v", i, err)
		}
	}
	// verify usage
	if usage, _ := db.GetDailyUsage(uid); usage != 20 {
		t.Fatalf("expected usage 20 got %d", usage)
	}
	if limits, _, _ := db.GetUserPlanLimits(uid); limits.DailyMessages != 20 {
		t.Fatalf("expected limits 20 got %d", limits.DailyMessages)
	}
	req = httptest.NewRequest("POST", "/api/v1/accounts/123/messages", nil)
	req = req.WithContext(context.WithValue(req.Context(), identityKey, id))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 429 {
		t.Errorf("expected 429 over quota got %d", w.Code)
	}

	// Feature gate: mcp
	req = httptest.NewRequest("GET", "/mcp", nil)
	req = req.WithContext(context.WithValue(req.Context(), identityKey, id))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 429 {
		t.Errorf("expected 429 for mcp gate got %d", w.Code)
	}

	// webhook gate
	req = httptest.NewRequest("PUT", "/api/v1/accounts/123/webhook", nil)
	req = req.WithContext(context.WithValue(req.Context(), identityKey, id))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 429 {
		t.Errorf("expected 429 for webhook gate got %d", w.Code)
	}
	// GET webhook should pass (read)
	req = httptest.NewRequest("GET", "/api/v1/accounts/123/webhook", nil)
	req = req.WithContext(context.WithValue(req.Context(), identityKey, id))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("GET webhook should pass got %d", w.Code)
	}
}

func TestIncrementMessageUsage(t *testing.T) {
	cleanDB(t)
	dsn := os.Getenv("NOTALK_TEST_DSN")
	if dsn == "" {
		t.Skip("NOTALK_TEST_DSN not set")
	}
	db, _ := database.Open(dsn)
	defer func() { _ = db.Close() }()
	// disabled
	IncrementMessageUsage(db, false, "user1")
	// system
	IncrementMessageUsage(db, true, "system")
	// empty
	IncrementMessageUsage(db, true, "")
	// valid - should not panic
	incUID := "inc-" + database.GenerateID()
	_ = db.CreateUser(&database.UserRecord{ID: incUID, Username: "incuser-" + incUID, Email: "i@e.com", PasswordHash: "h", RoleID: "builtin-user", Enabled: true})
	IncrementMessageUsage(db, true, incUID)
	// verify incremented
	c, _ := db.GetDailyUsage(incUID)
	if c != 1 {
		t.Errorf("expected 1 got %d", c)
	}
}

func TestAuthJWTAndAPIKey(t *testing.T) {
	cleanDB(t)
	dsn := os.Getenv("NOTALK_TEST_DSN")
	if dsn == "" {
		t.Skip("NOTALK_TEST_DSN not set")
	}
	db, _ := database.Open(dsn)
	defer func() { _ = db.Close() }()
	secret := "test-secret-for-jwt"
	// create user
	uid := "jwt-" + database.GenerateID()
	_ = db.CreateUser(&database.UserRecord{ID: uid, Username: "jwtuser-" + uid, Email: "j@e.com", PasswordHash: "h", RoleID: "builtin-user", Enabled: true})
	// disabled user cannot auth
	uidDisabled := "jwt-dis-" + database.GenerateID()
	_ = db.CreateUser(&database.UserRecord{ID: uidDisabled, Username: "disableduser-" + uidDisabled, Email: "d@e.com", PasswordHash: "h", RoleID: "builtin-user", Enabled: false})
	// create JWT for disabled
	claims := jwt.RegisteredClaims{Subject: uidDisabled}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	disabledJWT, _ := tok.SignedString([]byte(secret))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+disabledJWT)
	w := httptest.NewRecorder()
	Auth(secret, db, okHandler).ServeHTTP(w, req)
	if w.Code != 403 {
		t.Errorf("disabled should be 403 got %d", w.Code)
	}
	// valid JWT
	claims = jwt.RegisteredClaims{Subject: uid}
	tok = jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	validJWT, _ := tok.SignedString([]byte(secret))
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+validJWT)
	w = httptest.NewRecorder()
	Auth(secret, db, okHandler).ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("valid JWT should be 200 got %d body %s", w.Code, w.Body.String())
	}
	// invalid subject empty
	claims = jwt.RegisteredClaims{Subject: ""}
	tok = jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	emptySubJWT, _ := tok.SignedString([]byte(secret))
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+emptySubJWT)
	w = httptest.NewRecorder()
	Auth(secret, db, okHandler).ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("empty sub should be 401 got %d", w.Code)
	}
	// non-existent user
	claims = jwt.RegisteredClaims{Subject: "nonexistent-uid-xyz"}
	tok = jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	badUserJWT, _ := tok.SignedString([]byte(secret))
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+badUserJWT)
	w = httptest.NewRecorder()
	Auth(secret, db, okHandler).ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("bad user should be 401 got %d", w.Code)
	}
}

func TestMCPScope(t *testing.T) {
	cleanDB(t)
	dsn := os.Getenv("NOTALK_TEST_DSN")
	if dsn == "" {
		t.Skip("NOTALK_TEST_DSN not set")
	}
	db, _ := database.Open(dsn)
	defer func() { _ = db.Close() }()

	// no identity
	req := httptest.NewRequest("GET", "/mcp?account_id=acc1", nil)
	w := httptest.NewRecorder()
	MCPScope(db, okHandler).ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("no identity should be 401 got %d", w.Code)
	}

	// admin without account_id should pass
	admin := &Identity{UserID: "system", RoleName: "admin", Permissions: []string{"*"}}
	req = httptest.NewRequest("GET", "/mcp", nil)
	req = req.WithContext(context.WithValue(req.Context(), identityKey, admin))
	w = httptest.NewRecorder()
	MCPScope(db, okHandler).ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("admin without account should pass got %d", w.Code)
	}

	// non-admin without account_id should 400
	user := &Identity{UserID: "user1", RoleName: "user", Permissions: []string{"messages:read"}}
	req = httptest.NewRequest("GET", "/mcp", nil)
	req = req.WithContext(context.WithValue(req.Context(), identityKey, user))
	w = httptest.NewRecorder()
	MCPScope(db, okHandler).ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("user without account should be 400 got %d", w.Code)
	}

	// non-admin with non-existent account
	req = httptest.NewRequest("GET", "/mcp?account_id=nonexistent123", nil)
	req = req.WithContext(context.WithValue(req.Context(), identityKey, user))
	w = httptest.NewRecorder()
	MCPScope(db, okHandler).ServeHTTP(w, req)
	if w.Code != 404 {
		t.Errorf("expected 404 got %d", w.Code)
	}

	// create user and account for ownership test
	uid := "mcp-" + database.GenerateID()
	_ = db.CreateUser(&database.UserRecord{ID: uid, Username: "mcpuser1-" + uid, Email: "m@e.com", PasswordHash: "h", RoleID: "builtin-user", Enabled: true})
	acc1 := "mcp-acc-" + database.GenerateID()
	_ = db.CreateAccount(&database.AccountRecord{ID: acc1, PhoneNumber: "9000000001", AccountName: "a", DataDir: "/tmp/a", UserID: uid})
	otherUID := "mcp-" + database.GenerateID()
	_ = db.CreateUser(&database.UserRecord{ID: otherUID, Username: "mcpuser2-" + otherUID, Email: "m2@e.com", PasswordHash: "h", RoleID: "builtin-user", Enabled: true})
	acc2 := "mcp-acc-" + database.GenerateID()
	_ = db.CreateAccount(&database.AccountRecord{ID: acc2, PhoneNumber: "9000000002", AccountName: "b", DataDir: "/tmp/b", UserID: otherUID})

	owner := &Identity{UserID: uid, RoleName: "user", Permissions: []string{"messages:read"}}
	// owner should pass
	req = httptest.NewRequest("GET", "/mcp?account_id="+acc1, nil)
	req = req.WithContext(context.WithValue(req.Context(), identityKey, owner))
	w = httptest.NewRecorder()
	MCPScope(db, okHandler).ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("owner should pass got %d", w.Code)
	}
	// non-owner should 403
	req = httptest.NewRequest("GET", "/mcp?account_id="+acc2, nil)
	req = req.WithContext(context.WithValue(req.Context(), identityKey, owner))
	w = httptest.NewRecorder()
	MCPScope(db, okHandler).ServeHTTP(w, req)
	if w.Code != 403 {
		t.Errorf("non-owner should be 403 got %d", w.Code)
	}

	// account-bound API key scope (already in context)
	ctx := context.WithValue(context.Background(), identityKey, owner)
	ctx = context.WithValue(ctx, scopedAccountKey, acc1)
	req = httptest.NewRequest("GET", "/mcp", nil)
	req = req.WithContext(ctx)
	w = httptest.NewRecorder()
	MCPScope(db, okHandler).ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("bound account should pass got %d", w.Code)
	}
	// bound account not owned
	ctx = context.WithValue(context.Background(), identityKey, owner)
	ctx = context.WithValue(ctx, scopedAccountKey, acc2)
	req = httptest.NewRequest("GET", "/mcp", nil)
	req = req.WithContext(ctx)
	w = httptest.NewRecorder()
	MCPScope(db, okHandler).ServeHTTP(w, req)
	if w.Code != 403 {
		t.Errorf("bound not owned should be 403 got %d", w.Code)
	}
	// bound account not found (non-admin)
	ctx = context.WithValue(context.Background(), identityKey, owner)
	ctx = context.WithValue(ctx, scopedAccountKey, "nonexistent-bound")
	req = httptest.NewRequest("GET", "/mcp", nil)
	req = req.WithContext(ctx)
	w = httptest.NewRecorder()
	MCPScope(db, okHandler).ServeHTTP(w, req)
	if w.Code != 404 {
		t.Errorf("expected 404 for nonexistent bound got %d", w.Code)
	}
	// admin bound account not found should still pass? code checks !isAdmin, so admin passes
	adminCtx := context.WithValue(context.Background(), identityKey, admin)
	adminCtx = context.WithValue(adminCtx, scopedAccountKey, "any-nonexistent")
	req = httptest.NewRequest("GET", "/mcp", nil)
	req = req.WithContext(adminCtx)
	w = httptest.NewRecorder()
	MCPScope(db, okHandler).ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("admin bound nonexistent should pass got %d", w.Code)
	}
}
