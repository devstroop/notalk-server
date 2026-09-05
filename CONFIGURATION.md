# Configuration

NoTalk reads configuration from `config/app.toml` (relative to working directory), falling back to `~/.notalk/config.toml`. Missing keys use defaults. All settings can be overridden via environment variables using the `NOTALK_SECTION_KEY` format (e.g. `NOTALK_SERVER_PORT=8080`).

## Reference

```toml
[server]
host = "0.0.0.0"       # Bind address
port = 3000             # Listen port

[auth]
secret_key = "..."      # Bearer token for API authentication (CHANGE THIS)
registration_enabled = false  # Set to true to allow public user registration

[smtp]
# host = "smtp.example.com"  # Required for forgot-password emails
# port = 587
# username = ""
# password = ""
# from = "NoTalk <noreply@example.com>"
# tls = false       # Use implicit TLS (port 465)
# starttls = true   # Use STARTTLS (port 587)

[logging]
level = "info"          # trace | debug | info | warn | error

[database]
# dsn = "postgres://notalk:notalk@localhost:5432/notalk?sslmode=disable"  # PostgreSQL DSN

[cors]
allow_origins = ["*"]
allow_methods = ["GET", "POST", "PUT", "DELETE", "OPTIONS"]
allow_headers = ["authorization", "content-type"]

[limits]
max_concurrent_requests = 50
request_timeout_ms = 30000
max_upload_size = 10485760      # 10MB

[accounts]
base_directory = ""             # Default: ~/.notalk/accounts
                                # Each account gets a subdirectory with its UUID

[accounts.defaults]
idle_timeout = 300              # Auto-disconnect after N seconds idle (0 = never)

[webhooks]
enabled = false
timeout_ms = 5000
retry_count = 3
retry_delay_ms = 1000

[swagger]
enabled = true
path = "/api-docs"

[mcp]
enabled = true          # Enable/disable MCP server (can also toggle at runtime via admin UI)
```

## Environment Variables

All config keys map to environment variables with the `NOTALK_` prefix. Nested keys use underscores.

| Config Key | Environment Variable | Example |
|-----------|---------------------|---------|
| `server.host` | `NOTALK_SERVER_HOST` | `0.0.0.0` |
| `server.port` | `NOTALK_SERVER_PORT` | `3000` |
| `auth.secret_key` | `NOTALK_AUTH_SECRET_KEY` | `my-secret` |
| `auth.registration_enabled` | `NOTALK_AUTH_REGISTRATION_ENABLED` | `true` |
| `logging.level` | `NOTALK_LOG_LEVEL` | `debug` |
| `database.dsn` | `NOTALK_DATABASE_DSN` | `postgres://notalk:notalk@localhost:5432/notalk?sslmode=disable` |
| `accounts.base_directory` | `NOTALK_ACCOUNTS_DIR` | `/data/accounts` |
| `cors.allow_origins` | `NOTALK_CORS_ORIGINS` | `*` |
| `swagger.enabled` | `NOTALK_SWAGGER_ENABLED` | `true` |

## Data Layout

```
PostgreSQL:
- account, app_user, role, api_key, message, webhook_config, proxy_config, usage, agent_* tables
- Each account's WhatsApp session stored via whatsmeow PostgreSQL store (sqlstore)

File system:
~/.notalk/accounts/{uuid}/  # WhatsApp session cache (if any)
```

## Docker

The included `docker-compose.yml` maps all key settings to environment variables:

```bash
docker compose up -d
```

Data is persisted in a named volume (`notalk-data`) mounted at `/data`.

## Notes

- **PostgreSQL**: Uses `github.com/lib/pq` with `whatsmeow` PostgreSQL store — requires PostgreSQL 16+ (see `docker-compose.yml`).
- **Idle timeout**: A background goroutine polls every 30s. When an account has been idle longer than `idle_timeout`, it disconnects automatically. Any API request to that account reconnects it on demand.
- **secret_key**: Used for Bearer token auth and JWT signing. All API endpoints under `/api/v1/` require authentication. Health and public auth endpoints are unauthenticated.
- **MCP runtime toggle**: The MCP endpoint can be enabled/disabled at runtime via the admin UI or `PATCH /api/v1/mcp` without restarting the server.
- **Billing**: Removed in current develop (billing stack deleted).
