package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devstroop/notalk/internal/model"
)

// ─── helpers ────────────────────────────────────────

// doReq performs an HTTP request with an optional bearer token and JSON body.
// It returns the response without closing the body so callers can inspect it.
func doReq(t *testing.T, srv *httptest.Server, method, path, token, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// expectStatus asserts the response status code and closes the body.
func expectStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != want {
		t.Errorf("expected status %d, got %d", want, resp.StatusCode)
	}
}

// decodeInto reads and decodes the response body into v.
func decodeInto(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode json: %v", err)
	}
}

// createUser creates a user via the admin secret key and returns the UserInfo.
func createUser(t *testing.T, srv *httptest.Server, username, password, roleID string) model.UserInfo {
	t.Helper()
	body := `{"username":"` + username + `","password":"` + password + `","role_id":"` + roleID + `"}`
	resp := doReq(t, srv, "POST", "/api/v1/users", testSecret, body)
	if resp.StatusCode != http.StatusCreated {
		_ = resp.Body.Close()
		t.Fatalf("create user %s: expected 201, got %d", username, resp.StatusCode)
	}
	var u model.UserInfo
	decodeInto(t, resp, &u)
	return u
}

// loginUser logs in and returns the JWT token.
func loginUser(t *testing.T, srv *httptest.Server, username, password string) string {
	t.Helper()
	body := `{"username":"` + username + `","password":"` + password + `"}`
	resp := doReq(t, srv, "POST", "/api/v1/auth/login", "", body)
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("login %s: expected 200, got %d", username, resp.StatusCode)
	}
	var lr model.LoginResponse
	decodeInto(t, resp, &lr)
	return lr.Token
}

// createAccount creates an account via the admin secret key and returns the id.
func createAccount(t *testing.T, srv *httptest.Server, phone, name, userID string) string {
	t.Helper()
	body := `{"phone_number":"` + phone + `","account_name":"` + name + `"`
	if userID != "" {
		body += `,"user_id":"` + userID + `"`
	}
	body += `}`
	resp := doReq(t, srv, "POST", "/api/v1/accounts", testSecret, body)
	if resp.StatusCode != http.StatusCreated {
		_ = resp.Body.Close()
		t.Fatalf("create account %s: expected 201, got %d", name, resp.StatusCode)
	}
	var out struct{ ID string `json:"id"` }
	decodeInto(t, resp, &out)
	return out.ID
}

// ═══════════════════════════════════════════════════════
// RBAC Tests
// ═══════════════════════════════════════════════════════

func TestRBACHealthNoAuth(t *testing.T) {
	srv, _ := testServer(t)
	resp := doReq(t, srv, "GET", "/api/health", "", "")
	expectStatus(t, resp, 200)
}

func TestRBACNoTokenReturns401(t *testing.T) {
	srv, _ := testServer(t)
	resp := doReq(t, srv, "GET", "/api/v1/accounts", "", "")
	expectStatus(t, resp, 401)
}

func TestRBACBadTokenReturns401(t *testing.T) {
	srv, _ := testServer(t)
	resp := doReq(t, srv, "GET", "/api/v1/accounts", "wrong-token", "")
	expectStatus(t, resp, 401)
}

func TestRBACSecretKeyIsAdmin(t *testing.T) {
	srv, _ := testServer(t)
	resp := doReq(t, srv, "GET", "/api/v1/accounts", testSecret, "")
	expectStatus(t, resp, 200)
}

func TestRBACBuiltinRolesExist(t *testing.T) {
	srv, _ := testServer(t)
	resp := doReq(t, srv, "GET", "/api/v1/roles", testSecret, "")
	var lr model.RoleListResponse
	decodeInto(t, resp, &lr)
	if lr.Total != 2 {
		t.Fatalf("expected 2 built-in roles, got %d", lr.Total)
	}
	var foundAdmin, foundUser bool
	for _, r := range lr.Roles {
		if r.Name == "admin" && r.IsBuiltin {
			foundAdmin = true
		}
		if r.Name == "user" && r.IsBuiltin {
			foundUser = true
		}
	}
	if !foundAdmin || !foundUser {
		t.Error("missing admin or user built-in role")
	}
}

func TestRBACCreateCustomRole(t *testing.T) {
	srv, _ := testServer(t)
	body := `{"name":"viewer","description":"Read-only","permissions":["accounts:read","messages:read"]}`
	resp := doReq(t, srv, "POST", "/api/v1/roles", testSecret, body)
	if resp.StatusCode != http.StatusCreated {
		_ = resp.Body.Close()
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var ri model.RoleInfo
	decodeInto(t, resp, &ri)
	if ri.Name != "viewer" {
		t.Errorf("expected name viewer, got %s", ri.Name)
	}

	// Delete custom role should succeed
	resp = doReq(t, srv, "DELETE", "/api/v1/roles/"+ri.ID, testSecret, "")
	expectStatus(t, resp, 200)
}

func TestRBACDeleteBuiltinRoleBlocked(t *testing.T) {
	srv, _ := testServer(t)
	resp := doReq(t, srv, "DELETE", "/api/v1/roles/builtin-admin", testSecret, "")
	expectStatus(t, resp, 403)
	resp = doReq(t, srv, "DELETE", "/api/v1/roles/builtin-user", testSecret, "")
	expectStatus(t, resp, 403)
}

func TestRBACLoginUnknownUser(t *testing.T) {
	srv, _ := testServer(t)
	resp := doReq(t, srv, "POST", "/api/v1/auth/login", "", `{"username":"nobody","password":"nopassword1"}`)
	expectStatus(t, resp, 401)
}

func TestRBACLoginWrongPassword(t *testing.T) {
	srv, _ := testServer(t)
	createUser(t, srv, "alice", "alicepass123", "builtin-admin")
	resp := doReq(t, srv, "POST", "/api/v1/auth/login", "", `{"username":"alice","password":"wrongpassword"}`)
	expectStatus(t, resp, 401)
}

func TestRBACDuplicateUsername(t *testing.T) {
	srv, _ := testServer(t)
	createUser(t, srv, "alice", "alicepass123", "builtin-admin")
	resp := doReq(t, srv, "POST", "/api/v1/users", testSecret,
		`{"username":"alice","password":"whatever123","role_id":"builtin-user"}`)
	expectStatus(t, resp, 409)
}

func TestRBACValidation(t *testing.T) {
	srv, _ := testServer(t)
	// Short password
	resp := doReq(t, srv, "POST", "/api/v1/users", testSecret,
		`{"username":"x","password":"short","role_id":"builtin-user"}`)
	expectStatus(t, resp, 400)
	// Bad role_id
	resp = doReq(t, srv, "POST", "/api/v1/users", testSecret,
		`{"username":"x","password":"longpassword","role_id":"nonexistent"}`)
	expectStatus(t, resp, 400)
}

func TestRBACAdminPermissions(t *testing.T) {
	srv, _ := testServer(t)
	createUser(t, srv, "alice", "alicepass123", "builtin-admin")
	aliceJWT := loginUser(t, srv, "alice", "alicepass123")

	// Admin can list accounts, users, roles
	expectStatus(t, doReq(t, srv, "GET", "/api/v1/accounts", aliceJWT, ""), 200)
	expectStatus(t, doReq(t, srv, "GET", "/api/v1/users", aliceJWT, ""), 200)
	expectStatus(t, doReq(t, srv, "GET", "/api/v1/roles", aliceJWT, ""), 200)
}

func TestRBACUserPermissions(t *testing.T) {
	srv, _ := testServer(t)
	bob := createUser(t, srv, "bob", "bobsecure123", "builtin-user")
	bobJWT := loginUser(t, srv, "bob", "bobsecure123")

	// User can list accounts (but sees 0 — none assigned)
	resp := doReq(t, srv, "GET", "/api/v1/accounts", bobJWT, "")
	var al model.AccountListResponse
	decodeInto(t, resp, &al)
	if al.Total != 0 {
		t.Errorf("expected bob to see 0 accounts, got %d", al.Total)
	}

	// User cannot list users, roles, or create users/accounts
	expectStatus(t, doReq(t, srv, "GET", "/api/v1/users", bobJWT, ""), 403)
	expectStatus(t, doReq(t, srv, "GET", "/api/v1/roles", bobJWT, ""), 403)
	expectStatus(t, doReq(t, srv, "POST", "/api/v1/users", bobJWT,
		`{"username":"hacker","password":"hacking123","role_id":"builtin-admin"}`), 403)
	expectStatus(t, doReq(t, srv, "POST", "/api/v1/accounts", bobJWT,
		`{"phone_number":"1111111111","account_name":"hack"}`), 403)

	// User can read own profile
	resp = doReq(t, srv, "GET", "/api/v1/users/"+bob.ID, bobJWT, "")
	var ui model.UserInfo
	decodeInto(t, resp, &ui)
	if ui.Username != "bob" {
		t.Errorf("expected bob, got %s", ui.Username)
	}
}

func TestRBACUserCannotReadOtherUser(t *testing.T) {
	srv, _ := testServer(t)
	alice := createUser(t, srv, "alice", "alicepass123", "builtin-admin")
	createUser(t, srv, "bob", "bobsecure123", "builtin-user")
	bobJWT := loginUser(t, srv, "bob", "bobsecure123")

	resp := doReq(t, srv, "GET", "/api/v1/users/"+alice.ID, bobJWT, "")
	expectStatus(t, resp, 403)
}

func TestRBACAccountOwnership(t *testing.T) {
	srv, _ := testServer(t)
	user1 := createUser(t, srv, "user1", "user1pass123", "builtin-user")
	createUser(t, srv, "user2", "user2pass123", "builtin-user")
	jwt1 := loginUser(t, srv, "user1", "user1pass123")
	jwt2 := loginUser(t, srv, "user2", "user2pass123")

	// Admin creates accounts assigned to user1
	createAccount(t, srv, "9100000001", "Acct1", user1.ID)
	createAccount(t, srv, "9100000002", "Acct2", user1.ID)

	// user1 sees 2 accounts
	resp := doReq(t, srv, "GET", "/api/v1/accounts", jwt1, "")
	var al model.AccountListResponse
	decodeInto(t, resp, &al)
	if al.Total != 2 {
		t.Errorf("expected user1 to see 2 accounts, got %d", al.Total)
	}

	// user2 sees 0 accounts
	resp = doReq(t, srv, "GET", "/api/v1/accounts", jwt2, "")
	decodeInto(t, resp, &al)
	if al.Total != 0 {
		t.Errorf("expected user2 to see 0 accounts, got %d", al.Total)
	}
}

func TestRBACDisableAndReenableUser(t *testing.T) {
	srv, _ := testServer(t)
	bob := createUser(t, srv, "bob", "bobsecure123", "builtin-user")

	// Disable
	resp := doReq(t, srv, "PATCH", "/api/v1/users/"+bob.ID, testSecret, `{"enabled":false}`)
	expectStatus(t, resp, 200)

	// Disabled user cannot login
	resp = doReq(t, srv, "POST", "/api/v1/auth/login", "", `{"username":"bob","password":"bobsecure123"}`)
	expectStatus(t, resp, 403)

	// Re-enable
	resp = doReq(t, srv, "PATCH", "/api/v1/users/"+bob.ID, testSecret, `{"enabled":true}`)
	expectStatus(t, resp, 200)

	// Now can login
	resp = doReq(t, srv, "POST", "/api/v1/auth/login", "", `{"username":"bob","password":"bobsecure123"}`)
	expectStatus(t, resp, 200)
}

func TestRBACDeleteUser(t *testing.T) {
	srv, _ := testServer(t)
	bob := createUser(t, srv, "bob", "bobsecure123", "builtin-user")
	resp := doReq(t, srv, "DELETE", "/api/v1/users/"+bob.ID, testSecret, "")
	expectStatus(t, resp, 200)
}

// ═══════════════════════════════════════════════════════
// API Key Tests
// ═══════════════════════════════════════════════════════

func TestAPIKeyJWTAuthWorks(t *testing.T) {
	srv, _ := testServer(t)
	createUser(t, srv, "user1", "user1pass123", "builtin-user")
	jwt1 := loginUser(t, srv, "user1", "user1pass123")

	resp := doReq(t, srv, "GET", "/api/v1/api-keys", jwt1, "")
	var kl model.APIKeyListResponse
	decodeInto(t, resp, &kl)
	if kl.Total != 0 {
		t.Errorf("expected 0 keys, got %d", kl.Total)
	}
}

func TestAPIKeyMissingAuth(t *testing.T) {
	srv, _ := testServer(t)
	resp := doReq(t, srv, "GET", "/api/v1/api-keys", "", "")
	expectStatus(t, resp, 401)
}

func TestAPIKeyInvalidToken(t *testing.T) {
	srv, _ := testServer(t)
	resp := doReq(t, srv, "GET", "/api/v1/api-keys", "invalid", "")
	expectStatus(t, resp, 401)
}

func TestAPIKeyCRUD(t *testing.T) {
	srv, _ := testServer(t)
	user1 := createUser(t, srv, "user1", "user1pass123", "builtin-user")
	jwt1 := loginUser(t, srv, "user1", "user1pass123")
	acct1 := createAccount(t, srv, "9100000001", "Acct1", user1.ID)

	// Create basic API key (no account binding)
	resp := doReq(t, srv, "POST", "/api/v1/api-keys", jwt1, `{"name":"Basic Key"}`)
	if resp.StatusCode != http.StatusCreated {
		_ = resp.Body.Close()
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var key1 model.CreateAPIKeyResponse
	decodeInto(t, resp, &key1)
	if !strings.HasPrefix(key1.Key, "notalk_") {
		t.Errorf("expected notalk_ prefix, got %s", key1.Key[:10])
	}
	if key1.Name != "Basic Key" {
		t.Errorf("expected 'Basic Key', got %s", key1.Name)
	}

	// Create account-bound API key
	resp = doReq(t, srv, "POST", "/api/v1/api-keys", jwt1,
		`{"name":"Bound Key","account_id":"`+acct1+`"}`)
	if resp.StatusCode != http.StatusCreated {
		_ = resp.Body.Close()
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var key2 model.CreateAPIKeyResponse
	decodeInto(t, resp, &key2)
	if key2.AccountID == nil || *key2.AccountID != acct1 {
		t.Error("expected key bound to account")
	}

	// Create key with expiry
	exp := time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	resp = doReq(t, srv, "POST", "/api/v1/api-keys", jwt1,
		`{"name":"Expiring Key","expires_at":"`+exp+`"}`)
	if resp.StatusCode != http.StatusCreated {
		_ = resp.Body.Close()
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var key3 model.CreateAPIKeyResponse
	decodeInto(t, resp, &key3)
	if key3.ExpiresAt == nil {
		t.Error("expected expiry on key")
	}

	// List keys — should see 3
	resp = doReq(t, srv, "GET", "/api/v1/api-keys", jwt1, "")
	var kl model.APIKeyListResponse
	decodeInto(t, resp, &kl)
	if kl.Total != 3 {
		t.Errorf("expected 3 keys, got %d", kl.Total)
	}

	// Verify account_id appears in list
	var foundBound bool
	for _, k := range kl.Keys {
		if k.Name == "Bound Key" && k.AccountID != nil && *k.AccountID == acct1 {
			foundBound = true
		}
	}
	if !foundBound {
		t.Error("bound key not found in list with correct account_id")
	}
}

func TestAPIKeyNameRequired(t *testing.T) {
	srv, _ := testServer(t)
	createUser(t, srv, "user1", "user1pass123", "builtin-user")
	jwt1 := loginUser(t, srv, "user1", "user1pass123")

	resp := doReq(t, srv, "POST", "/api/v1/api-keys", jwt1, `{"name":""}`)
	expectStatus(t, resp, 400)
}

func TestAPIKeyCannotBindToOtherUsersAccount(t *testing.T) {
	srv, _ := testServer(t)
	createUser(t, srv, "user1", "user1pass123", "builtin-user")
	user2 := createUser(t, srv, "user2", "user2pass123", "builtin-user")
	jwt1 := loginUser(t, srv, "user1", "user1pass123")
	acct3 := createAccount(t, srv, "9100000003", "Acct3", user2.ID)

	resp := doReq(t, srv, "POST", "/api/v1/api-keys", jwt1,
		`{"name":"Bad Bind","account_id":"`+acct3+`"}`)
	expectStatus(t, resp, 403)
}

func TestAPIKeyCannotBindToNonexistentAccount(t *testing.T) {
	srv, _ := testServer(t)
	createUser(t, srv, "user1", "user1pass123", "builtin-user")
	jwt1 := loginUser(t, srv, "user1", "user1pass123")

	resp := doReq(t, srv, "POST", "/api/v1/api-keys", jwt1,
		`{"name":"Bad","account_id":"nonexistent"}`)
	expectStatus(t, resp, 404)
}

func TestAPIKeyOtherUserSeesNoKeys(t *testing.T) {
	srv, _ := testServer(t)
	createUser(t, srv, "user1", "user1pass123", "builtin-user")
	createUser(t, srv, "user2", "user2pass123", "builtin-user")
	jwt1 := loginUser(t, srv, "user1", "user1pass123")
	jwt2 := loginUser(t, srv, "user2", "user2pass123")

	// user1 creates a key
	_ = doReq(t, srv, "POST", "/api/v1/api-keys", jwt1, `{"name":"mine"}`).Body.Close()

	// user2 sees 0 keys
	resp := doReq(t, srv, "GET", "/api/v1/api-keys", jwt2, "")
	var kl model.APIKeyListResponse
	decodeInto(t, resp, &kl)
	if kl.Total != 0 {
		t.Errorf("expected user2 to see 0 keys, got %d", kl.Total)
	}
}

func TestAPIKeyAuthenticates(t *testing.T) {
	srv, _ := testServer(t)
	createUser(t, srv, "user1", "user1pass123", "builtin-user")
	jwt1 := loginUser(t, srv, "user1", "user1pass123")

	// Create key
	resp := doReq(t, srv, "POST", "/api/v1/api-keys", jwt1, `{"name":"auth-test"}`)
	var key model.CreateAPIKeyResponse
	decodeInto(t, resp, &key)

	// Use API key to authenticate
	resp = doReq(t, srv, "GET", "/api/v1/api-keys", key.Key, "")
	var kl model.APIKeyListResponse
	decodeInto(t, resp, &kl)
	if kl.Total != 1 {
		t.Errorf("expected 1 key via api-key auth, got %d", kl.Total)
	}
}

func TestAPIKeyInheritsUserPermissions(t *testing.T) {
	srv, _ := testServer(t)
	createUser(t, srv, "user1", "user1pass123", "builtin-user")
	jwt1 := loginUser(t, srv, "user1", "user1pass123")

	resp := doReq(t, srv, "POST", "/api/v1/api-keys", jwt1, `{"name":"perm-test"}`)
	var key model.CreateAPIKeyResponse
	decodeInto(t, resp, &key)

	// User role cannot list users → API key inherits that restriction
	expectStatus(t, doReq(t, srv, "GET", "/api/v1/users", key.Key, ""), 403)
}

func TestAPIKeyRevokedKeyRejected(t *testing.T) {
	srv, _ := testServer(t)
	createUser(t, srv, "user1", "user1pass123", "builtin-user")
	jwt1 := loginUser(t, srv, "user1", "user1pass123")

	// Create and delete a key
	resp := doReq(t, srv, "POST", "/api/v1/api-keys", jwt1, `{"name":"tmp"}`)
	var key model.CreateAPIKeyResponse
	decodeInto(t, resp, &key)

	_ = doReq(t, srv, "DELETE", "/api/v1/api-keys/"+key.ID, jwt1, "").Body.Close()

	// Revoked key is rejected
	expectStatus(t, doReq(t, srv, "GET", "/api/v1/api-keys", key.Key, ""), 401)
}

func TestAPIKeyInvalidFormatRejected(t *testing.T) {
	srv, _ := testServer(t)
	resp := doReq(t, srv, "GET", "/api/v1/api-keys", "notalk_invalid", "")
	expectStatus(t, resp, 401)
}

func TestAPIKeyOwnershipDelete(t *testing.T) {
	srv, _ := testServer(t)
	createUser(t, srv, "user1", "user1pass123", "builtin-user")
	createUser(t, srv, "user2", "user2pass123", "builtin-user")
	jwt1 := loginUser(t, srv, "user1", "user1pass123")
	jwt2 := loginUser(t, srv, "user2", "user2pass123")

	// user1 creates a key
	resp := doReq(t, srv, "POST", "/api/v1/api-keys", jwt1, `{"name":"owned"}`)
	var key model.CreateAPIKeyResponse
	decodeInto(t, resp, &key)

	// user2 cannot delete user1's key
	expectStatus(t, doReq(t, srv, "DELETE", "/api/v1/api-keys/"+key.ID, jwt2, ""), 403)

	// user1 can delete own key
	resp = doReq(t, srv, "DELETE", "/api/v1/api-keys/"+key.ID, jwt1, "")
	var del map[string]string
	decodeInto(t, resp, &del)
	if del["status"] != "deleted" {
		t.Errorf("expected deleted, got %s", del["status"])
	}
}

func TestAPIKeyAdminCanDeleteAnyKey(t *testing.T) {
	srv, _ := testServer(t)
	createUser(t, srv, "user1", "user1pass123", "builtin-user")
	createUser(t, srv, "admin1", "adminpass123", "builtin-admin")
	jwt1 := loginUser(t, srv, "user1", "user1pass123")
	adminJWT := loginUser(t, srv, "admin1", "adminpass123")

	resp := doReq(t, srv, "POST", "/api/v1/api-keys", jwt1, `{"name":"admin-del"}`)
	var key model.CreateAPIKeyResponse
	decodeInto(t, resp, &key)

	resp = doReq(t, srv, "DELETE", "/api/v1/api-keys/"+key.ID, adminJWT, "")
	var del map[string]string
	decodeInto(t, resp, &del)
	if del["status"] != "deleted" {
		t.Errorf("expected deleted, got %s", del["status"])
	}
}

func TestAPIKeyDeleteNonexistent(t *testing.T) {
	srv, _ := testServer(t)
	createUser(t, srv, "user1", "user1pass123", "builtin-user")
	jwt1 := loginUser(t, srv, "user1", "user1pass123")

	expectStatus(t, doReq(t, srv, "DELETE", "/api/v1/api-keys/nonexistent", jwt1, ""), 404)
}

func TestAPIKeyAdminCanBindToAnyAccount(t *testing.T) {
	srv, _ := testServer(t)
	user2 := createUser(t, srv, "user2", "user2pass123", "builtin-user")
	createUser(t, srv, "admin1", "adminpass123", "builtin-admin")
	adminJWT := loginUser(t, srv, "admin1", "adminpass123")
	acct3 := createAccount(t, srv, "9100000003", "Acct3", user2.ID)

	resp := doReq(t, srv, "POST", "/api/v1/api-keys", adminJWT,
		`{"name":"Admin Bound","account_id":"`+acct3+`"}`)
	if resp.StatusCode != http.StatusCreated {
		_ = resp.Body.Close()
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var key model.CreateAPIKeyResponse
	decodeInto(t, resp, &key)
	if key.AccountID == nil || *key.AccountID != acct3 {
		t.Error("expected admin to bind key to other user's account")
	}
}

func TestAPIKeyUserRBACPermissions(t *testing.T) {
	srv, _ := testServer(t)
	user1 := createUser(t, srv, "user1", "user1pass123", "builtin-user")
	jwt1 := loginUser(t, srv, "user1", "user1pass123")

	acct1 := createAccount(t, srv, "9100000001", "Acct1", user1.ID)
	createAccount(t, srv, "9100000002", "Acct2", user1.ID)

	// User can read own accounts
	resp := doReq(t, srv, "GET", "/api/v1/accounts", jwt1, "")
	var al model.AccountListResponse
	decodeInto(t, resp, &al)
	if al.Total != 2 {
		t.Errorf("expected user to see 2 accounts, got %d", al.Total)
	}

	// User cannot manage users or roles
	expectStatus(t, doReq(t, srv, "GET", "/api/v1/users", jwt1, ""), 403)
	expectStatus(t, doReq(t, srv, "POST", "/api/v1/roles", jwt1,
		`{"name":"hack","permissions":["*"]}`), 403)
	expectStatus(t, doReq(t, srv, "GET", "/api/v1/roles", jwt1, ""), 403)

	// Admin account-bound key sees only owner's accounts
	resp = doReq(t, srv, "POST", "/api/v1/api-keys", jwt1,
		`{"name":"Bound","account_id":"`+acct1+`"}`)
	var bk model.CreateAPIKeyResponse
	decodeInto(t, resp, &bk)

	resp = doReq(t, srv, "GET", "/api/v1/accounts", bk.Key, "")
	decodeInto(t, resp, &al)
	if al.Total != 2 {
		t.Errorf("expected account-bound key to see 2 accounts, got %d", al.Total)
	}
}

func TestAPIKeyAdminSeesAllAccounts(t *testing.T) {
	srv, _ := testServer(t)
	user1 := createUser(t, srv, "user1", "user1pass123", "builtin-user")
	createUser(t, srv, "admin1", "adminpass123", "builtin-admin")
	adminJWT := loginUser(t, srv, "admin1", "adminpass123")

	createAccount(t, srv, "9100000001", "Acct1", user1.ID)
	createAccount(t, srv, "9100000002", "Acct2", user1.ID)
	createAccount(t, srv, "9100000003", "Acct3", "")

	resp := doReq(t, srv, "GET", "/api/v1/accounts", adminJWT, "")
	var al model.AccountListResponse
	decodeInto(t, resp, &al)
	if al.Total != 3 {
		t.Errorf("expected admin to see 3 accounts, got %d", al.Total)
	}
}

func TestAPIKeyLastUsedUpdates(t *testing.T) {
	srv, _ := testServer(t)
	createUser(t, srv, "user1", "user1pass123", "builtin-user")
	jwt1 := loginUser(t, srv, "user1", "user1pass123")

	resp := doReq(t, srv, "POST", "/api/v1/api-keys", jwt1, `{"name":"track-use"}`)
	var key model.CreateAPIKeyResponse
	decodeInto(t, resp, &key)

	// Use the key
	_ = doReq(t, srv, "GET", "/api/v1/api-keys", key.Key, "").Body.Close()

	// Small wait for async last_used update
	time.Sleep(200 * time.Millisecond)

	// Check last_used is set
	resp = doReq(t, srv, "GET", "/api/v1/api-keys", jwt1, "")
	var kl model.APIKeyListResponse
	decodeInto(t, resp, &kl)
	for _, k := range kl.Keys {
		if k.ID == key.ID && k.LastUsed == nil {
			t.Error("expected last_used to be set after use")
		}
	}
}
