# NoTalk

**WhatsApp HTTP API + MCP server.** Multi-account WhatsApp over the native multi-device protocol — REST API and MCP for AI agents in a single Go binary. No browser, no Selenium. Web dashboard is now a separate project [`notalk-web`](../notalk-web).

> `Go 1.26` · `PostgreSQL 18` · `whatsmeow @ main` · 26 MCP tools · Swagger at `/api-docs`

## Highlights

- **Multi-account** — dozens of numbers on one server, isolated sessions
- **Phone-first** — `?phone=9198…` auto-resolves to JID (`@s.whatsapp.net` / `@g.us` / LID)
- **MCP** — 26 tools (`send_message`, `list_chats`, `create_group`, …) via Streamable HTTP at fixed `/mcp`
- **Auth + RBAC** — secret key → JWT → API key (`notalk_…`), `resource:action` permissions (`*` for admin)
- **Browserless** — Noise + Signal + Protobuf over encrypted WebSocket (`whatsmeow`)
- **Pure Go** — single binary, `CGO_ENABLED=0`, no runtime deps
- **Resilient** — auto-connect on demand, idle disconnect (`idle_timeout`), crash recovery
- **Webhooks** — `message`/`receipt` with `X-Webhook-Signature` (HMAC) + retry

## Requirements

- Go `1.26` (`main go 1.26`, `whatsmeow` toolchain `go1.27`)
- PostgreSQL `16`+ (compose uses `postgres:18-alpine` → mount `pg-data:/var/lib/postgresql` — required for 18+, see `postgres#1259` / `PGDATA` parent)
- `NOTALK_AUTH_SECRET_KEY` set in production

## Quick Start

```bash
# 1. config
cp config/app.example.toml config/app.toml   # set secret_key
# 2. run
go run ./cmd/notalk
# or
go build -trimpath -o /tmp/notalk ./cmd/notalk && \
  NOTALK_DATABASE_DSN=postgres://notalk:notalk@localhost:5432/notalk?sslmode=disable /tmp/notalk
```

```bash
# Docker (PostgreSQL + NoTalk)
docker compose up -d          # → http://localhost:3000
curl -sf http://localhost:3000/api/health  # {"status":"ok"}
docker compose logs -f
```

| URL | Description |
|-----|-------------|
| `/api-docs` | Swagger UI |
| `/mcp` | MCP endpoint (fixed, Bearer auth) |
| `/api/health` | Health check (no auth) |

Three calls to send a message:

```bash
AUTH="Authorization: Bearer <secret_key>"

# 1. create account
curl -X POST http://localhost:3000/api/v1/accounts \
  -H "$AUTH" -H "Content-Type: application/json" \
  -d '{"phone_number":"+919876543210","account_name":"main"}' # → {id}

# 2. link (QR)
curl http://localhost:3000/api/v1/accounts/{id}/session/qr -H "$AUTH" -o qr.png

# 3. send
curl -X POST "http://localhost:3000/api/v1/accounts/{id}/messages?phone=919876543210&text=Hello!" -H "$AUTH"
```

## Authentication

All `/api/v1/*` require `Authorization: Bearer <token>`.

| Method | Format | Scope | Use case |
|--------|--------|-------|----------|
| Secret key | `Bearer <secret_key>` (`NOTALK_AUTH_SECRET_KEY`) | `*` | System admin, no user context |
| JWT | `Bearer eyJ…` (`POST /api/v1/auth/login`) | `role.permissions` | User login |
| API key | `Bearer notalk_…` (`POST /api/v1/api-keys`) | `user + optional account_id` + expiry | Programmatic, MCP account-scoping |

**Public (no auth):** `POST /api/v1/auth/login`, `/register` (if enabled), `/forgot-password`, `/reset-password`, `GET /api/health`.

**RBAC:** built-in `admin` (`*`) and `user`. Permissions `resource:action` e.g. `messages:write`, `accounts:read`.

## REST API

Base: `http://localhost:3000/api/v1/accounts/{id}` (`{id}` = account UUID). All phone-accepting endpoints also accept `phone` → JID resolved via WhatsApp.

**Accounts**

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/accounts` | List all accounts |
| POST | `/api/v1/accounts` | Create account |
| GET | `/api/v1/accounts/{id}` | Get account details |
| PATCH | `/api/v1/accounts/{id}` | Update account |
| DELETE | `/api/v1/accounts/{id}?delete_data=true` | Delete account and optionally wipe data |

**Session**

| Method | Path | Description |
|--------|------|-------------|
| GET | `/{id}/session` | Connection and auth status |
| GET | `/{id}/session/qr` | QR code PNG for linking |
| POST | `/{id}/session/pair` | Phone-number pairing code |
| DELETE | `/{id}/session` | Logout and unlink device |

**Messaging**

| Method | Path | Description |
|--------|------|-------------|
| POST | `/{id}/messages?phone=NUM&text=...` | Send message by phone number |
| POST | `/{id}/messages?jid=JID&text=...` | Send message by JID |
| GET | `/{id}/messages?chat=JID` | Message history (paginated) |
| POST | `/{id}/messages/reactions` | React to a message |
| POST | `/{id}/messages/mark-read` | Mark messages as read |
| DELETE | `/{id}/messages/{msg_id}?chat=JID` | Revoke / delete for everyone |

Single-call send: `phone`/`jid` (one required), `text` (query or multipart), `reply_to`, `file` (multipart, caption = `text`). `text` not required when sending file; query `text` takes precedence over body.

### Chats & Contacts

| Method | Path | Description |
|--------|------|-------------|
| GET | `/{id}/chats` | List chats (pinned first, then by recency) |
| GET | `/{id}/contacts` | List all contacts |
| POST | `/{id}/contacts/check` | Check if phone numbers are on WhatsApp |
| GET | `/{id}/contacts/{jid}` | Get contact info (also accepts `?phone=`) |

### Groups

| Method | Path | Description |
|--------|------|-------------|
| GET | `/{id}/groups` | List joined groups |
| POST | `/{id}/groups` | Create group (participants can be phone numbers) |
| GET | `/{id}/groups/{jid}` | Group info |
| PATCH | `/{id}/groups/{jid}` | Update name, topic, locked, announce |
| DELETE | `/{id}/groups/{jid}` | Leave group |
| GET | `/{id}/groups/{jid}/invite` | Get invite link |
| POST | `/{id}/groups/{jid}/participants` | Add / remove / promote / demote (accepts phone numbers) |

### Newsletters (Channels)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/{id}/newsletters` | List followed channels |
| POST | `/{id}/newsletters/follow` | Follow a channel |
| POST | `/{id}/newsletters/unfollow` | Unfollow a channel |
| GET | `/{id}/newsletters/{jid}` | Channel info |
| GET | `/{id}/newsletters/{jid}/messages` | Channel messages |
| POST | `/{id}/newsletters/{jid}/mute` | Mute/unmute channel |

### Presence & Profile

| Method | Path | Description |
|--------|------|-------------|
| POST | `/{id}/presence` | Send typing indicator or set global presence (accepts `phone`) |
| GET | `/{id}/profile` | Get own profile (includes business fields) |
| PATCH | `/{id}/profile` | Update about text |

### Proxy

| Method | Path | Description |
|--------|------|-------------|
| GET | `/{id}/proxy` | Get proxy config |
| PUT | `/{id}/proxy` | Set proxy (http / https / socks5) |
| DELETE | `/{id}/proxy` | Remove proxy |

### Webhook

| Method | Path | Description |
|--------|------|-------------|
| GET | `/{id}/webhook` | Get webhook config |
| PUT | `/{id}/webhook` | Set webhook URL, events, and HMAC secret |
| DELETE | `/{id}/webhook` | Remove webhook |

Webhook payload: `event_type`, `account_id`, `timestamp`, `payload` + `X-Webhook-Signature` (HMAC-SHA256) on `secret`; retries `retry_count` × backoff.

**Admin (requires `*`):** `GET/POST /api/v1/users`, `GET/PATCH/DELETE /api/v1/users/{id}`, `GET/POST /api/v1/roles`, `GET/PATCH/DELETE /api/v1/roles/{id}`, `GET/POST/DELETE /api/v1/api-keys`


Full interactive docs: `http://localhost:3000/api-docs` (Swagger, `internal/handler/openapi.json`).

## MCP Server

Fixed endpoint `http://localhost:3000/mcp` (Streamable HTTP, stateful SSE, `10m` idle TTL). Auth: `Authorization: Bearer <secret_key>` or `Bearer notalk_…` (account-scoped keys auto-bind `account_id`).

**VS Code / Copilot** `.vscode/mcp.json`:

```json
{
  "servers": {
    "notalk": {
      "type": "http",
      "url": "http://localhost:3000/mcp",
      "headers": { "Authorization": "Bearer <secret_key>" }
    }
  }
}
```

**26 tools:** `list_accounts`, `get_session`, `get_qr`, `pair_phone`, `logout`, `send_message`, `send_media` (base64), `get_messages`, `list_chats`, `react_message`, `mark_read`, `revoke_message`, `list_contacts`, `check_contacts`, `get_contact`, `list_groups`, `get_group`, `create_group`, `update_group`, `leave_group`, `update_participants`, `get_group_invite`, `get_profile`, `update_profile`, `send_presence`, `send_chat_presence`.

Toggle at runtime: `PATCH /api/v1/mcp {"enabled":false}` or Admin → MCP Server → Enable/Disable (no restart). Path is not configurable — always `/mcp` (see `docs/architecture.md`).

## Examples

```bash
AUTH="Authorization: Bearer change-this-secret-key-in-production"

# Accounts
curl -X POST http://localhost:3000/api/v1/accounts -H "$AUTH" -H "Content-Type: application/json" \
  -d '{"phone_number":"+919876543210","account_name":"main"}'

# QR / Pair
curl http://localhost:3000/api/v1/accounts/{id}/session/qr -H "$AUTH" -o qr.png
curl -X POST http://localhost:3000/api/v1/accounts/{id}/session/pair -H "$AUTH" -d '{"phone":"919876543210"}'

# Send (phone) + media
curl -X POST "http://localhost:3000/api/v1/accounts/{id}/messages?phone=919876543210&text=Hello!" -H "$AUTH"
curl -X POST "http://localhost:3000/api/v1/accounts/{id}/messages?phone=919876543210" -H "$AUTH" -F "text=caption" -F "file=@photo.jpg"

# Read / React
curl -X POST http://localhost:3000/api/v1/accounts/{id}/messages/mark-read -H "$AUTH" -H "Content-Type: application/json" \
  -d '{"phone":"919876543210","message_ids":["ABCD"]}'
curl -X POST http://localhost:3000/api/v1/accounts/{id}/messages/reactions -H "$AUTH" -H "Content-Type: application/json" \
  -d '{"phone":"919876543210","message_id":"ABCD","emoji":"👍"}'

# Groups / Webhook / Auth
curl -X POST http://localhost:3000/api/v1/accounts/{id}/groups -H "$AUTH" -H "Content-Type: application/json" \
  -d '{"name":"Team","participants":["919876543210"]}'
curl -X PUT http://localhost:3000/api/v1/accounts/{id}/webhook -H "$AUTH" -H "Content-Type: application/json" \
  -d '{"url":"https://example.com/hook","secret":"s","events":["message","receipt"]}'
curl -X POST http://localhost:3000/api/v1/auth/login -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"secret"}' # → JWT
curl -X POST http://localhost:3000/api/v1/api-keys -H "$AUTH" -H "Content-Type: application/json" \
  -d '{"name":"bot","account_id":"{id}"}' # → notalk_…
```

## Error Responses

```json
{ "error": "description" }
```

| Status | Meaning |
|--------|---------|
| 400 | Bad request |
| 401 | Unauthorized |
| 403 | Forbidden (RBAC) |
| 404 | Not found |
| 409 | Conflict (duplicate / session linked) |
| 429 | Rate limited (`Retry-After`) |
| 500 | Internal |
| 504 | Gateway timeout (QR, WA op) |

## Configuration

`config/app.example.toml` → `config/app.toml` (or `~/.notalk/config.toml`). Env overrides `NOTALK_*` — see `CONFIGURATION.md` for exact names (`NOTALK_SERVER_PORT`, `NOTALK_ACCOUNTS_DIR` not `NOTALK_ACCOUNTS_BASE_DIRECTORY`, `NOTALK_MCP_ENABLED`, `NOTALK_CORS_ORIGINS`, `NOTALK_WEBHOOKS_*`, etc.).

| Section | Key | Default | Description |
|---------|-----|---------|-------------|
| `server` | `host` / `port` | `0.0.0.0` / `3000` | HTTP listen |
| `auth` | `secret_key` | — | Bearer + JWT signing (required) |
| `auth` | `registration_enabled` | `false` | Public `POST /auth/register` |
| `smtp` | `host`/`port`/`username`/`password`/`from`/`tls`/`starttls` | — / `587` / `true` | Password reset email |
| `logging` | `level` | `info` | `trace`/`debug`/`info`/`warn`/`error` |
| `database` | `dsn` | `postgres://localhost:5432/notalk?sslmode=disable` | PostgreSQL DSN |
| `cors` | `allow_origins` | `["*"]` | CORS |
| `limits` | `max_concurrent_requests` / `request_timeout_ms` / `max_upload_size` | `50` / `30000` / `10MiB` | Rate limit |
| `accounts` | `base_directory` | `~/.notalk/accounts` | Session cache dir |
| `accounts.defaults` | `idle_timeout` | `300` | Auto-disconnect (0 = never) |
| `webhooks` | `enabled` / `timeout_ms` / `retry_count` / `retry_delay_ms` | `false` / `5000` / `3` / `1000` | Webhook |
| `swagger` | `enabled` / `path` | `true` / `/api-docs` | Swagger UI |
| `mcp` | `enabled` | `true` | MCP at fixed `/mcp` (toggle via `PATCH /api/v1/mcp`) |
| `llm` | `enabled` / `provider` / `api_key` / `base_url` / `model` | `false` / `openai` / — | Copilot/Autopilot |

See `CONFIGURATION.md` for full TOML + env table. Docker compose maps these to `NOTALK_*`.

**Notes:** `secret_key` signs JWTs; `idle_timeout` polls every 30s; MCP `enabled` toggles without restart via DB `setting` + `PATCH /api/v1/mcp`.

## Architecture

See `docs/architecture.md` — Mermaid overview, project layout (`cmd/notalk/main.go`, `internal/{config,database,handler,mcpserver,middleware,service}`), request flows (HTTP + MCP `sequenceDiagram`), data layout (PostgreSQL `account`, `app_user`, `api_key`, `setting`, `plans` + `~/.notalk/accounts/{uuid}/`), and Noise/Signal/Protobuf protocol. Web dashboard lives in the separate `notalk-web` project.

## Development

```bash
go vet ./...
go test -race -count=1 -timeout 120s ./...         # unit + integration (needs NOTALK_TEST_DSN)
golangci-lint run --timeout 5m
docker compose config --quiet && docker compose build && docker compose up -d && curl -sf http://localhost:3000/api/health
```

CI (`.github/workflows/ci.yml`): `Test` (ubuntu/macos/windows `1.26` + `go test -race`), `Lint` (`golangci-lint`), `Docker` (build → `pg_isready` → `curl /api/health`).

## License

MIT
