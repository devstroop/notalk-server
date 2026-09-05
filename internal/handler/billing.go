package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/devstroop/notalk/internal/database"
	"github.com/devstroop/notalk/internal/middleware"
	"github.com/devstroop/notalk/internal/model"
)

// BillingHandler handles plan, subscription, and usage endpoints.
type BillingHandler struct {
	db *database.DB
}

// NewBillingHandler creates a new billing handler.
func NewBillingHandler(db *database.DB) *BillingHandler {
	return &BillingHandler{db: db}
}

func planRecordToInfo(rec *database.PlanRecord) model.PlanInfo {
	return model.PlanInfo{
		ID:          rec.ID,
		Name:        rec.Name,
		Description: rec.Description,
		PriceCents:  rec.PriceCents,
		Interval:    rec.Interval,
		Limits:      rec.PlanLimits(),
		IsDefault:   rec.IsDefault,
	}
}

// ListPlans — GET /api/v1/billing/plans (public, no auth required)
func (h *BillingHandler) ListPlans(w http.ResponseWriter, r *http.Request) {
	recs, err := h.db.ListPlans()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list plans")
		return
	}

	plans := make([]model.PlanInfo, 0, len(recs))
	for _, rec := range recs {
		plans = append(plans, planRecordToInfo(rec))
	}

	writeJSON(w, http.StatusOK, model.PlanListResponse{Plans: plans, Total: len(plans)})
}

// GetBilling — GET /api/v1/billing
func (h *BillingHandler) GetBilling(w http.ResponseWriter, r *http.Request) {
	id := middleware.GetIdentity(r)
	if id == nil {
		writeError(w, http.StatusUnauthorized, "missing authorization")
		return
	}

	limits, planID, _ := h.db.GetUserPlanLimits(id.UserID)

	plan, err := h.db.GetPlan(planID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get plan")
		return
	}

	sub, _ := h.db.GetSubscription(id.UserID)
	subInfo := model.SubscriptionInfo{PlanID: planID, PlanName: plan.Name, Status: "active"}
	if sub != nil {
		subInfo = model.SubscriptionInfo{
			ID:                 sub.ID,
			PlanID:             sub.PlanID,
			PlanName:           plan.Name,
			Status:             sub.Status,
			CurrentPeriodStart: sub.CurrentPeriodStart,
			CurrentPeriodEnd:   sub.CurrentPeriodEnd,
			CancelAtPeriodEnd:  sub.CancelAtPeriodEnd,
		}
	}

	dailyUsage, _ := h.db.GetDailyUsage(id.UserID)
	acctCount, _ := h.db.CountUserAccounts(id.UserID)

	usage := model.UsageInfo{
		Date:         time.Now().UTC().Format("2006-01-02"),
		MessagesSent: dailyUsage,
		DailyLimit:   limits.DailyMessages,
		AccountsUsed: acctCount,
		AccountLimit: limits.MaxAccounts,
	}

	writeJSON(w, http.StatusOK, model.BillingResponse{
		Subscription: subInfo,
		Usage:        usage,
		Plan:         planRecordToInfo(plan),
	})
}

// GetUsage — GET /api/v1/billing/usage
func (h *BillingHandler) GetUsage(w http.ResponseWriter, r *http.Request) {
	id := middleware.GetIdentity(r)
	if id == nil {
		writeError(w, http.StatusUnauthorized, "missing authorization")
		return
	}

	limits, _, _ := h.db.GetUserPlanLimits(id.UserID)
	dailyUsage, _ := h.db.GetDailyUsage(id.UserID)
	acctCount, _ := h.db.CountUserAccounts(id.UserID)

	writeJSON(w, http.StatusOK, model.UsageInfo{
		Date:         time.Now().UTC().Format("2006-01-02"),
		MessagesSent: dailyUsage,
		DailyLimit:   limits.DailyMessages,
		AccountsUsed: acctCount,
		AccountLimit: limits.MaxAccounts,
	})
}

// ── Admin plan endpoints ────────────────────────────

// CreatePlan — POST /api/v1/billing/plans
func (h *BillingHandler) CreatePlan(w http.ResponseWriter, r *http.Request) {
	var req model.CreatePlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Interval == "" {
		req.Interval = "month"
	}

	limitsJSON, _ := json.Marshal(req.Limits)
	id := req.ID
	if id == "" {
		id = strings.ToLower(strings.ReplaceAll(req.Name, " ", "-"))
	}

	rec := &database.PlanRecord{
		ID:          id,
		Name:        req.Name,
		Description: req.Description,
		PriceCents:  req.PriceCents,
		Interval:    req.Interval,
		Limits:      string(limitsJSON),
		IsDefault:   req.IsDefault,
	}
	if err := h.db.CreatePlan(rec); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, planRecordToInfo(rec))
}

// GetPlan — GET /api/v1/billing/plans/{plan_id}
func (h *BillingHandler) GetPlan(w http.ResponseWriter, r *http.Request) {
	planID := r.PathValue("plan_id")
	rec, err := h.db.GetPlan(planID)
	if err != nil {
		writeError(w, http.StatusNotFound, "plan not found")
		return
	}
	writeJSON(w, http.StatusOK, planRecordToInfo(rec))
}

// UpdatePlan — PUT /api/v1/billing/plans/{plan_id}
func (h *BillingHandler) UpdatePlan(w http.ResponseWriter, r *http.Request) {
	planID := r.PathValue("plan_id")
	rec, err := h.db.GetPlan(planID)
	if err != nil {
		writeError(w, http.StatusNotFound, "plan not found")
		return
	}

	var req model.UpdatePlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.Name != nil {
		rec.Name = *req.Name
	}
	if req.Description != nil {
		rec.Description = *req.Description
	}
	if req.PriceCents != nil {
		rec.PriceCents = *req.PriceCents
	}
	if req.Interval != nil {
		rec.Interval = *req.Interval
	}
	if req.Limits != nil {
		limitsJSON, _ := json.Marshal(*req.Limits)
		rec.Limits = string(limitsJSON)
	}
	if req.IsDefault != nil {
		rec.IsDefault = *req.IsDefault
	}

	if err := h.db.UpdatePlan(rec); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, planRecordToInfo(rec))
}

// DeletePlan — DELETE /api/v1/billing/plans/{plan_id}
func (h *BillingHandler) DeletePlan(w http.ResponseWriter, r *http.Request) {
	planID := r.PathValue("plan_id")
	if err := h.db.DeletePlan(planID); err != nil {
		if strings.Contains(err.Error(), "active subscriptions") {
			writeError(w, http.StatusConflict, err.Error())
		} else {
			writeError(w, http.StatusNotFound, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ── Admin subscription endpoints ────────────────────

// ListSubscriptions — GET /api/v1/billing/subscriptions
func (h *BillingHandler) ListSubscriptions(w http.ResponseWriter, r *http.Request) {
	subs, err := h.db.ListSubscriptions()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list subscriptions")
		return
	}

	users, _ := h.db.ListUsers()
	userMap := make(map[string]string, len(users))
	for _, u := range users {
		userMap[u.ID] = u.Username
	}

	result := make([]model.AdminSubscriptionInfo, 0, len(subs))
	for _, sub := range subs {
		plan, _ := h.db.GetPlan(sub.PlanID)
		planName := sub.PlanID
		if plan != nil {
			planName = plan.Name
		}
		result = append(result, model.AdminSubscriptionInfo{
			SubscriptionInfo: model.SubscriptionInfo{
				ID:                 sub.ID,
				PlanID:             sub.PlanID,
				PlanName:           planName,
				Status:             sub.Status,
				CurrentPeriodStart: sub.CurrentPeriodStart,
				CurrentPeriodEnd:   sub.CurrentPeriodEnd,
				CancelAtPeriodEnd:  sub.CancelAtPeriodEnd,
			},
			UserID:   sub.UserID,
			Username: userMap[sub.UserID],
		})
	}

	writeJSON(w, http.StatusOK, model.AdminSubscriptionListResponse{Subscriptions: result, Total: len(result)})
}

// AssignPlan — PUT /api/v1/billing/subscriptions/{user_id}
func (h *BillingHandler) AssignPlan(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("user_id")

	var req model.AssignPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.PlanID == "" {
		writeError(w, http.StatusBadRequest, "plan_id is required")
		return
	}

	// Verify plan exists
	if _, err := h.db.GetPlan(req.PlanID); err != nil {
		writeError(w, http.StatusNotFound, "plan not found")
		return
	}

	status := req.Status
	if status == "" {
		status = "active"
	}

	now := time.Now().UTC()
	rec := &database.SubscriptionRecord{
		ID:                 database.GenerateID(),
		UserID:             userID,
		PlanID:             req.PlanID,
		Status:             status,
		CurrentPeriodStart: now.Format(time.RFC3339),
		CurrentPeriodEnd:   now.AddDate(0, 1, 0).Format(time.RFC3339),
		CreatedAt:          now.Format(time.RFC3339),
	}
	if err := h.db.UpsertSubscription(rec); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "assigned", "plan_id": req.PlanID, "user_id": userID})
}

// DeleteSubscription — DELETE /api/v1/billing/subscriptions/{user_id}
func (h *BillingHandler) DeleteSubscription(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("user_id")
	if err := h.db.DeleteSubscription(userID); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ── Admin usage endpoint ────────────────────────────

// ListAllUsage — GET /api/v1/billing/usage/all
func (h *BillingHandler) ListAllUsage(w http.ResponseWriter, r *http.Request) {
	records, err := h.db.GetAllDailyUsage()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get usage")
		return
	}

	users, _ := h.db.ListUsers()
	userMap := make(map[string]string, len(users))
	for _, u := range users {
		userMap[u.ID] = u.Username
	}

	result := make([]model.AdminUsageEntry, 0, len(records))
	for _, rec := range records {
		result = append(result, model.AdminUsageEntry{
			UserID:       rec.UserID,
			Username:     userMap[rec.UserID],
			Date:         rec.Date,
			MessagesSent: rec.Messages,
		})
	}

	writeJSON(w, http.StatusOK, model.AdminUsageResponse{Usage: result, Total: len(result)})
}

// ── Route registration ──────────────────────────────

// RegisterBillingRoutes wires billing endpoints into the mux.
func RegisterBillingRoutes(publicMux *http.ServeMux, authedMux *http.ServeMux, db *database.DB) {
	billing := NewBillingHandler(db)
	perm := middleware.RequirePermission

	// Public (no auth) — browsing available plans
	publicMux.HandleFunc("GET /api/v1/billing/plans", billing.ListPlans)

	// Authenticated (user)
	authedMux.HandleFunc("GET /api/v1/billing", billing.GetBilling)
	authedMux.HandleFunc("GET /api/v1/billing/usage", billing.GetUsage)

	// Admin — plan CRUD
	authedMux.HandleFunc("POST /api/v1/billing/plans", perm("*", billing.CreatePlan))
	authedMux.HandleFunc("GET /api/v1/billing/plans/{plan_id}", perm("*", billing.GetPlan))
	authedMux.HandleFunc("PUT /api/v1/billing/plans/{plan_id}", perm("*", billing.UpdatePlan))
	authedMux.HandleFunc("DELETE /api/v1/billing/plans/{plan_id}", perm("*", billing.DeletePlan))

	// Admin — subscription management
	authedMux.HandleFunc("GET /api/v1/billing/subscriptions", perm("*", billing.ListSubscriptions))
	authedMux.HandleFunc("PUT /api/v1/billing/subscriptions/{user_id}", perm("*", billing.AssignPlan))
	authedMux.HandleFunc("DELETE /api/v1/billing/subscriptions/{user_id}", perm("*", billing.DeleteSubscription))

	// Admin — usage overview
	authedMux.HandleFunc("GET /api/v1/billing/usage/all", perm("*", billing.ListAllUsage))
}
