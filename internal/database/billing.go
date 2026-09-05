package database

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/devstroop/notalk/internal/model"
	"github.com/google/uuid"
)

func GenerateID() string { return uuid.New().String() }
func generateID() string  { return GenerateID() }

// PlanRecord is the persistent plan row.
type PlanRecord struct {
	ID          string
	Name        string
	Description string
	PriceCents  int
	Interval    string // month | year
	Limits      string // JSON blob
	IsDefault   bool
	CreatedAt   string
}

// SubscriptionRecord is the persistent subscription row.
type SubscriptionRecord struct {
	ID                 string
	UserID             string
	PlanID             string
	Status             string // active | trialing | past_due | canceled
	StripeSubID        *string
	StripeCustomerID   *string
	CurrentPeriodStart string
	CurrentPeriodEnd   string
	CancelAtPeriodEnd  bool
	CreatedAt          string
	UpdatedAt          string
}

// ── Schema migrations ───────────────────────────────

// migrateBilling creates billing tables. Called from migrate() which already holds the lock.
func (d *DB) migrateBilling() error {
	_, err := d.db.Exec(`
		CREATE TABLE IF NOT EXISTS plan (
			id          TEXT PRIMARY KEY,
			name        TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			price_cents INTEGER NOT NULL DEFAULT 0,
			interval    TEXT NOT NULL DEFAULT 'month',
			limits      TEXT NOT NULL DEFAULT '{}',
			is_default  BOOLEAN NOT NULL DEFAULT FALSE,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
	`)
	if err != nil {
		return err
	}

	_, err = d.db.Exec(`
		CREATE TABLE IF NOT EXISTS subscription (
			id                   TEXT PRIMARY KEY,
			user_id              TEXT NOT NULL UNIQUE REFERENCES app_user(id) ON DELETE CASCADE,
			plan_id              TEXT NOT NULL REFERENCES plan(id),
			status               TEXT NOT NULL DEFAULT 'active',
			stripe_sub_id        TEXT,
			stripe_customer_id   TEXT,
			current_period_start TEXT NOT NULL,
			current_period_end   TEXT NOT NULL,
			cancel_at_period_end BOOLEAN NOT NULL DEFAULT FALSE,
			created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
	`)
	if err != nil {
		return err
	}

	_, err = d.db.Exec(`
		CREATE TABLE IF NOT EXISTS usage (
			user_id  TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
			date     TEXT NOT NULL,
			messages INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (user_id, date)
		);
	`)
	if err != nil {
		return err
	}

	// Migration: add plan_id column to app_user if missing
	_, _ = d.db.Exec(`ALTER TABLE app_user ADD COLUMN IF NOT EXISTS plan_id TEXT DEFAULT 'free'`)

	return d.seedPlans()
}

// seedPlans inserts the built-in plans only if they don't already exist.
func (d *DB) seedPlans() error {
	plans := []struct {
		id, name, desc string
		priceCents     int
		interval       string
		limits         model.PlanLimits
		isDefault      bool
	}{
		{
			id: "free", name: "Free", desc: "Free forever — 20 messages/day, 1 account",
			priceCents: 0, interval: "month",
			limits:    model.PlanLimits{DailyMessages: 20, MaxAccounts: 1, APIAccess: false, MCPAccess: false, Webhooks: false, Copilot: false, Autopilot: false},
			isDefault: true,
		},
		{
			id: "pro", name: "Professional", desc: "1 account, unlimited messages, full API & MCP access",
			priceCents: 900, interval: "month",
			limits:    model.PlanLimits{DailyMessages: 0, MaxAccounts: 1, APIAccess: true, MCPAccess: true, Webhooks: true, Copilot: true, Autopilot: false},
			isDefault: false,
		},
		{
			id: "business", name: "Business", desc: "Pay per account, unlimited messages, full API & MCP access",
			priceCents: 2900, interval: "month",
			limits:    model.PlanLimits{DailyMessages: 0, MaxAccounts: 5, APIAccess: true, MCPAccess: true, Webhooks: true, Copilot: true, Autopilot: true},
			isDefault: false,
		},
		{
			id: "enterprise", name: "Enterprise", desc: "Unlimited accounts, unlimited messages, dedicated support",
			priceCents: 9900, interval: "month",
			limits:    model.PlanLimits{DailyMessages: 0, MaxAccounts: 0, APIAccess: true, MCPAccess: true, Webhooks: true, Copilot: true, Autopilot: true},
			isDefault: false,
		},
	}

	for _, p := range plans {
		limitsJSON, _ := json.Marshal(p.limits)
		_, err := d.db.Exec(`
			INSERT INTO plan (id, name, description, price_cents, interval, limits, is_default)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT(id) DO NOTHING
		`, p.id, p.name, p.desc, p.priceCents, p.interval, string(limitsJSON), p.isDefault)
		if err != nil {
			return err
		}
	}
	return nil
}

// ── Plan CRUD ───────────────────────────────────────

// GetPlan returns a plan by ID.
func (d *DB) GetPlan(id string) (*PlanRecord, error) {
	r := &PlanRecord{}
	err := d.db.QueryRow(`SELECT id, name, description, price_cents, interval, limits, is_default, created_at FROM plan WHERE id = $1`, id).
		Scan(&r.ID, &r.Name, &r.Description, &r.PriceCents, &r.Interval, &r.Limits, &r.IsDefault, &r.CreatedAt)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// ListPlans returns all plans.
func (d *DB) ListPlans() ([]*PlanRecord, error) {
	rows, err := d.db.Query(`SELECT id, name, description, price_cents, interval, limits, is_default, created_at FROM plan ORDER BY price_cents ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*PlanRecord
	for rows.Next() {
		r := &PlanRecord{}
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, &r.PriceCents, &r.Interval, &r.Limits, &r.IsDefault, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PlanLimits parses the JSON limits blob from a PlanRecord.
func (r *PlanRecord) PlanLimits() model.PlanLimits {
	var l model.PlanLimits
	_ = json.Unmarshal([]byte(r.Limits), &l)
	return l
}

// ── Subscription CRUD ───────────────────────────────

// GetSubscription returns the subscription for a user.
func (d *DB) GetSubscription(userID string) (*SubscriptionRecord, error) {
	r := &SubscriptionRecord{}
	err := d.db.QueryRow(`
		SELECT id, user_id, plan_id, status, stripe_sub_id, stripe_customer_id,
		       current_period_start, current_period_end, cancel_at_period_end, created_at, updated_at
		FROM subscription WHERE user_id = $1
	`, userID).Scan(&r.ID, &r.UserID, &r.PlanID, &r.Status, &r.StripeSubID, &r.StripeCustomerID,
		&r.CurrentPeriodStart, &r.CurrentPeriodEnd, &r.CancelAtPeriodEnd, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// GetSubscriptionByStripeID looks up a subscription by its Stripe subscription ID.
func (d *DB) GetSubscriptionByStripeID(stripeSubID string) (*SubscriptionRecord, error) {
	r := &SubscriptionRecord{}
	err := d.db.QueryRow(`
		SELECT id, user_id, plan_id, status, stripe_sub_id, stripe_customer_id,
		       current_period_start, current_period_end, cancel_at_period_end, created_at, updated_at
		FROM subscription WHERE stripe_sub_id = $1
	`, stripeSubID).Scan(&r.ID, &r.UserID, &r.PlanID, &r.Status, &r.StripeSubID, &r.StripeCustomerID,
		&r.CurrentPeriodStart, &r.CurrentPeriodEnd, &r.CancelAtPeriodEnd, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// UpsertSubscription creates or updates a user's subscription.
func (d *DB) UpsertSubscription(rec *SubscriptionRecord) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.db.Exec(`
		INSERT INTO subscription (id, user_id, plan_id, status, stripe_sub_id, stripe_customer_id,
		                          current_period_start, current_period_end, cancel_at_period_end, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT(user_id) DO UPDATE SET
			plan_id              = excluded.plan_id,
			status               = excluded.status,
			stripe_sub_id        = excluded.stripe_sub_id,
			stripe_customer_id   = excluded.stripe_customer_id,
			current_period_start = excluded.current_period_start,
			current_period_end   = excluded.current_period_end,
			cancel_at_period_end = excluded.cancel_at_period_end,
			updated_at           = excluded.updated_at
	`, rec.ID, rec.UserID, rec.PlanID, rec.Status, rec.StripeSubID, rec.StripeCustomerID,
		rec.CurrentPeriodStart, rec.CurrentPeriodEnd, rec.CancelAtPeriodEnd,
		rec.CreatedAt, now)
	return err
}

// GetSubscriptionByStripeCustomer looks up a subscription by Stripe customer ID.
func (d *DB) GetSubscriptionByStripeCustomer(customerID string) (*SubscriptionRecord, error) {
	r := &SubscriptionRecord{}
	err := d.db.QueryRow(`
		SELECT id, user_id, plan_id, status, stripe_sub_id, stripe_customer_id,
		       current_period_start, current_period_end, cancel_at_period_end, created_at, updated_at
		FROM subscription WHERE stripe_customer_id = $1
	`, customerID).Scan(&r.ID, &r.UserID, &r.PlanID, &r.Status, &r.StripeSubID, &r.StripeCustomerID,
		&r.CurrentPeriodStart, &r.CurrentPeriodEnd, &r.CancelAtPeriodEnd, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// ── Usage tracking ──────────────────────────────────

// IncrementUsage atomically increments the daily message counter and returns the new count.
func (d *DB) IncrementUsage(userID string) (int, error) {
	today := time.Now().UTC().Format("2006-01-02")
	var count int
	err := d.db.QueryRow(`
		INSERT INTO usage (user_id, date, messages) VALUES ($1, $2, 1)
		ON CONFLICT(user_id, date) DO UPDATE SET messages = usage.messages + 1
		RETURNING messages
	`, userID, today).Scan(&count)
	return count, err
}

// GetDailyUsage returns today's message count for a user.
func (d *DB) GetDailyUsage(userID string) (int, error) {
	today := time.Now().UTC().Format("2006-01-02")
	var count int
	err := d.db.QueryRow(`SELECT messages FROM usage WHERE user_id = $1 AND date = $2`, userID, today).Scan(&count)
	if err != nil {
		return 0, nil // no row = 0 usage
	}
	return count, nil
}

// CountUserAccounts returns the number of accounts owned by a user.
func (d *DB) CountUserAccounts(userID string) (int, error) {
	var count int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM account WHERE user_id = $1`, userID).Scan(&count)
	return count, err
}

// GetUserPlanLimits returns the plan limits for a user by looking up their subscription (or default plan).
func (d *DB) GetUserPlanLimits(userID string) (model.PlanLimits, string, error) {
	// Try subscription first
	sub, err := d.GetSubscription(userID)
	if err == nil && sub.Status == "active" || (sub != nil && sub.Status == "trialing") {
		plan, err := d.GetPlan(sub.PlanID)
		if err == nil {
			return plan.PlanLimits(), plan.ID, nil
		}
	}

	// Fallback: check app_user.plan_id column
	var planID string
	err = d.db.QueryRow(`SELECT COALESCE(plan_id, 'free') FROM app_user WHERE id = $1`, userID).Scan(&planID)
	if err != nil {
		planID = "free"
	}

	plan, err := d.GetPlan(planID)
	if err != nil {
		// Ultimate fallback: return free plan defaults
		return model.PlanLimits{DailyMessages: 20, MaxAccounts: 1}, "free", nil
	}
	return plan.PlanLimits(), plan.ID, nil
}

// EnsureUserSubscription creates a free subscription for a user if they don't have one.
func (d *DB) EnsureUserSubscription(userID, defaultPlan string) error {
	_, err := d.GetSubscription(userID)
	if err == nil {
		return nil // already has one
	}

	if defaultPlan == "" {
		defaultPlan = "free"
	}

	now := time.Now().UTC()
	periodEnd := now.AddDate(0, 1, 0) // 1 month from now
	rec := &SubscriptionRecord{
		ID:                 generateID(),
		UserID:             userID,
		PlanID:             defaultPlan,
		Status:             "active",
		CurrentPeriodStart: now.Format(time.RFC3339),
		CurrentPeriodEnd:   periodEnd.Format(time.RFC3339),
		CreatedAt:          now.Format(time.RFC3339),
	}
	return d.UpsertSubscription(rec)
}

// ── Admin plan CRUD ─────────────────────────────────

// CreatePlan inserts a new plan.
func (d *DB) CreatePlan(rec *PlanRecord) error {
	if rec.ID == "" {
		rec.ID = generateID()
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.db.Exec(`
		INSERT INTO plan (id, name, description, price_cents, interval, limits, is_default, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, rec.ID, rec.Name, rec.Description, rec.PriceCents, rec.Interval, rec.Limits, rec.IsDefault, now)
	return err
}

// UpdatePlan updates an existing plan by ID.
func (d *DB) UpdatePlan(rec *PlanRecord) error {
	res, err := d.db.Exec(`
		UPDATE plan SET name = $1, description = $2, price_cents = $3, interval = $4, limits = $5, is_default = $6
		WHERE id = $7
	`, rec.Name, rec.Description, rec.PriceCents, rec.Interval, rec.Limits, rec.IsDefault, rec.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("plan not found")
	}
	return nil
}

// DeletePlan deletes a plan by ID. Fails if subscriptions reference it.
func (d *DB) DeletePlan(id string) error {
	var count int
	_ = d.db.QueryRow(`SELECT COUNT(*) FROM subscription WHERE plan_id = $1`, id).Scan(&count)
	if count > 0 {
		return fmt.Errorf("cannot delete plan with %d active subscriptions", count)
	}

	res, err := d.db.Exec(`DELETE FROM plan WHERE id = $1`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("plan not found")
	}
	return nil
}

// ── Admin subscription management ───────────────────

// ListSubscriptions returns all subscriptions with user and plan info.
func (d *DB) ListSubscriptions() ([]*SubscriptionRecord, error) {
	rows, err := d.db.Query(`
		SELECT id, user_id, plan_id, status, stripe_sub_id, stripe_customer_id,
		       current_period_start, current_period_end, cancel_at_period_end, created_at, updated_at
		FROM subscription ORDER BY updated_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*SubscriptionRecord
	for rows.Next() {
		r := &SubscriptionRecord{}
		if err := rows.Scan(&r.ID, &r.UserID, &r.PlanID, &r.Status, &r.StripeSubID, &r.StripeCustomerID,
			&r.CurrentPeriodStart, &r.CurrentPeriodEnd, &r.CancelAtPeriodEnd, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteSubscription removes a user's subscription.
func (d *DB) DeleteSubscription(userID string) error {
	res, err := d.db.Exec(`DELETE FROM subscription WHERE user_id = $1`, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("subscription not found")
	}
	return nil
}

// ── Admin usage queries ─────────────────────────────

// GetUsageRange returns daily usage for a user within a date range.
func (d *DB) GetUsageRange(userID, startDate, endDate string) ([]UsageRecord, error) {
	rows, err := d.db.Query(`
		SELECT user_id, date, messages FROM usage
		WHERE user_id = $1 AND date >= $2 AND date <= $3
		ORDER BY date ASC
	`, userID, startDate, endDate)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []UsageRecord
	for rows.Next() {
		var r UsageRecord
		if err := rows.Scan(&r.UserID, &r.Date, &r.Messages); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetAllDailyUsage returns today's usage for all users.
func (d *DB) GetAllDailyUsage() ([]UsageRecord, error) {
	today := time.Now().UTC().Format("2006-01-02")
	rows, err := d.db.Query(`SELECT user_id, date, messages FROM usage WHERE date = $1 ORDER BY messages DESC`, today)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []UsageRecord
	for rows.Next() {
		var r UsageRecord
		if err := rows.Scan(&r.UserID, &r.Date, &r.Messages); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UsageRecord represents a daily usage row.
type UsageRecord struct {
	UserID   string
	Date     string
	Messages int
}
