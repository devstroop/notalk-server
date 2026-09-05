package database

import (
	"os"
	"testing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("NOTALK_TEST_DSN")
	if dsn == "" {
		t.Skip("NOTALK_TEST_DSN not set, skipping database tests")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open(%s): %v", dsn, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Clean tables for test isolation
	for _, table := range []string{
		"password_reset_token", "api_key", "agent_log", "agent_config", "agent_session",
		"usage", "subscription", "message", "webhook_config", "proxy_config",
		"account", "role_permission", "app_user",
	} {
		_, _ = db.db.Exec("DELETE FROM " + table + " WHERE TRUE")
	}
	return db
}

func TestOpenAndMigrate(t *testing.T) {
	db := openTestDB(t)
	// Verify table exists by listing (should return empty)
	records, err := db.ListAccounts()
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("expected 0 records, got %d", len(records))
	}
}

func TestCreateAndGetAccount(t *testing.T) {
	db := openTestDB(t)

	rec := &AccountRecord{
		ID:          "test-id-1",
		PhoneNumber: "919876543210",
		AccountName: "test-account",
		DataDir:     "/tmp/test",
	}
	if err := db.CreateAccount(rec); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	got, err := db.GetAccount("test-id-1")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if got == nil {
		t.Fatal("expected account, got nil")
	}
	if got.PhoneNumber != "919876543210" {
		t.Errorf("expected phone 919876543210, got %s", got.PhoneNumber)
	}
	if got.AccountName != "test-account" {
		t.Errorf("expected name test-account, got %s", got.AccountName)
	}
}

func TestGetAccountNotFound(t *testing.T) {
	db := openTestDB(t)

	got, err := db.GetAccount("nonexistent")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for nonexistent account, got %+v", got)
	}
}

func TestGetAccountByPhone(t *testing.T) {
	db := openTestDB(t)

	rec := &AccountRecord{
		ID: "id-phone-test", PhoneNumber: "1234567890",
		AccountName: "phone-test", DataDir: "/tmp/pt",
	}
	if err := db.CreateAccount(rec); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetAccountByPhone("1234567890")
	if err != nil {
		t.Fatalf("GetAccountByPhone: %v", err)
	}
	if got == nil || got.ID != "id-phone-test" {
		t.Errorf("expected id-phone-test, got %+v", got)
	}

	// Not found
	got, err = db.GetAccountByPhone("0000000000")
	if err != nil {
		t.Fatalf("GetAccountByPhone: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestDuplicatePhone(t *testing.T) {
	db := openTestDB(t)

	rec := &AccountRecord{
		ID: "dup-1", PhoneNumber: "5551234567",
		AccountName: "first", DataDir: "/tmp/d1",
	}
	if err := db.CreateAccount(rec); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	rec2 := &AccountRecord{
		ID: "dup-2", PhoneNumber: "5551234567",
		AccountName: "second", DataDir: "/tmp/d2",
	}
	err := db.CreateAccount(rec2)
	if err == nil {
		t.Error("expected UNIQUE constraint error, got nil")
	}
}

func TestListAccounts(t *testing.T) {
	db := openTestDB(t)

	for _, phone := range []string{"1111111111", "2222222222", "3333333333"} {
		if err := db.CreateAccount(&AccountRecord{
			ID: phone, PhoneNumber: phone,
			AccountName: "acct", DataDir: "/tmp/" + phone,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// All
	all, err := db.ListAccounts()
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3, got %d", len(all))
	}
}

func TestUpdateAccountName(t *testing.T) {
	db := openTestDB(t)

	if err := db.CreateAccount(&AccountRecord{
		ID: "name-test", PhoneNumber: "8888888888",
		AccountName: "old-name", DataDir: "/tmp/nt",
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.UpdateAccountName("name-test", "new-name"); err != nil {
		t.Fatalf("UpdateAccountName: %v", err)
	}

	got, _ := db.GetAccount("name-test")
	if got.AccountName != "new-name" {
		t.Errorf("expected new-name, got %s", got.AccountName)
	}
}

func TestDeleteAccount(t *testing.T) {
	db := openTestDB(t)

	if err := db.CreateAccount(&AccountRecord{
		ID: "del-test", PhoneNumber: "7777777777",
		AccountName: "del", DataDir: "/tmp/del",
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.DeleteAccount("del-test"); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}

	got, _ := db.GetAccount("del-test")
	if got != nil {
		t.Errorf("expected nil after delete, got %+v", got)
	}
}
