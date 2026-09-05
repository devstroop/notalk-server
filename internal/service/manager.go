package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/devstroop/notalk/internal/config"
	"github.com/devstroop/notalk/internal/database"
	"github.com/devstroop/notalk/internal/model"
)

// AccountManager manages the lifecycle of all WhatsApp accounts.
type AccountManager struct {
	mu       sync.RWMutex
	accounts map[string]*Account // keyed by account ID

	cfg     *config.Config
	db      *database.DB
	baseDir string

	// onMessage is invoked (in a goroutine) for every incoming message across all accounts.
	// Set via SetOnMessage; nil = no hook.
	onMessage func(accountID, chatJID, senderJID, body string)
}

// NewAccountManager creates a new manager.
func NewAccountManager(cfg *config.Config, db *database.DB) (*AccountManager, error) {
	baseDir := cfg.Accounts.BaseDirectory
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("create base dir: %w", err)
	}
	return &AccountManager{
		accounts: make(map[string]*Account),
		cfg:      cfg,
		db:       db,
		baseDir:  baseDir,
	}, nil
}

// Config returns the application configuration.
func (m *AccountManager) Config() *config.Config { return m.cfg }

// CreateAccount validates input, persists to DB, and returns the new account.
func (m *AccountManager) CreateAccount(req model.CreateAccountRequest) (*model.CreateAccountResponse, error) {
	phone := NormalizePhone(req.PhoneNumber)
	if len(phone) < 7 || len(phone) > 15 {
		return nil, fmt.Errorf("invalid phone number: must be 7-15 digits")
	}

	// Uniqueness check
	existing, err := m.db.GetAccountByPhone(phone)
	if err != nil {
		return nil, fmt.Errorf("db lookup: %w", err)
	}
	if existing != nil {
		return nil, fmt.Errorf("phone number '%s' already exists", phone)
	}

	id := NewUUID()
	dataDir := filepath.Join(m.baseDir, id)

	name := req.AccountName
	if name == "" {
		name = "unknown"
	}

	now := time.Now().UTC()
	rec := &database.AccountRecord{
		ID:          id,
		PhoneNumber: phone,
		AccountName: name,
		DataDir:     dataDir,
		UserID:      req.UserID,
	}
	if err := m.db.CreateAccount(rec); err != nil {
		return nil, fmt.Errorf("db insert: %w", err)
	}

	acct := NewAccount(id, phone, name, dataDir, req.UserID, m.cfg.Database.DSN, now, m.db)
	acct.WebhookCfg = m.cfg.Webhooks

	m.mu.Lock()
	// Attach the global message hook if one is registered.
	if m.onMessage != nil {
		fn := m.onMessage
		acct.OnIncomingMessage = func(chatJID, senderJID, body string) {
			fn(id, chatJID, senderJID, body)
		}
	}
	m.accounts[id] = acct
	m.mu.Unlock()

	return &model.CreateAccountResponse{
		ID:          id,
		PhoneNumber: phone,
		AccountName: name,
		CreatedAt:   now.Format(time.RFC3339),
	}, nil
}

// GetAccount returns the in-memory account, if loaded.
func (m *AccountManager) GetAccount(id string) *Account {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.accounts[id]
}

// DB returns the underlying database handle.
func (m *AccountManager) DB() *database.DB {
	return m.db
}

// SetOnMessage registers a global hook called whenever any account receives a message.
// Safe to call at any time; applies to all currently-loaded and future accounts.
func (m *AccountManager) SetOnMessage(fn func(accountID, chatJID, senderJID, body string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onMessage = fn
	for _, acct := range m.accounts {
		id := acct.ID
		acct.mu.Lock()
		acct.OnIncomingMessage = func(chatJID, senderJID, body string) {
			fn(id, chatJID, senderJID, body)
		}
		acct.mu.Unlock()
	}
}

// ListAccounts returns info for all known accounts.
func (m *AccountManager) ListAccounts() model.AccountListResponse {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]model.AccountInfo, 0, len(m.accounts))
	for _, acct := range m.accounts {
		list = append(list, acct.Info())
	}
	return model.AccountListResponse{Accounts: list, Total: len(list)}
}

// DeleteAccount removes an account from memory, DB, and optionally disk.
func (m *AccountManager) DeleteAccount(id string, deleteData bool) (*model.DeleteAccountResponse, error) {
	m.mu.RLock()
	acct, ok := m.accounts[id]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("account not found")
	}

	if acct.IsAuthorized() {
		return nil, fmt.Errorf("account is still linked to WhatsApp — unlink the session first (DELETE session)")
	}

	m.mu.Lock()
	delete(m.accounts, id)
	m.mu.Unlock()

	acct.Disconnect()

	if err := m.db.DeleteAccount(id); err != nil {
		return nil, fmt.Errorf("db delete: %w", err)
	}

	if deleteData {
		_ = os.RemoveAll(acct.DataDir)
	}

	return &model.DeleteAccountResponse{
		Message:     "account deleted",
		AccountID:   id,
		DataDeleted: deleteData,
	}, nil
}

// ConnectAccount ensures the account's WhatsApp client is connected.
func (m *AccountManager) ConnectAccount(ctx context.Context, id string) error {
	acct := m.GetAccount(id)
	if acct == nil {
		return fmt.Errorf("account not found")
	}
	return acct.EnsureConnected(ctx)
}

// UpdateAccountName updates the display name in memory and DB.
func (m *AccountManager) UpdateAccountName(id, name string) error {
	acct := m.GetAccount(id)
	if acct == nil {
		return fmt.Errorf("account not found")
	}
	if err := m.db.UpdateAccountName(id, name); err != nil {
		return fmt.Errorf("db update name: %w", err)
	}
	acct.mu.Lock()
	acct.AccountName = name
	acct.mu.Unlock()
	return nil
}

// UpdatePhoneNumber updates the phone number in memory and DB.
func (m *AccountManager) UpdatePhoneNumber(id, phone string) error {
	phone = NormalizePhone(phone)
	if len(phone) < 7 || len(phone) > 15 {
		return fmt.Errorf("invalid phone number: must be 7-15 digits")
	}
	acct := m.GetAccount(id)
	if acct == nil {
		return fmt.Errorf("account not found")
	}
	existing, err := m.db.GetAccountByPhone(phone)
	if err != nil {
		return fmt.Errorf("db lookup: %w", err)
	}
	if existing != nil && existing.ID != id {
		return fmt.Errorf("phone number '%s' already exists", phone)
	}
	if err := m.db.UpdatePhoneNumber(id, phone); err != nil {
		return fmt.Errorf("db update phone: %w", err)
	}
	acct.mu.Lock()
	acct.PhoneNumber = phone
	acct.mu.Unlock()
	return nil
}

// UpdateProxyURL is replaced by the ProxyConfig entity - see SetProxy/GetProxy/DeleteProxy.

// SetProxy upserts the proxy config for an account and disconnects so the next connect uses it.
func (m *AccountManager) SetProxy(id string, cfg *ProxyConfig) error {
	acct := m.GetAccount(id)
	if acct == nil {
		return fmt.Errorf("account not found")
	}
	if err := m.db.UpsertProxyConfig(ProxyConfigToDB(id, cfg)); err != nil {
		return fmt.Errorf("db upsert proxy: %w", err)
	}
	acct.Disconnect()
	acct.mu.Lock()
	acct.Proxy = cfg
	acct.mu.Unlock()
	return nil
}

// GetProxy returns the proxy config for an account, or nil.
func (m *AccountManager) GetProxy(id string) (*ProxyConfig, error) {
	acct := m.GetAccount(id)
	if acct == nil {
		return nil, fmt.Errorf("account not found")
	}
	acct.mu.RLock()
	defer acct.mu.RUnlock()
	return acct.Proxy, nil
}

// DeleteProxy removes the proxy config and disconnects.
func (m *AccountManager) DeleteProxy(id string) error {
	acct := m.GetAccount(id)
	if acct == nil {
		return fmt.Errorf("account not found")
	}
	if err := m.db.DeleteProxyConfig(id); err != nil {
		return fmt.Errorf("db delete proxy: %w", err)
	}
	acct.Disconnect()
	acct.mu.Lock()
	acct.Proxy = nil
	acct.mu.Unlock()
	return nil
}

// DiscoverAccounts loads all DB accounts into memory and verifies any that
// have stored WhatsApp credentials by connecting to the server. This ensures
// sessions revoked from the phone while the server was down are detected.
func (m *AccountManager) DiscoverAccounts(ctx context.Context) error {
	records, err := m.db.ListAccounts()
	if err != nil {
		return fmt.Errorf("list accounts: %w", err)
	}

	m.mu.Lock()

	for _, rec := range records {
		if _, ok := m.accounts[rec.ID]; ok {
			continue
		}

		created, _ := time.Parse(time.RFC3339, rec.CreatedAt)
		acct := NewAccount(rec.ID, rec.PhoneNumber, rec.AccountName, rec.DataDir, rec.UserID, m.cfg.Database.DSN, created, m.db)
		acct.WebhookCfg = m.cfg.Webhooks

		// Load proxy config if present
		proxyCfg, err := m.db.GetProxyConfig(rec.ID)
		if err != nil {
			log.Warn().Str("id", rec.ID).Err(err).Msg("failed to load proxy config")
		} else if proxyCfg != nil {
			acct.Proxy = ProxyConfigFromDB(proxyCfg)
		}

		// Attach global message hook if registered.
		if m.onMessage != nil {
			fn := m.onMessage
			id := rec.ID
			acct.OnIncomingMessage = func(chatJID, senderJID, body string) {
				fn(id, chatJID, senderJID, body)
			}
		}

		m.accounts[rec.ID] = acct
		log.Info().Str("id", rec.ID).Str("phone", rec.PhoneNumber).Msg("discovered account")
	}

	m.mu.Unlock()

	// Verify stored sessions by connecting. If a session was revoked from the
	// phone while the server was down, the client will receive a LoggedOut event
	// and the handler will clear the local session data.
	m.mu.RLock()
	var toVerify []*Account
	for _, acct := range m.accounts {
		if acct.HasStoredCredentials() {
			toVerify = append(toVerify, acct)
		}
	}
	m.mu.RUnlock()

	for _, acct := range toVerify {
		log.Info().Str("id", acct.ID).Msg("verifying stored session")
		if err := acct.Connect(ctx); err != nil {
			log.Warn().Str("id", acct.ID).Err(err).Msg("session verification connect failed")
		}
	}

	log.Info().Int("count", len(m.accounts)).Int("verified", len(toVerify)).Msg("accounts loaded")
	return nil
}

// ShutdownAll disconnects every active account.
func (m *AccountManager) ShutdownAll() {
	m.mu.RLock()
	ids := make([]string, 0, len(m.accounts))
	for id := range m.accounts {
		ids = append(ids, id)
	}
	m.mu.RUnlock()

	for _, id := range ids {
		if acct := m.GetAccount(id); acct != nil {
			acct.Disconnect()
		}
	}
	log.Info().Int("count", len(ids)).Msg("all accounts disconnected")
}
