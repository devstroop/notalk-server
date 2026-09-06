package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/devstroop/notalk/internal/config"
	"github.com/devstroop/notalk/internal/database"
	"github.com/devstroop/notalk/internal/model"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"google.golang.org/protobuf/proto"
)

// Account wraps a single WhatsApp client with lifecycle management.
type Account struct {
	mu sync.RWMutex

	ID          string
	PhoneNumber string
	AccountName string
	DataDir     string
	UserID      string
	SessionDSN  string // PostgreSQL DSN for whatsmeow session store
	CreatedAt   time.Time

	Proxy        *ProxyConfig // nil = direct, set via PUT /accounts/{id}/proxy
	WebhookCfg   config.WebhookConfig // global webhook defaults (timeout, retries)

	// OnIncomingMessage is called (in a goroutine) for every non-self text message received.
	// Set by the AccountManager after registration.
	OnIncomingMessage func(chatJID, senderJID, body string)

	rejected     bool // true after phone-number mismatch; blocks autoReconnect
	db           *database.DB
	client       *whatsmeow.Client
	container    *sqlstore.Container

	// sendLimiter is a per-account token bucket for message sending.
	sendLimiter *tokenBucket
}

// tokenBucket implements a simple token bucket rate limiter.
type tokenBucket struct {
	mu       sync.Mutex
	tokens   float64
	max      float64
	rate     float64 // tokens per second
	lastTime time.Time
}

func newTokenBucket(perMinute float64) *tokenBucket {
	rate := perMinute / 60.0
	return &tokenBucket{
		tokens:   perMinute, // start full
		max:      perMinute,
		rate:     rate,
		lastTime: time.Now(),
	}
}

// allow returns true if a token is available, consuming one.
func (tb *tokenBucket) allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastTime).Seconds()
	tb.tokens += elapsed * tb.rate
	if tb.tokens > tb.max {
		tb.tokens = tb.max
	}
	tb.lastTime = now

	if tb.tokens < 1 {
		return false
	}
	tb.tokens--
	return true
}

// NewAccount constructs an Account (not yet connected).
func NewAccount(id, phone, name, dataDir, userID, sessionDSN string, createdAt time.Time, db *database.DB) *Account {
	return &Account{
		ID:           id,
		PhoneNumber:  phone,
		AccountName:  name,
		DataDir:      dataDir,
		UserID:       userID,
		SessionDSN:   sessionDSN,
		CreatedAt:    createdAt,
		db:           db,
		sendLimiter:  newTokenBucket(30), // 30 messages per minute
	}
}

// Connect initialises the WhatsApp client and connects to WhatsApp servers.
func (a *Account) Connect(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Reset rejection flag so the user can try pairing again.
	a.rejected = false

	if a.client != nil && a.client.IsConnected() {
		return nil
	}

	if err := a.prepareClient(ctx); err != nil {
		return err
	}

	if err := a.client.Connect(); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	log.Info().Str("account", a.ID).Msg("connected to WhatsApp")
	return nil
}

// prepareClient creates the WhatsApp client and store without connecting.
// Must be called with a.mu held.
func (a *Account) prepareClient(ctx context.Context) error {
	logger := waLog.Noop
	container, err := sqlstore.New(ctx, "postgres", a.SessionDSN, logger)
	if err != nil {
		return fmt.Errorf("open session store: %w", err)
	}
	a.container = container

	// Get or create device
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return fmt.Errorf("get device: %w", err)
	}

	// Ensure push name is set before connecting so the handshake includes it
	// and SendPresence won't fail after the Connected event.
	if device.PushName == "" {
		device.PushName = "NoTalk"
	}

	client := whatsmeow.NewClient(device, logger)
	// Build HTTP transport — force IPv4, optionally route through proxy
	ipv4Dialer := &net.Dialer{}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return ipv4Dialer.DialContext(ctx, "tcp4", addr)
		},
		ForceAttemptHTTP2: true,
	}
	if a.Proxy != nil && a.Proxy.Enabled {
		proxyURL := a.Proxy.URL()
		transport.Proxy = http.ProxyURL(proxyURL)
		log.Info().Str("account", a.ID).Str("proxy", proxyURL.Host).Msg("using proxy")
	}
	client.SetWebsocketHTTPClient(&http.Client{Transport: transport})
	a.client = client

	// Event handler
	client.AddEventHandler(func(evt interface{}) {
		a.handleEvent(evt)
	})

	return nil
}

// Disconnect gracefully closes the WhatsApp connection.
func (a *Account) Disconnect() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.client != nil {
		a.client.Disconnect()
		a.client = nil
	}
	log.Info().Str("account", a.ID).Msg("disconnected")
}

// EnsureConnected auto-connects if sleeping and waits until ready.
func (a *Account) EnsureConnected(ctx context.Context) error {
	a.mu.RLock()
	connected := a.client != nil && a.client.IsConnected()
	a.mu.RUnlock()

	if connected {
		return nil
	}

	return a.Connect(ctx)
}

// requireConnectedClient returns the WhatsApp client, or an error if not connected.
// Replaces the repeated lock-check-unlock pattern across all methods.
func (a *Account) requireConnectedClient() (*whatsmeow.Client, error) {
	a.mu.RLock()
	c := a.client
	a.mu.RUnlock()
	if c == nil || !c.IsConnected() {
		return nil, fmt.Errorf("client not connected")
	}
	return c, nil
}

// IsLoggedIn returns true if the client has a valid session.
func (a *Account) IsLoggedIn() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.client != nil && a.client.IsLoggedIn()
}

// GetQR sets up the client for QR linking. It disconnects any existing session,
// prepares a fresh client, obtains a QR channel, then connects.
// GetQRChannel must be called before Connect.
//
// The returned channel emits QR code events. The caller MUST drain the channel
// after reading the desired code (call DrainQR) so the client doesn't disconnect
// due to a full channel buffer.
func (a *Account) GetQR(ctx context.Context) (<-chan whatsmeow.QRChannelItem, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Tear down any existing connection so we can start fresh
	if a.client != nil {
		a.client.Disconnect()
		a.client = nil
	}

	if err := a.prepareClient(ctx); err != nil {
		return nil, err
	}

	if a.client.Store.ID != nil {
		return nil, fmt.Errorf("already logged in")
	}

	// Use a long-lived background context so the QR channel + connection
	// survive after the HTTP handler returns the QR PNG to the client.
	qrCtx, qrCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	ch, err := a.client.GetQRChannel(qrCtx)
	if err != nil {
		qrCancel()
		return nil, fmt.Errorf("get qr channel: %w", err)
	}

	if err := a.client.Connect(); err != nil {
		qrCancel()
		return nil, fmt.Errorf("connect: %w", err)
	}

	// Cancel the context after the QR channel is drained (in DrainQR).
	// Wrap the channel so cancel is called when done.
	wrapped := make(chan whatsmeow.QRChannelItem, 1)
	go func() {
		defer qrCancel()
		for item := range ch {
			wrapped <- item
		}
		close(wrapped)
	}()

	return wrapped, nil
}

// DrainQR consumes remaining QR channel events in the background so
// the client doesn't disconnect due to a full channel buffer.
func DrainQR(ch <-chan whatsmeow.QRChannelItem) {
	go func() {
		for range ch {
		}
	}()
}

// PairPhone requests phone-number pairing and returns the linking code.
func (a *Account) PairPhone(ctx context.Context, phone string) (string, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.client == nil {
		return "", fmt.Errorf("client not connected")
	}
	if a.client.Store.ID != nil {
		return "", fmt.Errorf("already logged in")
	}

	code, err := a.client.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		return "", fmt.Errorf("pair phone: %w", err)
	}
	return code, nil
}

// Logout logs out and clears the device store.
// If the client is connected, it sends a logout to WhatsApp servers first.
// If the client is nil (sleeping/already disconnected), it clears only local session data.
func (a *Account) Logout() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.client != nil {
		if a.client.IsConnected() {
			// Connected: tell WhatsApp servers + clear local store
			err := a.client.Logout(context.Background())
			a.client.Disconnect()
			a.client = nil
			if err != nil {
				return fmt.Errorf("logout: %w", err)
			}
			return nil
		}
		// Client exists but not connected — clean up the object
		a.client.Disconnect()
		a.client = nil
	}

	// Not connected: just wipe local session data so
	// hasStoredSession() stops returning true.
	logger := waLog.Noop
	container, err := sqlstore.New(context.Background(), "postgres", a.SessionDSN, logger)
	if err != nil {
		return fmt.Errorf("open store for cleanup: %w", err)
	}
	device, err := container.GetFirstDevice(context.Background())
	if err != nil {
		return fmt.Errorf("get device for cleanup: %w", err)
	}
	if device.ID != nil {
		if err := device.Delete(context.Background()); err != nil {
			return fmt.Errorf("delete stored device: %w", err)
		}
	}
	log.Info().Str("account", a.ID).Msg("cleared local session data")
	return nil
}

// ResolvePhone checks whether a phone number is registered on WhatsApp and
// returns the canonical JID string (e.g. "919999999999@s.whatsapp.net").
// The phone can be in any format — it will be normalised to digits first.
func (a *Account) ResolvePhone(ctx context.Context, phone string) (string, error) {
	client, err := a.requireConnectedClient()
	if err != nil {
		return "", err
	}

	normalized := NormalizePhone(phone)
	if len(normalized) < 7 || len(normalized) > 15 {
		return "", fmt.Errorf("invalid phone number %q", phone)
	}

	resp, err := client.IsOnWhatsApp(ctx, []string{"+" + normalized})
	if err != nil {
		return "", fmt.Errorf("phone lookup: %w", err)
	}
	for _, r := range resp {
		if r.IsIn {
			return r.JID.String(), nil
		}
	}
	return "", fmt.Errorf("phone %s is not registered on WhatsApp", normalized)
}

// SendMessage sends a text message to the given JID.
func (a *Account) SendMessage(ctx context.Context, jid string, text string) (string, error) {
	if !a.sendLimiter.allow() {
		return "", fmt.Errorf("rate limit exceeded: too many messages sent, try again shortly")
	}

	client, err := a.requireConnectedClient()
	if err != nil {
		return "", err
	}

	target, err := types.ParseJID(jid)
	if err != nil {
		return "", fmt.Errorf("invalid jid %q: %w", jid, err)
	}

	msg := &waE2E.Message{
		Conversation: proto.String(text),
	}

	resp, err := client.SendMessage(ctx, target, msg)
	if err != nil {
		return "", fmt.Errorf("send: %w", err)
	}

	a.storeOutgoing(resp.ID, jid, "text", text, "")
	return resp.ID, nil
}

// SendMedia sends a file with optional caption, using the appropriate WhatsApp
// message type based on MIME type (image, video, audio, sticker, or document).
func (a *Account) SendMedia(ctx context.Context, jid string, data []byte, filename, mimetype string, caption *string) (string, error) {
	if !a.sendLimiter.allow() {
		return "", fmt.Errorf("rate limit exceeded: too many messages sent, try again shortly")
	}

	client, err := a.requireConnectedClient()
	if err != nil {
		return "", err
	}

	target, err := types.ParseJID(jid)
	if err != nil {
		return "", fmt.Errorf("invalid jid %q: %w", jid, err)
	}

	mediaType, msgType := classifyMediaMIME(mimetype)

	uploaded, err := client.Upload(ctx, data, mediaType)
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}

	fileLen := proto.Uint64(uint64(len(data)))
	var msg *waE2E.Message

	switch msgType {
	case "image":
		msg = &waE2E.Message{
			ImageMessage: &waE2E.ImageMessage{
				URL:           &uploaded.URL,
				Mimetype:      &mimetype,
				Caption:       caption,
				DirectPath:    &uploaded.DirectPath,
				MediaKey:      uploaded.MediaKey,
				FileEncSHA256: uploaded.FileEncSHA256,
				FileSHA256:    uploaded.FileSHA256,
				FileLength:    fileLen,
			},
		}
	case "video":
		msg = &waE2E.Message{
			VideoMessage: &waE2E.VideoMessage{
				URL:           &uploaded.URL,
				Mimetype:      &mimetype,
				Caption:       caption,
				DirectPath:    &uploaded.DirectPath,
				MediaKey:      uploaded.MediaKey,
				FileEncSHA256: uploaded.FileEncSHA256,
				FileSHA256:    uploaded.FileSHA256,
				FileLength:    fileLen,
			},
		}
	case "audio":
		msg = &waE2E.Message{
			AudioMessage: &waE2E.AudioMessage{
				URL:           &uploaded.URL,
				Mimetype:      &mimetype,
				DirectPath:    &uploaded.DirectPath,
				MediaKey:      uploaded.MediaKey,
				FileEncSHA256: uploaded.FileEncSHA256,
				FileSHA256:    uploaded.FileSHA256,
				FileLength:    fileLen,
			},
		}
	case "sticker":
		msg = &waE2E.Message{
			StickerMessage: &waE2E.StickerMessage{
				URL:           &uploaded.URL,
				Mimetype:      &mimetype,
				DirectPath:    &uploaded.DirectPath,
				MediaKey:      uploaded.MediaKey,
				FileEncSHA256: uploaded.FileEncSHA256,
				FileSHA256:    uploaded.FileSHA256,
				FileLength:    fileLen,
			},
		}
	default: // document
		msg = &waE2E.Message{
			DocumentMessage: &waE2E.DocumentMessage{
				URL:           &uploaded.URL,
				Mimetype:      &mimetype,
				FileName:      &filename,
				DirectPath:    &uploaded.DirectPath,
				MediaKey:      uploaded.MediaKey,
				FileEncSHA256: uploaded.FileEncSHA256,
				FileSHA256:    uploaded.FileSHA256,
				FileLength:    fileLen,
				Caption:       caption,
			},
		}
	}

	resp, err := client.SendMessage(ctx, target, msg)
	if err != nil {
		return "", fmt.Errorf("send media: %w", err)
	}

	cap := ""
	if caption != nil {
		cap = *caption
	}
	a.storeOutgoing(resp.ID, jid, msgType, cap, mimetype)
	return resp.ID, nil
}

// Info builds the API-facing AccountInfo.
func (a *Account) Info() model.AccountInfo {
	a.mu.RLock()
	defer a.mu.RUnlock()

	info := model.AccountInfo{
		ID:          a.ID,
		AccountName: a.AccountName,
		UserID:      a.UserID,
		Authorized:  a.hasStoredSession(),
		CreatedAt:   a.CreatedAt,
	}
	if a.PhoneNumber != "" {
		info.PhoneNumber = &a.PhoneNumber
	}
	return info
}

// StatusResponse builds a WhatsAppStatusResponse.
func (a *Account) StatusResponse() model.WhatsAppStatusResponse {
	a.mu.RLock()
	defer a.mu.RUnlock()

	resp := model.WhatsAppStatusResponse{
		AccountID:  a.ID,
		Authorized: a.hasStoredSession(),
	}
	if a.PhoneNumber != "" {
		resp.PhoneNumber = &a.PhoneNumber
	}
	return resp
}

// IsAuthorized reports whether the account has a valid WhatsApp session.
func (a *Account) IsAuthorized() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.hasStoredSession()
}

// HasStoredCredentials reports whether the on-disk session store contains
// device credentials. This does NOT verify they are still valid on the server.
// Used to decide whether a connect-and-verify is worthwhile.
func (a *Account) HasStoredCredentials() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()

	// If we have a live client, it knows
	if a.client != nil {
		return a.client.Store.ID != nil
	}
	// Probe the shared PostgreSQL session store
	logger := waLog.Noop
	container, err := sqlstore.New(context.Background(), "postgres", a.SessionDSN, logger)
	if err != nil {
		return false
	}
	device, err := container.GetFirstDevice(context.Background())
	if err != nil {
		return false
	}
	return device.ID != nil
}

// hasStoredSession checks whether there is a valid session from a live client.
// When the client is nil (sleeping), we cannot verify the session against the
// WhatsApp server, so we conservatively report false. Accounts with stored
// credentials are verified on startup via DiscoverAccounts, which connects
// them and lets the client detect revoked sessions via the LoggedOut event.
// Must be called with a.mu held.
func (a *Account) hasStoredSession() bool {
	if a.client != nil {
		return a.client.IsLoggedIn()
	}
	return false
}

// Reset clears all session data from the shared PostgreSQL store for this account.
func (a *Account) Reset() error {
	a.Disconnect()

	a.mu.Lock()
	defer a.mu.Unlock()

	// Delete whatsmeow device records from the shared DB
	if a.container != nil {
		ctx := context.Background()
		if devs, err := a.container.GetAllDevices(ctx); err == nil {
			for _, d := range devs {
				_ = a.container.DeleteDevice(ctx, d)
			}
		}
		return nil
	}

	// If no container cached, open a temporary one to clean up
	logger := waLog.Noop
	container, err := sqlstore.New(context.Background(), "postgres", a.SessionDSN, logger)
	if err != nil {
		return fmt.Errorf("open store for reset: %w", err)
	}
	ctx := context.Background()
	devs, err := container.GetAllDevices(ctx)
	if err != nil {
		return fmt.Errorf("list devices for reset: %w", err)
	}
	for _, d := range devs {
		_ = container.DeleteDevice(ctx, d)
	}
	return nil
}

// SendChatPresence sends a typing or paused indicator in a chat.
func (a *Account) SendChatPresence(ctx context.Context, jid string, state string) error {
	client, err := a.requireConnectedClient()
	if err != nil {
		return err
	}

	target, err := types.ParseJID(jid)
	if err != nil {
		return fmt.Errorf("invalid jid %q: %w", jid, err)
	}

	var presence types.ChatPresence
	switch state {
	case "composing":
		presence = types.ChatPresenceComposing
	case "paused":
		presence = types.ChatPresencePaused
	default:
		return fmt.Errorf("invalid state %q: must be composing or paused", state)
	}

	if err := client.SendChatPresence(ctx, target, presence, types.ChatPresenceMediaText); err != nil {
		return fmt.Errorf("send presence: %w", err)
	}

	return nil
}

// MarkRead marks messages as read in a chat.
func (a *Account) MarkRead(ctx context.Context, chatJID string, messageIDs []string) error {
	client, err := a.requireConnectedClient()
	if err != nil {
		return err
	}

	target, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid jid %q: %w", chatJID, err)
	}

	if err := client.MarkRead(ctx, messageIDs, time.Now(), target, types.JID{}); err != nil {
		return fmt.Errorf("mark read: %w", err)
	}

	return nil
}

// SendReaction sends an emoji reaction on a message.
func (a *Account) SendReaction(ctx context.Context, chatJID, messageID, emoji string) error {
	client, err := a.requireConnectedClient()
	if err != nil {
		return err
	}

	target, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid jid %q: %w", chatJID, err)
	}

	// Use the client's BuildReaction helper — it handles MessageKey construction.
	msg := client.BuildReaction(target, types.EmptyJID, types.MessageID(messageID), emoji)

	if _, err := client.SendMessage(ctx, target, msg); err != nil {
		return fmt.Errorf("send reaction: %w", err)
	}

	return nil
}

// SendReply sends a text message quoting another message.
func (a *Account) SendReply(ctx context.Context, chatJID, messageID, text string) (string, error) {
	if !a.sendLimiter.allow() {
		return "", fmt.Errorf("rate limit exceeded: too many messages sent, try again shortly")
	}

	client, err := a.requireConnectedClient()
	if err != nil {
		return "", err
	}

	target, err := types.ParseJID(chatJID)
	if err != nil {
		return "", fmt.Errorf("invalid jid %q: %w", chatJID, err)
	}

	msg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(text),
			ContextInfo: &waE2E.ContextInfo{
				StanzaID:      proto.String(messageID),
				QuotedMessage: &waE2E.Message{},
			},
		},
	}

	resp, err := client.SendMessage(ctx, target, msg)
	if err != nil {
		return "", fmt.Errorf("send reply: %w", err)
	}

	a.storeOutgoing(resp.ID, chatJID, "text", text, "")
	return resp.ID, nil
}

// GetContactInfo returns contact details from the local store.
func (a *Account) GetContactInfo(ctx context.Context, contactJID string) (model.ContactInfo, error) {
	client, err := a.requireConnectedClient()
	if err != nil {
		return model.ContactInfo{}, err
	}

	jid, err := types.ParseJID(contactJID)
	if err != nil {
		return model.ContactInfo{}, fmt.Errorf("invalid jid %q: %w", contactJID, err)
	}

	info, err := client.Store.Contacts.GetContact(ctx, jid)
	if err != nil {
		return model.ContactInfo{}, fmt.Errorf("get contact: %w", err)
	}

	result := model.ContactInfo{
		ID:           contactJID,
		PushName:     info.PushName,
		FullName:     info.FullName,
		FirstName:    info.FirstName,
		BusinessName: info.BusinessName,
	}
	if jid.Server == types.DefaultUserServer {
		phone := jid.User
		result.Phone = &phone
	}

	// Profile picture (best-effort)
	pic, err := client.GetProfilePictureInfo(ctx, jid, &whatsmeow.GetProfilePictureParams{Preview: false})
	if err == nil && pic != nil {
		result.PictureURL = &pic.URL
	}

	return result, nil
}

// GetGroupInfo fetches group details from WhatsApp servers.
func (a *Account) GetGroupInfo(ctx context.Context, groupJID string) (model.GroupInfo, error) {
	client, err := a.requireConnectedClient()
	if err != nil {
		return model.GroupInfo{}, err
	}

	jid, err := types.ParseJID(groupJID)
	if err != nil {
		return model.GroupInfo{}, fmt.Errorf("invalid jid %q: %w", groupJID, err)
	}

	gi, err := client.GetGroupInfo(ctx, jid)
	if err != nil {
		return model.GroupInfo{}, fmt.Errorf("get group info: %w", err)
	}

	return groupInfoToModel(gi), nil
}

// ListGroups returns all joined groups.
func (a *Account) ListGroups(ctx context.Context) ([]model.GroupInfo, error) {
	client, err := a.requireConnectedClient()
	if err != nil {
		return nil, err
	}

	groups, err := client.GetJoinedGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("get joined groups: %w", err)
	}

	result := make([]model.GroupInfo, len(groups))
	for i, gi := range groups {
		result[i] = groupInfoToModel(gi)
	}
	return result, nil
}

// CreateGroup creates a new WhatsApp group.
func (a *Account) CreateGroup(ctx context.Context, name string, participants []string) (model.GroupInfo, error) {
	client, err := a.requireConnectedClient()
	if err != nil {
		return model.GroupInfo{}, err
	}

	jids := make([]types.JID, len(participants))
	for i, p := range participants {
		j, err := types.ParseJID(p)
		if err != nil {
			return model.GroupInfo{}, fmt.Errorf("invalid participant jid %q: %w", p, err)
		}
		jids[i] = j
	}

	gi, err := client.CreateGroup(ctx, whatsmeow.ReqCreateGroup{
		Name:         name,
		Participants: jids,
	})
	if err != nil {
		return model.GroupInfo{}, fmt.Errorf("create group: %w", err)
	}

	return groupInfoToModel(gi), nil
}

// LeaveGroup leaves a WhatsApp group.
func (a *Account) LeaveGroup(ctx context.Context, groupJID string) error {
	client, err := a.requireConnectedClient()
	if err != nil {
		return err
	}

	jid, err := types.ParseJID(groupJID)
	if err != nil {
		return fmt.Errorf("invalid jid %q: %w", groupJID, err)
	}

	return client.LeaveGroup(ctx, jid)
}

// UpdateGroup updates group settings (name, description, locked, announce).
func (a *Account) UpdateGroup(ctx context.Context, groupJID string, req model.UpdateGroupRequest) error {
	client, err := a.requireConnectedClient()
	if err != nil {
		return err
	}

	jid, err := types.ParseJID(groupJID)
	if err != nil {
		return fmt.Errorf("invalid jid %q: %w", groupJID, err)
	}

	if req.Name != nil {
		if err := client.SetGroupName(ctx, jid, *req.Name); err != nil {
			return fmt.Errorf("set group name: %w", err)
		}
	}
	if req.Description != nil {
		if err := client.SetGroupDescription(ctx, jid, *req.Description); err != nil {
			return fmt.Errorf("set group description: %w", err)
		}
	}
	if req.Locked != nil {
		if err := client.SetGroupLocked(ctx, jid, *req.Locked); err != nil {
			return fmt.Errorf("set group locked: %w", err)
		}
	}
	if req.Announce != nil {
		if err := client.SetGroupAnnounce(ctx, jid, *req.Announce); err != nil {
			return fmt.Errorf("set group announce: %w", err)
		}
	}
	return nil
}

// UpdateGroupParticipants adds/removes/promotes/demotes group members.
func (a *Account) UpdateGroupParticipants(ctx context.Context, groupJID string, participants []string, action string) error {
	client, err := a.requireConnectedClient()
	if err != nil {
		return err
	}

	jid, err := types.ParseJID(groupJID)
	if err != nil {
		return fmt.Errorf("invalid jid %q: %w", groupJID, err)
	}

	jids := make([]types.JID, len(participants))
	for i, p := range participants {
		j, err := types.ParseJID(p)
		if err != nil {
			return fmt.Errorf("invalid participant jid %q: %w", p, err)
		}
		jids[i] = j
	}

	var change whatsmeow.ParticipantChange
	switch action {
	case "add":
		change = whatsmeow.ParticipantChangeAdd
	case "remove":
		change = whatsmeow.ParticipantChangeRemove
	case "promote":
		change = whatsmeow.ParticipantChangePromote
	case "demote":
		change = whatsmeow.ParticipantChangeDemote
	default:
		return fmt.Errorf("invalid action %q: must be add, remove, promote, or demote", action)
	}

	_, err = client.UpdateGroupParticipants(ctx, jid, jids, change)
	return err
}

// GetGroupInviteLink returns the group's invite link.
func (a *Account) GetGroupInviteLink(ctx context.Context, groupJID string, reset bool) (string, error) {
	client, err := a.requireConnectedClient()
	if err != nil {
		return "", err
	}

	jid, err := types.ParseJID(groupJID)
	if err != nil {
		return "", fmt.Errorf("invalid jid %q: %w", groupJID, err)
	}

	return client.GetGroupInviteLink(ctx, jid, reset)
}

// ── Newsletters (Channels) ──────────────────────────

// ListNewsletters returns all subscribed WhatsApp channels.
func (a *Account) ListNewsletters(ctx context.Context) ([]model.NewsletterInfo, error) {
	client, err := a.requireConnectedClient()
	if err != nil {
		return nil, err
	}

	newsletters, err := client.GetSubscribedNewsletters(ctx)
	if err != nil {
		return nil, fmt.Errorf("get subscribed newsletters: %w", err)
	}

	result := make([]model.NewsletterInfo, len(newsletters))
	for i, nl := range newsletters {
		result[i] = newsletterMetadataToModel(nl)
	}
	return result, nil
}

// GetNewsletterInfo returns info about a specific newsletter.
func (a *Account) GetNewsletterInfo(ctx context.Context, jid string) (model.NewsletterInfo, error) {
	client, err := a.requireConnectedClient()
	if err != nil {
		return model.NewsletterInfo{}, err
	}

	j, err := types.ParseJID(jid)
	if err != nil {
		return model.NewsletterInfo{}, fmt.Errorf("invalid jid %q: %w", jid, err)
	}

	nl, err := client.GetNewsletterInfo(ctx, j)
	if err != nil {
		return model.NewsletterInfo{}, fmt.Errorf("get newsletter info: %w", err)
	}

	return newsletterMetadataToModel(nl), nil
}

// FollowNewsletter subscribes to a WhatsApp channel.
func (a *Account) FollowNewsletter(ctx context.Context, jid string) error {
	client, err := a.requireConnectedClient()
	if err != nil {
		return err
	}

	j, err := types.ParseJID(jid)
	if err != nil {
		return fmt.Errorf("invalid jid %q: %w", jid, err)
	}

	return client.FollowNewsletter(ctx, j)
}

// UnfollowNewsletter unsubscribes from a WhatsApp channel.
func (a *Account) UnfollowNewsletter(ctx context.Context, jid string) error {
	client, err := a.requireConnectedClient()
	if err != nil {
		return err
	}

	j, err := types.ParseJID(jid)
	if err != nil {
		return fmt.Errorf("invalid jid %q: %w", jid, err)
	}

	return client.UnfollowNewsletter(ctx, j)
}

// GetNewsletterMessages returns messages from a WhatsApp channel.
func (a *Account) GetNewsletterMessages(ctx context.Context, jid string, count int, before int) (model.NewsletterMessageListResponse, error) {
	client, err := a.requireConnectedClient()
	if err != nil {
		return model.NewsletterMessageListResponse{}, err
	}

	j, err := types.ParseJID(jid)
	if err != nil {
		return model.NewsletterMessageListResponse{}, fmt.Errorf("invalid jid %q: %w", jid, err)
	}

	params := &whatsmeow.GetNewsletterMessagesParams{Count: count}
	if before > 0 {
		params.Before = types.MessageServerID(before)
	}

	msgs, err := client.GetNewsletterMessages(ctx, j, params)
	if err != nil {
		return model.NewsletterMessageListResponse{}, fmt.Errorf("get newsletter messages: %w", err)
	}

	result := make([]model.NewsletterMessageInfo, len(msgs))
	for i, m := range msgs {
		body := ""
		if m.Message != nil {
			if m.Message.GetExtendedTextMessage() != nil {
				body = m.Message.GetExtendedTextMessage().GetText()
			} else if m.Message.GetConversation() != "" {
				body = m.Message.GetConversation()
			} else if m.Message.GetImageMessage() != nil {
				body = m.Message.GetImageMessage().GetCaption()
			} else if m.Message.GetVideoMessage() != nil {
				body = m.Message.GetVideoMessage().GetCaption()
			}
		}
		result[i] = model.NewsletterMessageInfo{
			ServerID:   int(m.MessageServerID),
			MessageID:  string(m.MessageID),
			Type:       m.Type,
			Body:       body,
			Timestamp:  m.Timestamp.Format(time.RFC3339),
			ViewsCount: m.ViewsCount,
			Reactions:  m.ReactionCounts,
		}
	}

	return model.NewsletterMessageListResponse{
		Messages: result,
		Count:    len(result),
	}, nil
}

// ToggleMuteNewsletter mutes or unmutes a WhatsApp channel.
func (a *Account) ToggleMuteNewsletter(ctx context.Context, jid string, mute bool) error {
	client, err := a.requireConnectedClient()
	if err != nil {
		return err
	}

	j, err := types.ParseJID(jid)
	if err != nil {
		return fmt.Errorf("invalid jid %q: %w", jid, err)
	}

	return client.NewsletterToggleMute(ctx, j, mute)
}

// newsletterMetadataToModel converts whatsmeow NewsletterMetadata to our model.
func newsletterMetadataToModel(nl *types.NewsletterMetadata) model.NewsletterInfo {
	info := model.NewsletterInfo{
		ID:              nl.ID.String(),
		Name:            nl.ThreadMeta.Name.Text,
		Description:     nl.ThreadMeta.Description.Text,
		SubscriberCount: nl.ThreadMeta.SubscriberCount,
		Verification:    string(nl.ThreadMeta.VerificationState),
		InviteCode:      nl.ThreadMeta.InviteCode,
	}
	if nl.ThreadMeta.Picture != nil && nl.ThreadMeta.Picture.URL != "" {
		url := nl.ThreadMeta.Picture.URL
		info.PictureURL = &url
	}
	if nl.ThreadMeta.Preview.URL != "" {
		url := nl.ThreadMeta.Preview.URL
		info.PreviewURL = &url
	}
	if nl.ViewerMeta != nil {
		info.Mute = string(nl.ViewerMeta.Mute)
		info.Role = string(nl.ViewerMeta.Role)
	}
	return info
}

// ── Presence ────────────────────────────────────────

// SendPresence sets global online/offline presence.
func (a *Account) SendPresence(ctx context.Context, state string) error {
	client, err := a.requireConnectedClient()
	if err != nil {
		return err
	}

	var p types.Presence
	switch state {
	case "available":
		p = types.PresenceAvailable
	case "unavailable":
		p = types.PresenceUnavailable
	default:
		return fmt.Errorf("invalid state %q: must be available or unavailable", state)
	}
	return client.SendPresence(ctx, p)
}

// ── Profile ─────────────────────────────────────────

// GetProfile returns the account's own profile info.
func (a *Account) GetProfile(ctx context.Context) (model.ProfileResponse, error) {
	client, err := a.requireConnectedClient()
	if err != nil {
		return model.ProfileResponse{}, err
	}

	resp := model.ProfileResponse{ID: a.ID}
	if a.PhoneNumber != "" {
		resp.PhoneNumber = &a.PhoneNumber
	}

	// Push name and business name from the local store.
	if pn := client.Store.PushName; pn != "" {
		resp.PushName = &pn
	}
	if bn := client.Store.BusinessName; bn != "" {
		resp.BusinessName = &bn
	}

	if client.Store.ID != nil {
		// Use ToNonAD() to strip the device suffix — the client's
		// GetUserInfo keys the response map by the bare JID, so a
		// lookup with the device-scoped JID would silently miss.
		ownJID := client.Store.ID.ToNonAD()

		// Profile picture
		pic, err := client.GetProfilePictureInfo(ctx, ownJID, &whatsmeow.GetProfilePictureParams{Preview: false})
		if err == nil && pic != nil {
			resp.PictureURL = &pic.URL
		}

		// About / status text + verified business name via GetUserInfo.
		// The response may be keyed by a LID JID rather than the phone JID,
		// so iterate the map instead of doing a direct lookup.
		userInfo, err := client.GetUserInfo(ctx, []types.JID{ownJID})
		if err == nil {
			for _, info := range userInfo {
				if info.Status != "" {
					resp.About = &info.Status
				}
				if info.VerifiedName != nil && info.VerifiedName.Details != nil {
					if vn := info.VerifiedName.Details.GetVerifiedName(); vn != "" {
						resp.VerifiedName = &vn
					}
				}
				break
			}
		}

		// Business profile (address, email, categories, hours, description).
		// Only returns data for WhatsApp Business accounts.
		bizProfile, err := client.GetBusinessProfile(ctx, ownJID)
		if err == nil && bizProfile != nil {
			resp.IsBusiness = true
			if bizProfile.Address != "" {
				resp.Address = &bizProfile.Address
			}
			if bizProfile.Email != "" {
				resp.Email = &bizProfile.Email
			}
			if desc, ok := bizProfile.ProfileOptions["description"]; ok && desc != "" {
				resp.Description = &desc
			}
			if len(bizProfile.Categories) > 0 {
				cats := make([]model.ProfileCategory, len(bizProfile.Categories))
				for i, c := range bizProfile.Categories {
					cats[i] = model.ProfileCategory{ID: c.ID, Name: c.Name}
				}
				resp.Categories = cats
			}
			if len(bizProfile.BusinessHours) > 0 {
				slots := make([]model.BusinessHoursSlot, len(bizProfile.BusinessHours))
				for i, h := range bizProfile.BusinessHours {
					slots[i] = model.BusinessHoursSlot{
						DayOfWeek: h.DayOfWeek,
						Mode:      h.Mode,
						OpenTime:  h.OpenTime,
						CloseTime: h.CloseTime,
					}
				}
				resp.BusinessHours = &model.BusinessHoursInfo{
					Timezone: bizProfile.BusinessHoursTimeZone,
					Config:   slots,
				}
			}
			// Forward any remaining profile options (e.g. cart_enabled, catalog, etc.)
			opts := make(map[string]string)
			for k, v := range bizProfile.ProfileOptions {
				if k != "description" {
					opts[k] = v
				}
			}
			if len(opts) > 0 {
				resp.ProfileOptions = opts
			}
		}
	}

	return resp, nil
}

// SetStatusMessage sets the "About" text.
func (a *Account) SetStatusMessage(ctx context.Context, about string) error {
	client, err := a.requireConnectedClient()
	if err != nil {
		return err
	}

	return client.SetStatusMessage(ctx, types.SetStatusInput{Text: &about})
}

// ── helpers ─────────────────────────────────────────

func groupInfoToModel(gi *types.GroupInfo) model.GroupInfo {
	participants := make([]model.GroupParticipant, len(gi.Participants))
	for i, p := range gi.Participants {
		gp := model.GroupParticipant{
			ID:      p.JID.String(),
			IsAdmin: p.IsAdmin || p.IsSuperAdmin,
		}
		if p.DisplayName != "" {
			gp.Name = &p.DisplayName
		}
		participants[i] = gp
	}

	created := gi.GroupCreated.Format(time.RFC3339)
	owner := gi.OwnerJID.String()

	result := model.GroupInfo{
		ID:               gi.JID.String(),
		Name:             gi.Name,
		ParticipantCount: len(gi.Participants),
		Participants:     participants,
		IsAnnounce:       gi.IsAnnounce,
		IsLocked:         gi.IsLocked,
		CreatedAt:        &created,
		CreatedBy:        &owner,
	}
	if gi.Topic != "" {
		result.Description = &gi.Topic
	}
	return result
}

// ListContacts returns all contacts from the local store.
func (a *Account) ListContacts(ctx context.Context) ([]model.ContactInfo, error) {
	client, err := a.requireConnectedClient()
	if err != nil {
		return nil, err
	}

	contacts, err := client.Store.Contacts.GetAllContacts(ctx)
	if err != nil {
		return nil, fmt.Errorf("get contacts: %w", err)
	}

	result := make([]model.ContactInfo, 0, len(contacts))
	seen := make(map[string]bool)
	for jid, info := range contacts {
		// Resolve LID JIDs to phone JIDs.
		resolved := jid.String()
		if jid.Server == types.HiddenUserServer {
			resolved = a.resolveLID(resolved)
		}
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
		ci := model.ContactInfo{
			ID:           resolved,
			PushName:     info.PushName,
			FullName:     info.FullName,
			FirstName:    info.FirstName,
			BusinessName: info.BusinessName,
		}
		parsedResolved, parseErr := types.ParseJID(resolved)
		if parseErr == nil && parsedResolved.Server == types.DefaultUserServer {
			phone := parsedResolved.User
			ci.Phone = &phone
		}
		result = append(result, ci)
	}
	return result, nil
}

// CheckContacts checks which phone numbers are registered on WhatsApp.
func (a *Account) CheckContacts(ctx context.Context, phones []string) ([]model.CheckContactResult, error) {
	client, err := a.requireConnectedClient()
	if err != nil {
		return nil, err
	}

	resp, err := client.IsOnWhatsApp(ctx, phones)
	if err != nil {
		return nil, fmt.Errorf("check contacts: %w", err)
	}

	results := make([]model.CheckContactResult, len(resp))
	for i, r := range resp {
		results[i] = model.CheckContactResult{
			Phone:      r.Query,
			OnWhatsApp: r.IsIn,
		}
		if r.IsIn {
			results[i].JID = r.JID.String()
		}
	}
	return results, nil
}

// RevokeMessage revokes (deletes for everyone) a previously sent message.
func (a *Account) RevokeMessage(ctx context.Context, chatJID, messageID string) (model.RevokeMessageResponse, error) {
	client, err := a.requireConnectedClient()
	if err != nil {
		return model.RevokeMessageResponse{}, err
	}

	target, err := types.ParseJID(chatJID)
	if err != nil {
		return model.RevokeMessageResponse{}, fmt.Errorf("invalid jid %q: %w", chatJID, err)
	}

	revokeMsg := client.BuildRevoke(target, types.EmptyJID, types.MessageID(messageID))
	resp, err := client.SendMessage(ctx, target, revokeMsg)
	if err != nil {
		return model.RevokeMessageResponse{}, fmt.Errorf("revoke: %w", err)
	}

	return model.RevokeMessageResponse{
		Revoked:   true,
		Timestamp: resp.Timestamp.Format(time.RFC3339),
	}, nil
}

// ListMessages returns stored messages for a chat with cursor pagination.
func (a *Account) ListMessages(chatJID string, limit int, before string) (model.MessageListResponse, error) {
	if a.db == nil {
		return model.MessageListResponse{}, fmt.Errorf("no database")
	}

	// Query both PN and LID variants since history may be stored under either
	// (older messages with LID, newer with PN after resolveLID, or unsaved chats).
	phoneJID := a.resolveLID(chatJID)
	lidJID := a.reverseLID(chatJID)
	jids := map[string]struct{}{chatJID: {}}
	if phoneJID != chatJID {
		jids[phoneJID] = struct{}{}
	}
	if lidJID != chatJID {
		jids[lidJID] = struct{}{}
	}

	var records []*database.MessageRecord
	first := true
	for jid := range jids {
		recs, err := a.db.ListMessages(a.ID, jid, limit, before)
		if err != nil {
			if first {
				return model.MessageListResponse{}, fmt.Errorf("list messages: %w", err)
			}
			continue
		}
		if first {
			records = recs
			first = false
		} else if len(recs) > 0 {
			records = append(records, recs...)
		}
	}
	if len(jids) > 1 && len(records) > 1 {
		sort.Slice(records, func(i, j int) bool {
			return records[i].Timestamp > records[j].Timestamp
		})
		if len(records) > limit {
			records = records[:limit]
		}
	}

	msgs := make([]model.MessageInfo, 0, len(records))
	for _, r := range records {
		// Only skip protocol/system messages; keep "other" with empty body so
		// unsaved chats where all messages are type=other don't appear empty.
		// Frontend will render a placeholder.
		if r.Type == "protocol" {
			continue
		}
		msgs = append(msgs, model.MessageInfo{
			ID:        r.ID,
			ChatJID:   r.ChatJID,
			SenderJID: r.SenderJID,
			FromMe:    r.FromMe,
			Type:      r.Type,
			Body:      r.Body,
			MediaType: r.MediaType,
			Timestamp: r.Timestamp,
		})
	}
	return model.MessageListResponse{
		Messages: msgs,
		Count:    len(msgs),
	}, nil
}

// DownloadMedia downloads media from a received message.
func (a *Account) DownloadMedia(ctx context.Context, msg *waE2E.Message) ([]byte, error) {
	client, err := a.requireConnectedClient()
	if err != nil {
		return nil, err
	}

	//nolint:staticcheck // DownloadAny is the only generic entry point for mixed media messages.
	data, err := client.DownloadAny(ctx, msg)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	return data, nil
}

// ListChats returns known contacts and groups from the local store,
// enriched with last message, unread count, and sorted by most recent activity.
func (a *Account) ListChats(ctx context.Context) ([]model.ChatInfo, error) {
	client, err := a.requireConnectedClient()
	if err != nil {
		return nil, err
	}

	// Pre-fetch last message and unread counts from our message DB.
	var lastMsgs map[string]*database.LastMessageInfo
	var unreadCounts map[string]int
	if a.db != nil {
		var err error
		lastMsgs, err = a.db.GetLastMessagePerChat(a.ID)
		if err != nil {
			log.Warn().Err(err).Msg("failed to fetch last messages")
		}
		unreadCounts, err = a.db.GetUnreadCountPerChat(a.ID)
		if err != nil {
			log.Warn().Err(err).Msg("failed to fetch unread counts")
		}
	}
	if lastMsgs == nil {
		lastMsgs = make(map[string]*database.LastMessageInfo)
	}
	if unreadCounts == nil {
		unreadCounts = make(map[string]int)
	}

	seen := make(map[string]bool)
	var chats []model.ChatInfo

	// skipJID returns true for JIDs that should not appear in the chat list
	// (status broadcasts, newsletter channels, lid, etc.).
	skipJID := func(id string) bool {
		return id == "status@broadcast" || strings.HasSuffix(id, "@broadcast") ||
			strings.HasSuffix(id, "@newsletter")
	}

	// Helper to enrich a chat entry with local settings (pinned/muted/archived)
	// and last message / unread count.
	enrich := func(chat *model.ChatInfo, jid types.JID) {
		settings, err := client.Store.ChatSettings.GetChatSettings(ctx, jid)
		if err == nil && settings.Found {
			chat.Pinned = settings.Pinned
			chat.Archived = settings.Archived
			chat.Muted = !settings.MutedUntil.IsZero()
		}
		if lm, ok := lastMsgs[chat.ID]; ok {
			chat.LastMessage = &lm.Body
			chat.Timestamp = &lm.Timestamp
			if chat.IsGroup && !lm.FromMe {
				chat.LastSender = &lm.SenderJID
			}
		}
		chat.UnreadCount = unreadCounts[chat.ID]
	}

	// 1. Groups from server
	groups, err := client.GetJoinedGroups(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("failed to fetch groups from server")
	} else {
		for _, g := range groups {
			id := g.JID.String()
			if skipJID(id) {
				continue
			}
			seen[id] = true
			chat := model.ChatInfo{
				ID:      id,
				Name:    g.Name,
				IsGroup: true,
			}
			enrich(&chat, g.JID)
			chats = append(chats, chat)
		}
	}

	// 2. Contacts from local store — only include those that have at least one message.
	contacts, err := client.Store.Contacts.GetAllContacts(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("failed to fetch contacts from store")
	} else {
		for jid, info := range contacts {
			// Resolve LID JIDs to phone JIDs so they merge with existing chats.
			id := jid.String()
			if jid.Server == types.HiddenUserServer {
				id = a.resolveLID(id)
			}
			if seen[id] || skipJID(id) {
				continue
			}
			// Only show contacts that have exchanged messages.
			if _, hasMsg := lastMsgs[id]; !hasMsg {
				// Also check with original (unresolved) JID string.
				if _, hasMsg2 := lastMsgs[jid.String()]; !hasMsg2 {
					continue
				}
			}
			// Priority: saved contact name > business name > profile name > phone
			var name string
			switch {
			case info.FullName != "":
				name = info.FullName
			case info.BusinessName != "":
				name = info.BusinessName
			case info.PushName != "":
				name = info.PushName
			default:
				name = "+" + jid.User
			}
			chat := model.ChatInfo{
				ID:      id,
				Name:    name,
				IsGroup: jid.Server == types.GroupServer,
			}
			enrich(&chat, jid)
			seen[id] = true
			chats = append(chats, chat)
		}
	}

	// 3. Chats from message DB that aren't in contacts or groups
	// (e.g. messages exchanged with numbers not in contacts, or history-synced chats)
	for chatJID, lm := range lastMsgs {
		// Resolve any LID JIDs that weren't resolved at storage time.
		resolvedJID := a.resolveLID(chatJID)
		if seen[resolvedJID] || skipJID(resolvedJID) {
			continue
		}
		seen[resolvedJID] = true
		isGroup := strings.HasSuffix(resolvedJID, "@g.us")
		var name string
		if !isGroup {
			// Extract phone number from JID for display
			name = "+" + strings.SplitN(resolvedJID, "@", 2)[0]
		} else {
			// Try to fetch group name; if unavailable leave empty so frontend shows placeholder
			name = ""
			if jid, err := types.ParseJID(resolvedJID); err == nil {
				if gi, err := client.GetGroupInfo(ctx, jid); err == nil && gi.Name != "" {
					name = gi.Name
				}
			}
		}
		chat := model.ChatInfo{
			ID:      resolvedJID,
			Name:    name,
			IsGroup: isGroup,
		}
		chat.LastMessage = &lm.Body
		chat.Timestamp = &lm.Timestamp
		if chat.IsGroup && !lm.FromMe {
			chat.LastSender = &lm.SenderJID
		}
		chat.UnreadCount = unreadCounts[chatJID]
		chats = append(chats, chat)
	}

	// Sort: pinned first, then by timestamp descending (most recent first).
	sort.Slice(chats, func(i, j int) bool {
		// Pinned chats always come first.
		if chats[i].Pinned != chats[j].Pinned {
			return chats[i].Pinned
		}
		// Then by timestamp descending (string comparison works for RFC3339).
		ti, tj := "", ""
		if chats[i].Timestamp != nil {
			ti = *chats[i].Timestamp
		}
		if chats[j].Timestamp != nil {
			tj = *chats[j].Timestamp
		}
		return ti > tj
	})

	return chats, nil
}

// ---- internal ----

// normalizePhone strips everything except digits from a phone string.
func normalizePhone(s string) string {
	var buf strings.Builder
	buf.Grow(len(s))
	for _, r := range s {
		if r >= '0' && r <= '9' {
			buf.WriteRune(r)
		}
	}
	return buf.String()
}

// phoneMatches returns true if the authed phone matches the registered phone.
// It handles the case where the registered phone may lack a country code
// (i.e. the authed phone ends with the registered phone).
func phoneMatches(authed, registered string) bool {
	a := normalizePhone(authed)
	r := normalizePhone(registered)
	if a == "" || r == "" {
		return true // can't validate if either is empty
	}
	if a == r {
		return true
	}
	// Allow match if registered phone is a suffix (missing country code)
	if len(r) >= 7 && strings.HasSuffix(a, r) {
		return true
	}
	return false
}

func (a *Account) handleEvent(evt interface{}) {
	switch v := evt.(type) {
	case *events.Message:
		log.Debug().Str("account", a.ID).Str("from", v.Info.Sender.String()).Str("chat", v.Info.Chat.String()).Bool("from_me", v.Info.IsFromMe).Str("type", classifyMessage(v.Message)).Msg("message event received")
		a.storeMessage(v)
		a.dispatchWebhook("message", map[string]any{
			"id":         v.Info.ID,
			"chat":       v.Info.Chat.String(),
			"sender":     v.Info.Sender.String(),
			"from_me":    v.Info.IsFromMe,
			"type":       classifyMessage(v.Message),
			"body":       extractBody(v.Message),
			"media_type": v.Info.MediaType,
			"timestamp":  v.Info.Timestamp.UTC().Format(time.RFC3339),
		})
		// Fire auto-reply hook for incoming (non-self) text messages.
		if !v.Info.IsFromMe {
			body := extractBody(v.Message)
			if body != "" {
				a.mu.RLock()
				hook := a.OnIncomingMessage
				a.mu.RUnlock()
				if hook != nil {
					go hook(v.Info.Chat.String(), v.Info.Sender.String(), body)
				}
			}
		}
	case *events.Receipt:
		log.Debug().Str("account", a.ID).Str("type", string(v.Type)).Int("count", len(v.MessageIDs)).Msg("receipt")
		a.dispatchWebhook("receipt", map[string]any{
			"type":        string(v.Type),
			"chat":        v.Chat.String(),
			"sender":      v.Sender.String(),
			"message_ids": v.MessageIDs,
			"timestamp":   v.Timestamp.UTC().Format(time.RFC3339),
		})
	case *events.PairSuccess:
		// PairSuccess fires right after QR scan, before Connected.
		// Reject early if the paired phone doesn't match.
		if a.PhoneNumber != "" && v.ID.User != "" {
			if !phoneMatches(v.ID.User, a.PhoneNumber) {
				log.Warn().
					Str("account", a.ID).
					Str("expected", a.PhoneNumber).
					Str("got", v.ID.User).
					Msg("phone number mismatch on pair — rejecting")
				a.rejectMismatch()
				return
			}
		}
	case *events.PushName:
		log.Debug().Str("account", a.ID).Str("jid", v.JID.String()).Str("name", v.NewPushName).Msg("push name update")
	case *events.HistorySync:
		log.Info().Str("account", a.ID).Msg("history sync received")
		a.storeHistorySync(v)
	case *events.Connected:
		log.Info().Str("account", a.ID).Msg("connected event")
		a.mu.RLock()
		client := a.client
		a.mu.RUnlock()
		if client != nil {
			// Validate that the authenticated WhatsApp number matches the
			// phone number registered on this account. If they differ the
			// user linked the wrong device — disconnect immediately.
			if client.Store.ID != nil && a.PhoneNumber != "" {
				if !phoneMatches(client.Store.ID.User, a.PhoneNumber) {
					log.Warn().
						Str("account", a.ID).
						Str("expected", a.PhoneNumber).
						Str("got", client.Store.ID.User).
						Msg("phone number mismatch — disconnecting")
					a.rejectMismatch()
					return
				}
			}
			if client.Store.PushName == "" {
				client.Store.PushName = "NoTalk"
			}
			_ = client.SendPresence(context.Background(), types.PresenceAvailable)
		}
	case *events.LoggedOut:
		log.Warn().Str("account", a.ID).Int("reason", int(v.Reason)).Msg("logged out by phone — cleaning up")
		a.mu.Lock()
		if a.client != nil {
			a.client.Disconnect()
			a.client = nil
		}
		a.mu.Unlock()
	case *events.Disconnected:
		log.Warn().Str("account", a.ID).Msg("disconnected event")
		a.mu.RLock()
		rejected := a.rejected
		a.mu.RUnlock()
		if !rejected {
			go a.autoReconnect()
		}
	default:
		log.Debug().Str("account", a.ID).Str("event_type", fmt.Sprintf("%T", evt)).Msg("unhandled event")
	}
}

// rejectMismatch logs out, disconnects, wipes the session store, and sets
// the rejected flag so autoReconnect will not retry.
func (a *Account) rejectMismatch() {
	go func() {
		a.mu.Lock()
		a.rejected = true
		if a.client != nil {
			_ = a.client.Logout(context.Background())
			a.client.Disconnect()
			a.client = nil
		}
		// Wipe all device sessions so this mismatched device cannot
		// auto-reconnect on the next server restart.
		if a.container != nil {
			ctx := context.Background()
			if devs, err := a.container.GetAllDevices(ctx); err == nil {
				for _, d := range devs {
					_ = a.container.DeleteDevice(ctx, d)
				}
			}
		}
		a.mu.Unlock()
		log.Info().Str("account", a.ID).Msg("phone mismatch: session wiped, account rejected")
	}()
}

// storeOutgoing persists a sent message to the DB so it appears in message history.
func (a *Account) storeOutgoing(msgID, chatJID, msgType, body, mediaType string) {
	if a.db == nil {
		return
	}
	rec := &database.MessageRecord{
		ID:        msgID,
		AccountID: a.ID,
		ChatJID:   chatJID,
		SenderJID: "me",
		FromMe:    true,
		Type:      msgType,
		Body:      body,
		MediaType: mediaType,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	if err := a.db.InsertMessage(rec); err != nil {
		log.Warn().Str("account", a.ID).Err(err).Msg("failed to store outgoing message")
	}
}

// autoReconnect attempts to reconnect after an unexpected disconnect.
func (a *Account) autoReconnect() {
	delays := []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second, 60 * time.Second}
	for i, delay := range delays {
		time.Sleep(delay)

		a.mu.RLock()
		client := a.client
		a.mu.RUnlock()

		if client == nil {
			return // intentionally disconnected or logged out
		}
		if client.IsConnected() {
			return // already reconnected
		}

		log.Info().Str("account", a.ID).Int("attempt", i+1).Msg("auto-reconnect attempt")
		if err := client.Connect(); err != nil {
			log.Warn().Str("account", a.ID).Err(err).Msg("auto-reconnect failed")
			continue
		}
		log.Info().Str("account", a.ID).Msg("auto-reconnected")
		return
	}
	log.Error().Str("account", a.ID).Msg("auto-reconnect: all retries exhausted")
}

// resolveLID converts a LID JID (e.g. 12345@lid) to a phone JID (e.g. 919876543210@s.whatsapp.net)
// using the whatsmeow LID mapping table. Returns the original string if resolution fails.
func (a *Account) resolveLID(jidStr string) string {
	if !strings.HasSuffix(jidStr, "@lid") {
		return jidStr
	}
	a.mu.RLock()
	client := a.client
	a.mu.RUnlock()
	if client == nil || client.Store == nil {
		return jidStr
	}
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return jidStr
	}
	pn, err := client.Store.LIDs.GetPNForLID(context.Background(), jid)
	if err != nil || pn.IsEmpty() {
		return jidStr
	}
	return pn.String()
}

// reverseLID converts a phone JID to its LID variant, for querying old DB records.
// Returns the same JID if no mapping exists.
func (a *Account) reverseLID(jidStr string) string {
	if !strings.HasSuffix(jidStr, "@s.whatsapp.net") {
		return jidStr
	}
	a.mu.RLock()
	client := a.client
	a.mu.RUnlock()
	if client == nil || client.Store == nil {
		return jidStr
	}
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return jidStr
	}
	lid, err := client.Store.LIDs.GetLIDForPN(context.Background(), jid)
	if err != nil || lid.IsEmpty() {
		return jidStr
	}
	return lid.String()
}

// storeMessage persists a received message to the DB.
func (a *Account) storeMessage(v *events.Message) {
	if a.db == nil {
		return
	}
	if !isStorableMessage(v.Message) {
		return
	}
	// Resolve LID JIDs to phone JIDs so conversations unify properly.
	chatJID := a.resolveLID(v.Info.Chat.String())
	senderJID := a.resolveLID(v.Info.Sender.String())
	rec := &database.MessageRecord{
		ID:        v.Info.ID,
		AccountID: a.ID,
		ChatJID:   chatJID,
		SenderJID: senderJID,
		FromMe:    v.Info.IsFromMe,
		Type:      classifyMessage(v.Message),
		Body:      extractBody(v.Message),
		MediaType: v.Info.MediaType,
		Timestamp: v.Info.Timestamp.UTC().Format(time.RFC3339),
	}
	if err := a.db.InsertMessage(rec); err != nil {
		log.Warn().Str("account", a.ID).Err(err).Msg("failed to store message")
	}
}

// storeHistorySync processes a history sync event and stores the messages.
func (a *Account) storeHistorySync(evt *events.HistorySync) {
	if a.db == nil || evt.Data == nil {
		return
	}
	var stored int
	for _, conv := range evt.Data.GetConversations() {
		chatJID := conv.GetID()
		if chatJID == "" {
			continue
		}
		for _, hsMsg := range conv.GetMessages() {
			wmi := hsMsg.GetMessage()
			if wmi == nil || wmi.GetKey() == nil {
				continue
			}
			msg := wmi.GetMessage()
			if msg == nil {
				continue
			}
			key := wmi.GetKey()
			msgID := key.GetID()
			if msgID == "" {
				msgID = uuid.NewString()
			}

			senderJID := chatJID
			fromMe := key.GetFromMe()
			if fromMe {
				senderJID = "me"
			} else if p := wmi.GetParticipant(); p != "" {
				senderJID = p
			} else if p := key.GetParticipant(); p != "" {
				senderJID = p
			}

			if !isStorableMessage(msg) {
				continue
			}

			ts := time.Unix(int64(wmi.GetMessageTimestamp()), 0).UTC().Format(time.RFC3339)

			rec := &database.MessageRecord{
				ID:        msgID,
				AccountID: a.ID,
				ChatJID:   a.resolveLID(chatJID),
				SenderJID: a.resolveLID(senderJID),
				FromMe:    fromMe,
				Type:      classifyMessage(msg),
				Body:      extractBody(msg),
				MediaType: "",
				Timestamp: ts,
			}
			if err := a.db.InsertMessage(rec); err == nil {
				stored++
			}
		}
	}
	if stored > 0 {
		log.Info().Str("account", a.ID).Int("stored", stored).Msg("history sync: messages stored")
	}
}

// classifyMediaMIME returns the whatsmeow MediaType and a short message type
// label based on the MIME type of the file being sent.
func classifyMediaMIME(mimetype string) (whatsmeow.MediaType, string) {
	mt := strings.ToLower(mimetype)
	switch {
	case mt == "image/webp":
		return whatsmeow.MediaImage, "sticker"
	case strings.HasPrefix(mt, "image/"):
		return whatsmeow.MediaImage, "image"
	case strings.HasPrefix(mt, "video/"):
		return whatsmeow.MediaVideo, "video"
	case strings.HasPrefix(mt, "audio/"):
		return whatsmeow.MediaAudio, "audio"
	default:
		return whatsmeow.MediaDocument, "document"
	}
}

// classifyMessage returns a short type label for the message.
func classifyMessage(msg *waE2E.Message) string {
	if msg == nil {
		return "unknown"
	}
	switch {
	case msg.Conversation != nil || msg.ExtendedTextMessage != nil:
		return "text"
	case msg.ImageMessage != nil:
		return "image"
	case msg.VideoMessage != nil:
		return "video"
	case msg.AudioMessage != nil:
		return "audio"
	case msg.DocumentMessage != nil:
		return "document"
	case msg.StickerMessage != nil:
		return "sticker"
	case msg.ReactionMessage != nil:
		return "reaction"
	case msg.ContactMessage != nil:
		return "contact"
	case msg.LocationMessage != nil:
		return "location"
	case msg.ProtocolMessage != nil:
		return "protocol"
	case msg.SenderKeyDistributionMessage != nil:
		return "protocol"
	default:
		return "other"
	}
}

// isStorableMessage returns true if the message is worth storing in the DB.
// Protocol messages, key distribution, and unknown/empty messages are skipped.
func isStorableMessage(msg *waE2E.Message) bool {
	if msg == nil {
		return false
	}
	t := classifyMessage(msg)
	return t != "protocol" && t != "unknown"
}

// extractBody returns the textual content of a message.
func extractBody(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	switch {
	case msg.Conversation != nil:
		return msg.GetConversation()
	case msg.ExtendedTextMessage != nil:
		return msg.ExtendedTextMessage.GetText()
	case msg.ImageMessage != nil:
		return msg.ImageMessage.GetCaption()
	case msg.VideoMessage != nil:
		return msg.VideoMessage.GetCaption()
	case msg.DocumentMessage != nil:
		return msg.DocumentMessage.GetCaption()
	case msg.ReactionMessage != nil:
		return msg.ReactionMessage.GetText()
	default:
		return ""
	}
}

// dispatchWebhook sends an event to the account's webhook URL, if configured.
func (a *Account) dispatchWebhook(eventType string, payload map[string]any) {
	if a.db == nil {
		return
	}
	go a.doDispatchWebhook(eventType, payload)
}

func (a *Account) doDispatchWebhook(eventType string, payload map[string]any) {
	cfg, err := a.db.GetWebhookConfig(a.ID)
	if err != nil || cfg == nil || !cfg.Enabled || cfg.URL == "" {
		return
	}

	// Filter by event type if events are specified
	if cfg.Events != "" {
		allowed := false
		for _, e := range splitCSV(cfg.Events) {
			if e == eventType {
				allowed = true
				break
			}
		}
		if !allowed {
			return
		}
	}

	evt := model.WebhookEvent{
		EventType: eventType,
		AccountID: a.ID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   payload,
	}

	body, err := json.Marshal(evt)
	if err != nil {
		log.Warn().Str("account", a.ID).Err(err).Msg("webhook: marshal failed")
		return
	}

	// Build HMAC signature header value (reused across retries)
	var signature string
	if cfg.Secret != "" {
		mac := hmac.New(sha256.New, []byte(cfg.Secret))
		mac.Write(body)
		signature = hex.EncodeToString(mac.Sum(nil))
	}

	timeout := time.Duration(a.WebhookCfg.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	httpClient := &http.Client{Timeout: timeout}

	retryCount := a.WebhookCfg.RetryCount
	if retryCount <= 0 {
		retryCount = 3
	}
	maxAttempts := 1 + retryCount

	retryDelay := time.Duration(a.WebhookCfg.RetryDelay) * time.Millisecond
	if retryDelay <= 0 {
		retryDelay = 1 * time.Second
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequest("POST", cfg.URL, bytes.NewReader(body))
		if err != nil {
			log.Warn().Str("account", a.ID).Err(err).Msg("webhook: create request failed")
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if signature != "" {
			req.Header.Set("X-Webhook-Signature", signature)
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			log.Warn().Str("account", a.ID).Err(err).Int("attempt", attempt).Msg("webhook: POST failed")
			if attempt < maxAttempts {
				time.Sleep(retryDelay * time.Duration(attempt))
				continue
			}
			return
		}
		_ = resp.Body.Close()

		if resp.StatusCode < 500 {
			if resp.StatusCode >= 400 {
				log.Warn().Str("account", a.ID).Int("status", resp.StatusCode).Msg("webhook: client error (no retry)")
			}
			return // 2xx–4xx: done
		}

		// 5xx: retry
		log.Warn().Str("account", a.ID).Int("status", resp.StatusCode).Int("attempt", attempt).Msg("webhook: server error, retrying")
		if attempt < maxAttempts {
			time.Sleep(retryDelay * time.Duration(attempt))
		}
	}
}

// splitCSV splits a comma-separated string into trimmed parts.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---- phone helpers ----

// NormalizePhone strips non-digit chars and ensures a clean E.164-ish string.
func NormalizePhone(phone string) string {
	var out []byte
	for _, c := range []byte(phone) {
		if c >= '0' && c <= '9' {
			out = append(out, c)
		}
	}
	return string(out)
}

// PhoneToJID converts a phone number to a WhatsApp JID.
func PhoneToJID(phone string) string {
	p := NormalizePhone(phone)
	return p + "@s.whatsapp.net"
}

// NewUUID generates a new UUID string.
func NewUUID() string { return uuid.New().String() }
