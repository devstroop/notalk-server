package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaults(t *testing.T) {
	cfg := defaults()

	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("expected host 0.0.0.0, got %s", cfg.Server.Host)
	}
	if cfg.Server.Port != 5000 {
		t.Errorf("expected port 5000, got %d", cfg.Server.Port)
	}
	if cfg.Logging.Level != "info" {
		t.Errorf("expected log level info, got %s", cfg.Logging.Level)
	}
	if cfg.Limits.MaxUploadSize != 10*1024*1024 {
		t.Errorf("expected max_upload_size 10MB, got %d", cfg.Limits.MaxUploadSize)
	}
	if !cfg.Swagger.Enabled {
		t.Error("expected swagger enabled by default")
	}
	if cfg.Swagger.Path != "/api-docs" {
		t.Errorf("expected swagger path /api-docs, got %s", cfg.Swagger.Path)
	}
	if cfg.Webhooks.RetryCount != 3 {
		t.Errorf("expected retry_count 3, got %d", cfg.Webhooks.RetryCount)
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := `
[server]
host = "127.0.0.1"
port = 8080

[auth]
secret_key = "test-key"

[logging]
level = "debug"
`
	configPath := filepath.Join(configDir, "app.toml")
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Change to temp dir so config/app.toml is found
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("expected host 127.0.0.1, got %s", cfg.Server.Host)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("expected port 8080, got %d", cfg.Server.Port)
	}
	if cfg.Auth.SecretKey != "test-key" {
		t.Errorf("expected secret_key test-key, got %s", cfg.Auth.SecretKey)
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("expected log level debug, got %s", cfg.Logging.Level)
	}

	// Defaults should still be applied for unset fields
	if cfg.Limits.MaxConcurrentRequests != 50 {
		t.Errorf("expected default max_concurrent_requests 50, got %d", cfg.Limits.MaxConcurrentRequests)
	}
}

func TestLoadNoFile(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// Should get all defaults
	if cfg.Server.Port != 5000 {
		t.Errorf("expected default port 5000, got %d", cfg.Server.Port)
	}
}

func TestLoadInvalidToml(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "app.toml"), []byte("not valid [[[ toml"), 0o644); err != nil {
		t.Fatal(err)
	}

	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	_, err := Load()
	if err == nil {
		t.Error("expected error for invalid toml, got nil")
	}
}

func TestEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	t.Setenv("NOTALK_SERVER_HOST", "10.0.0.1")
	t.Setenv("NOTALK_SERVER_PORT", "9090")
	t.Setenv("NOTALK_AUTH_SECRET_KEY", "env-secret")
	t.Setenv("NOTALK_LOG_LEVEL", "debug")
	t.Setenv("NOTALK_DATABASE_DSN", "postgres://localhost:5432/notalk_test?sslmode=disable")
	t.Setenv("NOTALK_ACCOUNTS_DIR", "/data/accounts")
	t.Setenv("NOTALK_CORS_ORIGINS", "https://a.com,https://b.com")
	t.Setenv("NOTALK_SWAGGER_ENABLED", "false")
	t.Setenv("NOTALK_WEBHOOKS_ENABLED", "true")
	t.Setenv("NOTALK_LIMITS_MAX_CONCURRENT", "100")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Server.Host != "10.0.0.1" {
		t.Errorf("expected host 10.0.0.1, got %s", cfg.Server.Host)
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("expected port 9090, got %d", cfg.Server.Port)
	}
	if cfg.Auth.SecretKey != "env-secret" {
		t.Errorf("expected secret env-secret, got %s", cfg.Auth.SecretKey)
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("expected log level debug, got %s", cfg.Logging.Level)
	}
	if cfg.Database.DSN != "postgres://localhost:5432/notalk_test?sslmode=disable" {
		t.Errorf("expected db dsn postgres://localhost:5432/notalk_test?sslmode=disable, got %s", cfg.Database.DSN)
	}
	if cfg.Accounts.BaseDirectory != "/data/accounts" {
		t.Errorf("expected accounts dir /data/accounts, got %s", cfg.Accounts.BaseDirectory)
	}
	if len(cfg.CORS.AllowOrigins) != 2 || cfg.CORS.AllowOrigins[0] != "https://a.com" {
		t.Errorf("expected 2 CORS origins, got %v", cfg.CORS.AllowOrigins)
	}
	if cfg.Swagger.Enabled {
		t.Error("expected swagger disabled via env")
	}
	if !cfg.Webhooks.Enabled {
		t.Error("expected webhooks enabled via env")
	}
	if cfg.Limits.MaxConcurrentRequests != 100 {
		t.Errorf("expected max_concurrent 100, got %d", cfg.Limits.MaxConcurrentRequests)
	}
}
