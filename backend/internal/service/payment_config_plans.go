package service

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/subscriptionplan"
	"github.com/Wei-Shaw/sub2api/ent/subscriptionplanversion"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// SubscriptionPlan combines the mutable catalog entry with its currently
// published, immutable commercial terms.
type SubscriptionPlan struct {
	SaleSchedule       domain.PlanSaleSchedule `json:"sale_schedule"`
	SaleAvailable      bool                    `json:"sale_available"`
	SaleEndsAt         *time.Time              `json:"sale_ends_at"`
	CurrentPrice       float64                 `json:"current_price"`
	SaleTimezone       string                  `json:"sale_timezone"`
	ID                 int64                   `json:"id"`
	PublishedVersionID int64                   `json:"published_version_id"`
	Version            int                     `json:"version"`
	Name               string                  `json:"name"`
	Description        string                  `json:"description"`
	Price              float64                 `json:"price"`
	OriginalPrice      *float64                `json:"original_price,omitempty"`
	Currency           string                  `json:"currency,omitempty"`
	ValidityDays       int                     `json:"validity_days"`
	ValidityUnit       string                  `json:"validity_unit"`
	CardMode           string                  `json:"card_mode"`
	ValidityHours      int                     `json:"validity_hours"`
	DeadlineAt         *time.Time              `json:"deadline_at"`
	TotalLimitUSD      *float64                `json:"total_limit_usd"`
	DailyLimitUSD      *float64                `json:"daily_limit_usd"`
	WeeklyLimitUSD     *float64                `json:"weekly_limit_usd"`
	MonthlyLimitUSD    *float64                `json:"monthly_limit_usd"`
	Features           string                  `json:"features"`
	ProductName        string                  `json:"product_name"`
	ForSale            bool                    `json:"for_sale"`
	IsHistorical       bool                    `json:"is_historical"`
	SortOrder          int                     `json:"sort_order"`
	CreatedAt          time.Time               `json:"created_at"`
	UpdatedAt          time.Time               `json:"updated_at"`
}

func normalizePlanCurrency(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	currency, err := payment.NormalizePaymentCurrency(raw)
	if err != nil {
		return "", infraerrors.BadRequest("PLAN_CURRENCY_INVALID", "currency must be a 3-letter ISO currency code")
	}
	return currency, nil
}

func validPlanLimit(limit *float64) bool {
	return limit == nil || (!math.IsNaN(*limit) && !math.IsInf(*limit, 0) && *limit >= 0)
}

func validateCardTerms(mode string, hours int, deadline *time.Time, total, daily, weekly, monthly *float64) error {
	if mode == "" {
		if hours != 0 || deadline != nil || total != nil {
			return infraerrors.BadRequest("CARD_MODE_REQUIRED", "total quota and timed expiry require a quota card mode")
		}
		return nil
	}
	if !validPlanLimit(total) || total == nil || *total <= 0 || daily != nil || weekly != nil || monthly != nil {
		return infraerrors.BadRequest("INVALID_CARD_QUOTA", "quota cards require a positive total quota and no recurring limits")
	}
	switch mode {
	case "hours":
		if hours < 1 || hours > 87600 || deadline != nil {
			return infraerrors.BadRequest("INVALID_CARD_HOURS", "card duration must be 1 to 87600 hours")
		}
	case "deadline":
		if deadline == nil || !deadline.After(time.Now()) || hours != 0 {
			return infraerrors.BadRequest("INVALID_CARD_DEADLINE", "card deadline must be in the future")
		}
	default:
		return infraerrors.BadRequest("INVALID_CARD_MODE", "card_mode must be hours or deadline")
	}
	return nil
}

func validatePlanRequired(req CreatePlanRequest) error {
	if err := validateCardTerms(req.CardMode, req.ValidityHours, req.DeadlineAt, req.TotalLimitUSD, req.DailyLimitUSD, req.WeeklyLimitUSD, req.MonthlyLimitUSD); err != nil {
		return err
	}
	if strings.TrimSpace(req.Name) == "" {
		return infraerrors.BadRequest("PLAN_NAME_REQUIRED", "plan name is required")
	}
	if math.IsNaN(req.Price) || math.IsInf(req.Price, 0) || req.Price <= 0 {
		return infraerrors.BadRequest("PLAN_PRICE_INVALID", "price must be > 0")
	}
	if req.ValidityDays <= 0 && req.CardMode == "" {
		return infraerrors.BadRequest("PLAN_VALIDITY_REQUIRED", "validity days must be > 0")
	}
	if strings.TrimSpace(req.ValidityUnit) == "" && req.CardMode == "" {
		return infraerrors.BadRequest("PLAN_VALIDITY_UNIT_REQUIRED", "validity unit is required")
	}
	if req.OriginalPrice != nil && (math.IsNaN(*req.OriginalPrice) || math.IsInf(*req.OriginalPrice, 0) || *req.OriginalPrice < 0) {
		return infraerrors.BadRequest("PLAN_ORIGINAL_PRICE_INVALID", "original price must be >= 0")
	}
	if !validPlanLimit(req.DailyLimitUSD) || !validPlanLimit(req.WeeklyLimitUSD) || !validPlanLimit(req.MonthlyLimitUSD) {
		return infraerrors.BadRequest("PLAN_LIMIT_INVALID", "subscription limits must be finite and non-negative")
	}
	return nil
}

func validatePlanPatch(req UpdatePlanRequest) error {
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		return infraerrors.BadRequest("PLAN_NAME_REQUIRED", "plan name is required")
	}
	if req.Price != nil && (math.IsNaN(*req.Price) || math.IsInf(*req.Price, 0) || *req.Price <= 0) {
		return infraerrors.BadRequest("PLAN_PRICE_INVALID", "price must be > 0")
	}
	if req.ValidityDays != nil && *req.ValidityDays <= 0 {
		return infraerrors.BadRequest("PLAN_VALIDITY_REQUIRED", "validity days must be > 0")
	}
	if req.ValidityUnit != nil && strings.TrimSpace(*req.ValidityUnit) == "" {
		return infraerrors.BadRequest("PLAN_VALIDITY_UNIT_REQUIRED", "validity unit is required")
	}
	if req.OriginalPrice.Set && req.OriginalPrice.Value != nil && (math.IsNaN(*req.OriginalPrice.Value) || math.IsInf(*req.OriginalPrice.Value, 0) || *req.OriginalPrice.Value < 0) {
		return infraerrors.BadRequest("PLAN_ORIGINAL_PRICE_INVALID", "original price must be >= 0")
	}
	if !validPlanLimit(req.DailyLimitUSD.Value) || !validPlanLimit(req.WeeklyLimitUSD.Value) || !validPlanLimit(req.MonthlyLimitUSD.Value) {
		return infraerrors.BadRequest("PLAN_LIMIT_INVALID", "subscription limits must be finite and non-negative")
	}
	return nil
}

func (s *PaymentConfigService) ListPlans(ctx context.Context) ([]*SubscriptionPlan, error) {
	return s.listPlans(ctx, false, false)
}

// Historical plans are needed when editing existing grants and announcement
// targeting, or filtering issued subscriptions.
func (s *PaymentConfigService) ListPlansIncludingHistorical(ctx context.Context) ([]*SubscriptionPlan, error) {
	return s.listPlans(ctx, false, true)
}

func (s *PaymentConfigService) ListPlansForSale(ctx context.Context) ([]*SubscriptionPlan, error) {
	return s.listPlans(ctx, true, false)
}

func (s *PaymentConfigService) listPlans(ctx context.Context, forSaleOnly, includeHistorical bool) ([]*SubscriptionPlan, error) {
	query := s.entClient.SubscriptionPlan.Query()
	if !includeHistorical {
		query = query.Where(subscriptionplan.IsHistoricalEQ(false))
	}
	if forSaleOnly {
		query = query.Where(subscriptionplan.ForSaleEQ(true))
	}
	plans, err := query.Order(subscriptionplan.BySortOrder(), subscriptionplan.ByID()).All(ctx)
	if err != nil {
		return nil, err
	}
	result, err := s.hydratePublishedPlans(ctx, plans)
	if err != nil {
		return nil, err
	}
	if !forSaleOnly {
		return result, nil
	}
	available := make([]*SubscriptionPlan, 0, len(result))
	for _, plan := range result {
		if plan.SaleAvailable {
			available = append(available, plan)
		}
	}
	return available, nil
}

func (s *PaymentConfigService) hydratePublishedPlans(ctx context.Context, plans []*dbent.SubscriptionPlan) ([]*SubscriptionPlan, error) {
	versionIDs := make([]int64, 0, len(plans))
	for _, plan := range plans {
		if plan.PublishedVersionID != nil {
			versionIDs = append(versionIDs, *plan.PublishedVersionID)
		}
	}
	versions := make(map[int64]*dbent.SubscriptionPlanVersion, len(versionIDs))
	if len(versionIDs) > 0 {
		rows, err := s.entClient.SubscriptionPlanVersion.Query().Where(subscriptionplanversion.IDIn(versionIDs...)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, version := range rows {
			versions[version.ID] = version
		}
	}
	result := make([]*SubscriptionPlan, 0, len(plans))
	for _, plan := range plans {
		if plan.PublishedVersionID == nil || versions[*plan.PublishedVersionID] == nil {
			return nil, fmt.Errorf("plan %d has no published version", plan.ID)
		}
		result = append(result, subscriptionPlanFromEntities(plan, versions[*plan.PublishedVersionID]))
	}
	return result, nil
}

func (s *PaymentConfigService) CreatePlan(ctx context.Context, req CreatePlanRequest) (*SubscriptionPlan, error) {
	if req.CardMode != "" {
		req.ValidityDays, req.ValidityUnit = 1, "day"
	}
	if err := validatePlanRequired(req); err != nil {
		return nil, err
	}
	if err := validateSaleSchedule(req.CardMode, req.DeadlineAt, req.SaleSchedule); err != nil {
		return nil, err
	}
	currency, err := normalizePlanCurrency(req.Currency)
	if err != nil {
		return nil, err
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin plan transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	create := tx.SubscriptionPlan.Create().
		SetName(strings.TrimSpace(req.Name)).
		SetDescription(req.Description).
		SetFeatures(req.Features).
		SetProductName(req.ProductName).
		SetForSale(req.ForSale).
		SetSortOrder(req.SortOrder)
	if req.SaleSchedule != nil {
		create.SetSaleSchedule(*req.SaleSchedule)
	}
	plan, err := create.Save(ctx)
	if err != nil {
		return nil, err
	}
	version, err := tx.SubscriptionPlanVersion.Create().
		SetPlanID(plan.ID).
		SetVersion(1).
		SetPrice(req.Price).
		SetNillableOriginalPrice(req.OriginalPrice).
		SetCurrency(currency).
		SetValidityDays(req.ValidityDays).
		SetValidityUnit(req.ValidityUnit).
		SetCardMode(req.CardMode).
		SetValidityHours(req.ValidityHours).
		SetNillableDeadlineAt(req.DeadlineAt).
		SetNillableTotalLimitUsd(req.TotalLimitUSD).
		SetNillableDailyLimitUsd(req.DailyLimitUSD).
		SetNillableWeeklyLimitUsd(req.WeeklyLimitUSD).
		SetNillableMonthlyLimitUsd(req.MonthlyLimitUSD).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	plan, err = tx.SubscriptionPlan.UpdateOneID(plan.ID).SetPublishedVersionID(version.ID).Save(ctx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return subscriptionPlanFromEntities(plan, version), nil
}

func planTermsChanged(req UpdatePlanRequest) bool {
	return req.CardMode != nil || req.ValidityHours != nil || req.DeadlineAt.Set || req.TotalLimitUSD.Set || req.Price != nil || req.OriginalPrice.Set || req.Currency != nil || req.ValidityDays != nil ||
		req.ValidityUnit != nil || req.DailyLimitUSD.Set || req.WeeklyLimitUSD.Set || req.MonthlyLimitUSD.Set
}

func (s *PaymentConfigService) UpdatePlan(ctx context.Context, id int64, req UpdatePlanRequest) (*SubscriptionPlan, error) {
	if err := validatePlanPatch(req); err != nil {
		return nil, err
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin plan transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	plan, err := tx.SubscriptionPlan.Query().Where(subscriptionplan.IDEQ(id)).ForUpdate().Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, infraerrors.NotFound("PLAN_NOT_FOUND", "subscription plan not found")
		}
		return nil, err
	}
	if plan.PublishedVersionID == nil {
		return nil, fmt.Errorf("plan %d has no published version", plan.ID)
	}
	if plan.IsHistorical {
		return nil, infraerrors.Conflict("PLAN_HISTORICAL_READ_ONLY", "historical subscription plans cannot be edited")
	}
	version, err := tx.SubscriptionPlanVersion.Get(ctx, *plan.PublishedVersionID)
	if err != nil {
		return nil, err
	}
	update := tx.SubscriptionPlan.UpdateOneID(plan.ID)
	if req.Name != nil {
		update.SetName(strings.TrimSpace(*req.Name))
	}
	if req.Description != nil {
		update.SetDescription(*req.Description)
	}
	if req.Features != nil {
		update.SetFeatures(*req.Features)
	}
	if req.ProductName != nil {
		update.SetProductName(*req.ProductName)
	}
	if req.ForSale != nil {
		update.SetForSale(*req.ForSale)
	}
	if req.SortOrder != nil {
		update.SetSortOrder(*req.SortOrder)
	}
	schedule := plan.SaleSchedule
	if req.SaleSchedule != nil {
		schedule = *req.SaleSchedule
		update.SetSaleSchedule(schedule)
	}
	mode, deadline := version.CardMode, version.DeadlineAt
	if req.CardMode != nil {
		mode = *req.CardMode
	}
	if req.DeadlineAt.Set {
		deadline = req.DeadlineAt.Value
	}
	if err := validateSaleSchedule(mode, deadline, &schedule); err != nil {
		return nil, err
	}
	if planTermsChanged(req) {
		price, originalPrice, currency := version.Price, version.OriginalPrice, version.Currency
		validityDays, validityUnit := version.ValidityDays, version.ValidityUnit
		daily, weekly, monthly := version.DailyLimitUsd, version.WeeklyLimitUsd, version.MonthlyLimitUsd
		mode, hours, deadline, total := version.CardMode, version.ValidityHours, version.DeadlineAt, version.TotalLimitUsd
		if req.CardMode != nil {
			mode = *req.CardMode
		}
		if req.ValidityHours != nil {
			hours = *req.ValidityHours
		}
		if req.DeadlineAt.Set {
			deadline = req.DeadlineAt.Value
		}
		if req.TotalLimitUSD.Set {
			total = req.TotalLimitUSD.Value
		}
		if req.Price != nil {
			price = *req.Price
		}
		if req.OriginalPrice.Set {
			originalPrice = req.OriginalPrice.Value
		}
		if req.Currency != nil {
			currency, err = normalizePlanCurrency(*req.Currency)
			if err != nil {
				return nil, err
			}
		}
		if req.ValidityDays != nil {
			validityDays = *req.ValidityDays
		}
		if req.ValidityUnit != nil {
			validityUnit = *req.ValidityUnit
		}
		if req.DailyLimitUSD.Set {
			daily = req.DailyLimitUSD.Value
		}
		if req.WeeklyLimitUSD.Set {
			weekly = req.WeeklyLimitUSD.Value
		}
		if req.MonthlyLimitUSD.Set {
			monthly = req.MonthlyLimitUSD.Value
		}
		if err := validateCardTerms(mode, hours, deadline, total, daily, weekly, monthly); err != nil {
			return nil, err
		}
		version, err = tx.SubscriptionPlanVersion.Create().
			SetPlanID(plan.ID).
			SetVersion(version.Version + 1).
			SetPrice(price).
			SetNillableOriginalPrice(originalPrice).
			SetCurrency(currency).
			SetValidityDays(validityDays).
			SetValidityUnit(validityUnit).
			SetCardMode(mode).
			SetValidityHours(hours).
			SetNillableDeadlineAt(deadline).
			SetNillableTotalLimitUsd(total).
			SetNillableDailyLimitUsd(daily).
			SetNillableWeeklyLimitUsd(weekly).
			SetNillableMonthlyLimitUsd(monthly).
			Save(ctx)
		if err != nil {
			return nil, err
		}
		update.SetPublishedVersionID(version.ID)
	}
	plan, err = update.Save(ctx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return subscriptionPlanFromEntities(plan, version), nil
}

func (s *PaymentConfigService) DeletePlan(ctx context.Context, id int64) error {
	count, err := s.countPendingOrdersByPlan(ctx, id)
	if err != nil {
		return fmt.Errorf("check pending orders: %w", err)
	}
	if count > 0 {
		return infraerrors.Conflict("PENDING_ORDERS", fmt.Sprintf("this plan has %d in-progress orders and cannot be deleted", count))
	}
	// Historical subscriptions retain their immutable plan version, so catalog
	// deletion is an unpublish operation rather than destructive data removal.
	_, err = s.entClient.SubscriptionPlan.UpdateOneID(id).SetForSale(false).Save(ctx)
	return err
}

func (s *PaymentConfigService) GetPlan(ctx context.Context, id int64) (*SubscriptionPlan, error) {
	plan, err := s.entClient.SubscriptionPlan.Get(ctx, id)
	if err != nil {
		return nil, infraerrors.NotFound("PLAN_NOT_FOUND", "subscription plan not found")
	}
	plans, err := s.hydratePublishedPlans(ctx, []*dbent.SubscriptionPlan{plan})
	if err != nil {
		return nil, err
	}
	return plans[0], nil
}

func (s *PaymentConfigService) GetPlanVersion(ctx context.Context, versionID int64) (*SubscriptionPlan, error) {
	version, err := s.entClient.SubscriptionPlanVersion.Get(ctx, versionID)
	if err != nil {
		return nil, infraerrors.NotFound("PLAN_VERSION_NOT_FOUND", "subscription plan version not found")
	}
	plan, err := s.entClient.SubscriptionPlan.Get(ctx, version.PlanID)
	if err != nil {
		return nil, infraerrors.NotFound("PLAN_NOT_FOUND", "subscription plan not found")
	}
	return subscriptionPlanFromEntities(plan, version), nil
}

func subscriptionPlanFromEntities(plan *dbent.SubscriptionPlan, version *dbent.SubscriptionPlanVersion) *SubscriptionPlan {
	result := &SubscriptionPlan{
		SaleSchedule: plan.SaleSchedule,
		ID:           plan.ID, PublishedVersionID: version.ID, Version: version.Version,
		Name: plan.Name, Description: plan.Description, Features: plan.Features, ProductName: plan.ProductName,
		ForSale: plan.ForSale, IsHistorical: plan.IsHistorical, SortOrder: plan.SortOrder, CreatedAt: plan.CreatedAt, UpdatedAt: plan.UpdatedAt,
		Price: version.Price, OriginalPrice: version.OriginalPrice, Currency: version.Currency,
		ValidityDays: version.ValidityDays, ValidityUnit: version.ValidityUnit,
		CardMode: version.CardMode, ValidityHours: version.ValidityHours, DeadlineAt: version.DeadlineAt, TotalLimitUSD: version.TotalLimitUsd,
		DailyLimitUSD: version.DailyLimitUsd, WeeklyLimitUSD: version.WeeklyLimitUsd, MonthlyLimitUSD: version.MonthlyLimitUsd,
	}
	result.ApplySaleAt(time.Now())
	return result
}
