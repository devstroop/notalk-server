package database

import (
	"github.com/google/uuid"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

// DB wraps a PostgreSQL connection pool.
type DB struct {
	db *sql.DB
}

// AccountRecord is the persistent account row.
type AccountRecord struct {
	ID          string
	PhoneNumber string
	AccountName string
	DataDir     string
	UserID      string // FK to app_user.id (empty = unassigned / legacy)
	CreatedAt   string
	UpdatedAt   string
}

// ProxyConfigRecord is the persistent proxy configuration row.
type ProxyConfigRecord struct {
	AccountID string
	Protocol  string // http, https, socks5
	Host      string
	Port      int
	Username  string
	Password  string
	Enabled   bool
}

// MessageRecord is a stored message row.
type MessageRecord struct {
	ID        string // message ID
	AccountID string
	ChatJID   string
	SenderJID string
	FromMe    bool
	Type      string // text, image, video, audio, document, sticker, reaction, other
	Body      string // text body, caption, or reaction emoji
	MediaType string // MIME type for media messages
	Timestamp string // RFC3339
}

// WebhookConfigRecord is the persistent webhook config row.
type WebhookConfigRecord struct {
	AccountID string
	URL       string
	Secret    string // optional HMAC signing secret
	Events    string // comma-separated event types, empty = all
	Enabled   bool
}

// RoleRecord is a persistent role row.
type RoleRecord struct {
	ID          string
	Name        string
	Description string
	IsBuiltin   bool // built-in roles (admin, user) cannot be deleted
	CreatedAt   string
}

// UserRecord is a persistent user row.
type UserRecord struct {
	ID           string
	Username     string
	Email        string
	PasswordHash string
	RoleID       string
	RoleName     string // joined from role table (read-only)
	Enabled      bool
	CreatedAt    string
	UpdatedAt    string
}

// APIKeyRecord is a persistent API key row.
type APIKeyRecord struct {
	ID        string
	UserID    string  // FK to app_user.id
	AccountID *string // FK to account.id, nil = not scoped to a specific account
	Name      string  // human-readable label
	Prefix    string  // first 8 chars of key for identification
	KeyHash   string  // SHA-256 hash of full key
	ExpiresAt *string // RFC3339, nil = never expires
	LastUsed  *string // RFC3339, nil = never used
	Enabled   bool
	CreatedAt string
}

// ResetTokenRecord is a password-reset token stored in the DB.
type ResetTokenRecord struct {
	ID        string
	UserID    string
	TokenHash string
	ExpiresAt string // RFC3339
	Used      bool
	CreatedAt string
}

// Open connects to the PostgreSQL database at dsn, running migrations.
func Open(dsn string) (*DB, error) {
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	conn.SetMaxOpenConns(25)
	conn.SetMaxIdleConns(5)
	conn.SetConnMaxLifetime(5 * time.Minute)

	if err := conn.Ping(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	d := &DB{db: conn}
	if err := d.migrate(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return d, nil
}

func GenerateID() string { return uuid.New().String() }

func (d *DB) Close() error { return d.db.Close() }

func (d *DB) migrate() error {
	_, err := d.db.Exec(`
		CREATE TABLE IF NOT EXISTS account (
			id            TEXT PRIMARY KEY,
			phone_number  TEXT NOT NULL UNIQUE,
			account_name  TEXT NOT NULL DEFAULT '',
			data_dir      TEXT NOT NULL,
			created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
	`)
	if err != nil {
		return err
	}

	_, err = d.db.Exec(`
		CREATE TABLE IF NOT EXISTS proxy_config (
			account_id TEXT PRIMARY KEY REFERENCES account(id) ON DELETE CASCADE,
			protocol   TEXT NOT NULL DEFAULT 'http',
			host       TEXT NOT NULL,
			port       INTEGER NOT NULL,
			username   TEXT NOT NULL DEFAULT '',
			password   TEXT NOT NULL DEFAULT '',
			enabled    BOOLEAN NOT NULL DEFAULT TRUE
		);
	`)
	if err != nil {
		return err
	}

	_, err = d.db.Exec(`
		CREATE TABLE IF NOT EXISTS message (
			id         TEXT NOT NULL,
			account_id TEXT NOT NULL REFERENCES account(id) ON DELETE CASCADE,
			chat_jid   TEXT NOT NULL,
			sender_jid TEXT NOT NULL,
			from_me    BOOLEAN NOT NULL DEFAULT FALSE,
			type       TEXT NOT NULL DEFAULT 'text',
			body       TEXT NOT NULL DEFAULT '',
			media_type TEXT NOT NULL DEFAULT '',
			timestamp  TEXT NOT NULL,
			PRIMARY KEY (account_id, id)
		);
	`)
	if err != nil {
		return err
	}

	// Index for paginated chat history queries
	_, err = d.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_message_chat_ts ON message (account_id, chat_jid, timestamp DESC);
	`)
	if err != nil {
		return err
	}

	_, err = d.db.Exec(`
		CREATE TABLE IF NOT EXISTS webhook_config (
			account_id TEXT PRIMARY KEY REFERENCES account(id) ON DELETE CASCADE,
			url        TEXT NOT NULL,
			secret     TEXT NOT NULL DEFAULT '',
			events     TEXT NOT NULL DEFAULT '',
			enabled    BOOLEAN NOT NULL DEFAULT TRUE
		);
	`)
	if err != nil {
		return err
	}

	// ── RBAC tables ─────────────────────────────────

	_, err = d.db.Exec(`
		CREATE TABLE IF NOT EXISTS role (
			id          TEXT PRIMARY KEY,
			name        TEXT NOT NULL UNIQUE,
			description TEXT NOT NULL DEFAULT '',
			is_builtin  BOOLEAN NOT NULL DEFAULT FALSE,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
	`)
	if err != nil {
		return err
	}

	_, err = d.db.Exec(`
		CREATE TABLE IF NOT EXISTS role_permission (
			role_id    TEXT NOT NULL REFERENCES role(id) ON DELETE CASCADE,
			permission TEXT NOT NULL,
			PRIMARY KEY (role_id, permission)
		);
	`)
	if err != nil {
		return err
	}

	_, err = d.db.Exec(`
		CREATE TABLE IF NOT EXISTS app_user (
			id            TEXT PRIMARY KEY,
			username      TEXT NOT NULL UNIQUE,
			email         TEXT NOT NULL DEFAULT '',
			password_hash TEXT NOT NULL,
			role_id       TEXT NOT NULL REFERENCES role(id),
			enabled       BOOLEAN NOT NULL DEFAULT TRUE,
			created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
	`)
	if err != nil {
		return err
	}

	// Migration: add email column to app_user if missing (upgrade from earlier schema)
	_, _ = d.db.Exec(`ALTER TABLE app_user ADD COLUMN IF NOT EXISTS email TEXT NOT NULL DEFAULT ''`)

	// Add user_id to account (idempotent). NULL means no user assigned (legacy/unassigned accounts).
	_, _ = d.db.Exec(`ALTER TABLE account ADD COLUMN IF NOT EXISTS user_id TEXT REFERENCES app_user(id) DEFAULT NULL`)

	// ── API key table ───────────────────────────────
	_, err = d.db.Exec(`
		CREATE TABLE IF NOT EXISTS api_key (
			id         TEXT PRIMARY KEY,
			user_id    TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
			account_id TEXT REFERENCES account(id) ON DELETE CASCADE,
			name       TEXT NOT NULL DEFAULT '',
			prefix     TEXT NOT NULL DEFAULT '',
			key_hash   TEXT NOT NULL UNIQUE,
			expires_at TEXT,
			last_used  TEXT,
			enabled    BOOLEAN NOT NULL DEFAULT TRUE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
	`)
	if err != nil {
		return err
	}

	// Migration: add account_id column to api_key if missing (upgrade from earlier schema)
	_, _ = d.db.Exec(`ALTER TABLE api_key ADD COLUMN IF NOT EXISTS account_id TEXT REFERENCES account(id) ON DELETE CASCADE`)

	// ── Password reset token table ──────────────────
	_, err = d.db.Exec(`
		CREATE TABLE IF NOT EXISTS password_reset_token (
			id         TEXT PRIMARY KEY,
			user_id    TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
			token_hash TEXT NOT NULL UNIQUE,
			expires_at TEXT NOT NULL,
			used       BOOLEAN NOT NULL DEFAULT FALSE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
	`)
	if err != nil {
		return err
	}

	// ── Settings key-value table ────────────────────
	_, err = d.db.Exec(`
		CREATE TABLE IF NOT EXISTS setting (
			"key"   TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT ''
		);
	`)
	if err != nil {
		return err
	}

	// ── Seed built-in roles ─────────────────────────
	if err := d.seedRoles(); err != nil {
		return err
	}

	// ── Agent tables (session, config, log) ─────────
	if err := d.migrateAgent(); err != nil {
		return err
	}

	return nil
}

// seedRoles inserts the built-in admin and user roles with default permissions.
func (d *DB) seedRoles() error {
	// Admin role — wildcard permission
	_, err := d.db.Exec(`INSERT INTO role (id, name, description, is_builtin) VALUES ('builtin-admin', 'admin', 'Full system access', TRUE) ON CONFLICT(id) DO NOTHING`)
	if err != nil {
		return err
	}
	_, err = d.db.Exec(`INSERT INTO role_permission (role_id, permission) VALUES ('builtin-admin', '*') ON CONFLICT(role_id, permission) DO NOTHING`)
	if err != nil {
		return err
	}

	// User role — restricted permissions
	_, err = d.db.Exec(`INSERT INTO role (id, name, description, is_builtin) VALUES ('builtin-user', 'user', 'Standard user access', TRUE) ON CONFLICT(id) DO NOTHING`)
	if err != nil {
		return err
	}

	userPerms := []string{
		"accounts:read",
		"session:*",
		"messages:*",
		"chats:read",
		"contacts:*",
		"groups:*",
		"presence:*",
		"profile:*",
		"api-keys:*",
		"mcp:read",
	}
	for _, p := range userPerms {
		_, err = d.db.Exec(`INSERT INTO role_permission (role_id, permission) VALUES ('builtin-user', $1) ON CONFLICT(role_id, permission) DO NOTHING`, p)
		if err != nil {
			return err
		}
	}

	return nil
}

// CreateAccount inserts a new account row.
func (d *DB) CreateAccount(rec *AccountRecord) error {
	now := time.Now().UTC().Format(time.RFC3339)
	// Map empty UserID to NULL for FK compatibility
	var userID any
	if rec.UserID != "" {
		userID = rec.UserID
	}
	_, err := d.db.Exec(`
		INSERT INTO account (id, phone_number, account_name, data_dir, user_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		rec.ID, rec.PhoneNumber, rec.AccountName, rec.DataDir, userID, now, now,
	)
	return err
}

// GetAccount retrieves a single account by ID.
func (d *DB) GetAccount(id string) (*AccountRecord, error) {
	row := d.db.QueryRow(
		`SELECT id, phone_number, account_name, data_dir, COALESCE(user_id, ''), created_at, updated_at
		 FROM account WHERE id = $1`, id)
	return scanAccount(row)
}

// GetAccountByPhone looks up an account by phone number.
func (d *DB) GetAccountByPhone(phone string) (*AccountRecord, error) {
	row := d.db.QueryRow(
		`SELECT id, phone_number, account_name, data_dir, COALESCE(user_id, ''), created_at, updated_at
		 FROM account WHERE phone_number = $1`, phone)
	return scanAccount(row)
}

// ListAccounts returns all accounts.
func (d *DB) ListAccounts() ([]*AccountRecord, error) {
	query := `SELECT id, phone_number, account_name, data_dir, COALESCE(user_id, ''), created_at, updated_at FROM account ORDER BY created_at DESC`

	rows, err := d.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*AccountRecord
	for rows.Next() {
		var r AccountRecord
		if err := rows.Scan(&r.ID, &r.PhoneNumber, &r.AccountName, &r.DataDir, &r.UserID, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// UpdateAccountName sets an account's display name.
func (d *DB) UpdateAccountName(id, name string) error {
	_, err := d.db.Exec(`UPDATE account SET account_name = $1, updated_at = NOW() WHERE id = $2`, name, id)
	return err
}

// UpdatePhoneNumber sets an account's phone number.
func (d *DB) UpdatePhoneNumber(id, phone string) error {
	_, err := d.db.Exec(`UPDATE account SET phone_number = $1, updated_at = NOW() WHERE id = $2`, phone, id)
	return err
}

// DeleteAccount removes the account row.
func (d *DB) DeleteAccount(id string) error {
	_, err := d.db.Exec(`DELETE FROM account WHERE id = $1`, id)
	return err
}

// UpdateAccountUserID assigns an account to a user.
func (d *DB) UpdateAccountUserID(id, userID string) error {
	var uid any
	if userID != "" {
		uid = userID
	}
	_, err := d.db.Exec(`UPDATE account SET user_id = $1, updated_at = NOW() WHERE id = $2`, uid, id)
	return err
}

// ListAccountsByUser returns accounts belonging to a specific user.
func (d *DB) ListAccountsByUser(userID string) ([]*AccountRecord, error) {
	rows, err := d.db.Query(
		`SELECT id, phone_number, account_name, data_dir, COALESCE(user_id, ''), created_at, updated_at
		 FROM account WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*AccountRecord
	for rows.Next() {
		var r AccountRecord
		if err := rows.Scan(&r.ID, &r.PhoneNumber, &r.AccountName, &r.DataDir, &r.UserID, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

func scanAccount(row *sql.Row) (*AccountRecord, error) {
	var r AccountRecord
	err := row.Scan(&r.ID, &r.PhoneNumber, &r.AccountName, &r.DataDir, &r.UserID, &r.CreatedAt, &r.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &r, err
}

// ─── Proxy Config CRUD ──────────────────────────────────────

// UpsertProxyConfig inserts or replaces the proxy config for an account.
func (d *DB) UpsertProxyConfig(rec *ProxyConfigRecord) error {
	_, err := d.db.Exec(`
		INSERT INTO proxy_config (account_id, protocol, host, port, username, password, enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT(account_id) DO UPDATE SET
			protocol = excluded.protocol,
			host     = excluded.host,
			port     = excluded.port,
			username = excluded.username,
			password = excluded.password,
			enabled  = excluded.enabled`,
		rec.AccountID, rec.Protocol, rec.Host, rec.Port, rec.Username, rec.Password, rec.Enabled,
	)
	return err
}

// GetProxyConfig returns the proxy config for an account, or nil if none.
func (d *DB) GetProxyConfig(accountID string) (*ProxyConfigRecord, error) {
	row := d.db.QueryRow(
		`SELECT account_id, protocol, host, port, username, password, enabled
		 FROM proxy_config WHERE account_id = $1`, accountID)
	var r ProxyConfigRecord
	err := row.Scan(&r.AccountID, &r.Protocol, &r.Host, &r.Port, &r.Username, &r.Password, &r.Enabled)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// DeleteProxyConfig removes the proxy config for an account.
func (d *DB) DeleteProxyConfig(accountID string) error {
	_, err := d.db.Exec(`DELETE FROM proxy_config WHERE account_id = $1`, accountID)
	return err
}

// ─── Message CRUD ───────────────────────────────────────────

// LastMessageInfo holds the latest message summary for a single chat.
type LastMessageInfo struct {
	ChatJID   string
	Body      string
	SenderJID string
	FromMe    bool
	Timestamp string // RFC3339
}

// GetLastMessagePerChat returns the most recent message for each chat belonging to accountID.
func (d *DB) GetLastMessagePerChat(accountID string) (map[string]*LastMessageInfo, error) {
	rows, err := d.db.Query(`
		SELECT m.chat_jid, m.body, m.sender_jid, m.from_me, m.timestamp
		FROM message m
		INNER JOIN (
			SELECT chat_jid, MAX(timestamp) AS max_ts
			FROM message
			WHERE account_id = $1
			GROUP BY chat_jid
		) latest ON m.chat_jid = latest.chat_jid AND m.timestamp = latest.max_ts AND m.account_id = $2
	`, accountID, accountID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := make(map[string]*LastMessageInfo)
	for rows.Next() {
		var lm LastMessageInfo
		if err := rows.Scan(&lm.ChatJID, &lm.Body, &lm.SenderJID, &lm.FromMe, &lm.Timestamp); err != nil {
			return nil, err
		}
		result[lm.ChatJID] = &lm
	}
	return result, rows.Err()
}

// GetUnreadCountPerChat returns the number of unread (not from_me, not yet read-receipted) messages per chat.
// For now we approximate "unread" as messages from others received after the latest outgoing or read-marked message.
func (d *DB) GetUnreadCountPerChat(accountID string) (map[string]int, error) {
	rows, err := d.db.Query(`
		SELECT chat_jid, COUNT(*) AS cnt
		FROM message
		WHERE account_id = $1 AND from_me = FALSE
		  AND timestamp > COALESCE(
		    (SELECT MAX(m2.timestamp) FROM message m2
		     WHERE m2.account_id = message.account_id
		       AND m2.chat_jid  = message.chat_jid
		       AND m2.from_me = TRUE), '')
		GROUP BY chat_jid
	`, accountID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := make(map[string]int)
	for rows.Next() {
		var jid string
		var cnt int
		if err := rows.Scan(&jid, &cnt); err != nil {
			return nil, err
		}
		result[jid] = cnt
	}
	return result, rows.Err()
}

// InsertMessage stores a message row (idempotent — ignores duplicates).
func (d *DB) InsertMessage(rec *MessageRecord) error {
	_, err := d.db.Exec(`
		INSERT INTO message (id, account_id, chat_jid, sender_jid, from_me, type, body, media_type, timestamp)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT(account_id, id) DO NOTHING`,
		rec.ID, rec.AccountID, rec.ChatJID, rec.SenderJID, rec.FromMe, rec.Type, rec.Body, rec.MediaType, rec.Timestamp,
	)
	return err
}

// ListMessages returns messages for a chat, ordered newest-first with cursor pagination.
// If before is non-empty, only messages with timestamp < before are returned.
func (d *DB) ListMessages(accountID, chatJID string, limit int, before string) ([]*MessageRecord, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	var (
		rows *sql.Rows
		err  error
	)
	if before != "" {
		rows, err = d.db.Query(`
			SELECT id, account_id, chat_jid, sender_jid, from_me, type, body, media_type, timestamp
			FROM message
			WHERE account_id = $1 AND chat_jid = $2 AND timestamp < $3
			ORDER BY timestamp DESC
			LIMIT $4`, accountID, chatJID, before, limit)
	} else {
		rows, err = d.db.Query(`
			SELECT id, account_id, chat_jid, sender_jid, from_me, type, body, media_type, timestamp
			FROM message
			WHERE account_id = $1 AND chat_jid = $2
			ORDER BY timestamp DESC
			LIMIT $3`, accountID, chatJID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*MessageRecord
	for rows.Next() {
		var r MessageRecord
		if err := rows.Scan(&r.ID, &r.AccountID, &r.ChatJID, &r.SenderJID, &r.FromMe, &r.Type, &r.Body, &r.MediaType, &r.Timestamp); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// ─── Webhook Config CRUD ────────────────────────────────────

// UpsertWebhookConfig inserts or replaces the webhook config for an account.
func (d *DB) UpsertWebhookConfig(rec *WebhookConfigRecord) error {
	_, err := d.db.Exec(`
		INSERT INTO webhook_config (account_id, url, secret, events, enabled)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT(account_id) DO UPDATE SET
			url     = excluded.url,
			secret  = excluded.secret,
			events  = excluded.events,
			enabled = excluded.enabled`,
		rec.AccountID, rec.URL, rec.Secret, rec.Events, rec.Enabled,
	)
	return err
}

// GetWebhookConfig returns the webhook config for an account, or nil if none.
func (d *DB) GetWebhookConfig(accountID string) (*WebhookConfigRecord, error) {
	row := d.db.QueryRow(
		`SELECT account_id, url, secret, events, enabled
		 FROM webhook_config WHERE account_id = $1`, accountID)
	var r WebhookConfigRecord
	err := row.Scan(&r.AccountID, &r.URL, &r.Secret, &r.Events, &r.Enabled)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// DeleteWebhookConfig removes the webhook config for an account.
func (d *DB) DeleteWebhookConfig(accountID string) error {
	_, err := d.db.Exec(`DELETE FROM webhook_config WHERE account_id = $1`, accountID)
	return err
}

// ─── Role CRUD ──────────────────────────────────────────────

// CreateRole inserts a new role.
func (d *DB) CreateRole(rec *RoleRecord) error {
	_, err := d.db.Exec(`INSERT INTO role (id, name, description, is_builtin) VALUES ($1, $2, $3, $4)`,
		rec.ID, rec.Name, rec.Description, rec.IsBuiltin)
	return err
}

// GetRole retrieves a role by ID.
func (d *DB) GetRole(id string) (*RoleRecord, error) {
	row := d.db.QueryRow(`SELECT id, name, description, is_builtin, created_at FROM role WHERE id = $1`, id)
	var r RoleRecord
	err := row.Scan(&r.ID, &r.Name, &r.Description, &r.IsBuiltin, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// GetRoleByName retrieves a role by name.
func (d *DB) GetRoleByName(name string) (*RoleRecord, error) {
	row := d.db.QueryRow(`SELECT id, name, description, is_builtin, created_at FROM role WHERE name = $1`, name)
	var r RoleRecord
	err := row.Scan(&r.ID, &r.Name, &r.Description, &r.IsBuiltin, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ListRoles returns all roles.
func (d *DB) ListRoles() ([]*RoleRecord, error) {
	rows, err := d.db.Query(`SELECT id, name, description, is_builtin, created_at FROM role ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*RoleRecord
	for rows.Next() {
		var r RoleRecord
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, &r.IsBuiltin, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// UpdateRole updates a role's name and description.
func (d *DB) UpdateRole(id, name, description string) error {
	_, err := d.db.Exec(`UPDATE role SET name = $1, description = $2 WHERE id = $3`, name, description, id)
	return err
}

// DeleteRole removes a role. Built-in roles should be checked by the caller.
func (d *DB) DeleteRole(id string) error {
	_, err := d.db.Exec(`DELETE FROM role WHERE id = $1`, id)
	return err
}

// GetRolePermissions returns the permission strings for a role.
func (d *DB) GetRolePermissions(roleID string) ([]string, error) {
	rows, err := d.db.Query(`SELECT permission FROM role_permission WHERE role_id = $1 ORDER BY permission`, roleID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var perms []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		perms = append(perms, p)
	}
	return perms, rows.Err()
}

// SetRolePermissions replaces all permissions for a role.
func (d *DB) SetRolePermissions(roleID string, permissions []string) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM role_permission WHERE role_id = $1`, roleID); err != nil {
		return err
	}
	for _, p := range permissions {
		if _, err := tx.Exec(`INSERT INTO role_permission (role_id, permission) VALUES ($1, $2)`, roleID, p); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ─── User CRUD ──────────────────────────────────────────────

// CreateUser inserts a new user.
func (d *DB) CreateUser(rec *UserRecord) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.db.Exec(`
		INSERT INTO app_user (id, username, email, password_hash, role_id, enabled, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		rec.ID, rec.Username, rec.Email, rec.PasswordHash, rec.RoleID, rec.Enabled, now, now)
	return err
}

// GetUser retrieves a user by ID (joins role name).
func (d *DB) GetUser(id string) (*UserRecord, error) {
	row := d.db.QueryRow(`
		SELECT u.id, u.username, u.email, u.password_hash, u.role_id, r.name, u.enabled, u.created_at, u.updated_at
		FROM app_user u JOIN role r ON u.role_id = r.id
		WHERE u.id = $1`, id)
	return scanUser(row)
}

// GetUserByUsername retrieves a user by username.
func (d *DB) GetUserByUsername(username string) (*UserRecord, error) {
	row := d.db.QueryRow(`
		SELECT u.id, u.username, u.email, u.password_hash, u.role_id, r.name, u.enabled, u.created_at, u.updated_at
		FROM app_user u JOIN role r ON u.role_id = r.id
		WHERE u.username = $1`, username)
	return scanUser(row)
}

// GetUserByEmail retrieves a user by email address.
func (d *DB) GetUserByEmail(email string) (*UserRecord, error) {
	row := d.db.QueryRow(`
		SELECT u.id, u.username, u.email, u.password_hash, u.role_id, r.name, u.enabled, u.created_at, u.updated_at
		FROM app_user u JOIN role r ON u.role_id = r.id
		WHERE u.email = $1`, email)
	return scanUser(row)
}

// ListUsers returns all users.
func (d *DB) ListUsers() ([]*UserRecord, error) {
	rows, err := d.db.Query(`
		SELECT u.id, u.username, u.email, u.password_hash, u.role_id, r.name, u.enabled, u.created_at, u.updated_at
		FROM app_user u JOIN role r ON u.role_id = r.id
		ORDER BY u.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*UserRecord
	for rows.Next() {
		var r UserRecord
		if err := rows.Scan(&r.ID, &r.Username, &r.Email, &r.PasswordHash, &r.RoleID, &r.RoleName, &r.Enabled, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// UpdateUser updates a user's role and enabled status.
func (d *DB) UpdateUser(id, roleID string, enabled bool) error {
	_, err := d.db.Exec(`UPDATE app_user SET role_id = $1, enabled = $2, updated_at = NOW() WHERE id = $3`,
		roleID, enabled, id)
	return err
}

// UpdateUserPassword updates a user's password hash.
func (d *DB) UpdateUserPassword(id, passwordHash string) error {
	_, err := d.db.Exec(`UPDATE app_user SET password_hash = $1, updated_at = NOW() WHERE id = $2`,
		passwordHash, id)
	return err
}

// UpdateUserEmail updates a user's email address.
func (d *DB) UpdateUserEmail(id, email string) error {
	_, err := d.db.Exec(`UPDATE app_user SET email = $1, updated_at = NOW() WHERE id = $2`,
		email, id)
	return err
}

// UpdateUserUsername updates a user's username.
func (d *DB) UpdateUserUsername(id, username string) error {
	_, err := d.db.Exec(`UPDATE app_user SET username = $1, updated_at = NOW() WHERE id = $2`,
		username, id)
	return err
}

// DeleteUser removes a user.
func (d *DB) DeleteUser(id string) error {
	_, err := d.db.Exec(`DELETE FROM app_user WHERE id = $1`, id)
	return err
}

// CountUsersByRole returns how many users have a given role.
func (d *DB) CountUsersByRole(roleID string) (int, error) {
	var count int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM app_user WHERE role_id = $1`, roleID).Scan(&count)
	return count, err
}

func scanUser(row *sql.Row) (*UserRecord, error) {
	var r UserRecord
	err := row.Scan(&r.ID, &r.Username, &r.Email, &r.PasswordHash, &r.RoleID, &r.RoleName, &r.Enabled, &r.CreatedAt, &r.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ─── API Key CRUD ───────────────────────────────────────────

// CreateAPIKey inserts a new API key.
func (d *DB) CreateAPIKey(rec *APIKeyRecord) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.db.Exec(`
		INSERT INTO api_key (id, user_id, account_id, name, prefix, key_hash, expires_at, last_used, enabled, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULL, $8, $9)`,
		rec.ID, rec.UserID, rec.AccountID, rec.Name, rec.Prefix, rec.KeyHash, rec.ExpiresAt, rec.Enabled, now)
	return err
}

// GetAPIKey retrieves an API key by ID.
func (d *DB) GetAPIKey(id string) (*APIKeyRecord, error) {
	row := d.db.QueryRow(`SELECT id, user_id, account_id, name, prefix, key_hash, expires_at, last_used, enabled, created_at FROM api_key WHERE id = $1`, id)
	return scanAPIKey(row)
}

// GetAPIKeyByHash retrieves an API key by its SHA-256 hash.
func (d *DB) GetAPIKeyByHash(keyHash string) (*APIKeyRecord, error) {
	row := d.db.QueryRow(`SELECT id, user_id, account_id, name, prefix, key_hash, expires_at, last_used, enabled, created_at FROM api_key WHERE key_hash = $1`, keyHash)
	return scanAPIKey(row)
}

// ListAPIKeysByUser returns all API keys for a user.
func (d *DB) ListAPIKeysByUser(userID string) ([]*APIKeyRecord, error) {
	rows, err := d.db.Query(`SELECT id, user_id, account_id, name, prefix, key_hash, expires_at, last_used, enabled, created_at FROM api_key WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*APIKeyRecord
	for rows.Next() {
		r, err := scanAPIKeyRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListAllAPIKeys returns all API keys (admin use).
func (d *DB) ListAllAPIKeys() ([]*APIKeyRecord, error) {
	rows, err := d.db.Query(`SELECT id, user_id, account_id, name, prefix, key_hash, expires_at, last_used, enabled, created_at FROM api_key ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*APIKeyRecord
	for rows.Next() {
		r, err := scanAPIKeyRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteAPIKey removes an API key.
func (d *DB) DeleteAPIKey(id string) error {
	_, err := d.db.Exec(`DELETE FROM api_key WHERE id = $1`, id)
	return err
}

// UpdateAPIKeyLastUsed updates the last_used timestamp.
func (d *DB) UpdateAPIKeyLastUsed(id string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.db.Exec(`UPDATE api_key SET last_used = $1 WHERE id = $2`, now, id)
	return err
}

func scanAPIKey(row *sql.Row) (*APIKeyRecord, error) {
	var r APIKeyRecord
	var accountID, expiresAt, lastUsed sql.NullString
	err := row.Scan(&r.ID, &r.UserID, &accountID, &r.Name, &r.Prefix, &r.KeyHash, &expiresAt, &lastUsed, &r.Enabled, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if accountID.Valid {
		r.AccountID = &accountID.String
	}
	if expiresAt.Valid {
		r.ExpiresAt = &expiresAt.String
	}
	if lastUsed.Valid {
		r.LastUsed = &lastUsed.String
	}
	return &r, nil
}

func scanAPIKeyRow(rows *sql.Rows) (*APIKeyRecord, error) {
	var r APIKeyRecord
	var accountID, expiresAt, lastUsed sql.NullString
	err := rows.Scan(&r.ID, &r.UserID, &accountID, &r.Name, &r.Prefix, &r.KeyHash, &expiresAt, &lastUsed, &r.Enabled, &r.CreatedAt)
	if err != nil {
		return nil, err
	}
	if accountID.Valid {
		r.AccountID = &accountID.String
	}
	if expiresAt.Valid {
		r.ExpiresAt = &expiresAt.String
	}
	if lastUsed.Valid {
		r.LastUsed = &lastUsed.String
	}
	return &r, nil
}

// ─── Password Reset Token CRUD ──────────────────────────────

// CreateResetToken stores a hashed password reset token.
func (d *DB) CreateResetToken(rec *ResetTokenRecord) error {
	// Invalidate any existing unused tokens for this user
	_, _ = d.db.Exec(`UPDATE password_reset_token SET used = TRUE WHERE user_id = $1 AND used = FALSE`, rec.UserID)

	_, err := d.db.Exec(
		`INSERT INTO password_reset_token (id, user_id, token_hash, expires_at) VALUES ($1, $2, $3, $4)`,
		rec.ID, rec.UserID, rec.TokenHash, rec.ExpiresAt,
	)
	return err
}

// GetResetTokenByHash finds an unused, non-expired reset token by its hash.
func (d *DB) GetResetTokenByHash(tokenHash string) (*ResetTokenRecord, error) {
	row := d.db.QueryRow(
		`SELECT id, user_id, token_hash, expires_at, used, created_at FROM password_reset_token WHERE token_hash = $1 AND used = FALSE`,
		tokenHash,
	)

	var r ResetTokenRecord
	err := row.Scan(&r.ID, &r.UserID, &r.TokenHash, &r.ExpiresAt, &r.Used, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// MarkResetTokenUsed marks a token as consumed.
func (d *DB) MarkResetTokenUsed(id string) error {
	_, err := d.db.Exec(`UPDATE password_reset_token SET used = TRUE WHERE id = $1`, id)
	return err
}

// ─── Settings CRUD ──────────────────────────────────────────

// GetSetting returns the value for a setting key, or defaultVal if not set.
func (d *DB) GetSetting(key, defaultVal string) string {
	var val string
	err := d.db.QueryRow(`SELECT value FROM setting WHERE "key" = $1`, key).Scan(&val)
	if err != nil {
		return defaultVal
	}
	return val
}

// SetSetting upserts a setting key-value pair.
func (d *DB) SetSetting(key, value string) error {
	_, err := d.db.Exec(`INSERT INTO setting ("key", value) VALUES ($1, $2) ON CONFLICT("key") DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// GetAllSettings returns all settings as a map.
func (d *DB) GetAllSettings() (map[string]string, error) {
	rows, err := d.db.Query(`SELECT "key", value FROM setting`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	m := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		m[k] = v
	}
	return m, nil
}

// GetSettingBool returns a setting as a bool (true if value is "true" or "1").
func (d *DB) GetSettingBool(key string, defaultVal bool) bool {
	def := "false"
	if defaultVal {
		def = "true"
	}
	v := d.GetSetting(key, def)
	return v == "true" || v == "1"
}
