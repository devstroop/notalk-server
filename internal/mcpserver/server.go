package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	qrcode "github.com/skip2/go-qrcode"

	"github.com/devstroop/notalk/internal/database"
	"github.com/devstroop/notalk/internal/middleware"
	"github.com/devstroop/notalk/internal/model"
	"github.com/devstroop/notalk/internal/service"
)

// New creates an MCP server backed by the given AccountManager.
// It exposes notalk's core capabilities as MCP tools.
//
// Auth + account scoping is handled by middleware before requests reach here.
// Tool handlers read the caller's Identity and optional scoped account_id from context.
//
// Admin callers (Permission "*") may omit account_id to manage any account.
// Standard users are pre-scoped to a single account_id (set by MCPScope middleware).
func New(mgr *service.AccountManager, db *database.DB, version string) *server.MCPServer {
	s := server.NewMCPServer(
		"NoTalk",
		version,
		server.WithToolCapabilities(false),
		server.WithRecovery(),
	)

	registerTools(s, mgr, db)
	return s
}

// helper to marshal any value to JSON text for MCP tool results.
func jsonResult(v any) (*mcp.CallToolResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal result: %w", err)
	}
	return mcp.NewToolResultText(string(data)), nil
}

// requirePermission checks that the caller has the required RBAC permission.
// Returns a tool-level error result if the check fails, nil otherwise.
func requirePermission(ctx context.Context, perm string) *mcp.CallToolResult {
	identity := middleware.GetIdentityFromContext(ctx)
	if identity == nil {
		return mcp.NewToolResultError("unauthorized: no identity in context")
	}
	if !identity.HasPermission(perm) {
		return mcp.NewToolResultError(fmt.Sprintf("forbidden: requires %q permission", perm))
	}
	return nil
}

// resolveAccount determines the target account from context scope or tool param.
//
// Resolution order:
//  1. Context scoped account_id (set by MCPScope middleware for non-admin users).
//  2. "account_id" tool parameter (admin mode, or explicit override).
//  3. Error if neither is available.
//
// For non-admin callers with a context scope, the tool parameter is ignored
// (they are locked to their scoped account).
func resolveAccount(ctx context.Context, mgr *service.AccountManager, db *database.DB, req mcp.CallToolRequest) (*service.Account, *mcp.CallToolResult) {
	identity := middleware.GetIdentityFromContext(ctx)
	scopedID := middleware.GetScopedAccountID(ctx)
	isAdmin := identity != nil && identity.HasPermission("*")

	var accountID string

	if scopedID != "" {
		// User-scoped (or admin who passed ?account_id=)
		accountID = scopedID
	} else if isAdmin {
		// Admin without scope — require from tool param
		id, err := req.RequireString("account_id")
		if err != nil {
			return nil, mcp.NewToolResultError("account_id is required (admin multi-account mode)")
		}
		accountID = id
	} else {
		// Non-admin without scope — should not happen (MCPScope rejects this)
		return nil, mcp.NewToolResultError("account_id scope is required")
	}

	acct := mgr.GetAccount(accountID)
	if acct == nil {
		return nil, mcp.NewToolResultError(fmt.Sprintf("account %q not found", accountID))
	}

	// Ownership check for non-admin callers
	if !isAdmin && identity != nil {
		rec, err := db.GetAccount(accountID)
		if err != nil || rec == nil || rec.UserID != identity.UserID {
			return nil, mcp.NewToolResultError("forbidden: you do not own this account")
		}
	}

	return acct, nil
}

// resolveConnectedAccount resolves + ensures connected.
func resolveConnectedAccount(ctx context.Context, mgr *service.AccountManager, db *database.DB, req mcp.CallToolRequest) (*service.Account, *mcp.CallToolResult) {
	acct, errResult := resolveAccount(ctx, mgr, db, req)
	if errResult != nil {
		return nil, errResult
	}
	if !acct.HasStoredCredentials() {
		return nil, mcp.NewToolResultError("account is not linked to WhatsApp — scan QR or pair first")
	}
	if err := acct.EnsureConnected(ctx); err != nil {
		return nil, mcp.NewToolResultError(fmt.Sprintf("connect: %v", err))
	}
	return acct, nil
}

func registerTools(s *server.MCPServer, mgr *service.AccountManager, db *database.DB) {
	// ── Account management ──────────────────────────

	s.AddTool(
		mcp.NewTool("list_accounts",
			mcp.WithDescription("List WhatsApp accounts. Admins see all accounts; standard users see only their own. If scoped to one account, returns that account's info."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "accounts:read"); deny != nil {
				return deny, nil
			}
			identity := middleware.GetIdentityFromContext(ctx)
			scopedID := middleware.GetScopedAccountID(ctx)
			isAdmin := identity != nil && identity.HasPermission("*")

			if scopedID != "" {
				// Scoped mode — return just the scoped account
				acct := mgr.GetAccount(scopedID)
				if acct == nil {
					return mcp.NewToolResultError("scoped account not found"), nil
				}
				resp := model.AccountListResponse{
					Accounts: []model.AccountInfo{acct.Info()},
					Total:    1,
				}
				return jsonResult(resp)
			}

			resp := mgr.ListAccounts()

			// Non-admin without scope: filter to own accounts only
			if !isAdmin && identity != nil {
				filtered := make([]model.AccountInfo, 0)
				for _, a := range resp.Accounts {
					if a.UserID == identity.UserID {
						filtered = append(filtered, a)
					}
				}
				resp.Accounts = filtered
				resp.Total = len(filtered)
			}

			return jsonResult(resp)
		},
	)

	s.AddTool(
		mcp.NewTool("get_session",
			mcp.WithDescription("Get session/authentication status for a WhatsApp account"),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "session:read"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}
			return jsonResult(acct.StatusResponse())
		},
	)

	s.AddTool(
		mcp.NewTool("get_qr",
			mcp.WithDescription("Get a QR code string for linking a WhatsApp account. The returned code can be rendered as a QR code for scanning with WhatsApp mobile. Only works if the account is not already logged in."),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "session:write"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}

			ch, err := acct.GetQR(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			select {
			case item := <-ch:
				service.DrainQR(ch)
				if item.Error != nil {
					return mcp.NewToolResultError(item.Error.Error()), nil
				}
				if item.Event == "code" {
					png, err := qrcode.Encode(item.Code, qrcode.Medium, 512)
					if err != nil {
						return mcp.NewToolResultError(fmt.Sprintf("failed to render QR: %v", err)), nil
					}
					b64png := base64.StdEncoding.EncodeToString(png)
					return mcp.NewToolResultImage("Scan this QR code with WhatsApp to link the account.", b64png, "image/png"), nil
				}
				return mcp.NewToolResultError(fmt.Sprintf("unexpected qr event: %s", item.Event)), nil
			case <-ctx.Done():
				service.DrainQR(ch)
				return mcp.NewToolResultError("timeout waiting for QR code"), nil
			}
		},
	)

	s.AddTool(
		mcp.NewTool("pair_phone",
			mcp.WithDescription("Pair a WhatsApp account using a phone number. Returns a linking code to enter on WhatsApp mobile. The account must be connected but not yet logged in (call get_qr first to initialize the connection)."),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
			mcp.WithString("phone", mcp.Required(), mcp.Description("Phone number to pair (international format, digits only, e.g. 919999999999)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "session:write"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}

			phone, err := req.RequireString("phone")
			if err != nil {
				return mcp.NewToolResultError("phone is required"), nil
			}

			code, err := acct.PairPhone(ctx, phone)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(map[string]string{"linking_code": code})
		},
	)

	s.AddTool(
		mcp.NewTool("logout",
			mcp.WithDescription("Disconnect and log out a WhatsApp account, clearing session credentials"),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "session:write"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}

			if err := acct.Logout(); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(map[string]string{"status": "logged_out", "account_id": acct.ID})
		},
	)

	// ── Messaging ───────────────────────────────────

	s.AddTool(
		mcp.NewTool("send_message",
			mcp.WithDescription("Send a text message to a WhatsApp chat or phone number"),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
			mcp.WithString("text", mcp.Required(), mcp.Description("Message text")),
			mcp.WithString("phone", mcp.Description("Phone number (international, digits only). Provide phone or jid, not both.")),
			mcp.WithString("jid", mcp.Description("WhatsApp JID (e.g. 919999999999@s.whatsapp.net). Provide phone or jid, not both.")),
			mcp.WithString("reply_to", mcp.Description("Message ID to reply to (optional)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "messages:write"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveConnectedAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}

			text, err := req.RequireString("text")
			if err != nil {
				return mcp.NewToolResultError("text is required"), nil
			}

			phone := req.GetString("phone", "")
			jid := req.GetString("jid", "")
			replyTo := req.GetString("reply_to", "")

			if phone == "" && jid == "" {
				return mcp.NewToolResultError("phone or jid is required"), nil
			}
			if phone != "" && jid != "" {
				return mcp.NewToolResultError("provide phone or jid, not both"), nil
			}

			chatJID := jid
			if phone != "" {
				resolved, err := acct.ResolvePhone(ctx, phone)
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				chatJID = resolved
			}

			var msgID string
			if replyTo != "" {
				msgID, err = acct.SendReply(ctx, chatJID, replyTo, text)
			} else {
				msgID, err = acct.SendMessage(ctx, chatJID, text)
			}
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(map[string]string{"status": "sent", "message_id": msgID})
		},
	)

	// ── Contacts ────────────────────────────────────

	s.AddTool(
		mcp.NewTool("check_contacts",
			mcp.WithDescription("Check if phone numbers are registered on WhatsApp"),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
			mcp.WithString("phones", mcp.Required(), mcp.Description("Comma-separated phone numbers (international, digits only)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "contacts:write"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveConnectedAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}

			phonesStr, err := req.RequireString("phones")
			if err != nil {
				return mcp.NewToolResultError("phones is required"), nil
			}

			results, err := acct.CheckContacts(ctx, splitCSV(phonesStr))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(results)
		},
	)

	s.AddTool(
		mcp.NewTool("get_contact",
			mcp.WithDescription("Get contact details (name, picture) by JID"),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
			mcp.WithString("jid", mcp.Required(), mcp.Description("Contact JID (e.g. 919999999999@s.whatsapp.net)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "contacts:read"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveConnectedAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}

			jid, err := req.RequireString("jid")
			if err != nil {
				return mcp.NewToolResultError("jid is required"), nil
			}

			info, err := acct.GetContactInfo(ctx, jid)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(info)
		},
	)

	// ── Groups ──────────────────────────────────────

	s.AddTool(
		mcp.NewTool("list_groups",
			mcp.WithDescription("List all WhatsApp groups the account has joined"),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "groups:read"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveConnectedAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}

			groups, err := acct.ListGroups(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(map[string]any{"groups": groups, "total": len(groups)})
		},
	)

	s.AddTool(
		mcp.NewTool("get_group",
			mcp.WithDescription("Get detailed info about a WhatsApp group"),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
			mcp.WithString("jid", mcp.Required(), mcp.Description("Group JID (e.g. 120363012345@g.us)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "groups:read"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveConnectedAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}

			jid, err := req.RequireString("jid")
			if err != nil {
				return mcp.NewToolResultError("jid is required"), nil
			}

			info, err := acct.GetGroupInfo(ctx, jid)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(info)
		},
	)

	// ── Profile ─────────────────────────────────────

	s.AddTool(
		mcp.NewTool("get_profile",
			mcp.WithDescription("Get the WhatsApp profile of an account (name, about, picture)"),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "profile:read"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveConnectedAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}

			profile, err := acct.GetProfile(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(profile)
		},
	)

	s.AddTool(
		mcp.NewTool("update_profile",
			mcp.WithDescription("Update the account's WhatsApp status/about text"),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
			mcp.WithString("about", mcp.Required(), mcp.Description("New status/about text")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "profile:write"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveConnectedAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}

			about, err := req.RequireString("about")
			if err != nil {
				return mcp.NewToolResultError("about is required"), nil
			}

			if err := acct.SetStatusMessage(ctx, about); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(map[string]string{"status": "updated"})
		},
	)

	// ── Chats ───────────────────────────────────────

	s.AddTool(
		mcp.NewTool("list_chats",
			mcp.WithDescription("List recent WhatsApp conversations with last message and unread count"),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "chats:read"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveConnectedAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}

			chats, err := acct.ListChats(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(map[string]any{"chats": chats, "total": len(chats)})
		},
	)

	// ── Messages ────────────────────────────────────

	s.AddTool(
		mcp.NewTool("get_messages",
			mcp.WithDescription("Get paginated message history for a WhatsApp chat"),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
			mcp.WithString("chat_jid", mcp.Required(), mcp.Description("Chat JID (e.g. 919999999999@s.whatsapp.net or 120363012345@g.us)")),
			mcp.WithString("limit", mcp.Description("Max messages to return (default 50, max 200)")),
			mcp.WithString("before", mcp.Description("Cursor: message ID to paginate before")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "messages:read"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}

			chatJID, err := req.RequireString("chat_jid")
			if err != nil {
				return mcp.NewToolResultError("chat_jid is required"), nil
			}

			limit := 50
			if ls := req.GetString("limit", ""); ls != "" {
				if v, err := strconv.Atoi(ls); err == nil && v > 0 {
					limit = v
				}
			}
			if limit > 200 {
				limit = 200
			}

			before := req.GetString("before", "")

			msgs, err := acct.ListMessages(chatJID, limit, before)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(msgs)
		},
	)

	s.AddTool(
		mcp.NewTool("react_message",
			mcp.WithDescription("Send an emoji reaction to a WhatsApp message"),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
			mcp.WithString("chat_jid", mcp.Required(), mcp.Description("Chat JID where the message is")),
			mcp.WithString("message_id", mcp.Required(), mcp.Description("ID of the message to react to")),
			mcp.WithString("emoji", mcp.Required(), mcp.Description("Emoji to react with (e.g. 👍). Send empty string to remove reaction.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "messages:write"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveConnectedAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}

			chatJID, err := req.RequireString("chat_jid")
			if err != nil {
				return mcp.NewToolResultError("chat_jid is required"), nil
			}
			messageID, err := req.RequireString("message_id")
			if err != nil {
				return mcp.NewToolResultError("message_id is required"), nil
			}
			emoji, err := req.RequireString("emoji")
			if err != nil {
				return mcp.NewToolResultError("emoji is required"), nil
			}

			if err := acct.SendReaction(ctx, chatJID, messageID, emoji); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(map[string]string{"status": "reacted"})
		},
	)

	s.AddTool(
		mcp.NewTool("mark_read",
			mcp.WithDescription("Mark messages as read in a WhatsApp chat"),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
			mcp.WithString("chat_jid", mcp.Required(), mcp.Description("Chat JID")),
			mcp.WithString("message_ids", mcp.Required(), mcp.Description("Comma-separated message IDs to mark as read")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "messages:write"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveConnectedAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}

			chatJID, err := req.RequireString("chat_jid")
			if err != nil {
				return mcp.NewToolResultError("chat_jid is required"), nil
			}
			idsStr, err := req.RequireString("message_ids")
			if err != nil {
				return mcp.NewToolResultError("message_ids is required"), nil
			}

			if err := acct.MarkRead(ctx, chatJID, splitCSV(idsStr)); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(map[string]string{"status": "read"})
		},
	)

	s.AddTool(
		mcp.NewTool("revoke_message",
			mcp.WithDescription("Revoke (delete for everyone) a previously sent WhatsApp message"),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
			mcp.WithString("chat_jid", mcp.Required(), mcp.Description("Chat JID where the message was sent")),
			mcp.WithString("message_id", mcp.Required(), mcp.Description("ID of the message to revoke")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "messages:write"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveConnectedAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}

			chatJID, err := req.RequireString("chat_jid")
			if err != nil {
				return mcp.NewToolResultError("chat_jid is required"), nil
			}
			messageID, err := req.RequireString("message_id")
			if err != nil {
				return mcp.NewToolResultError("message_id is required"), nil
			}

			result, err := acct.RevokeMessage(ctx, chatJID, messageID)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(result)
		},
	)

	// ── Media ───────────────────────────────────────

	s.AddTool(
		mcp.NewTool("send_media",
			mcp.WithDescription("Send a media file (image, video, audio, document) to a WhatsApp chat. Provide either media_base64 with the file data, or both are required along with filename and mimetype."),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
			mcp.WithString("jid", mcp.Required(), mcp.Description("Recipient JID (e.g. 919999999999@s.whatsapp.net)")),
			mcp.WithString("media_base64", mcp.Required(), mcp.Description("Base64-encoded file data")),
			mcp.WithString("filename", mcp.Required(), mcp.Description("Original filename (e.g. photo.jpg)")),
			mcp.WithString("mimetype", mcp.Required(), mcp.Description("MIME type (e.g. image/jpeg, video/mp4, audio/ogg, application/pdf)")),
			mcp.WithString("caption", mcp.Description("Optional caption for the media")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "messages:write"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveConnectedAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}

			jid, err := req.RequireString("jid")
			if err != nil {
				return mcp.NewToolResultError("jid is required"), nil
			}
			b64, err := req.RequireString("media_base64")
			if err != nil {
				return mcp.NewToolResultError("media_base64 is required"), nil
			}
			filename, err := req.RequireString("filename")
			if err != nil {
				return mcp.NewToolResultError("filename is required"), nil
			}
			mimetype, err := req.RequireString("mimetype")
			if err != nil {
				return mcp.NewToolResultError("mimetype is required"), nil
			}

			data, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid base64: %v", err)), nil
			}

			var caption *string
			if c := req.GetString("caption", ""); c != "" {
				caption = &c
			}

			msgID, err := acct.SendMedia(ctx, jid, data, filename, mimetype, caption)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(map[string]string{"status": "sent", "message_id": msgID})
		},
	)

	// ── Presence ────────────────────────────────────

	s.AddTool(
		mcp.NewTool("send_presence",
			mcp.WithDescription("Set global online/offline presence status"),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
			mcp.WithString("state", mcp.Required(), mcp.Description("Presence state: 'available' or 'unavailable'")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "presence:write"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveConnectedAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}

			state, err := req.RequireString("state")
			if err != nil {
				return mcp.NewToolResultError("state is required"), nil
			}

			if err := acct.SendPresence(ctx, state); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(map[string]string{"status": "ok"})
		},
	)

	s.AddTool(
		mcp.NewTool("send_chat_presence",
			mcp.WithDescription("Send typing/paused indicator in a specific WhatsApp chat"),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
			mcp.WithString("jid", mcp.Required(), mcp.Description("Chat JID")),
			mcp.WithString("state", mcp.Required(), mcp.Description("Chat presence state: 'composing' or 'paused'")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "presence:write"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveConnectedAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}

			jid, err := req.RequireString("jid")
			if err != nil {
				return mcp.NewToolResultError("jid is required"), nil
			}
			state, err := req.RequireString("state")
			if err != nil {
				return mcp.NewToolResultError("state is required"), nil
			}

			if err := acct.SendChatPresence(ctx, jid, state); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(map[string]string{"status": "ok"})
		},
	)

	// ── Contacts (full list) ────────────────────────

	s.AddTool(
		mcp.NewTool("list_contacts",
			mcp.WithDescription("List all WhatsApp contacts known to the account"),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "contacts:read"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveConnectedAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}

			contacts, err := acct.ListContacts(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(map[string]any{"contacts": contacts, "total": len(contacts)})
		},
	)

	// ── Group management ────────────────────────────

	s.AddTool(
		mcp.NewTool("create_group",
			mcp.WithDescription("Create a new WhatsApp group"),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Group name")),
			mcp.WithString("participants", mcp.Required(), mcp.Description("Comma-separated participant JIDs")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "groups:write"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveConnectedAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}

			name, err := req.RequireString("name")
			if err != nil {
				return mcp.NewToolResultError("name is required"), nil
			}
			participantsStr, err := req.RequireString("participants")
			if err != nil {
				return mcp.NewToolResultError("participants is required"), nil
			}

			parts := splitCSV(participantsStr)
			group, err := acct.CreateGroup(ctx, name, parts)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(group)
		},
	)

	s.AddTool(
		mcp.NewTool("update_group",
			mcp.WithDescription("Update WhatsApp group settings (name, description, locked, announce)"),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
			mcp.WithString("jid", mcp.Required(), mcp.Description("Group JID")),
			mcp.WithString("name", mcp.Description("New group name")),
			mcp.WithString("description", mcp.Description("New group description")),
			mcp.WithString("locked", mcp.Description("'true' or 'false' — restrict group info editing to admins")),
			mcp.WithString("announce", mcp.Description("'true' or 'false' — restrict messaging to admins only")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "groups:write"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveConnectedAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}

			jid, err := req.RequireString("jid")
			if err != nil {
				return mcp.NewToolResultError("jid is required"), nil
			}

			var updateReq model.UpdateGroupRequest
			if n := req.GetString("name", ""); n != "" {
				updateReq.Name = &n
			}
			if d := req.GetString("description", ""); d != "" {
				updateReq.Description = &d
			}
			if l := req.GetString("locked", ""); l != "" {
				v := l == "true"
				updateReq.Locked = &v
			}
			if a := req.GetString("announce", ""); a != "" {
				v := a == "true"
				updateReq.Announce = &v
			}

			if err := acct.UpdateGroup(ctx, jid, updateReq); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(map[string]string{"status": "updated"})
		},
	)

	s.AddTool(
		mcp.NewTool("leave_group",
			mcp.WithDescription("Leave a WhatsApp group"),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
			mcp.WithString("jid", mcp.Required(), mcp.Description("Group JID")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "groups:write"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveConnectedAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}

			jid, err := req.RequireString("jid")
			if err != nil {
				return mcp.NewToolResultError("jid is required"), nil
			}

			if err := acct.LeaveGroup(ctx, jid); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(map[string]string{"status": "left"})
		},
	)

	s.AddTool(
		mcp.NewTool("update_participants",
			mcp.WithDescription("Add, remove, promote, or demote participants in a WhatsApp group"),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
			mcp.WithString("jid", mcp.Required(), mcp.Description("Group JID")),
			mcp.WithString("participants", mcp.Required(), mcp.Description("Comma-separated participant JIDs")),
			mcp.WithString("action", mcp.Required(), mcp.Description("Action: 'add', 'remove', 'promote', or 'demote'")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "groups:write"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveConnectedAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}

			jid, err := req.RequireString("jid")
			if err != nil {
				return mcp.NewToolResultError("jid is required"), nil
			}
			participantsStr, err := req.RequireString("participants")
			if err != nil {
				return mcp.NewToolResultError("participants is required"), nil
			}
			action, err := req.RequireString("action")
			if err != nil {
				return mcp.NewToolResultError("action is required"), nil
			}

			if err := acct.UpdateGroupParticipants(ctx, jid, splitCSV(participantsStr), action); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(map[string]string{"status": "ok"})
		},
	)

	s.AddTool(
		mcp.NewTool("get_group_invite",
			mcp.WithDescription("Get the invite link for a WhatsApp group"),
			mcp.WithString("account_id", mcp.Description("Account ID (optional when scoped to a single account)")),
			mcp.WithString("jid", mcp.Required(), mcp.Description("Group JID")),
			mcp.WithString("reset", mcp.Description("Set to 'true' to revoke the current link and generate a new one")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if deny := requirePermission(ctx, "groups:read"); deny != nil {
				return deny, nil
			}
			acct, errResult := resolveConnectedAccount(ctx, mgr, db, req)
			if errResult != nil {
				return errResult, nil
			}

			jid, err := req.RequireString("jid")
			if err != nil {
				return mcp.NewToolResultError("jid is required"), nil
			}
			reset := req.GetString("reset", "") == "true"

			link, err := acct.GetGroupInviteLink(ctx, jid, reset)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(map[string]string{"invite_link": link})
		},
	)
}

// splitCSV splits a comma-separated string into trimmed, non-empty parts.
// It also handles JSON array input (e.g. ["a","b"]) which some MCP clients send.
func splitCSV(s string) []string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[") {
		var arr []string
		if json.Unmarshal([]byte(s), &arr) == nil {
			return arr
		}
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
