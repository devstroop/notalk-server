# NoTalk Server

WhatsApp HTTP API + MCP server over the native multi-device protocol. Single Go binary, no browser, no Selenium.

- **Multi-account** — dozens of numbers, isolated sessions, `phone` → JID resolve
- **MCP** — 26 tools (`send_message`, `list_chats`, `create_group`, …) via Streamable HTTP at `/mcp`
- **Auth + RBAC** — `secret` → `JWT` → `api_key` (`notalk_…`), `resource:action` (`*` for admin)
- **Resilient** — auto-connect, idle disconnect, crash recovery, rate limit, webhooks

> Go 1.26 · PostgreSQL 18 · whatsmeow · Swagger at `/api-docs`

## Requirements

- Go 1.26
- PostgreSQL 16+
- `NOTALK_AUTH_SECRET_KEY` (production)

## Quick Start

```bash
cp config/app.example.toml config/app.toml  # set secret_key
go run ./cmd/notalk                         # :5000
# or
go build -o /tmp/notalk ./cmd/notalk && NOTALK_DATABASE_DSN=postgres://notalk:notalk@localhost:5432/notalk?sslmode=disable /tmp/notalk
```

```bash
docker compose up -d          # → http://localhost:5000
curl -sf http://localhost:5000/api/health
```

| URL | Description |
|-----|-------------|
| `/api-docs` | Swagger UI |
| `/mcp` | MCP (Streamable HTTP, stateful SSE) |
| `/api/health` | Health (no auth) |

Three calls:

```bash
AUTH="Authorization: Bearer <secret_key>"
curl -X POST http://localhost:5000/api/v1/accounts -H "$AUTH" -d '{"phone_number":"+919876543210","account_name":"main"}' # → {id}
curl http://localhost:5000/api/v1/accounts/{id}/session/qr -H "$AUTH" -o qr.png
curl -X POST "http://localhost:5000/api/v1/accounts/{id}/messages?phone=919876543210&text=Hi" -H "$AUTH"
```

## Authentication

All `/api/v1/*` require `Authorization: Bearer <token>`.

| Method | Token | Scope |
|--------|-------|-------|
| Secret | `Bearer <secret_key>` (`NOTALK_AUTH_SECRET_KEY`) | `*` |
| JWT | `Bearer eyJ…` (`POST /api/v1/auth/login`) | `role.permissions` |
| API key | `Bearer notalk_…` (`POST /api/v1/api-keys`) | `user + account_id` |

Public: `POST /api/v1/auth/login`, `/register` (if enabled), `/forgot-password`, `/reset-password`, `GET /api/health`.

## REST API

Base `http://localhost:5000/api/v1/accounts/{id}`. `phone` auto-resolves to JID.

| Area | Endpoints |
|------|-----------|
| Accounts | `GET/POST /api/v1/accounts`, `GET/PATCH/DELETE /api/v1/accounts/{id}` |
| Session | `GET /session`, `GET /session/qr`, `POST /session/pair`, `DELETE /session` |
| Messages | `POST /messages?phone=&text=`, `GET /messages?chat=`, `POST /messages/reactions`, `POST /messages/mark-read`, `DELETE /messages/{id}` |
| Chats/Contacts | `GET /chats`, `GET /contacts`, `POST /contacts/check` |
| Groups | `GET/POST /groups`, `GET/PATCH/DELETE /groups/{jid}`, `.../participants`, `.../invite` |
| Newsletters | `GET /newsletters`, `POST /newsletters/follow` |
| Presence/Profile | `POST /presence`, `GET/PATCH /profile` |
| Proxy/Webhook | `GET/PUT/DELETE /proxy`, `GET/PUT/DELETE /webhook` |
| Admin | `GET/POST /api/v1/users|roles|api-keys`, `GET/PATCH /api/v1/mcp` |
| Billing | `GET /api/v1/billing/plans` (public) |

Full docs: `http://localhost:5000/api-docs`.

## MCP

`POST http://localhost:5000/mcp` — 26 tools, `10m` idle TTL.

```json
{
  "servers": {
    "notalk": {
      "type": "http",
      "url": "http://localhost:5000/mcp",
      "headers": { "Authorization": "Bearer <secret_key>" }
    }
  }
}
```

## Configuration

`config/app.example.toml` → `config/app.toml` or `~/.notalk/config.toml`. Env `NOTALK_*` overrides.

| Section | Key | Default |
|---------|-----|---------|
| `server` | `host` / `port` | `0.0.0.0` / `5000` |
| `auth` | `secret_key` | — |
| `database` | `dsn` | `postgres://localhost:5432/notalk?sslmode=disable` |
| `logging` | `level` | `info` |

See `CONFIGURATION.md`.

## Architecture

See `docs/architecture.md` — request flows, data layout (`account`, `app_user`, `api_key`, `setting`), Noise/Signal/Protobuf.

Web UI is `notalk-web` (`http://localhost:3000`).

## Development

```bash
go vet ./...
go test -race -count=1 -timeout 120s ./...
docker compose config --quiet && docker compose up -d && curl -sf http://localhost:5000/api/health
```

## License

MIT
