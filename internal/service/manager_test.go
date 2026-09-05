package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/devstroop/notalk/internal/config"
	"github.com/devstroop/notalk/internal/database"
	"github.com/devstroop/notalk/internal/model"
)

func setupManager(t *testing.T) *AccountManager {
	t.Helper()
	dsn := os.Getenv("NOTALK_TEST_DSN")
	if dsn == "" {
		t.Skip("NOTALK_TEST_DSN not set, skipping manager tests")
	}
	dir := t.TempDir()

	db, err := database.Open(dsn)
	if err != nil {
		t.Fatalf("Open DB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cfg := &config.Config{
		Accounts: config.AccountsConfig{
			BaseDirectory: filepath.Join(dir, "accounts"),
		},
		Database: config.DatabaseConfig{
			DSN: dsn,
		},
	}

	mgr, err := NewAccountManager(cfg, db)
	if err != nil {
		t.Fatalf("NewAccountManager: %v", err)
	}
	return mgr
}

func TestManagerCreateAccount(t *testing.T) {
	mgr := setupManager(t)

	resp, err := mgr.CreateAccount(model.CreateAccountRequest{
		PhoneNumber: "+919876543210",
		AccountName: "main",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if resp.PhoneNumber != "919876543210" {
		t.Errorf("expected normalized phone 919876543210, got %s", resp.PhoneNumber)
	}
	if resp.AccountName != "main" {
		t.Errorf("expected name main, got %s", resp.AccountName)
	}
	if resp.ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestManagerCreateAccountDefaultName(t *testing.T) {
	mgr := setupManager(t)

	resp, err := mgr.CreateAccount(model.CreateAccountRequest{
		PhoneNumber: "1234567890",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if resp.AccountName != "unknown" {
		t.Errorf("expected default name 'unknown', got %s", resp.AccountName)
	}
}

func TestManagerCreateAccountInvalidPhone(t *testing.T) {
	mgr := setupManager(t)

	_, err := mgr.CreateAccount(model.CreateAccountRequest{
		PhoneNumber: "123", // too short
	})
	if err == nil {
		t.Error("expected error for short phone number")
	}
}

func TestManagerCreateAccountDuplicatePhone(t *testing.T) {
	mgr := setupManager(t)

	if _, err := mgr.CreateAccount(model.CreateAccountRequest{
		PhoneNumber: "9876543210",
		AccountName: "first",
	}); err != nil {
		t.Fatal(err)
	}

	_, err := mgr.CreateAccount(model.CreateAccountRequest{
		PhoneNumber: "9876543210",
		AccountName: "second",
	})
	if err == nil {
		t.Error("expected error for duplicate phone")
	}
}

func TestManagerGetAccount(t *testing.T) {
	mgr := setupManager(t)

	resp, _ := mgr.CreateAccount(model.CreateAccountRequest{
		PhoneNumber: "5551234567",
		AccountName: "find-me",
	})

	acct := mgr.GetAccount(resp.ID)
	if acct == nil {
		t.Fatal("expected to find account, got nil")
	}
	if acct.AccountName != "find-me" {
		t.Errorf("expected name find-me, got %s", acct.AccountName)
	}
}

func TestManagerGetAccountNotFound(t *testing.T) {
	mgr := setupManager(t)

	acct := mgr.GetAccount("nonexistent-id")
	if acct != nil {
		t.Errorf("expected nil, got %+v", acct)
	}
}

func TestManagerListAccounts(t *testing.T) {
	mgr := setupManager(t)

	if _, err := mgr.CreateAccount(model.CreateAccountRequest{PhoneNumber: "1111111111", AccountName: "a1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.CreateAccount(model.CreateAccountRequest{PhoneNumber: "2222222222", AccountName: "a2"}); err != nil {
		t.Fatal(err)
	}

	list := mgr.ListAccounts()
	if list.Total != 2 {
		t.Errorf("expected 2 accounts, got %d", list.Total)
	}
}

func TestManagerDeleteAccount(t *testing.T) {
	mgr := setupManager(t)

	resp, _ := mgr.CreateAccount(model.CreateAccountRequest{
		PhoneNumber: "3333333333",
		AccountName: "deletable",
	})

	delResp, err := mgr.DeleteAccount(resp.ID, false)
	if err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if delResp.AccountID != resp.ID {
		t.Errorf("expected account_id %s, got %s", resp.ID, delResp.AccountID)
	}
	if delResp.DataDeleted {
		t.Error("expected DataDeleted=false")
	}

	// Should be gone
	acct := mgr.GetAccount(resp.ID)
	if acct != nil {
		t.Error("expected nil after delete")
	}
}

func TestManagerDeleteAccountNotFound(t *testing.T) {
	mgr := setupManager(t)

	_, err := mgr.DeleteAccount("nonexistent", false)
	if err == nil {
		t.Error("expected error for nonexistent account")
	}
}

func TestManagerDiscoverAccounts(t *testing.T) {
	dsn := os.Getenv("NOTALK_TEST_DSN")
	if dsn == "" {
		t.Skip("NOTALK_TEST_DSN not set, skipping manager tests")
	}
	dir := t.TempDir()

	db, err := database.Open(dsn)
	if err != nil {
		t.Fatalf("Open DB: %v", err)
	}
	defer func() { _ = db.Close() }()

	// Pre-seed the DB
	if err := db.CreateAccount(&database.AccountRecord{
		ID: "pre-existing", PhoneNumber: "4444444444",
		AccountName: "pre", DataDir: filepath.Join(dir, "accounts", "pre-existing"),
	}); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Accounts: config.AccountsConfig{
			BaseDirectory: filepath.Join(dir, "accounts"),
		},
		Database: config.DatabaseConfig{
			DSN: dsn,
		},
	}

	mgr, _ := NewAccountManager(cfg, db)

	if err := mgr.DiscoverAccounts(context.Background()); err != nil {
		t.Fatalf("DiscoverAccounts: %v", err)
	}

	acct := mgr.GetAccount("pre-existing")
	if acct == nil {
		t.Fatal("expected to discover pre-existing account")
	}
	if acct.PhoneNumber != "4444444444" {
		t.Errorf("expected phone 4444444444, got %s", acct.PhoneNumber)
	}
}
