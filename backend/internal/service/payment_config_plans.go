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
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// SubscriptionPlan combines the mutable catalog entry with its currently
// published, immutable commercial terms.
type SubscriptionPlan struct {
	ID                 int64     `json:"id"`
	PublishedVersionID int64     `json:"published_version_id"`
	Version            int       `json:"version"`
	Name               string    `json:"name"`
	Description        string    `json:"description"`
	Price              float64   `json:"price"`
	OriginalPrice      *float64  `json:"original_price,omitempty"`
	Currency           string    `json:"currency,omitempty"`
	ValidityDays       int       `json:"validity_days"`
	ValidityUnit       string    `json:"validity_unit"`
	DailyLimitUSD      *float64  `json:"daily_limit_usd"`
	WeeklyLimitUSD     *float64  `json:"weekly_limit_usd"`
	MonthlyLimitUSD    *float64  `json:"monthly_limit_usd"`
	Features           string    `json:"features"`
	ProductName        string    `json:"product_name"`
	ForSale            bool      `json:"for_sale"`
	SortOrder          int       `json:"sort_order"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
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

func validatePlanRequired(req CreatePlanRequest) error {
	if strings.TrimSpace(req.Name) == "" {
		return infraerrors.BadRequest("PLAN_NAME_REQUIRED", "plan name is required")
	}
	if math.IsNaN(req.Price) || math.IsInf(req.Price, 0) || req.Price <= 0 {
		return infraerrors.BadRequest("PLAN_PRICE_INVALID", "price must be > 0")
	}
	if req.ValidityDays <= 0 {
		return infraerrors.BadRequest("PLAN_VALIDITY_REQUIRED", "validity days must be > 0")
	}
	if strings.TrimSpace(req.ValidityUnit) == "" {
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
	return s.listPlans(ctx, false)
}

func (s *PaymentConfigService) ListPlansForSale(ctx context.Context) ([]*SubscriptionPlan, error) {
	return s.listPlans(ctx, true)
}

func (s *PaymentConfigService) listPlans(ctx context.Context, forSaleOnly bool) ([]*SubscriptionPlan, error) {
	query := s.entClient.SubscriptionPlan.Query()
	if forSaleOnly {
		query = query.Where(subscriptionplan.ForSaleEQ(true))
	}
	plans, err := query.Order(subscriptionplan.BySortOrder(), subscriptionplan.ByID()).All(ctx)
	if err != nil {
		return nil, err
	}
	return s.hydratePublishedPlans(ctx, plans)
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
	if err := validatePlanRequired(req); err != nil {
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
	plan, err := tx.SubscriptionPlan.Create().
		SetName(strings.TrimSpace(req.Name)).
		SetDescription(req.Description).
		SetFeatures(req.Features).
		SetProductName(req.ProductName).
		SetForSale(req.ForSale).
		SetSortOrder(req.SortOrder).
		Save(ctx)
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
	return req.Price != nil || req.OriginalPrice.Set || req.Currency != nil || req.ValidityDays != nil ||
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
	if planTermsChanged(req) {
		price, originalPrice, currency := version.Price, version.OriginalPrice, version.Currency
		validityDays, validityUnit := version.ValidityDays, version.ValidityUnit
		daily, weekly, monthly := version.DailyLimitUsd, version.WeeklyLimitUsd, version.MonthlyLimitUsd
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
		version, err = tx.SubscriptionPlanVersion.Create().
			SetPlanID(plan.ID).
			SetVersion(version.Version + 1).
			SetPrice(price).
			SetNillableOriginalPrice(originalPrice).
			SetCurrency(currency).
			SetValidityDays(validityDays).
			SetValidityUnit(validityUnit).
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
	return &SubscriptionPlan{
		ID: plan.ID, PublishedVersionID: version.ID, Version: version.Version,
		Name: plan.Name, Description: plan.Description, Features: plan.Features, ProductName: plan.ProductName,
		ForSale: plan.ForSale, SortOrder: plan.SortOrder, CreatedAt: plan.CreatedAt, UpdatedAt: plan.UpdatedAt,
		Price: version.Price, OriginalPrice: version.OriginalPrice, Currency: version.Currency,
		ValidityDays: version.ValidityDays, ValidityUnit: version.ValidityUnit,
		DailyLimitUSD: version.DailyLimitUsd, WeeklyLimitUSD: version.WeeklyLimitUsd, MonthlyLimitUSD: version.MonthlyLimitUsd,
	}
}
