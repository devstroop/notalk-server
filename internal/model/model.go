package model

import "time"

// ──────────────────────────────────────────────────────
// Account CRUD
// ──────────────────────────────────────────────────────

// AccountInfo is the API-facing account representation.
type AccountInfo struct {
	ID          string    `json:"id"`
	PhoneNumber *string   `json:"phone_number"`
	AccountName string    `json:"account_name"`
	UserID      string    `json:"user_id,omitempty"`
	Authorized  bool      `json:"authorized"`
	CreatedAt   time.Time `json:"created_at"`
}

// CreateAccountRequest is the JSON body for POST /accounts.
type CreateAccountRequest struct {
	PhoneNumber string `json:"phone_number"`
	AccountName string `json:"account_name"`
	UserID      string `json:"user_id,omitempty"` // admin can assign to user; defaults to caller
}

// CreateAccountResponse is returned after creating an account.
type CreateAccountResponse struct {
	ID          string `json:"id"`
	PhoneNumber string `json:"phone_number"`
	AccountName string `json:"account_name"`
	CreatedAt   string `json:"created_at"`
}

// AccountListResponse is the response for GET /accounts.
type AccountListResponse struct {
	Accounts []AccountInfo `json:"accounts"`
	Total    int           `json:"total"`
}

// AccountActionResponse is a generic action acknowledgement.
type AccountActionResponse struct {
	Message   string `json:"message"`
	AccountID string `json:"account_id"`
}

// DeleteAccountResponse is the response for DELETE /accounts/{id}.
type DeleteAccountResponse struct {
	Message     string `json:"message"`
	AccountID   string `json:"account_id"`
	DataDeleted bool   `json:"data_deleted"`
}

// UpdateAccountRequest is the JSON body for PATCH /accounts/{id}.
type UpdateAccountRequest struct {
	AccountName *string `json:"account_name,omitempty"`
	PhoneNumber *string `json:"phone_number,omitempty"`
}

// ──────────────────────────────────────────────────────
// Proxy
// ──────────────────────────────────────────────────────

// SetProxyRequest is the JSON body for PUT /accounts/{id}/proxy.
type SetProxyRequest struct {
	Protocol string `json:"protocol"` // http, https, socks5
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Enabled  *bool  `json:"enabled,omitempty"` // defaults to true
}

// ProxyConfigResponse is returned when reading proxy config.
// Password is intentionally omitted.
type ProxyConfigResponse struct {
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Enabled  bool   `json:"enabled"`
}

// ──────────────────────────────────────────────────────
// Session
// ──────────────────────────────────────────────────────

// WhatsAppStatusResponse is the response for GET /accounts/{id}/session.
type WhatsAppStatusResponse struct {
	AccountID   string  `json:"account_id"`
	PhoneNumber *string `json:"phone_number"`
	Authorized  bool    `json:"authorized"`
}

// PhoneLinkResponse is the response for phone-number pairing.
type PhoneLinkResponse struct {
	LinkingCode string `json:"linking_code"`
}

// ──────────────────────────────────────────────────────
// Messaging
// ──────────────────────────────────────────────────────

// SendMessageRequest is the JSON body for POST /accounts/{id}/messages.
type SendMessageRequest struct {
	Chat    string  `json:"chat"`              // recipient JID (alternative to phone)
	Phone   string  `json:"phone,omitempty"`   // phone number (alternative to chat/jid)
	Text    *string `json:"text,omitempty"`
	ReplyTo *string `json:"reply_to,omitempty"` // message ID to reply to
	// File handled separately via multipart
}

// SendMessageResponse is the response after sending a message.
type SendMessageResponse struct {
	Status    string `json:"status"`
	MessageID string `json:"message_id"`
}

// ReactionRequest is the JSON body for POST /accounts/{id}/messages/reactions.
type ReactionRequest struct {
	Chat      string `json:"chat,omitempty"`
	Phone     string `json:"phone,omitempty"`  // alternative to chat — auto-resolves to JID
	MessageID string `json:"message_id"`
	Emoji     string `json:"emoji"`
}

// MarkReadRequest is the JSON body for POST /accounts/{id}/messages/mark-read.
type MarkReadRequest struct {
	Chat       string   `json:"chat,omitempty"`
	Phone      string   `json:"phone,omitempty"` // alternative to chat — auto-resolves to JID
	MessageIDs []string `json:"message_ids"`
}

// ──────────────────────────────────────────────────────
// Chats
// ──────────────────────────────────────────────────────

// ChatInfo represents a single chat in the chat list.
type ChatInfo struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	IsGroup      bool    `json:"is_group"`
	LastMessage  *string `json:"last_message"`
	LastSender   *string `json:"last_sender,omitempty"`
	Timestamp    *string `json:"timestamp"`
	UnreadCount  int     `json:"unread_count"`
	Pinned       bool    `json:"pinned"`
	Muted        bool    `json:"muted"`
	Archived     bool    `json:"archived"`
}

// ChatListResponse is the response for GET /accounts/{id}/chats.
type ChatListResponse struct {
	Chats []ChatInfo `json:"chats"`
	Total int        `json:"total"`
}

// ──────────────────────────────────────────────────────
// Contacts
// ──────────────────────────────────────────────────────

// ContactInfo represents a contact's details.
type ContactInfo struct {
	ID           string  `json:"id"`
	PushName     string  `json:"push_name,omitempty"`
	FullName     string  `json:"full_name,omitempty"`
	FirstName    string  `json:"first_name,omitempty"`
	BusinessName string  `json:"business_name,omitempty"`
	Phone        *string `json:"phone,omitempty"`
	PictureURL   *string `json:"picture_url,omitempty"`
}

// ContactListResponse is the response for GET /accounts/{id}/contacts.
type ContactListResponse struct {
	Contacts []ContactInfo `json:"contacts"`
	Total    int           `json:"total"`
}

// CheckContactsRequest is the JSON body for POST /accounts/{id}/contacts/check.
type CheckContactsRequest struct {
	Phones []string `json:"phones"`
}

// CheckContactResult is a single result from the IsOnWhatsApp check.
type CheckContactResult struct {
	Phone       string `json:"phone"`
	OnWhatsApp  bool   `json:"on_whatsapp"`
	JID         string `json:"jid,omitempty"`
}

// CheckContactsResponse is the response for POST /accounts/{id}/contacts/check.
type CheckContactsResponse struct {
	Results []CheckContactResult `json:"results"`
}

// RevokeMessageResponse is the response for DELETE /accounts/{id}/messages/{id}.
type RevokeMessageResponse struct {
	Revoked   bool   `json:"revoked"`
	Timestamp string `json:"timestamp"`
}

// ──────────────────────────────────────────────────────
// Message History
// ──────────────────────────────────────────────────────

// MessageInfo is a single stored message.
type MessageInfo struct {
	ID        string `json:"id"`
	ChatJID   string `json:"chat_jid"`
	SenderJID string `json:"sender_jid"`
	FromMe    bool   `json:"from_me"`
	Type      string `json:"type"`
	Body      string `json:"body,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Timestamp string `json:"timestamp"`
}

// MessageListResponse is the response for GET /accounts/{id}/messages.
type MessageListResponse struct {
	Messages []MessageInfo `json:"messages"`
	Count    int           `json:"count"`
}

// ──────────────────────────────────────────────────────
// Webhook
// ──────────────────────────────────────────────────────

// SetWebhookRequest is the JSON body for PUT /accounts/{id}/webhook.
type SetWebhookRequest struct {
	URL     string   `json:"url"`
	Secret  string   `json:"secret,omitempty"`
	Events  []string `json:"events,omitempty"` // empty = all events
	Enabled *bool    `json:"enabled,omitempty"` // defaults to true
}

// WebhookConfigResponse is returned when reading webhook config.
// Secret is intentionally omitted.
type WebhookConfigResponse struct {
	URL     string   `json:"url"`
	Events  []string `json:"events"`
	Enabled bool     `json:"enabled"`
}

// WebhookEvent is the payload POSTed to the webhook URL.
type WebhookEvent struct {
	EventType string `json:"event_type"`
	AccountID string `json:"account_id"`
	Timestamp string `json:"timestamp"`
	Payload   any    `json:"payload"`
}

// ──────────────────────────────────────────────────────
// Groups
// ──────────────────────────────────────────────────────

// GroupParticipant is a member of a group.
type GroupParticipant struct {
	ID      string  `json:"id"`
	Name    *string `json:"name"`
	IsAdmin bool    `json:"is_admin"`
}

// GroupInfo represents a group's details.
type GroupInfo struct {
	ID               string             `json:"id"`
	Name             string             `json:"name"`
	Description      *string            `json:"description"`
	CreatedAt        *string            `json:"created_at"`
	CreatedBy        *string            `json:"created_by"`
	ParticipantCount int                `json:"participant_count"`
	Participants     []GroupParticipant `json:"participants"`
	IsAnnounce       bool               `json:"is_announce"`
	IsLocked         bool               `json:"is_locked"`
	InviteLink       *string            `json:"invite_link,omitempty"`
}

// GroupListResponse is the response for GET /accounts/{id}/groups.
type GroupListResponse struct {
	Groups []GroupInfo `json:"groups"`
	Total  int         `json:"total"`
}

// CreateGroupRequest is the JSON body for POST /accounts/{id}/groups.
// Participants can be JIDs ("919999@s.whatsapp.net") or phone numbers ("919999").
// Phone numbers are auto-resolved to JIDs.
type CreateGroupRequest struct {
	Name         string   `json:"name"`
	Participants []string `json:"participants"`
}

// UpdateGroupRequest is the JSON body for PATCH /accounts/{id}/groups/{jid}.
type UpdateGroupRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Locked      *bool   `json:"locked,omitempty"`
	Announce    *bool   `json:"announce,omitempty"`
}

// GroupParticipantsRequest is the JSON body for POST /accounts/{id}/groups/{jid}/participants.
// Participants can be JIDs ("919999@s.whatsapp.net") or phone numbers ("919999").
// Phone numbers are auto-resolved to JIDs.
type GroupParticipantsRequest struct {
	Participants []string `json:"participants"`
	Action       string   `json:"action"` // add, remove, promote, demote
}

// GroupInviteLinkResponse is the response for GET /accounts/{id}/groups/{jid}/invite.
type GroupInviteLinkResponse struct {
	InviteLink string `json:"invite_link"`
}

// ──────────────────────────────────────────────────────
// Presence
// ──────────────────────────────────────────────────────

// PresenceRequest is the JSON body for POST /accounts/{id}/presence.
type PresenceRequest struct {
	// For chat typing: "composing" or "paused". For global: "available" or "unavailable".
	State string `json:"state"`
	// Optional chat JID for typing indicators. Omit for global presence.
	Chat  *string `json:"chat,omitempty"`
	Phone *string `json:"phone,omitempty"` // alternative to chat — auto-resolves to JID
}

// ──────────────────────────────────────────────────────
// Profile
// ──────────────────────────────────────────────────────

// ProfileResponse is returned for GET /accounts/{id}/profile.
type ProfileResponse struct {
	ID           string  `json:"id"`
	PhoneNumber  *string `json:"phone_number"`
	PushName     *string `json:"push_name"`
	BusinessName *string `json:"business_name,omitempty"`
	VerifiedName *string `json:"verified_name,omitempty"`
	About        *string `json:"about"`
	PictureURL   *string `json:"picture_url"`

	// Business-only fields (populated when account is WhatsApp Business).
	IsBusiness       bool                  `json:"is_business"`
	Description      *string               `json:"description,omitempty"`
	Address          *string               `json:"address,omitempty"`
	Email            *string               `json:"email,omitempty"`
	Categories       []ProfileCategory     `json:"categories,omitempty"`
	BusinessHours    *BusinessHoursInfo    `json:"business_hours,omitempty"`
	ProfileOptions   map[string]string     `json:"profile_options,omitempty"`
}

// ProfileCategory is a WhatsApp Business category.
type ProfileCategory struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// BusinessHoursInfo contains business operating hours.
type BusinessHoursInfo struct {
	Timezone string               `json:"timezone"`
	Config   []BusinessHoursSlot  `json:"config"`
}

// BusinessHoursSlot is a single day/slot of business hours.
type BusinessHoursSlot struct {
	DayOfWeek string `json:"day_of_week"`
	Mode      string `json:"mode"`
	OpenTime  string `json:"open_time,omitempty"`
	CloseTime string `json:"close_time,omitempty"`
}

// UpdateProfileRequest is the JSON body for PATCH /accounts/{id}/profile.
type UpdateProfileRequest struct {
	About *string `json:"about,omitempty"`
}

// ──────────────────────────────────────────────────────
// Auth
// ──────────────────────────────────────────────────────

// LoginRequest is the JSON body for POST /api/v1/auth/login.
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// LoginResponse is returned after successful login.
type LoginResponse struct {
	Token     string   `json:"token"`
	ExpiresAt string   `json:"expires_at"`
	User      UserInfo `json:"user"`
}

// RegisterRequest is the JSON body for POST /api/v1/auth/register.
type RegisterRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Email    string `json:"email"`
}

// ForgotPasswordRequest is the JSON body for POST /api/v1/auth/forgot-password.
type ForgotPasswordRequest struct {
	Email string `json:"email"`
}

// ForgotPasswordResponse is returned after a forgot-password request.
type ForgotPasswordResponse struct {
	Message string `json:"message"`
}

// ResetPasswordRequest is the JSON body for POST /api/v1/auth/reset-password.
type ResetPasswordRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

// ──────────────────────────────────────────────────────
// Users
// ──────────────────────────────────────────────────────

// UserInfo is the API-facing user representation. Password never exposed.
type UserInfo struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Email     string `json:"email"`
	RoleID    string `json:"role_id"`
	RoleName  string `json:"role_name"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
}

// CreateUserRequest is the JSON body for POST /api/v1/users.
type CreateUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Email    string `json:"email"`
	RoleID   string `json:"role_id"`
}

// UpdateUserRequest is the JSON body for PATCH /api/v1/users/{id}.
type UpdateUserRequest struct {
	Password *string `json:"password,omitempty"`
	Email    *string `json:"email,omitempty"`
	RoleID   *string `json:"role_id,omitempty"`
	Enabled  *bool   `json:"enabled,omitempty"`
}

// UserListResponse is the response for GET /api/v1/users.
type UserListResponse struct {
	Users []UserInfo `json:"users"`
	Total int        `json:"total"`
}

// ──────────────────────────────────────────────────────
// Roles
// ──────────────────────────────────────────────────────

// RoleInfo is the API-facing role representation.
type RoleInfo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	IsBuiltin   bool     `json:"is_builtin"`
	Permissions []string `json:"permissions"`
	CreatedAt   string   `json:"created_at"`
}

// CreateRoleRequest is the JSON body for POST /api/v1/roles.
type CreateRoleRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
}

// UpdateRoleRequest is the JSON body for PATCH /api/v1/roles/{id}.
type UpdateRoleRequest struct {
	Name        *string  `json:"name,omitempty"`
	Description *string  `json:"description,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
}

// RoleListResponse is the response for GET /api/v1/roles.
type RoleListResponse struct {
	Roles []RoleInfo `json:"roles"`
	Total int        `json:"total"`
}

// ──────────────────────────────────────────────────────
// API Keys
// ──────────────────────────────────────────────────────

// APIKeyInfo is the safe representation of an API key (key value never shown).
type APIKeyInfo struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Prefix    string  `json:"prefix"`
	AccountID *string `json:"account_id,omitempty"`
	ExpiresAt *string `json:"expires_at,omitempty"`
	LastUsed  *string `json:"last_used,omitempty"`
	Enabled   bool    `json:"enabled"`
	CreatedAt string  `json:"created_at"`
}

// CreateAPIKeyRequest is the JSON body for POST /api/v1/api-keys.
type CreateAPIKeyRequest struct {
	Name      string  `json:"name"`
	AccountID *string `json:"account_id,omitempty"` // optional: binds key to a specific account
	ExpiresAt *string `json:"expires_at,omitempty"` // RFC3339, optional
}

// CreateAPIKeyResponse is returned once on creation — the key is never retrievable again.
type CreateAPIKeyResponse struct {
	ID        string  `json:"id"`
	Key       string  `json:"key"`
	Name      string  `json:"name"`
	Prefix    string  `json:"prefix"`
	AccountID *string `json:"account_id,omitempty"`
	ExpiresAt *string `json:"expires_at,omitempty"`
	CreatedAt string  `json:"created_at"`
}

// APIKeyListResponse is the response for GET /api/v1/api-keys.
type APIKeyListResponse struct {
	Keys  []APIKeyInfo `json:"keys"`
	Total int          `json:"total"`
}

// ──────────────────────────────────────────────────────
// Newsletters (Channels)
// ──────────────────────────────────────────────────────

// NewsletterInfo is the API-facing newsletter/channel representation.
type NewsletterInfo struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Description     string  `json:"description,omitempty"`
	SubscriberCount int     `json:"subscriber_count"`
	Verification    string  `json:"verification"`
	PictureURL      *string `json:"picture_url,omitempty"`
	PreviewURL      *string `json:"preview_url,omitempty"`
	InviteCode      string  `json:"invite_code,omitempty"`
	Mute            string  `json:"mute,omitempty"`
	Role            string  `json:"role,omitempty"`
}

// NewsletterListResponse is the response for GET /accounts/{id}/newsletters.
type NewsletterListResponse struct {
	Newsletters []NewsletterInfo `json:"newsletters"`
	Total       int              `json:"total"`
}

// NewsletterMessageInfo is a single newsletter message.
type NewsletterMessageInfo struct {
	ServerID   int            `json:"server_id"`
	MessageID  string         `json:"message_id"`
	Type       string         `json:"type"`
	Body       string         `json:"body,omitempty"`
	Timestamp  string         `json:"timestamp"`
	ViewsCount int            `json:"views_count"`
	Reactions  map[string]int `json:"reactions,omitempty"`
}

// NewsletterMessageListResponse is the response for GET /accounts/{id}/newsletters/{jid}/messages.
type NewsletterMessageListResponse struct {
	Messages []NewsletterMessageInfo `json:"messages"`
	Count    int                     `json:"count"`
}

// FollowNewsletterRequest is the JSON body for POST /accounts/{id}/newsletters/follow.
type FollowNewsletterRequest struct {
	JID string `json:"jid"`
}

// UnfollowNewsletterRequest is the JSON body for POST /accounts/{id}/newsletters/unfollow.
type UnfollowNewsletterRequest struct {
	JID string `json:"jid"`
}

// MuteNewsletterRequest is the JSON body for POST /accounts/{id}/newsletters/{jid}/mute.
type MuteNewsletterRequest struct {
	Mute bool `json:"mute"`
}

// ──────────────────────────────────────────────────────
// Plans & Billing
// ──────────────────────────────────────────────────────

// PlanLimits defines enforceable quotas for a plan.
type PlanLimits struct {
	DailyMessages int  `json:"daily_messages"` // 0 = unlimited
	MaxAccounts   int  `json:"max_accounts"`   // 0 = unlimited
	APIAccess     bool `json:"api_access"`
	MCPAccess     bool `json:"mcp_access"`
	Webhooks      bool `json:"webhooks"`
	Copilot       bool `json:"copilot"`
	Autopilot     bool `json:"autopilot"`
}

// PlanInfo is the API-facing plan representation.
type PlanInfo struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	PriceCents  int        `json:"price_cents"`
	Interval    string     `json:"interval"` // month | year
	Limits      PlanLimits `json:"limits"`
	IsDefault   bool       `json:"is_default"`
}

// PlanListResponse is the response for GET /api/v1/billing/plans.
type PlanListResponse struct {
	Plans []PlanInfo `json:"plans"`
	Total int        `json:"total"`
}

// SubscriptionInfo is the API-facing subscription representation.
type SubscriptionInfo struct {
	ID                 string `json:"id"`
	PlanID             string `json:"plan_id"`
	PlanName           string `json:"plan_name"`
	Status             string `json:"status"` // active | trialing | past_due | canceled
	CurrentPeriodStart string `json:"current_period_start"`
	CurrentPeriodEnd   string `json:"current_period_end"`
	CancelAtPeriodEnd  bool   `json:"cancel_at_period_end"`
}

// UsageInfo is the API-facing daily usage summary.
type UsageInfo struct {
	Date          string `json:"date"`
	MessagesSent  int    `json:"messages_sent"`
	DailyLimit    int    `json:"daily_limit"` // 0 = unlimited
	AccountsUsed  int    `json:"accounts_used"`
	AccountLimit  int    `json:"account_limit"` // 0 = unlimited
}

// BillingResponse is the response for GET /api/v1/billing.
type BillingResponse struct {
	Subscription SubscriptionInfo `json:"subscription"`
	Usage        UsageInfo        `json:"usage"`
	Plan         PlanInfo         `json:"plan"`
}

// CheckoutRequest is the JSON body for POST /api/v1/billing/checkout.
type CheckoutRequest struct {
	PlanID    string `json:"plan_id"`
	SuccessURL string `json:"success_url,omitempty"`
	CancelURL  string `json:"cancel_url,omitempty"`
}

// CheckoutResponse is returned when creating a Stripe Checkout session.
type CheckoutResponse struct {
	URL string `json:"url"`
}

// PortalResponse is returned when creating a Stripe Customer Portal session.
type PortalResponse struct {
	URL string `json:"url"`
}

// QuotaExceededResponse is returned when a plan limit is hit.
type QuotaExceededResponse struct {
	Error      string `json:"error"`
	Limit      int    `json:"limit,omitempty"`
	Used       int    `json:"used,omitempty"`
	UpgradeURL string `json:"upgrade_url,omitempty"`
}

// CreatePlanRequest is the JSON body for POST /api/v1/billing/plans.
type CreatePlanRequest struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	PriceCents  int        `json:"price_cents"`
	Interval    string     `json:"interval"` // month | year
	Limits      PlanLimits `json:"limits"`
	IsDefault   bool       `json:"is_default"`
}

// UpdatePlanRequest is the JSON body for PUT /api/v1/billing/plans/{id}.
type UpdatePlanRequest struct {
	Name        *string     `json:"name,omitempty"`
	Description *string     `json:"description,omitempty"`
	PriceCents  *int        `json:"price_cents,omitempty"`
	Interval    *string     `json:"interval,omitempty"`
	Limits      *PlanLimits `json:"limits,omitempty"`
	IsDefault   *bool       `json:"is_default,omitempty"`
}

// AssignPlanRequest is the JSON body for PUT /api/v1/billing/subscriptions/{user_id}.
type AssignPlanRequest struct {
	PlanID string `json:"plan_id"`
	Status string `json:"status,omitempty"` // active | trialing | canceled (default: active)
}

// AdminSubscriptionInfo extends SubscriptionInfo with user details for admin views.
type AdminSubscriptionInfo struct {
	SubscriptionInfo
	UserID   string `json:"user_id"`
	Username string `json:"username"`
}

// AdminSubscriptionListResponse is the response for GET /api/v1/billing/subscriptions.
type AdminSubscriptionListResponse struct {
	Subscriptions []AdminSubscriptionInfo `json:"subscriptions"`
	Total         int                     `json:"total"`
}

// AdminUsageEntry is a single user's usage for admin views.
type AdminUsageEntry struct {
	UserID       string `json:"user_id"`
	Username     string `json:"username"`
	Date         string `json:"date"`
	MessagesSent int    `json:"messages_sent"`
}

// AdminUsageResponse is the response for GET /api/v1/billing/usage/all.
type AdminUsageResponse struct {
	Usage []AdminUsageEntry `json:"usage"`
	Total int               `json:"total"`
}

// ──────────────────────────────────────────────────────
// Common
// ──────────────────────────────────────────────────────

// ErrorResponse is a JSON error payload.
type ErrorResponse struct {
	Error   string  `json:"error"`
	Message *string `json:"message,omitempty"`
}

