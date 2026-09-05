package database

import (
	"testing"
	"time"
)

// ── UpdatePhone + User assignment ───────────────────────

func TestUpdatePhoneNumber(t *testing.T) {
	db := openTestDB(t)
	if err := db.CreateAccount(&AccountRecord{ID: "up-phone", PhoneNumber: "1112223333", AccountName: "a", DataDir: "/tmp/a"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdatePhoneNumber("up-phone", "9998887777"); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetAccount("up-phone")
	if got.PhoneNumber != "9998887777" {
		t.Errorf("expected 9998887777 got %s", got.PhoneNumber)
	}
}

func TestUpdateAccountUserIDAndListByUser(t *testing.T) {
	db := openTestDB(t)
	// need a role and user
	if err := db.CreateUser(&UserRecord{ID: "u1", Username: "u1", Email: "u1@e.com", PasswordHash: "h", RoleID: "builtin-user", Enabled: true}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.CreateAccount(&AccountRecord{ID: "acc1", PhoneNumber: "1000000001", AccountName: "a1", DataDir: "/tmp/a1", UserID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateAccount(&AccountRecord{ID: "acc2", PhoneNumber: "1000000002", AccountName: "a2", DataDir: "/tmp/a2"}); err != nil {
		t.Fatal(err)
	}
	// assign acc2 to u1
	if err := db.UpdateAccountUserID("acc2", "u1"); err != nil {
		t.Fatal(err)
	}
	list, err := db.ListAccountsByUser("u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 got %d", len(list))
	}
	// unassign
	if err := db.UpdateAccountUserID("acc2", ""); err != nil {
		t.Fatal(err)
	}
	list, _ = db.ListAccountsByUser("u1")
	if len(list) != 1 {
		t.Errorf("expected 1 after unassign got %d", len(list))
	}
}

// ── Proxy ───────────────────────────────────────────────

func TestProxyCRUD(t *testing.T) {
	db := openTestDB(t)
	if err := db.CreateAccount(&AccountRecord{ID: "px1", PhoneNumber: "2000000001", AccountName: "px", DataDir: "/tmp/px"}); err != nil {
		t.Fatal(err)
	}
	// get none
	got, err := db.GetProxyConfig("px1")
	if err != nil || got != nil {
		t.Fatalf("expected nil got %v err %v", got, err)
	}
	rec := &ProxyConfigRecord{AccountID: "px1", Protocol: "http", Host: "proxy.example.com", Port: 8080, Username: "u", Password: "p", Enabled: true}
	if err := db.UpsertProxyConfig(rec); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetProxyConfig("px1")
	if got.Host != "proxy.example.com" || got.Port != 8080 {
		t.Errorf("mismatch %+v", got)
	}
	// upsert update
	rec.Host = "proxy2.example.com"
	if err := db.UpsertProxyConfig(rec); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetProxyConfig("px1")
	if got.Host != "proxy2.example.com" {
		t.Error("expected updated host")
	}
	if err := db.DeleteProxyConfig("px1"); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetProxyConfig("px1")
	if got != nil {
		t.Error("expected nil after delete")
	}
}

// ── Message ─────────────────────────────────────────────

func TestMessageInsertListLastUnread(t *testing.T) {
	db := openTestDB(t)
	if err := db.CreateAccount(&AccountRecord{ID: "msg1", PhoneNumber: "3000000001", AccountName: "m", DataDir: "/tmp/m"}); err != nil {
		t.Fatal(err)
	}
	chat := "919999999999@s.whatsapp.net"
	msgs := []*MessageRecord{
		{ID: "m1", AccountID: "msg1", ChatJID: chat, SenderJID: "a@s.whatsapp.net", FromMe: false, Type: "text", Body: "hello1", Timestamp: "2026-01-01T00:00:01Z"},
		{ID: "m2", AccountID: "msg1", ChatJID: chat, SenderJID: "me@s.whatsapp.net", FromMe: true, Type: "text", Body: "hello2", Timestamp: "2026-01-01T00:00:02Z"},
		{ID: "m3", AccountID: "msg1", ChatJID: "other@s.whatsapp.net", SenderJID: "other@s.whatsapp.net", FromMe: false, Type: "text", Body: "hi", Timestamp: "2026-01-01T00:00:03Z"},
	}
	for _, m := range msgs {
		if err := db.InsertMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	// duplicate ignored
	if err := db.InsertMessage(msgs[0]); err != nil {
		t.Fatal(err)
	}
	list, err := db.ListMessages("msg1", chat, 10, "")
	if err != nil || len(list) != 2 {
		t.Fatalf("list %v len %d", err, len(list))
	}
	if list[0].ID != "m2" {
		t.Errorf("expected newest m2 got %s", list[0].ID)
	}
	// pagination before
	list, _ = db.ListMessages("msg1", chat, 10, "2026-01-01T00:00:02Z")
	if len(list) != 1 || list[0].ID != "m1" {
		t.Errorf("before filter failed %+v", list)
	}
	// limit
	list, _ = db.ListMessages("msg1", chat, 1, "")
	if len(list) != 1 {
		t.Error("limit failed")
	}

	last, err := db.GetLastMessagePerChat("msg1")
	if err != nil || len(last) != 2 {
		t.Fatalf("last %v err %v", last, err)
	}
	if last[chat].Body != "hello2" {
		t.Errorf("expected hello2 got %s", last[chat].Body)
	}

	unread, err := db.GetUnreadCountPerChat("msg1")
	if err != nil {
		t.Fatal(err)
	}
	// chat has 1 unread after last from_me? other chat has 1
	if unread["other@s.whatsapp.net"] != 1 {
		t.Errorf("expected 1 unread for other, got %d", unread["other@s.whatsapp.net"])
	}
}

// ── Webhook ─────────────────────────────────────────────

func TestWebhookCRUDCoverage(t *testing.T) {
	db := openTestDB(t)
	if err := db.CreateAccount(&AccountRecord{ID: "wh1", PhoneNumber: "4000000001", AccountName: "wh", DataDir: "/tmp/wh"}); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetWebhookConfig("wh1")
	if got != nil {
		t.Error("expected nil")
	}
	rec := &WebhookConfigRecord{AccountID: "wh1", URL: "https://example.com/hook", Secret: "s", Events: "message", Enabled: true}
	if err := db.UpsertWebhookConfig(rec); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetWebhookConfig("wh1")
	if got.URL != "https://example.com/hook" {
		t.Error("mismatch")
	}
	rec.URL = "https://other.com/hook"
	if err := db.UpsertWebhookConfig(rec); err != nil {
		t.Fatalf("UpsertWebhookConfig: %v", err)
	}
	got, _ = db.GetWebhookConfig("wh1")
	if got.URL != "https://other.com/hook" {
		t.Error("update failed")
	}
	if err := db.DeleteWebhookConfig("wh1"); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetWebhookConfig("wh1")
	if got != nil {
		t.Error("expected nil after delete")
	}
}

// ── Role ────────────────────────────────────────────────

func TestRoleCRUDAndPermissions(t *testing.T) {
	db := openTestDB(t)
	// create custom
	if err := db.CreateRole(&RoleRecord{ID: "role1", Name: "viewer", Description: "read", IsBuiltin: false}); err != nil {
		t.Fatal(err)
	}
	// duplicate name should be handled? just check get
	got, err := db.GetRole("role1")
	if err != nil || got.Name != "viewer" {
		t.Fatalf("get %v %v", got, err)
	}
	got2, _ := db.GetRoleByName("viewer")
	if got2 == nil || got2.ID != "role1" {
		t.Error("GetRoleByName failed")
	}
	list, _ := db.ListRoles()
	if len(list) < 3 { // builtin 2 + 1
		t.Errorf("expected >=3 got %d", len(list))
	}
	perms, _ := db.GetRolePermissions("role1")
	if len(perms) != 0 {
		t.Error("expected 0 perms")
	}
	if err := db.SetRolePermissions("role1", []string{"accounts:read", "messages:read"}); err != nil {
		t.Fatal(err)
	}
	perms, _ = db.GetRolePermissions("role1")
	if len(perms) != 2 {
		t.Errorf("expected 2 got %d", len(perms))
	}
	if err := db.UpdateRole("role1", "viewer2", "updated"); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetRole("role1")
	if got.Name != "viewer2" {
		t.Error("update failed")
	}
	if err := db.DeleteRole("role1"); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetRole("role1")
	if got != nil {
		t.Error("expected nil after delete")
	}
	// not found
	got, _ = db.GetRole("nonexistent")
	if got != nil {
		t.Error("expected nil")
	}
	got2, _ = db.GetRoleByName("nonexistent")
	if got2 != nil {
		t.Error("expected nil")
	}
}

// ── User ────────────────────────────────────────────────

func TestUserCRUDCoverage(t *testing.T) {
	db := openTestDB(t)
	// create
	rec := &UserRecord{ID: "user1", Username: "alice", Email: "alice@e.com", PasswordHash: "hash", RoleID: "builtin-user", Enabled: true}
	if err := db.CreateUser(rec); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetUser("user1")
	if err != nil || got.Username != "alice" || got.Email != "alice@e.com" {
		t.Fatalf("get %v %v", got, err)
	}
	got2, _ := db.GetUserByUsername("alice")
	if got2 == nil || got2.ID != "user1" {
		t.Error("GetUserByUsername failed")
	}
	got3, _ := db.GetUserByEmail("alice@e.com")
	if got3 == nil || got3.ID != "user1" {
		t.Error("GetUserByEmail failed")
	}
	list, _ := db.ListUsers()
	if len(list) != 1 {
		t.Errorf("expected 1 got %d", len(list))
	}
	// update
	if err := db.UpdateUser("user1", "builtin-admin", false); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetUser("user1")
	if got.RoleID != "builtin-admin" || got.Enabled {
		t.Error("UpdateUser failed")
	}
	if err := db.UpdateUserPassword("user1", "newhash"); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetUserByUsername("alice")
	if got.PasswordHash != "newhash" {
		t.Error("UpdateUserPassword failed")
	}
	if err := db.UpdateUserEmail("user1", "new@e.com"); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetUser("user1")
	if got.Email != "new@e.com" {
		t.Error("UpdateUserEmail failed")
	}
	if err := db.UpdateUserUsername("user1", "alice2"); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetUser("user1")
	if got.Username != "alice2" {
		t.Error("UpdateUserUsername failed")
	}
	// count
	c, _ := db.CountUsersByRole("builtin-admin")
	if c != 1 {
		t.Errorf("expected 1 got %d", c)
	}
	// delete
	if err := db.DeleteUser("user1"); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetUser("user1")
	if got != nil {
		t.Error("expected nil after delete")
	}
	// not found
	got, _ = db.GetUser("nonexistent")
	if got != nil {
		t.Error("expected nil")
	}
	got2, _ = db.GetUserByUsername("nonexistent")
	if got2 != nil {
		t.Error("expected nil")
	}
	got3, _ = db.GetUserByEmail("nonexistent@e.com")
	if got3 != nil {
		t.Error("expected nil")
	}
}

// ── API Key ─────────────────────────────────────────────

func TestAPIKeyCRUDCoverage(t *testing.T) {
	db := openTestDB(t)
	if err := db.CreateUser(&UserRecord{ID: "u-apikey", Username: "apikeyuser", Email: "a@e.com", PasswordHash: "h", RoleID: "builtin-user", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateAccount(&AccountRecord{ID: "acc-apikey", PhoneNumber: "5000000001", AccountName: "a", DataDir: "/tmp/a"}); err != nil {
		t.Fatal(err)
	}
	accID := "acc-apikey"
	rec := &APIKeyRecord{ID: "k1", UserID: "u-apikey", AccountID: &accID, Name: "key1", Prefix: "notalk_k", KeyHash: "hash1", Enabled: true}
	if err := db.CreateAPIKey(rec); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetAPIKey("k1")
	if got == nil || got.Name != "key1" {
		t.Error("GetAPIKey failed")
	}
	got2, _ := db.GetAPIKeyByHash("hash1")
	if got2 == nil || got2.ID != "k1" {
		t.Error("GetAPIKeyByHash failed")
	}
	// list by user
	list, _ := db.ListAPIKeysByUser("u-apikey")
	if len(list) != 1 {
		t.Errorf("expected 1 got %d", len(list))
	}
	// list all
	all, _ := db.ListAllAPIKeys()
	if len(all) < 1 {
		t.Error("ListAll failed")
	}
	// last used
	if err := db.UpdateAPIKeyLastUsed("k1"); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetAPIKey("k1")
	if got.LastUsed == nil {
		t.Error("expected last_used")
	}
	// delete
	if err := db.DeleteAPIKey("k1"); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetAPIKey("k1")
	if got != nil {
		t.Error("expected nil after delete")
	}
	// not found
	got, _ = db.GetAPIKey("nonexistent")
	if got != nil {
		t.Error("expected nil")
	}
	got2, _ = db.GetAPIKeyByHash("nonexistent")
	if got2 != nil {
		t.Error("expected nil")
	}
	// expiry
	exp := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	rec2 := &APIKeyRecord{ID: "k2", UserID: "u-apikey", Name: "k2", Prefix: "notalk_k2", KeyHash: "hash2", ExpiresAt: &exp, Enabled: true}
	if err := db.CreateAPIKey(rec2); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetAPIKey("k2")
	if got.ExpiresAt == nil || *got.ExpiresAt != exp {
		t.Error("expiry failed")
	}
}

// ── Settings ────────────────────────────────────────────

func TestSettingsCRUD(t *testing.T) {
	db := openTestDB(t)
	// clean settings first
	if _, err := db.db.Exec("DELETE FROM setting WHERE TRUE"); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if got := db.GetSetting("nonexistent", "def"); got != "def" {
		t.Error("default failed")
	}
	if err := db.SetSetting("k1", "v1"); err != nil {
		t.Fatal(err)
	}
	if got := db.GetSetting("k1", ""); got != "v1" {
		t.Error("get failed")
	}
	if err := db.SetSetting("k1", "v2"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if got := db.GetSetting("k1", ""); got != "v2" {
		t.Error("update failed")
	}
	if err := db.SetSetting("k2", "true"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	all, err := db.GetAllSettings()
	if err != nil || len(all) < 2 {
		t.Errorf("GetAll %v %d", err, len(all))
	}
	if !db.GetSettingBool("k2", false) {
		t.Error("GetSettingBool true failed")
	}
	if db.GetSettingBool("nonexistent", true) != true {
		t.Error("default bool failed")
	}
	if db.GetSettingBool("nonexistent2", false) != false {
		t.Error("default false failed")
	}
	if err := db.SetSetting("k3", "1"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if !db.GetSettingBool("k3", false) {
		t.Error("1 should be true")
	}
}

// ── Reset Token ─────────────────────────────────────────

func TestResetTokenCRUD(t *testing.T) {
	db := openTestDB(t)
	if err := db.CreateUser(&UserRecord{ID: "u-reset", Username: "resetuser", Email: "r@e.com", PasswordHash: "h", RoleID: "builtin-user", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	exp := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
	rec := &ResetTokenRecord{ID: "t1", UserID: "u-reset", TokenHash: "hash1", ExpiresAt: exp}
	if err := db.CreateResetToken(rec); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetResetTokenByHash("hash1")
	if err != nil || got == nil || got.TokenHash != "hash1" {
		t.Fatalf("get %v %v", got, err)
	}
	if err := db.MarkResetTokenUsed("t1"); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetResetTokenByHash("hash1")
	if got != nil {
		t.Error("expected nil after used")
	}
	// second token should invalidate first if any left? test creation again
	rec2 := &ResetTokenRecord{ID: "t2", UserID: "u-reset", TokenHash: "hash2", ExpiresAt: exp}
	if err := db.CreateResetToken(rec2); err != nil {
		t.Fatalf("CreateResetToken: %v", err)
	}
	got, _ = db.GetResetTokenByHash("hash2")
	if got == nil {
		t.Error("second token failed")
	}
	// not found
	got, _ = db.GetResetTokenByHash("nonexistent")
	if got != nil {
		t.Error("expected nil")
	}
}

// ── Agent ───────────────────────────────────────────────

func TestAgentCRUD(t *testing.T) {
	db := openTestDB(t)
	// session
	msg, err := db.GetAgentSession("user-nonexistent")
	if err != nil || msg != "[]" {
		t.Errorf("expected [] got %s err %v", msg, err)
	}
	if err := db.SaveAgentSession("u-agent", `["hi"]`); err != nil {
		t.Fatal(err)
	}
	msg, _ = db.GetAgentSession("u-agent")
	if msg != `["hi"]` {
		t.Error("Save failed")
	}
	if err := db.SaveAgentSession("u-agent", `["hello"]`); err != nil {
		t.Fatalf("SaveAgentSession: %v", err)
	}
	msg, _ = db.GetAgentSession("u-agent")
	if msg != `["hello"]` {
		t.Error("update failed")
	}
	if err := db.ClearAgentSession("u-agent"); err != nil {
		t.Fatal(err)
	}
	msg, _ = db.GetAgentSession("u-agent")
	if msg != "[]" {
		t.Error("clear failed")
	}
	// config
	cfg, err := db.GetAgentConfig("acc-nonexistent")
	if err != nil || cfg.Enabled {
		t.Error("expected disabled default")
	}
	rec := AgentConfigRecord{AccountID: "acc-agent", Enabled: true, SystemPrompt: "prompt", Model: "gpt-4", EscalationEnabled: true, EscalationMessage: "escalate", Whitelist: "123", Blacklist: "456"}
	if err := db.SetAgentConfig(rec); err != nil {
		t.Fatal(err)
	}
	cfg, _ = db.GetAgentConfig("acc-agent")
	if !cfg.Enabled || cfg.SystemPrompt != "prompt" || cfg.Model != "gpt-4" {
		t.Errorf("config mismatch %+v", cfg)
	}
	// update
	rec.SystemPrompt = "newprompt"
	if err := db.SetAgentConfig(rec); err != nil {
		t.Fatalf("SetAgentConfig: %v", err)
	}
	cfg, _ = db.GetAgentConfig("acc-agent")
	if cfg.SystemPrompt != "newprompt" {
		t.Error("update failed")
	}
	// log
	logRec := AgentLogRecord{ID: "log1", AccountID: "acc-agent", ChatJID: "chat@s.whatsapp.net", SenderJID: "sender@s.whatsapp.net", IncomingMessage: "hi", OutgoingMessage: "hello", Model: "gpt-4"}
	if err := db.InsertAgentLog(logRec); err != nil {
		t.Fatal(err)
	}
	logs, err := db.ListAgentLogs("acc-agent", 10)
	if err != nil || len(logs) != 1 || logs[0].IncomingMessage != "hi" {
		t.Fatalf("ListAgentLogs %v %d", err, len(logs))
	}
	// limit
	logs, _ = db.ListAgentLogs("acc-agent", 1)
	if len(logs) != 1 {
		t.Error("limit failed")
	}
	// enabled configs
	enabled, _ := db.ListAllEnabledAgentConfigs()
	if len(enabled) != 1 || enabled[0].AccountID != "acc-agent" {
		t.Errorf("ListAllEnabled failed %+v", enabled)
	}
	// disable and check
	rec.Enabled = false
	if err := db.SetAgentConfig(rec); err != nil {
		t.Fatalf("SetAgentConfig: %v", err)
	}
	enabled, _ = db.ListAllEnabledAgentConfigs()
	if len(enabled) != 0 {
		t.Error("expected 0 after disable")
	}
}

func TestGenerateID(t *testing.T) {
	id := GenerateID()
	if id == "" {
		t.Error("expected non-empty")
	}
	if GenerateID() == id {
		t.Error("expected unique")
	}
}

// ── Additional invalid/missing cases ────────────────────

func TestGetSettingAndAllSettings(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.db.Exec("DELETE FROM setting WHERE TRUE"); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if err := db.SetSetting("a", "1"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := db.SetSetting("b", "2"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	all, err := db.GetAllSettings()
	if err != nil || len(all) != 2 {
		t.Errorf("expected 2 got %d err %v", len(all), err)
	}
	if all["a"] != "1" {
		t.Error("mismatch")
	}
}

