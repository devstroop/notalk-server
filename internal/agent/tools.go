package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/devstroop/notalk/internal/service"
)

// jsonParam builds an OpenAI-compatible JSON Schema parameter object.
func jsonParam(required []string, props map[string]any) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

// ToolRegistry exposes WhatsApp service capabilities to the LLM.
type ToolRegistry struct {
	mgr     *service.AccountManager
	userID  string
	isAdmin bool
}

// NewToolRegistry creates a registry scoped to a specific user.
func NewToolRegistry(mgr *service.AccountManager, userID string, isAdmin bool) *ToolRegistry {
	return &ToolRegistry{mgr: mgr, userID: userID, isAdmin: isAdmin}
}

// Tools returns the full list of function definitions sent to the LLM.
func (tr *ToolRegistry) Tools() []Tool {
	return []Tool{
		{
			Type: "function",
			Function: ToolSpec{
				Name:        "list_accounts",
				Description: "List all your WhatsApp accounts with their connection status.",
				Parameters:  jsonParam(nil, nil),
			},
		},
		{
			Type: "function",
			Function: ToolSpec{
				Name:        "list_chats",
				Description: "List recent chats (conversations) for a WhatsApp account.",
				Parameters: jsonParam(
					[]string{"account_id"},
					map[string]any{"account_id": strProp("The WhatsApp account ID")},
				),
			},
		},
		{
			Type: "function",
			Function: ToolSpec{
				Name:        "get_messages",
				Description: "Get recent messages from a specific chat. Returns message history.",
				Parameters: jsonParam(
					[]string{"account_id", "chat_jid"},
					map[string]any{
						"account_id": strProp("The WhatsApp account ID"),
						"chat_jid":   strProp("The chat JID (e.g. 1234567890@s.whatsapp.net or group@g.us)"),
						"limit":      intProp("Max messages to return (default 30, max 100)"),
					},
				),
			},
		},
		{
			Type: "function",
			Function: ToolSpec{
				Name:        "send_message",
				Description: "Send a text message to a WhatsApp chat or phone number.",
				Parameters: jsonParam(
					[]string{"account_id", "to", "text"},
					map[string]any{
						"account_id": strProp("The WhatsApp account ID to send from"),
						"to":         strProp("Recipient: phone number (e.g. +15551234567) or JID"),
						"text":       strProp("The message text to send"),
					},
				),
			},
		},
		{
			Type: "function",
			Function: ToolSpec{
				Name:        "list_contacts",
				Description: "List contacts saved in a WhatsApp account.",
				Parameters: jsonParam(
					[]string{"account_id"},
					map[string]any{"account_id": strProp("The WhatsApp account ID")},
				),
			},
		},
		{
			Type: "function",
			Function: ToolSpec{
				Name:        "list_groups",
				Description: "List WhatsApp groups for an account.",
				Parameters: jsonParam(
					[]string{"account_id"},
					map[string]any{"account_id": strProp("The WhatsApp account ID")},
				),
			},
		},
		{
			Type: "function",
			Function: ToolSpec{
				Name:        "react_to_message",
				Description: "React to a message with an emoji.",
				Parameters: jsonParam(
					[]string{"account_id", "chat_jid", "message_id", "emoji"},
					map[string]any{
						"account_id": strProp("The WhatsApp account ID"),
						"chat_jid":   strProp("The chat JID"),
						"message_id": strProp("The message ID to react to"),
						"emoji":      strProp("The emoji reaction (e.g. 👍 ❤️ 😂)"),
					},
				),
			},
		},
		{
			Type: "function",
			Function: ToolSpec{
				Name:        "mark_read",
				Description: "Mark messages in a chat as read.",
				Parameters: jsonParam(
					[]string{"account_id", "chat_jid"},
					map[string]any{
						"account_id": strProp("The WhatsApp account ID"),
						"chat_jid":   strProp("The chat JID to mark as read"),
					},
				),
			},
		},
		{
			Type: "function",
			Function: ToolSpec{
				Name:        "check_contacts",
				Description: "Check if phone numbers are registered on WhatsApp.",
				Parameters: jsonParam(
					[]string{"account_id", "phones"},
					map[string]any{
						"account_id": strProp("The WhatsApp account ID"),
						"phones":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "List of phone numbers to check (e.g. [\"+15551234567\"])"},
					},
				),
			},
		},
	}
}

// Execute dispatches a tool call by name with JSON-encoded arguments.
func (tr *ToolRegistry) Execute(ctx context.Context, name, args string) (string, error) {
	var input map[string]any
	if args != "" && args != "{}" {
		if err := json.Unmarshal([]byte(args), &input); err != nil {
			return "", fmt.Errorf("invalid tool args: %w", err)
		}
	}
	if input == nil {
		input = make(map[string]any)
	}

	switch name {
	case "list_accounts":
		return tr.execListAccounts(ctx)
	case "list_chats":
		return tr.execListChats(ctx, input)
	case "get_messages":
		return tr.execGetMessages(ctx, input)
	case "send_message":
		return tr.execSendMessage(ctx, input)
	case "list_contacts":
		return tr.execListContacts(ctx, input)
	case "list_groups":
		return tr.execListGroups(ctx, input)
	case "react_to_message":
		return tr.execReactToMessage(ctx, input)
	case "mark_read":
		return tr.execMarkRead(ctx, input)
	case "check_contacts":
		return tr.execCheckContacts(ctx, input)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

// ── Tool implementations ──────────────────────────────────────────────────────

func (tr *ToolRegistry) execListAccounts(_ context.Context) (string, error) {
	list := tr.mgr.ListAccounts()
	type row struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Phone       string `json:"phone"`
		Connected   bool   `json:"connected"`
	}
	var rows []row
	for _, a := range list.Accounts {
		if !tr.isAdmin && a.UserID != tr.userID {
			continue
		}
		phone := ""
		if a.PhoneNumber != nil {
			phone = *a.PhoneNumber
		}
		acct := tr.mgr.GetAccount(a.ID)
		rows = append(rows, row{
			ID:        a.ID,
			Name:      a.AccountName,
			Phone:     phone,
			Connected: acct != nil && acct.IsLoggedIn(),
		})
	}
	return toJSON(rows)
}

func (tr *ToolRegistry) getAccount(accountID string) (*service.Account, error) {
	acct := tr.mgr.GetAccount(accountID)
	if acct == nil {
		return nil, fmt.Errorf("account not found: %s", accountID)
	}
	// Non-admins may only use their own accounts.
	if !tr.isAdmin {
		info := acct.Info()
		if info.UserID != tr.userID {
			return nil, fmt.Errorf("access denied")
		}
	}
	// Auto-connect sleeping accounts (core NoTalk behaviour).
	if err := acct.EnsureConnected(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to connect account %s: %w", accountID, err)
	}
	if !acct.IsLoggedIn() {
		return nil, fmt.Errorf("account %s is not connected to WhatsApp", accountID)
	}
	return acct, nil
}

func (tr *ToolRegistry) execListChats(ctx context.Context, input map[string]any) (string, error) {
	accountID, _ := input["account_id"].(string)
	acct, err := tr.getAccount(accountID)
	if err != nil {
		return "", err
	}
	result, err := acct.ListChats(ctx)
	if err != nil {
		return "", err
	}
	return toJSON(result)
}

func (tr *ToolRegistry) execGetMessages(ctx context.Context, input map[string]any) (string, error) {
	accountID, _ := input["account_id"].(string)
	chatJID, _ := input["chat_jid"].(string)
	limit := 30
	if l, ok := input["limit"].(float64); ok && l > 0 {
		limit = int(l)
		if limit > 100 {
			limit = 100
		}
	}

	acct, err := tr.getAccount(accountID)
	if err != nil {
		return "", err
	}
	_ = ctx
	result, err := acct.ListMessages(chatJID, limit, "")
	if err != nil {
		return "", err
	}
	return toJSON(result)
}

func (tr *ToolRegistry) execSendMessage(ctx context.Context, input map[string]any) (string, error) {
	accountID, _ := input["account_id"].(string)
	to, _ := input["to"].(string)
	text, _ := input["text"].(string)

	if to == "" || text == "" {
		return "", fmt.Errorf("to and text are required")
	}
	// Convert phone number to JID if needed.
	if strings.HasPrefix(to, "+") || isDigits(to) {
		to = strings.TrimPrefix(to, "+") + "@s.whatsapp.net"
	}

	acct, err := tr.getAccount(accountID)
	if err != nil {
		return "", err
	}
	result, err := acct.SendMessage(ctx, to, text)
	if err != nil {
		return "", err
	}
	return toJSON(result)
}

func (tr *ToolRegistry) execListContacts(ctx context.Context, input map[string]any) (string, error) {
	accountID, _ := input["account_id"].(string)
	acct, err := tr.getAccount(accountID)
	if err != nil {
		return "", err
	}
	result, err := acct.ListContacts(ctx)
	if err != nil {
		return "", err
	}
	return toJSON(result)
}

func (tr *ToolRegistry) execListGroups(ctx context.Context, input map[string]any) (string, error) {
	accountID, _ := input["account_id"].(string)
	acct, err := tr.getAccount(accountID)
	if err != nil {
		return "", err
	}
	result, err := acct.ListGroups(ctx)
	if err != nil {
		return "", err
	}
	return toJSON(result)
}

func (tr *ToolRegistry) execReactToMessage(ctx context.Context, input map[string]any) (string, error) {
	accountID, _ := input["account_id"].(string)
	chatJID, _ := input["chat_jid"].(string)
	messageID, _ := input["message_id"].(string)
	emoji, _ := input["emoji"].(string)

	acct, err := tr.getAccount(accountID)
	if err != nil {
		return "", err
	}
	err = acct.SendReaction(ctx, chatJID, messageID, emoji)
	if err != nil {
		return "", err
	}
	return `{"status":"ok"}`, nil
}

func (tr *ToolRegistry) execMarkRead(ctx context.Context, input map[string]any) (string, error) {
	accountID, _ := input["account_id"].(string)
	chatJID, _ := input["chat_jid"].(string)

	acct, err := tr.getAccount(accountID)
	if err != nil {
		return "", err
	}
	// Mark with empty message IDs — marks whole chat as read.
	err = acct.MarkRead(ctx, chatJID, nil)
	if err != nil {
		return "", err
	}
	return `{"status":"ok"}`, nil
}

func (tr *ToolRegistry) execCheckContacts(ctx context.Context, input map[string]any) (string, error) {
	accountID, _ := input["account_id"].(string)
	acct, err := tr.getAccount(accountID)
	if err != nil {
		return "", err
	}

	var phones []string
	if raw, ok := input["phones"].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok {
				phones = append(phones, s)
			}
		}
	}
	if len(phones) == 0 {
		return "", fmt.Errorf("phones array is required")
	}

	result, err := acct.CheckContacts(ctx, phones)
	if err != nil {
		return "", err
	}
	return toJSON(result)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func toJSON(v any) (string, error) {
	if v == nil {
		return "null", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}
