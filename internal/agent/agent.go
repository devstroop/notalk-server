package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/devstroop/notalk/internal/database"
	"github.com/devstroop/notalk/internal/service"
)

const (
	maxToolIterations = 10

	defaultSystemPrompt = `You are an AI assistant that helps users manage their WhatsApp accounts.
You have access to tools that let you read messages, list chats, send messages, and more.
Always be helpful, concise, and accurate. When the user asks you to do something with WhatsApp,
use the available tools. If you're unsure which account to use, ask the user to specify one.`
)

// SSEWriter writes Server-Sent Events to an HTTP response.
type SSEWriter struct {
	w io.Writer
	f http.Flusher
}

// NewSSEWriter wraps a ResponseWriter and sets SSE headers.
func NewSSEWriter(w http.ResponseWriter) *SSEWriter {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	f, _ := w.(http.Flusher)
	return &SSEWriter{w: w, f: f}
}

// Send writes a JSON-encoded SSE data event.
func (s *SSEWriter) Send(payload any) {
	b, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(s.w, "data: %s\n\n", b)
	if s.f != nil {
		s.f.Flush()
	}
}

// ── Agent ─────────────────────────────────────────────────────────────────────

// Agent orchestrates LLM calls and tool execution.
type Agent struct {
	llm      *Client
	registry *ToolRegistry
	db       *database.DB
}

// New creates an Agent, preferring the supplied cfgOverride for LLM credentials.
// If cfgOverride has no API key (or no provider), values are read from the DB.
func New(db *database.DB, mgr *service.AccountManager, userID string, isAdmin bool, cfgOverride LLMConfig) *Agent {
	provider := cfgOverride.Provider
	apiKey := cfgOverride.APIKey
	baseURL := cfgOverride.BaseURL
	model := cfgOverride.Model

	if apiKey == "" && provider == "" {
		// Fall back to DB-stored settings (legacy / web-UI managed).
		provider = db.GetSetting("ai.provider", "openai")
		apiKey = db.GetSetting("ai.api_key", "")
		baseURL = db.GetSetting("ai.base_url", "")
		model = db.GetSetting("ai.model", "gpt-4o-mini")
	} else if model == "" {
		model = db.GetSetting("ai.model", "gpt-4o-mini")
	}

	cfg := LLMConfig{
		Provider: provider,
		APIKey:   apiKey,
		BaseURL:  baseURL,
		Model:    model,
	}

	return &Agent{
		llm:      NewClient(cfg),
		registry: NewToolRegistry(mgr, userID, isAdmin),
		db:       db,
	}
}

// NewWithConfig creates an Agent with an explicit model override (used by autopilot).
func NewWithConfig(db *database.DB, mgr *service.AccountManager, userID string, isAdmin bool, modelOverride string, cfgOverride LLMConfig) *Agent {
	a := New(db, mgr, userID, isAdmin, cfgOverride)
	if modelOverride != "" {
		a.llm.cfg.Model = modelOverride
	}
	return a
}

// ── Mode 1: Personal Assistant (streaming SSE) ────────────────────────────────

// RunChat executes the agent loop for a single user turn and streams results via SSE.
// History is loaded from / saved to the database keyed by userID.
func (a *Agent) RunChat(ctx context.Context, userID, userMessage string, w http.ResponseWriter) {
	sse := NewSSEWriter(w)

	// Load conversation history.
	histJSON, err := a.db.GetAgentSession(userID)
	if err != nil {
		log.Warn().Err(err).Msg("agent: load session failed")
		histJSON = "[]"
	}

	var history []Message
	if err := json.Unmarshal([]byte(histJSON), &history); err != nil {
		history = nil
	}

	// Prepend system prompt if history is empty or has no system message.
	if len(history) == 0 || history[0].Role != RoleSystem {
		history = append([]Message{{Role: RoleSystem, Content: defaultSystemPrompt}}, history...)
	}

	// Append the new user turn.
	history = append(history, Message{Role: RoleUser, Content: userMessage})

	tools := a.registry.Tools()

	// ── Tool-calling loop ──────────────────────────────────────────────────────
	for i := 0; i < maxToolIterations; i++ {
		msg, err := a.llm.Chat(ctx, history, tools)
		if err != nil {
			sse.Send(map[string]any{"type": "error", "message": err.Error()})
			return
		}

		// No tool calls → this is the final answer; stream it.
		if len(msg.ToolCalls) == 0 {
			// Emit via streaming for typing effect.
			a.streamFinal(ctx, history, msg, sse)
			// Persist history (drop system message to save space).
			a.saveHistory(userID, history, msg)
			return
		}

		// Add the assistant message (which contains tool call requests).
		history = append(history, *msg)

		// Execute each requested tool.
		for _, tc := range msg.ToolCalls {
			sse.Send(map[string]any{"type": "tool", "name": tc.Function.Name, "status": "calling"})

			result, execErr := a.registry.Execute(ctx, tc.Function.Name, tc.Function.Arguments)
			if execErr != nil {
				result = fmt.Sprintf(`{"error":%q}`, execErr.Error())
			}

			sse.Send(map[string]any{"type": "tool", "name": tc.Function.Name, "status": "done"})

			history = append(history, Message{
				Role:       RoleTool,
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    result,
			})
		}
	}

	sse.Send(map[string]any{"type": "error", "message": "max tool iterations reached"})
}

// streamFinal streams the final assistant text response.
// It tries real LLM streaming; falls back to sending the already-received text.
func (a *Agent) streamFinal(ctx context.Context, history []Message, msg *Message, sse *SSEWriter) {
	// If the model already returned a final text in the non-streaming call, use it.
	if msg.Content != "" {
		// Simulate streaming by sending in small chunks for a smooth UX.
		// Split on newlines first to preserve them, then chunk words within each line.
		lines := strings.Split(msg.Content, "\n")
		wordCount := 0
		var buf strings.Builder
		for li, line := range lines {
			if li > 0 {
				buf.WriteString("\n")
			}
			words := strings.Fields(line)
			for wi, w := range words {
				if wi > 0 {
					buf.WriteString(" ")
				}
				buf.WriteString(w)
				wordCount++
				if wordCount%8 == 0 {
					sse.Send(map[string]any{"type": "text", "content": buf.String()})
					buf.Reset()
					time.Sleep(20 * time.Millisecond)
				}
			}
		}
		if buf.Len() > 0 {
			sse.Send(map[string]any{"type": "text", "content": buf.String()})
		}
		sse.Send(map[string]any{"type": "done"})
		return
	}

	// Append the (empty) assistant message then do a streaming call.
	history = append(history, *msg)
	ch, err := a.llm.ChatStream(ctx, history)
	if err != nil {
		sse.Send(map[string]any{"type": "error", "message": err.Error()})
		return
	}
	for chunk := range ch {
		if chunk.Err != nil {
			sse.Send(map[string]any{"type": "error", "message": chunk.Err.Error()})
			return
		}
		if chunk.Done {
			break
		}
		sse.Send(map[string]any{"type": "text", "content": chunk.Content})
	}
	sse.Send(map[string]any{"type": "done"})
}

func (a *Agent) saveHistory(userID string, history []Message, finalAssistant *Message) {
	// Build persisted history: keep only user and assistant (with content) messages.
	// Strip system, tool, and assistant tool-call messages to avoid orphaned tool results on reload.
	var persist []Message
	for _, m := range history {
		switch m.Role {
		case RoleSystem, RoleTool:
			continue
		case RoleAssistant:
			if m.Content == "" {
				continue // tool-call-only assistant message
			}
		}
		// Strip tool_calls from persisted assistant messages.
		clean := Message{Role: m.Role, Content: m.Content}
		persist = append(persist, clean)
	}
	if finalAssistant != nil && finalAssistant.Content != "" {
		persist = append(persist, Message{Role: RoleAssistant, Content: finalAssistant.Content})
	}

	// Keep only last 40 messages to limit storage.
	if len(persist) > 40 {
		persist = persist[len(persist)-40:]
	}

	b, _ := json.Marshal(persist)
	if err := a.db.SaveAgentSession(userID, string(b)); err != nil {
		log.Warn().Err(err).Str("user", userID).Msg("agent: save session failed")
	}
}

// ── Mode 2: Auto-Reply (one-shot, no streaming) ───────────────────────────────

// AutoReply generates a reply to an incoming WhatsApp message for an account's autopilot.
// It returns the reply text. The caller is responsible for sending it and logging.
func AutoReply(ctx context.Context, db *database.DB, mgr *service.AccountManager,
	accountID, chatJID, senderJID, incomingBody string, cfgOverride LLMConfig) (string, string, error) {

	// Skip group chats — autopilot only replies to 1:1 conversations.
	if strings.Contains(chatJID, "@g.us") || strings.Contains(chatJID, "@newsletter") {
		return "", "", nil
	}

	cfg, err := db.GetAgentConfig(accountID)
	if err != nil || !cfg.Enabled {
		return "", "", nil // autopilot not configured / disabled
	}

	// Extract the bare phone number from the sender JID (e.g. "919876543210@s.whatsapp.net" → "919876543210").
	senderPhone := senderJID
	if idx := strings.Index(senderPhone, "@"); idx != -1 {
		senderPhone = senderPhone[:idx]
	}

	// Whitelist check — if non-empty, only listed numbers get replies.
	if cfg.Whitelist != "" {
		allowed := false
		for _, line := range strings.Split(cfg.Whitelist, "\n") {
			num := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "+"))
			if num != "" && (num == senderPhone || "+"+num == senderJID[:strings.Index(senderJID+"@", "@")]) {
				allowed = true
				break
			}
		}
		if !allowed {
			log.Debug().Str("account", accountID).Str("sender", senderJID).Msg("agent: sender not in whitelist, skipping")
			return "", "", nil
		}
	}

	// Blacklist check — always skip listed numbers.
	if cfg.Blacklist != "" {
		for _, line := range strings.Split(cfg.Blacklist, "\n") {
			num := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "+"))
			if num != "" && num == senderPhone {
				log.Debug().Str("account", accountID).Str("sender", senderJID).Msg("agent: sender in blacklist, skipping")
				return "", "", nil
			}
		}
	}

	provider := cfgOverride.Provider
	apiKey := cfgOverride.APIKey
	baseURL := cfgOverride.BaseURL
	model := cfgOverride.Model

	if apiKey == "" && provider == "" {
		// Fall back to DB-stored settings.
		provider = db.GetSetting("ai.provider", "openai")
		apiKey = db.GetSetting("ai.api_key", "")
		baseURL = db.GetSetting("ai.base_url", "")
		model = db.GetSetting("ai.model", "gpt-4o-mini")
	} else if model == "" {
		model = db.GetSetting("ai.model", "gpt-4o-mini")
	}
	// Per-account model override takes highest priority.
	if cfg.Model != "" {
		model = cfg.Model
	}

	llm := NewClient(LLMConfig{
		Provider: provider,
		APIKey:   apiKey,
		BaseURL:  baseURL,
		Model:    model,
	})

	// AI escalation detection — if enabled, classify intent before generating a reply.
	if cfg.EscalationEnabled {
		classifyMsgs := []Message{
			{Role: RoleSystem, Content: "You are a classifier. Reply with only 'yes' if the user's message expresses a desire to speak with a human agent or customer support representative, otherwise reply 'no'. Do not explain."},
			{Role: RoleUser, Content: incomingBody},
		}
		decision, classifyErr := llm.Chat(ctx, classifyMsgs, nil)
		if classifyErr == nil && strings.HasPrefix(strings.ToLower(strings.TrimSpace(decision.Content)), "yes") {
			log.Info().Str("account", accountID).Str("chat", chatJID).Msg("agent: escalation intent detected, sending handoff message")
			msg := cfg.EscalationMessage
			if msg == "" {
				msg = "Thank you for reaching out! One of our team members will get back to you shortly. 🙏"
			}
			return msg, model, nil
		}
	}

	systemPrompt := cfg.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = "You are a helpful WhatsApp assistant. Reply concisely and naturally."
	}

	messages := []Message{
		{Role: RoleSystem, Content: systemPrompt},
		{Role: RoleUser, Content: incomingBody},
	}

	// Auto-reply uses no tools — just generates a text response.
	reply, err := llm.Chat(ctx, messages, nil)
	if err != nil {
		return "", model, fmt.Errorf("auto-reply llm error: %w", err)
	}

	return reply.Content, model, nil
}
