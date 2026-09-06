package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/devstroop/notalk/internal/model"
	"github.com/devstroop/notalk/internal/service"
)

// ── Session ─────────────────────────────────────────

// GetSession — GET /api/v1/accounts/{account_id}/session
// If the account has stored credentials but no active connection, this endpoint
// connects to WhatsApp to verify the session is still valid. A session revoked
// from the phone will be detected and cleaned up automatically.
func (a *API) GetSession(w http.ResponseWriter, r *http.Request) {
	acct := a.requireAccount(w, r)
	if acct == nil {
		return
	}

	// If there's stored session data, connect to verify it's still valid.
	// A revoked session will trigger the LoggedOut event, clearing stale data.
	if acct.HasStoredCredentials() && !acct.IsLoggedIn() {
		_ = acct.EnsureConnected(r.Context())   // best-effort
		time.Sleep(2 * time.Second)              // give the client time to auth or fire LoggedOut
	}

	writeJSON(w, http.StatusOK, acct.StatusResponse())
}

// GetQR — GET /api/v1/accounts/{account_id}/session/qr
func (a *API) GetQR(w http.ResponseWriter, r *http.Request) {
	acct := a.requireAccount(w, r)
	if acct == nil {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	ch, err := acct.GetQR(ctx)
	if err != nil {
		switch err.Error() {
		case "already logged in":
			writeError(w, http.StatusConflict, err.Error())
		case "qr auth already in progress":
			writeError(w, http.StatusConflict, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	select {
	case item := <-ch:
		if item.Error != nil {
			service.DrainQR(ch)
			writeError(w, http.StatusInternalServerError, item.Error.Error())
			return
		}
		if item.Event == "code" {
			// Start draining remaining QR events AFTER we got our code.
			// This prevents the client from disconnecting when the
			// channel buffer fills up with subsequent codes.
			service.DrainQR(ch)

			png, err := qrcode.Encode(item.Code, qrcode.Medium, 512)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to render QR: "+err.Error())
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(png)
			return
		}
		service.DrainQR(ch)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("unexpected qr event: %s", item.Event))
	case <-ctx.Done():
		service.DrainQR(ch)
		writeError(w, http.StatusGatewayTimeout, "timeout waiting for QR code")
	}
}

// PairPhone — POST /api/v1/accounts/{account_id}/session/pair
func (a *API) PairPhone(w http.ResponseWriter, r *http.Request) {
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	code, err := acct.PairPhone(r.Context(), acct.PhoneNumber)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, model.PhoneLinkResponse{LinkingCode: code})
}

// DeleteSession — DELETE /api/v1/accounts/{account_id}/session
func (a *API) DeleteSession(w http.ResponseWriter, r *http.Request) {
	acct := a.requireAccount(w, r)
	if acct == nil {
		return
	}

	if err := acct.Logout(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, model.AccountActionResponse{
		Message:   "unlinked",
		AccountID: acct.ID,
	})
}

// ── Messaging ───────────────────────────────────────

// SendMessage — POST /api/v1/accounts/{account_id}/messages
//
// A single-call endpoint that accepts phone or JID and text via query parameters.
// Files are sent as multipart/form-data body. Text can appear in query (?text=...)
// or in the multipart form field "text" (query takes precedence).
//
// Examples:
//
//	POST /{id}/messages?phone=919999999999&text=Hello
//	POST /{id}/messages?jid=919999999999@s.whatsapp.net&text=Hello
//	POST /{id}/messages?phone=919999999999          (text in body or multipart)
//	POST /{id}/messages?phone=919999999999          (file in multipart, text as caption)
//	POST /{id}/messages  {"chat":"...@s.whatsapp.net","text":"Hello"} (JSON body)
func (a *API) SendMessage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	// ── Read recipient from query params ──
	qJID := q.Get("jid")
	phone := q.Get("phone")

	// ── Pre-read text / body so we can validate before connecting ──
	text := q.Get("text")
	replyTo := q.Get("reply_to")
	ct := r.Header.Get("Content-Type")
	isMultipart := strings.HasPrefix(ct, "multipart/form-data")

	// For non-multipart requests, try parsing JSON body to fill in missing params.
	var jsonReq model.SendMessageRequest
	if !isMultipart {
		if err := readJSON(r, &jsonReq); err == nil {
			if text == "" && jsonReq.Text != nil && *jsonReq.Text != "" {
				text = *jsonReq.Text
			}
			if replyTo == "" && jsonReq.ReplyTo != nil {
				replyTo = *jsonReq.ReplyTo
			}
			// Accept recipient from JSON body when not provided via query.
			if qJID == "" && phone == "" {
				if jsonReq.Chat != "" {
					qJID = jsonReq.Chat
				} else if jsonReq.Phone != "" {
					phone = jsonReq.Phone
				}
			}
		}
	}

	// ── Multipart: handle FormData chat/phone/jid before validation ──
	if isMultipart {
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			writeError(w, http.StatusBadRequest, "parse multipart: "+err.Error())
			return
		}
		if qJID == "" && phone == "" {
			if c := r.FormValue("chat"); c != "" {
				qJID = c
			} else if c := r.FormValue("jid"); c != "" {
				qJID = c
			} else if c := r.FormValue("phone"); c != "" {
				phone = c
			}
		}
		if text == "" {
			text = r.FormValue("text")
		}
		if replyTo == "" {
			replyTo = r.FormValue("reply_to")
		}
	}

	// ── Validate recipient ──
	if qJID == "" && phone == "" {
		writeError(w, http.StatusBadRequest, "phone or jid/chat required (query param or JSON body)")
		return
	}
	if qJID != "" && phone != "" {
		writeError(w, http.StatusBadRequest, "provide phone or jid/chat, not both")
		return
	}

	// If not multipart and still no text, reject early.
	if !isMultipart && text == "" {
		writeError(w, http.StatusBadRequest, "text required (in query ?text=... or JSON body)")
		return
	}

	// ── Now connect (expensive) ────────────────────
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	// ── Resolve recipient ──────────────────────────
	chatJID := qJID
	if phone != "" {
		resolved, err := acct.ResolvePhone(r.Context(), phone)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		chatJID = resolved
	}

	// ── Multipart: may contain file ────
	if isMultipart {

		file, header, err := r.FormFile("file")
		if err == nil {
			defer func() { _ = file.Close() }()
			if header.Size > a.mgr.Config().Limits.MaxUploadSize {
				writeError(w, http.StatusRequestEntityTooLarge, "file too large")
				return
			}
			data, err := io.ReadAll(file)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "read file: "+err.Error())
				return
			}
			var caption *string
			if text != "" {
				caption = &text
			}
			msgID, err := acct.SendMedia(r.Context(), chatJID, data, header.Filename, header.Header.Get("Content-Type"), caption)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
					writeJSON(w, http.StatusOK, model.SendMessageResponse{Status: "sent", MessageID: msgID})
			return
		}

		// No file — send text only
		if text == "" {
			writeError(w, http.StatusBadRequest, "text or file required")
			return
		}
		var msgID string
		var sendErr error
		if replyTo != "" {
			msgID, sendErr = acct.SendReply(r.Context(), chatJID, replyTo, text)
		} else {
			msgID, sendErr = acct.SendMessage(r.Context(), chatJID, text)
		}
		if sendErr != nil {
			writeError(w, http.StatusInternalServerError, sendErr.Error())
			return
		}
			writeJSON(w, http.StatusOK, model.SendMessageResponse{Status: "sent", MessageID: msgID})
		return
	}

	// ── Non-multipart: text already resolved above ──
	var msgID string
	var err error
	if replyTo != "" {
		msgID, err = acct.SendReply(r.Context(), chatJID, replyTo, text)
	} else {
		msgID, err = acct.SendMessage(r.Context(), chatJID, text)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, model.SendMessageResponse{Status: "sent", MessageID: msgID})
}

// ReactMessage — POST /api/v1/accounts/{account_id}/messages/reactions
func (a *API) ReactMessage(w http.ResponseWriter, r *http.Request) {
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	var req model.ReactionRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	chat, ok := resolveRecipient(w, r, acct, req.Chat, req.Phone)
	if !ok {
		return
	}
	if req.MessageID == "" || req.Emoji == "" {
		writeError(w, http.StatusBadRequest, "message_id and emoji required")
		return
	}

	if err := acct.SendReaction(r.Context(), chat, req.MessageID, req.Emoji); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"message_id": req.MessageID,
		"emoji":      req.Emoji,
	})
}

// MarkRead — POST /api/v1/accounts/{account_id}/messages/mark-read
func (a *API) MarkRead(w http.ResponseWriter, r *http.Request) {
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	var req model.MarkReadRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	chat, ok := resolveRecipient(w, r, acct, req.Chat, req.Phone)
	if !ok {
		return
	}
	if len(req.MessageIDs) == 0 {
		writeError(w, http.StatusBadRequest, "message_ids required")
		return
	}

	if err := acct.MarkRead(r.Context(), chat, req.MessageIDs); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":       true,
		"chat":          chat,
		"messages_read": len(req.MessageIDs),
	})
}

// ── Chats ───────────────────────────────────────────

// ListChats — GET /api/v1/accounts/{account_id}/chats
func (a *API) ListChats(w http.ResponseWriter, r *http.Request) {
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	chats, err := acct.ListChats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, model.ChatListResponse{
		Chats: chats,
		Total: len(chats),
	})
}

// ── Contacts ────────────────────────────────────────

// ListContacts — GET /api/v1/accounts/{account_id}/contacts
func (a *API) ListContacts(w http.ResponseWriter, r *http.Request) {
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	contacts, err := acct.ListContacts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, model.ContactListResponse{
		Contacts: contacts,
		Total:    len(contacts),
	})
}

// CheckContacts — POST /api/v1/accounts/{account_id}/contacts/check
func (a *API) CheckContacts(w http.ResponseWriter, r *http.Request) {
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	var req model.CheckContactsRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	if len(req.Phones) == 0 {
		writeError(w, http.StatusBadRequest, "phones required")
		return
	}

	results, err := acct.CheckContacts(r.Context(), req.Phones)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, model.CheckContactsResponse{Results: results})
}

// GetContact — GET /api/v1/accounts/{account_id}/contacts/{jid}
// Also accepts ?phone=NUM (skips path param).
func (a *API) GetContact(w http.ResponseWriter, r *http.Request) {
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	jid := r.PathValue("jid")
	phone := r.URL.Query().Get("phone")
	if phone != "" {
		resolved, err := acct.ResolvePhone(r.Context(), phone)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		jid = resolved
	}

	info, err := acct.GetContactInfo(r.Context(), jid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, info)
}

// ── Groups ──────────────────────────────────────────

// ListGroups — GET /api/v1/accounts/{account_id}/groups
func (a *API) ListGroups(w http.ResponseWriter, r *http.Request) {
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	groups, err := acct.ListGroups(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, model.GroupListResponse{
		Groups: groups,
		Total:  len(groups),
	})
}

// CreateGroup — POST /api/v1/accounts/{account_id}/groups
// Participants can be JIDs or phone numbers (auto-resolved).
func (a *API) CreateGroup(w http.ResponseWriter, r *http.Request) {
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	var req model.CreateGroupRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}

	resolved, err := resolveParticipants(r.Context(), acct, req.Participants)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	info, err := acct.CreateGroup(r.Context(), req.Name, resolved)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, info)
}

// GetGroup — GET /api/v1/accounts/{account_id}/groups/{jid}
func (a *API) GetGroup(w http.ResponseWriter, r *http.Request) {
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	info, err := acct.GetGroupInfo(r.Context(), r.PathValue("jid"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, info)
}

// UpdateGroup — PATCH /api/v1/accounts/{account_id}/groups/{jid}
func (a *API) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	var req model.UpdateGroupRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	if err := acct.UpdateGroup(r.Context(), r.PathValue("jid"), req); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Fetch updated group info
	info, err := acct.GetGroupInfo(r.Context(), r.PathValue("jid"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, info)
}

// LeaveGroup — DELETE /api/v1/accounts/{account_id}/groups/{jid}
func (a *API) LeaveGroup(w http.ResponseWriter, r *http.Request) {
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	if err := acct.LeaveGroup(r.Context(), r.PathValue("jid")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// GetGroupInvite — GET /api/v1/accounts/{account_id}/groups/{jid}/invite
func (a *API) GetGroupInvite(w http.ResponseWriter, r *http.Request) {
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	reset := r.URL.Query().Get("reset") == "true"
	link, err := acct.GetGroupInviteLink(r.Context(), r.PathValue("jid"), reset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, model.GroupInviteLinkResponse{InviteLink: link})
}

// UpdateGroupParticipants — POST /api/v1/accounts/{account_id}/groups/{jid}/participants
// Participants can be JIDs or phone numbers (auto-resolved).
func (a *API) UpdateGroupParticipants(w http.ResponseWriter, r *http.Request) {
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	var req model.GroupParticipantsRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	if len(req.Participants) == 0 || req.Action == "" {
		writeError(w, http.StatusBadRequest, "participants and action required")
		return
	}

	resolved, err := resolveParticipants(r.Context(), acct, req.Participants)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := acct.UpdateGroupParticipants(r.Context(), r.PathValue("jid"), resolved, req.Action); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"success": true, "action": req.Action})
}

// ── Presence ────────────────────────────────────────

// SendPresence — POST /api/v1/accounts/{account_id}/presence
func (a *API) SendPresence(w http.ResponseWriter, r *http.Request) {
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	var req model.PresenceRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	// Validate state
	switch req.State {
	case "composing", "paused", "available", "unavailable":
		// ok
	default:
		writeError(w, http.StatusBadRequest, "invalid state: must be one of composing, paused, available, unavailable")
		return
	}

	// Resolve chat from phone if provided
	var chatTarget string
	if req.Phone != nil && *req.Phone != "" {
		resolved, err := acct.ResolvePhone(r.Context(), *req.Phone)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		chatTarget = resolved
	} else if req.Chat != nil && *req.Chat != "" {
		chatTarget = *req.Chat
	}

	// Chat-level typing indicator
	if chatTarget != "" {
		if err := acct.SendChatPresence(r.Context(), chatTarget, req.State); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "chat": chatTarget, "state": req.State})
		return
	}

	// Global presence
	if err := acct.SendPresence(r.Context(), req.State); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"success": true, "state": req.State})
}

// ── Profile ─────────────────────────────────────────

// GetProfile — GET /api/v1/accounts/{account_id}/profile
func (a *API) GetProfile(w http.ResponseWriter, r *http.Request) {
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	profile, err := acct.GetProfile(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, profile)
}

// UpdateProfile — PATCH /api/v1/accounts/{account_id}/profile
func (a *API) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	var req model.UpdateProfileRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	if req.About != nil {
		if err := acct.SetStatusMessage(r.Context(), *req.About); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	profile, err := acct.GetProfile(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, profile)
}

// RevokeMessage — DELETE /api/v1/accounts/{account_id}/messages/{message_id}
// Accepts ?chat=JID or ?phone=NUM.
func (a *API) RevokeMessage(w http.ResponseWriter, r *http.Request) {
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	messageID := r.PathValue("message_id")
	chat, ok := resolveRecipientQuery(w, r, acct)
	if !ok {
		return
	}

	resp, err := acct.RevokeMessage(r.Context(), chat, messageID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// ── Newsletters (Channels) ──────────────────────────

// ListNewsletters — GET /api/v1/accounts/{account_id}/newsletters
func (a *API) ListNewsletters(w http.ResponseWriter, r *http.Request) {
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	newsletters, err := acct.ListNewsletters(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, model.NewsletterListResponse{
		Newsletters: newsletters,
		Total:       len(newsletters),
	})
}

// GetNewsletter — GET /api/v1/accounts/{account_id}/newsletters/{jid}
func (a *API) GetNewsletter(w http.ResponseWriter, r *http.Request) {
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	info, err := acct.GetNewsletterInfo(r.Context(), r.PathValue("jid"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, info)
}

// FollowNewsletter — POST /api/v1/accounts/{account_id}/newsletters/follow
func (a *API) FollowNewsletter(w http.ResponseWriter, r *http.Request) {
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	var req model.FollowNewsletterRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if req.JID == "" {
		writeError(w, http.StatusBadRequest, "jid required")
		return
	}

	if err := acct.FollowNewsletter(r.Context(), req.JID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"success": true, "jid": req.JID})
}

// UnfollowNewsletter — POST /api/v1/accounts/{account_id}/newsletters/unfollow
func (a *API) UnfollowNewsletter(w http.ResponseWriter, r *http.Request) {
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	var req model.UnfollowNewsletterRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if req.JID == "" {
		writeError(w, http.StatusBadRequest, "jid required")
		return
	}

	if err := acct.UnfollowNewsletter(r.Context(), req.JID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"success": true, "jid": req.JID})
}

// GetNewsletterMessages — GET /api/v1/accounts/{account_id}/newsletters/{jid}/messages?count=...&before=...
func (a *API) GetNewsletterMessages(w http.ResponseWriter, r *http.Request) {
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	count := 50
	if c := r.URL.Query().Get("count"); c != "" {
		if n, err := strconv.Atoi(c); err == nil && n > 0 {
			count = n
		}
	}
	if count > 500 {
		count = 500
	}

	before := 0
	if b := r.URL.Query().Get("before"); b != "" {
		if n, err := strconv.Atoi(b); err == nil && n > 0 {
			before = n
		}
	}

	resp, err := acct.GetNewsletterMessages(r.Context(), r.PathValue("jid"), count, before)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// MuteNewsletter — POST /api/v1/accounts/{account_id}/newsletters/{jid}/mute
func (a *API) MuteNewsletter(w http.ResponseWriter, r *http.Request) {
	acct := a.requireConnectedAccount(w, r)
	if acct == nil {
		return
	}

	var req model.MuteNewsletterRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	if err := acct.ToggleMuteNewsletter(r.Context(), r.PathValue("jid"), req.Mute); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"success": true, "muted": req.Mute})
}
