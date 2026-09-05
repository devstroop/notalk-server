package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvOverridesFull(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	t.Setenv("NOTALK_SERVER_HOST", "1.1.1.1")
	t.Setenv("NOTALK_SERVER_PORT", "8081")
	t.Setenv("NOTALK_AUTH_SECRET_KEY", "full-secret")
	t.Setenv("NOTALK_AUTH_REGISTRATION_ENABLED", "true")
	t.Setenv("NOTALK_LOG_LEVEL", "warn")
	t.Setenv("NOTALK_DATABASE_DSN", "postgres://x:5432/y?sslmode=disable")
	t.Setenv("NOTALK_CORS_ORIGINS", "https://c.com")
	t.Setenv("NOTALK_LIMITS_MAX_CONCURRENT", "77")
	t.Setenv("NOTALK_LIMITS_TIMEOUT_MS", "12345")
	t.Setenv("NOTALK_LIMITS_MAX_UPLOAD", "9999")
	t.Setenv("NOTALK_ACCOUNTS_DIR", "/tmp/accounts")
	t.Setenv("NOTALK_WEBHOOKS_ENABLED", "true")
	t.Setenv("NOTALK_WEBHOOKS_TIMEOUT_MS", "7777")
	t.Setenv("NOTALK_WEBHOOKS_RETRY_COUNT", "5")
	t.Setenv("NOTALK_WEBHOOKS_RETRY_DELAY_MS", "8888")
	t.Setenv("NOTALK_SMTP_HOST", "smtp.example.com")
	t.Setenv("NOTALK_SMTP_PORT", "2525")
	t.Setenv("NOTALK_SMTP_USERNAME", "user")
	t.Setenv("NOTALK_SMTP_PASSWORD", "pass")
	t.Setenv("NOTALK_SMTP_FROM", "from@example.com")
	t.Setenv("NOTALK_SMTP_TLS", "true")
	t.Setenv("NOTALK_SMTP_STARTTLS", "true")
	t.Setenv("NOTALK_SWAGGER_ENABLED", "false")
	t.Setenv("NOTALK_SWAGGER_PATH", "/docs")
	t.Setenv("NOTALK_MCP_ENABLED", "false")
	t.Setenv("NOTALK_BILLING_ENABLED", "true")
	t.Setenv("NOTALK_BILLING_STRIPE_SECRET_KEY", "sk_test")
	t.Setenv("NOTALK_BILLING_STRIPE_WEBHOOK_SECRET", "whsec_test")
	t.Setenv("NOTALK_BILLING_DEFAULT_PLAN", "pro")
	t.Setenv("NOTALK_LLM_ENABLED", "true")
	t.Setenv("NOTALK_LLM_PROVIDER", "openai")
	t.Setenv("NOTALK_LLM_API_KEY", "sk-openai")
	t.Setenv("NOTALK_LLM_BASE_URL", "https://api.openai.com/v1")
	t.Setenv("NOTALK_LLM_MODEL", "gpt-4o")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Server.Host != "1.1.1.1" {
		t.Errorf("host %s", cfg.Server.Host)
	}
	if cfg.Server.Port != 8081 {
		t.Errorf("port %d", cfg.Server.Port)
	}
	if cfg.Auth.SecretKey != "full-secret" {
		t.Error("secret")
	}
	if !cfg.Auth.RegistrationEnabled {
		t.Error("registration")
	}
	if cfg.Logging.Level != "warn" {
		t.Error("log level")
	}
	if cfg.Database.DSN != "postgres://x:5432/y?sslmode=disable" {
		t.Error("dsn")
	}
	if cfg.CORS.AllowOrigins[0] != "https://c.com" {
		t.Error("cors")
	}
	if cfg.Limits.MaxConcurrentRequests != 77 {
		t.Error("max concurrent")
	}
	if cfg.Limits.RequestTimeoutMs != 12345 {
		t.Errorf("timeout %d", cfg.Limits.RequestTimeoutMs)
	}
	if cfg.Limits.MaxUploadSize != 9999 {
		t.Error("max upload")
	}
	if cfg.Accounts.BaseDirectory != "/tmp/accounts" {
		t.Error("accounts dir")
	}
	if !cfg.Webhooks.Enabled {
		t.Error("webhooks enabled")
	}
	if cfg.Webhooks.TimeoutMs != 7777 {
		t.Error("webhook timeout")
	}
	if cfg.Webhooks.RetryCount != 5 {
		t.Error("retry count")
	}
	if cfg.Webhooks.RetryDelay != 8888 {
		t.Error("retry delay")
	}
	if cfg.SMTP.Host != "smtp.example.com" {
		t.Error("smtp host")
	}
	if cfg.SMTP.Port != 2525 {
		t.Error("smtp port")
	}
	if cfg.SMTP.Username != "user" {
		t.Error("smtp user")
	}
	if cfg.SMTP.Password != "pass" {
		t.Error("smtp pass")
	}
	if cfg.SMTP.From != "from@example.com" {
		t.Error("smtp from")
	}
	if !cfg.SMTP.TLS {
		t.Error("smtp tls")
	}
	if !cfg.SMTP.StartTLS {
		t.Error("smtp starttls")
	}
	if cfg.Swagger.Enabled {
		t.Error("swagger should be false")
	}
	if cfg.Swagger.Path != "/docs" {
		t.Error("swagger path")
	}
	if cfg.MCP.Enabled {
		t.Error("mcp should be false")
	}
	if !cfg.LLM.Enabled {
		t.Error("llm enabled")
	}
	if cfg.LLM.Provider != "openai" {
		t.Error("llm provider")
	}
	if cfg.LLM.APIKey != "sk-openai" {
		t.Error("llm api key")
	}
	if cfg.LLM.BaseURL != "https://api.openai.com/v1" {
		t.Error("llm base url")
	}
	if cfg.LLM.Model != "gpt-4o" {
		t.Error("llm model")
	}
}

func TestEnvOverridesInvalidIntsIgnored(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	// invalid ints should be ignored, defaults remain
	t.Setenv("NOTALK_SERVER_PORT", "notanint")
	t.Setenv("NOTALK_LIMITS_MAX_CONCURRENT", "bad")
	t.Setenv("NOTALK_LIMITS_TIMEOUT_MS", "bad")
	t.Setenv("NOTALK_LIMITS_MAX_UPLOAD", "bad")
	t.Setenv("NOTALK_WEBHOOKS_TIMEOUT_MS", "bad")
	t.Setenv("NOTALK_WEBHOOKS_RETRY_COUNT", "bad")
	t.Setenv("NOTALK_WEBHOOKS_RETRY_DELAY_MS", "bad")
	t.Setenv("NOTALK_SMTP_PORT", "bad")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Server.Port != 5000 {
		t.Errorf("expected default port 5000 got %d", cfg.Server.Port)
	}
	if cfg.Limits.MaxConcurrentRequests != 50 {
		t.Errorf("expected 50 got %d", cfg.Limits.MaxConcurrentRequests)
	}
}

func TestLLMAutoEnable(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	// provider ollama should auto-enable even if enabled not set
	t.Setenv("NOTALK_LLM_ENABLED", "false")
	t.Setenv("NOTALK_LLM_PROVIDER", "ollama")
	t.Setenv("NOTALK_LLM_API_KEY", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	// applyEnvOverrides has logic: if !Enabled && (APIKey != "" || Provider == "ollama") then Enabled=true
	// But we set Enabled=false explicitly via env, then check auto-enable
	// Actually EnvOverrides sets Enabled from NOTALK_LLM_ENABLED first, then later checks auto-enable
	// So with ENABLED=false but PROVIDER=ollama, it should become true
	if !cfg.LLM.Enabled {
		t.Error("expected LLM auto-enabled for ollama")
	}

	// clean
	_ = os.Unsetenv("NOTALK_LLM_PROVIDER")
	_ = os.Unsetenv("NOTALK_LLM_API_KEY")
	_ = os.Unsetenv("NOTALK_LLM_ENABLED")

	dir2 := t.TempDir()
	origDir2, _ := os.Getwd()
	_ = os.Chdir(dir2)
	defer func() { _ = os.Chdir(origDir2) }()
	t.Setenv("NOTALK_LLM_API_KEY", "sk-xyz")
	t.Setenv("NOTALK_LLM_ENABLED", "false")
	cfg, _ = Load()
	if !cfg.LLM.Enabled {
		t.Error("expected auto-enable for api key")
	}
}

func TestDefaultsAllFields(t *testing.T) {
	cfg := defaults()
	if cfg.Server.Host != "0.0.0.0" {
		t.Error("host")
	}
	if !cfg.MCP.Enabled {
		t.Error("mcp default")
	}
}

func TestLoadWithDatabaseDSNDefault(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()
	// ensure no env
	_ = os.Unsetenv("NOTALK_DATABASE_DSN")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.DSN != "postgres://localhost:5432/notalk?sslmode=disable" {
		t.Errorf("expected default DSN got %s", cfg.Database.DSN)
	}
	if cfg.Accounts.BaseDirectory == "" {
		t.Error("expected accounts dir default")
	}
	// check homeDir via config
	if cfg.Accounts.BaseDirectory != filepath.Join(homeDir(), ".notalk", "accounts") {
		t.Errorf("expected home dir based path got %s", cfg.Accounts.BaseDirectory)
	}
}

func TestLoadWithAccountsBaseDirectoryFromFile(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := `
[accounts]
base_directory = "/custom/accounts"
[accounts.defaults]
idle_timeout = 999
`
	if err := os.WriteFile(filepath.Join(configDir, "app.toml"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Accounts.BaseDirectory != "/custom/accounts" {
		t.Errorf("expected /custom/accounts got %s", cfg.Accounts.BaseDirectory)
	}
}
