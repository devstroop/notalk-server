package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/devstroop/notalk/internal/database"
	"github.com/devstroop/notalk/internal/middleware"
	"github.com/devstroop/notalk/internal/model"
	"github.com/google/uuid"
)

// BillingHandler handles billing and subscription APIs.
type BillingHandler struct {
	db *database.DB
}

func NewBillingHandler(db *database.DB) *BillingHandler {
	return &BillingHandler{db: db}
}

// ListPlans — GET /api/v1/billing/plans
func (h *BillingHandler) ListPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := h.db.ListPlans()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to list plans"})
		return
	}
	out := make([]model.PlanInfo, 0, len(plans))
	for _, p := range plans {
		out = append(out, model.PlanInfo{
			ID:          p.ID,
			Name:        p.Name,
			Description: p.Description,
			PriceCents:  p.PriceCents,
			Interval:    p.Interval,
			Limits:      p.PlanLimits(),
			IsDefault:   p.IsDefault,
		})
	}
	writeJSON(w, http.StatusOK, model.PlanListResponse{Plans: out, Total: len(out)})
}

// CreatePlan — POST /api/v1/billing/plans
func (h *BillingHandler) CreatePlan(w http.ResponseWriter, r *http.Request) {
	identity := middleware.GetIdentity(r)
	if !identity.HasPermission("*") {
		writeJSON(w, http.StatusForbidden, model.ErrorResponse{Error: "admin required"})
		return
	}
	var req model.CreatePlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "invalid request"})
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "name is required"})
		return
	}
	limitsJSON, _ := json.Marshal(req.Limits)
	rec := &database.PlanRecord{
		ID:          req.ID,
		Name:        req.Name,
		Description: req.Description,
		PriceCents:  req.PriceCents,
		Interval:    req.Interval,
		Limits:      string(limitsJSON),
		IsDefault:   req.IsDefault,
	}
	if rec.ID == "" {
		rec.ID = uuid.NewString()
	}
	if rec.Interval == "" {
		rec.Interval = "month"
	}
	if err := h.db.CreatePlan(rec); err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: err.Error()})
		return
	}
	created, _ := h.db.GetPlan(rec.ID)
	var limits model.PlanLimits
	if created != nil {
		limits = created.PlanLimits()
	} else {
		limits = req.Limits
	}
	writeJSON(w, http.StatusCreated, model.PlanInfo{
		ID:          rec.ID,
		Name:        rec.Name,
		Description: rec.Description,
		PriceCents:  rec.PriceCents,
		Interval:    rec.Interval,
		Limits:      limits,
		IsDefault:   rec.IsDefault,
	})
}

// UpdatePlan — PATCH /api/v1/billing/plans/{id}
func (h *BillingHandler) UpdatePlan(w http.ResponseWriter, r *http.Request) {
	identity := middleware.GetIdentity(r)
	if !identity.HasPermission("*") {
		writeJSON(w, http.StatusForbidden, model.ErrorResponse{Error: "admin required"})
		return
	}
	id := r.PathValue("id")
	if id == "" {
		id = r.PathValue("plan_id")
	}
	existing, err := h.db.GetPlan(id)
	if err != nil || existing == nil {
		writeJSON(w, http.StatusNotFound, model.ErrorResponse{Error: "plan not found"})
		return
	}
	var req model.UpdatePlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "invalid request"})
		return
	}
	// Apply updates
	if req.Name != nil {
		existing.Name = *req.Name
	}
	if req.Description != nil {
		existing.Description = *req.Description
	}
	if req.PriceCents != nil {
		existing.PriceCents = *req.PriceCents
	}
	if req.Interval != nil {
		existing.Interval = *req.Interval
	}
	if req.Limits != nil {
		b, _ := json.Marshal(req.Limits)
		existing.Limits = string(b)
	}
	if req.IsDefault != nil {
		existing.IsDefault = *req.IsDefault
	}
	if err := h.db.UpdatePlan(existing); err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, model.PlanInfo{
		ID:          existing.ID,
		Name:        existing.Name,
		Description: existing.Description,
		PriceCents:  existing.PriceCents,
		Interval:    existing.Interval,
		Limits:      existing.PlanLimits(),
		IsDefault:   existing.IsDefault,
	})
}

// DeletePlan — DELETE /api/v1/billing/plans/{id}
func (h *BillingHandler) DeletePlan(w http.ResponseWriter, r *http.Request) {
	identity := middleware.GetIdentity(r)
	if !identity.HasPermission("*") {
		writeJSON(w, http.StatusForbidden, model.ErrorResponse{Error: "admin required"})
		return
	}
	id := r.PathValue("id")
	if id == "" {
		id = r.PathValue("plan_id")
	}
	if err := h.db.DeletePlan(id); err != nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ListSubscriptions — GET /api/v1/billing/subscriptions
func (h *BillingHandler) ListSubscriptions(w http.ResponseWriter, r *http.Request) {
	identity := middleware.GetIdentity(r)
	if !identity.HasPermission("*") {
		writeJSON(w, http.StatusForbidden, model.ErrorResponse{Error: "admin required"})
		return
	}
	subs, err := h.db.ListSubscriptions()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to list subscriptions"})
		return
	}
	out := make([]model.AdminSubscriptionInfo, 0, len(subs))
	for _, s := range subs {
		username := s.UserID
		if u, err := h.db.GetUser(s.UserID); err == nil && u != nil {
			username = u.Username
		}
		planName := s.PlanID
		if p, err := h.db.GetPlan(s.PlanID); err == nil && p != nil {
			planName = p.Name
		}
		out = append(out, model.AdminSubscriptionInfo{
			SubscriptionInfo: model.SubscriptionInfo{
				ID:                 s.ID,
				PlanID:             s.PlanID,
				PlanName:           planName,
				Status:             s.Status,
				CurrentPeriodStart: s.CurrentPeriodStart,
				CurrentPeriodEnd:   s.CurrentPeriodEnd,
				CancelAtPeriodEnd:  s.CancelAtPeriodEnd,
			},
			UserID:   s.UserID,
			Username: username,
		})
	}
	writeJSON(w, http.StatusOK, model.AdminSubscriptionListResponse{Subscriptions: out, Total: len(out)})
}

// AssignPlan — POST /api/v1/billing/subscriptions/{user_id}/assign
func (h *BillingHandler) AssignPlan(w http.ResponseWriter, r *http.Request) {
	identity := middleware.GetIdentity(r)
	if !identity.HasPermission("*") {
		writeJSON(w, http.StatusForbidden, model.ErrorResponse{Error: "admin required"})
		return
	}
	userID := r.PathValue("user_id")
	if userID == "" {
		userID = r.PathValue("id")
	}
	var req model.AssignPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Try form
		_ = r.ParseForm()
		req.PlanID = r.FormValue("plan_id")
		if req.PlanID == "" {
			writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "plan_id is required"})
			return
		}
	}
	if req.PlanID == "" {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "plan_id is required"})
		return
	}
	if req.Status == "" {
		req.Status = "active"
	}
	now := time.Now().UTC()
	rec := &database.SubscriptionRecord{
		ID:                 uuid.NewString(),
		UserID:             userID,
		PlanID:             req.PlanID,
		Status:             req.Status,
		CurrentPeriodStart: now.Format(time.RFC3339),
		CurrentPeriodEnd:   now.AddDate(0, 1, 0).Format(time.RFC3339),
		CreatedAt:          now.Format(time.RFC3339),
	}
	if err := h.db.UpsertSubscription(rec); err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "assigned"})
}

// DeleteSubscription — DELETE /api/v1/billing/subscriptions/{user_id}
func (h *BillingHandler) DeleteSubscription(w http.ResponseWriter, r *http.Request) {
	identity := middleware.GetIdentity(r)
	if !identity.HasPermission("*") {
		writeJSON(w, http.StatusForbidden, model.ErrorResponse{Error: "admin required"})
		return
	}
	userID := r.PathValue("user_id")
	if userID == "" {
		userID = r.PathValue("id")
	}
	if err := h.db.DeleteSubscription(userID); err != nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// GetUsage — GET /api/v1/billing/usage
func (h *BillingHandler) GetUsage(w http.ResponseWriter, r *http.Request) {
	identity := middleware.GetIdentity(r)
	if !identity.HasPermission("*") {
		writeJSON(w, http.StatusForbidden, model.ErrorResponse{Error: "admin required"})
		return
	}
	usage, err := h.db.GetAllDailyUsage()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "failed to get usage"})
		return
	}
	out := make([]model.AdminUsageEntry, 0, len(usage))
	for _, u := range usage {
		username := u.UserID
		if usr, err := h.db.GetUser(u.UserID); err == nil && usr != nil {
			username = usr.Username
		}
		out = append(out, model.AdminUsageEntry{
			UserID:       u.UserID,
			Username:     username,
			Date:         u.Date,
			MessagesSent: u.Messages,
		})
	}
	writeJSON(w, http.StatusOK, model.AdminUsageResponse{Usage: out, Total: len(out)})
}

// GetBillingConfig — GET /api/v1/billing/config
func (h *BillingHandler) GetBillingConfig(w http.ResponseWriter, r *http.Request) {
	identity := middleware.GetIdentity(r)
	if !identity.HasPermission("*") {
		writeJSON(w, http.StatusForbidden, model.ErrorResponse{Error: "admin required"})
		return
	}
	billingEnabled := h.db.GetSettingBool("billing.enabled", false)
	activeGateway := h.db.GetSetting("billing.active_gateway", "")
	stripeKey := h.db.GetSetting("billing.stripe_secret_key", "")
	stripeWebhook := h.db.GetSetting("billing.stripe_webhook_secret", "")
	razorpayKey := h.db.GetSetting("billing.razorpay_key_id", "")
	razorpaySecret := h.db.GetSetting("billing.razorpay_key_secret", "")
	payuKey := h.db.GetSetting("billing.payu_merchant_key", "")
	payuSalt := h.db.GetSetting("billing.payu_merchant_salt", "")
	writeJSON(w, http.StatusOK, map[string]any{
		"billing_enabled":    billingEnabled,
		"active_gateway":     activeGateway,
		"stripe_key_set":     stripeKey != "",
		"stripe_webhook_set": stripeWebhook != "",
		"razorpay_key_set":   razorpayKey != "",
		"razorpay_secret_set": razorpaySecret != "",
		"payu_key_set":       payuKey != "",
		"payu_salt_set":      payuSalt != "",
	})
}
