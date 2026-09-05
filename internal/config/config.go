package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the top-level application configuration.
type Config struct {
	Server   ServerConfig   `toml:"server"`
	Auth     AuthConfig     `toml:"auth"`
	SMTP     SMTPConfig     `toml:"smtp"`
	Logging  LoggingConfig  `toml:"logging"`
	Database DatabaseConfig `toml:"database"`
	CORS     CORSConfig     `toml:"cors"`
	Limits   LimitsConfig   `toml:"limits"`
	Accounts AccountsConfig `toml:"accounts"`
	Webhooks WebhookConfig  `toml:"webhooks"`
	Swagger  SwaggerConfig  `toml:"swagger"`
	MCP      MCPConfig       `toml:"mcp"`
	LLM      LLMConfig       `toml:"llm"`
}

type ServerConfig struct {
	Host string `toml:"host"`
	Port int    `toml:"port"`
}

type AuthConfig struct {
	SecretKey           string `toml:"secret_key"`
	JWTSecret           string `toml:"jwt_secret"`
	AdminSecret         string `toml:"admin_secret"`
	RegistrationEnabled bool   `toml:"registration_enabled"`
}

// EffectiveJWTSecret returns the JWT HMAC secret, preferring NOTALK_JWT_SECRET
// with fallback to NOTALK_AUTH_SECRET_KEY for compat.
func (a AuthConfig) EffectiveJWTSecret() string {
	if a.JWTSecret != "" {
		return a.JWTSecret
	}
	return a.SecretKey
}

// EffectiveAdminSecret returns the static admin bearer secret, preferring
// NOTALK_ADMIN_SECRET with fallback to NOTALK_AUTH_SECRET_KEY.
func (a AuthConfig) EffectiveAdminSecret() string {
	if a.AdminSecret != "" {
		return a.AdminSecret
	}
	return a.SecretKey
}

// UsesLegacySecret reports whether NOTALK_AUTH_SECRET_KEY fallback is in use
// for either JWT or admin auth (caller should log a startup warning).
func (a AuthConfig) UsesLegacySecret() bool {
	return (a.JWTSecret == "" && a.SecretKey != "") || (a.AdminSecret == "" && a.SecretKey != "")
}

type SMTPConfig struct {
	Host     string `toml:"host"`
	Port     int    `toml:"port"`
	Username string `toml:"username"`
	Password string `toml:"password"`
	From     string `toml:"from"`     // sender address e.g. "NoTalk <noreply@example.com>"
	TLS      bool   `toml:"tls"`      // use implicit TLS (port 465)
	StartTLS bool   `toml:"starttls"` // use STARTTLS (port 587)
}

type LoggingConfig struct {
	Level string `toml:"level"`
}

type DatabaseConfig struct {
	DSN string `toml:"dsn"`
}

type CORSConfig struct {
	AllowOrigins []string `toml:"allow_origins"`
	AllowMethods []string `toml:"allow_methods"`
	AllowHeaders []string `toml:"allow_headers"`
}

type LimitsConfig struct {
	MaxConcurrentRequests int   `toml:"max_concurrent_requests"`
	RequestTimeoutMs      int64 `toml:"request_timeout_ms"`
	MaxUploadSize         int64 `toml:"max_upload_size"`
}

type AccountsConfig struct {
	BaseDirectory string `toml:"base_directory"`
}

type WebhookConfig struct {
	Enabled    bool   `toml:"enabled"`
	TimeoutMs  int64  `toml:"timeout_ms"`
	RetryCount int    `toml:"retry_count"`
	RetryDelay int64  `toml:"retry_delay_ms"`
}

type SwaggerConfig struct {
	Enabled bool   `toml:"enabled"`
	Path    string `toml:"path"`
}

type MCPConfig struct {
	Enabled bool `toml:"enabled"`
}

type LLMConfig struct {
	Enabled  bool   `toml:"enabled"`
	Provider string `toml:"provider"` // "openai" | "ollama"
	APIKey   string `toml:"api_key"`  // required for OpenAI; leave blank for Ollama
	BaseURL  string `toml:"base_url"` // override API endpoint; blank = provider default
	Model    string `toml:"model"`    // e.g. "gpt-4o-mini", "llama3.2"
}

// Load reads config from config/app.toml (next to binary or working dir),
// falling back to sensible defaults.
func Load() (*Config, error) {
	cfg := defaults()

	// Search paths for config
	paths := []string{
		"config/app.toml",
		filepath.Join(homeDir(), ".notalk", "config.toml"),
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			if _, err := toml.DecodeFile(p, cfg); err != nil {
				return nil, fmt.Errorf("parse %s: %w", p, err)
			}
			break
		}
	}

	// Apply environment variable overrides
	applyEnvOverrides(cfg)

	// Resolve database DSN
	if cfg.Database.DSN == "" {
		cfg.Database.DSN = "postgres://localhost:5432/notalk?sslmode=disable"
	}

	// Resolve accounts base directory
	if cfg.Accounts.BaseDirectory == "" {
		cfg.Accounts.BaseDirectory = filepath.Join(homeDir(), ".notalk", "accounts")
	}

	return cfg, nil
}

// applyEnvOverrides overrides config values with NOTALK_* environment variables.
func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("NOTALK_SERVER_HOST"); v != "" {
		cfg.Server.Host = v
	}
	if v := os.Getenv("NOTALK_SERVER_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Server.Port = n
		}
	}
	if v := os.Getenv("NOTALK_JWT_SECRET"); v != "" {
		cfg.Auth.JWTSecret = v
	}
	if v := os.Getenv("NOTALK_ADMIN_SECRET"); v != "" {
		cfg.Auth.AdminSecret = v
	}
	if v := os.Getenv("NOTALK_AUTH_SECRET_KEY"); v != "" {
		cfg.Auth.SecretKey = v
		// Backfill split secrets for compat if explicit split not set
		if cfg.Auth.JWTSecret == "" {
			cfg.Auth.JWTSecret = v
		}
		if cfg.Auth.AdminSecret == "" {
			cfg.Auth.AdminSecret = v
		}
	}
	if v := os.Getenv("NOTALK_LOG_LEVEL"); v != "" {
		cfg.Logging.Level = v
	}
	if v := os.Getenv("NOTALK_DATABASE_DSN"); v != "" {
		cfg.Database.DSN = v
	}
	if v := os.Getenv("NOTALK_CORS_ORIGINS"); v != "" {
		cfg.CORS.AllowOrigins = strings.Split(v, ",")
	}
	if v := os.Getenv("NOTALK_LIMITS_MAX_CONCURRENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Limits.MaxConcurrentRequests = n
		}
	}
	if v := os.Getenv("NOTALK_LIMITS_TIMEOUT_MS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.Limits.RequestTimeoutMs = n
		}
	}
	if v := os.Getenv("NOTALK_LIMITS_MAX_UPLOAD"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.Limits.MaxUploadSize = n
		}
	}
	if v := os.Getenv("NOTALK_ACCOUNTS_DIR"); v != "" {
		cfg.Accounts.BaseDirectory = v
	}
	if v := os.Getenv("NOTALK_WEBHOOKS_ENABLED"); v != "" {
		cfg.Webhooks.Enabled = v == "true" || v == "1"
	}
	if v := os.Getenv("NOTALK_WEBHOOKS_TIMEOUT_MS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.Webhooks.TimeoutMs = n
		}
	}
	if v := os.Getenv("NOTALK_WEBHOOKS_RETRY_COUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Webhooks.RetryCount = n
		}
	}
	if v := os.Getenv("NOTALK_WEBHOOKS_RETRY_DELAY_MS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.Webhooks.RetryDelay = n
		}
	}
	if v := os.Getenv("NOTALK_AUTH_REGISTRATION_ENABLED"); v != "" {
		cfg.Auth.RegistrationEnabled = v == "true" || v == "1"
	}
	if v := os.Getenv("NOTALK_SMTP_HOST"); v != "" {
		cfg.SMTP.Host = v
	}
	if v := os.Getenv("NOTALK_SMTP_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.SMTP.Port = n
		}
	}
	if v := os.Getenv("NOTALK_SMTP_USERNAME"); v != "" {
		cfg.SMTP.Username = v
	}
	if v := os.Getenv("NOTALK_SMTP_PASSWORD"); v != "" {
		cfg.SMTP.Password = v
	}
	if v := os.Getenv("NOTALK_SMTP_FROM"); v != "" {
		cfg.SMTP.From = v
	}
	if v := os.Getenv("NOTALK_SMTP_TLS"); v != "" {
		cfg.SMTP.TLS = v == "true" || v == "1"
	}
	if v := os.Getenv("NOTALK_SMTP_STARTTLS"); v != "" {
		cfg.SMTP.StartTLS = v == "true" || v == "1"
	}
	if v := os.Getenv("NOTALK_SWAGGER_ENABLED"); v != "" {
		cfg.Swagger.Enabled = v == "true" || v == "1"
	}
	if v := os.Getenv("NOTALK_SWAGGER_PATH"); v != "" {
		cfg.Swagger.Path = v
	}
	if v := os.Getenv("NOTALK_MCP_ENABLED"); v != "" {
		cfg.MCP.Enabled = v == "true" || v == "1"
	}
	if v := os.Getenv("NOTALK_LLM_ENABLED"); v != "" {
		cfg.LLM.Enabled = v == "true" || v == "1"
	}
	if v := os.Getenv("NOTALK_LLM_PROVIDER"); v != "" {
		cfg.LLM.Provider = v
	}
	if v := os.Getenv("NOTALK_LLM_API_KEY"); v != "" {
		cfg.LLM.APIKey = v
	}
	if v := os.Getenv("NOTALK_LLM_BASE_URL"); v != "" {
		cfg.LLM.BaseURL = v
	}
	if v := os.Getenv("NOTALK_LLM_MODEL"); v != "" {
		cfg.LLM.Model = v
	}

	// Auto-enable LLM if credentials are configured but enabled wasn't explicitly set.
	if !cfg.LLM.Enabled && (cfg.LLM.APIKey != "" || cfg.LLM.Provider == "ollama") {
		cfg.LLM.Enabled = true
	}
}

func defaults() *Config {
	return &Config{
		Server:  ServerConfig{Host: "0.0.0.0", Port: 3000},
		Auth:    AuthConfig{SecretKey: "change-this-secret-key-in-production"},
		SMTP:    SMTPConfig{Port: 587, StartTLS: true},
		Logging: LoggingConfig{Level: "info"},
		CORS: CORSConfig{
			AllowOrigins: []string{"*"},
			AllowMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
			AllowHeaders: []string{"authorization", "content-type"},
		},
		Limits: LimitsConfig{
			MaxConcurrentRequests: 50,
			RequestTimeoutMs:      30000,
			MaxUploadSize:         10 * 1024 * 1024,
		},
		Accounts: AccountsConfig{},
		Webhooks: WebhookConfig{
			TimeoutMs:  5000,
			RetryCount: 3,
			RetryDelay: 1000,
		},
		Swagger: SwaggerConfig{Enabled: true, Path: "/api-docs"},
		MCP:     MCPConfig{Enabled: true},
	}
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}
