# Changelog

## [0.2.1] — 2026-03-18

### Fixed
- Message bubble rendering extra `>` character in web messaging UI
- MCP `splitCSV` now handles JSON array input from VS Code MCP client (fixes group creation timeout)
- MCP SSE stream stability — removed HTTP read/write timeouts that killed long-lived connections
- MCP "Unexpected 200 response" warning — removed heartbeat that caused bare 200 responses

## [0.2.0] — 2026-03-17

### Added
- **MCP server** — 26 tools for AI agents via Streamable HTTP transport with stateful SSE sessions
  - Account management: `list_accounts`, `get_session`, `get_qr`, `pair_phone`, `logout`
  - Messaging: `send_message`, `send_media`, `get_messages`, `list_chats`, `react_message`, `mark_read`, `revoke_message`
  - Contacts: `list_contacts`, `check_contacts`, `get_contact`
  - Groups: `list_groups`, `get_group`, `create_group`, `update_group`, `leave_group`, `update_participants`, `get_group_invite`
  - Profile: `get_profile`, `update_profile`
  - Presence: `send_presence`, `send_chat_presence`
- **RBAC authentication** — users, roles, permissions, JWT login, API keys with account binding
- **Web dashboard** — full messaging UI with chats, contacts, groups, channels, bulk send, reactions
- **Admin panel** — user/role management, API key management, MCP settings
- **Billing** — plan-based metering (free/pro/business/enterprise) with Stripe integration
- **Newsletter support** — follow/unfollow channels, channel messages, mute/unmute
- **Password reset** — SMTP-based forgot-password flow
- **Pricing page** — public billing plans page
- **Docker support** — multi-stage Dockerfile and docker-compose with environment variable config
- **API keys** — create scoped API keys with expiry and account binding (`notalk_` prefix)
- **MCP runtime toggle** — enable/disable MCP at runtime via admin UI or API
- **Media type detection** — sends images, videos, audio, stickers with proper WhatsApp message types
- **Per-account rate limiting** — token bucket (30 msg/min) for send operations
- **Webhook retries** — configurable timeout, retry count, and exponential back-off with HMAC signing

### API Endpoints Added
- Auth: `POST /auth/login`, `/auth/register`, `/auth/forgot-password`, `/auth/reset-password`
- Users: full CRUD at `/api/v1/users`
- Roles: full CRUD at `/api/v1/roles`
- API Keys: `GET/POST/DELETE /api/v1/api-keys`
- Billing: `GET /api/v1/billing/plans`, `/billing`, `/billing/usage`
- MCP settings: `GET/PATCH /api/v1/mcp`
- Newsletters: `GET /newsletters`, `POST /newsletters/follow`, `POST /newsletters/unfollow`, `GET /newsletters/{jid}`, `GET /newsletters/{jid}/messages`, `POST /newsletters/{jid}/mute`

## [0.1.0] — 2026-02-27

Initial scaffolding.

### Added
- Multi-account management with UUID-based isolation
- Native WhatsApp multi-device protocol integration (browserless)
- QR code and phone number pairing for authentication
- Send text messages and file attachments (documents with captions)
- Account lifecycle: auto-connect on demand, idle timeout auto-disconnect
- SQLite-backed account registry
- Bearer token authentication middleware
- CORS middleware with configurable origins
- TOML configuration with sensible defaults
- RESTful API with 20 endpoints covering accounts, auth, messaging, contacts, groups
- Graceful shutdown with connection cleanup
