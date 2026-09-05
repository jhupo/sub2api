package repository

import (
	"context"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/ent/subscriptionplan"
	"github.com/Wei-Shaw/sub2api/ent/subscriptionplanversion"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type userSubscriptionRepository struct {
	client *dbent.Client
}

func NewUserSubscriptionRepository(client *dbent.Client) service.UserSubscriptionRepository {
	return &userSubscriptionRepository{client: client}
}

func withSubscriptionRelations(query *dbent.UserSubscriptionQuery) *dbent.UserSubscriptionQuery {
	return query.WithUser().WithPlan().WithPlanVersion().WithAssignedByUser()
}

func (r *userSubscriptionRepository) Create(ctx context.Context, sub *service.UserSubscription) error {
	if sub == nil {
		return service.ErrSubscriptionNilInput
	}
	client := clientFromContext(ctx, r.client)
	builder := client.UserSubscription.Create().
		SetUserID(sub.UserID).
		SetPlanID(sub.PlanID).
		SetPlanVersionID(sub.PlanVersionID).
		SetExpiresAt(sub.ExpiresAt).
		SetNillableDailyWindowStart(sub.DailyWindowStart).
		SetNillableWeeklyWindowStart(sub.WeeklyWindowStart).
		SetNillableMonthlyWindowStart(sub.MonthlyWindowStart).
		SetDailyUsageUsd(sub.DailyUsageUSD).
		SetWeeklyUsageUsd(sub.WeeklyUsageUSD).
		SetMonthlyUsageUsd(sub.MonthlyUsageUSD).
		SetDailyReservedUsd(sub.DailyReservedUSD).
		SetWeeklyReservedUsd(sub.WeeklyReservedUSD).
		SetMonthlyReservedUsd(sub.MonthlyReservedUSD).
		SetNillableAssignedBy(sub.AssignedBy).
		SetNotes(sub.Notes)
	if sub.StartsAt.IsZero() {
		builder.SetStartsAt(time.Now())
	} else {
		builder.SetStartsAt(sub.StartsAt)
	}
	if sub.Status != "" {
		builder.SetStatus(sub.Status)
	}
	if !sub.AssignedAt.IsZero() {
		builder.SetAssignedAt(sub.AssignedAt)
	}
	created, err := builder.Save(ctx)
	if err == nil {
		applyUserSubscriptionEntityToService(sub, created)
	}
	return translatePersistenceError(err, nil, service.ErrSubscriptionAlreadyExists)
}

func (r *userSubscriptionRepository) GetByID(ctx context.Context, id int64) (*service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	m, err := withSubscriptionRelations(client.UserSubscription.Query().Where(usersubscription.IDEQ(id))).Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	}
	return userSubscriptionEntityToService(m), nil
}

func (r *userSubscriptionRepository) GetByIDForUpdate(ctx context.Context, id int64) (*service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	// This method is used by mutation paths that need a row lock. Keep it as a
	// single-row read: relation hydration would issue additional queries outside
	// the locked statement and adds avoidable contention to renewal/maintenance.
	m, err := client.UserSubscription.Query().Where(usersubscription.IDEQ(id)).ForUpdate().Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	}
	return userSubscriptionEntityToService(m), nil
}

func (r *userSubscriptionRepository) GetByIDIncludeDeleted(ctx context.Context, id int64) (*service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	queryCtx := mixins.SkipSoftDelete(ctx)
	m, err := withSubscriptionRelations(client.UserSubscription.Query().Where(usersubscription.IDEQ(id))).Only(queryCtx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	}
	return userSubscriptionEntityToServicePreserveStatus(m), nil
}

func (r *userSubscriptionRepository) GetByUserIDAndPlanVersionID(ctx context.Context, userID, planVersionID int64) (*service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	m, err := withSubscriptionRelations(client.UserSubscription.Query().Where(
		usersubscription.UserIDEQ(userID),
		usersubscription.PlanVersionIDEQ(planVersionID),
	)).ForUpdate().Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	}
	return userSubscriptionEntityToService(m), nil
}

func (r *userSubscriptionRepository) GetActiveByUserIDAndPlanID(ctx context.Context, userID, planID int64) (*service.UserSubscription, error) {
	now := time.Now()
	client := clientFromContext(ctx, r.client)
	m, err := withSubscriptionRelations(client.UserSubscription.Query().Where(
		usersubscription.UserIDEQ(userID),
		usersubscription.PlanIDEQ(planID),
		usersubscription.StatusEQ(service.SubscriptionStatusActive),
		usersubscription.StartsAtLTE(now),
		usersubscription.ExpiresAtGT(now),
	)).Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	}
	return userSubscriptionEntityToService(m), nil
}

func (r *userSubscriptionRepository) Update(ctx context.Context, sub *service.UserSubscription) error {
	if sub == nil {
		return service.ErrSubscriptionNilInput
	}
	client := clientFromContext(ctx, r.client)
	updated, err := client.UserSubscription.UpdateOneID(sub.ID).
		SetUserID(sub.UserID).
		SetPlanID(sub.PlanID).
		SetPlanVersionID(sub.PlanVersionID).
		SetStartsAt(sub.StartsAt).
		SetExpiresAt(sub.ExpiresAt).
		SetStatus(sub.Status).
		SetNillableDailyWindowStart(sub.DailyWindowStart).
		SetNillableWeeklyWindowStart(sub.WeeklyWindowStart).
		SetNillableMonthlyWindowStart(sub.MonthlyWindowStart).
		SetDailyUsageUsd(sub.DailyUsageUSD).
		SetWeeklyUsageUsd(sub.WeeklyUsageUSD).
		SetMonthlyUsageUsd(sub.MonthlyUsageUSD).
		SetDailyReservedUsd(sub.DailyReservedUSD).
		SetWeeklyReservedUsd(sub.WeeklyReservedUSD).
		SetMonthlyReservedUsd(sub.MonthlyReservedUSD).
		SetNillableAssignedBy(sub.AssignedBy).
		SetAssignedAt(sub.AssignedAt).
		SetNotes(sub.Notes).
		Save(ctx)
	if err == nil {
		applyUserSubscriptionEntityToService(sub, updated)
		return nil
	}
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, service.ErrSubscriptionAlreadyExists)
}

func (r *userSubscriptionRepository) Delete(ctx context.Context, id int64) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.Delete().Where(usersubscription.IDEQ(id)).Exec(ctx)
	return err
}

func (r *userSubscriptionRepository) Restore(ctx context.Context, id int64, status string) (*service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	queryCtx := mixins.SkipSoftDelete(ctx)
	_, err := client.UserSubscription.UpdateOneID(id).SetStatus(status).ClearDeletedAt().SetUpdatedAt(time.Now()).Save(queryCtx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrSubscriptionNotFound, service.ErrSubscriptionRestoreConflict)
	}
	return r.GetByID(ctx, id)
}

func (r *userSubscriptionRepository) ListByUserID(ctx context.Context, userID int64) ([]service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	rows, err := withSubscriptionRelations(client.UserSubscription.Query().Where(usersubscription.UserIDEQ(userID))).
		Order(dbent.Desc(usersubscription.FieldCreatedAt)).All(ctx)
	if err != nil {
		return nil, err
	}
	return userSubscriptionEntitiesToService(rows), nil
}

func (r *userSubscriptionRepository) ListActiveByUserID(ctx context.Context, userID int64) ([]service.UserSubscription, error) {
	now := time.Now()
	client := clientFromContext(ctx, r.client)
	rows, err := withSubscriptionRelations(client.UserSubscription.Query().Where(
		usersubscription.UserIDEQ(userID),
		usersubscription.StatusEQ(service.SubscriptionStatusActive),
		usersubscription.StartsAtLTE(now),
		usersubscription.ExpiresAtGT(now),
	)).Order(dbent.Desc(usersubscription.FieldCreatedAt)).All(ctx)
	if err != nil {
		return nil, err
	}
	return userSubscriptionEntitiesToService(rows), nil
}

func (r *userSubscriptionRepository) ListByPlanID(ctx context.Context, planID int64, params pagination.PaginationParams) ([]service.UserSubscription, *pagination.PaginationResult, error) {
	client := clientFromContext(ctx, r.client)
	query := client.UserSubscription.Query().Where(usersubscription.PlanIDEQ(planID))
	total, err := query.Clone().Count(ctx)
	if err != nil {
		return nil, nil, err
	}
	rows, err := withSubscriptionRelations(query).Order(dbent.Desc(usersubscription.FieldCreatedAt)).
		Offset(params.Offset()).Limit(params.Limit()).All(ctx)
	if err != nil {
		return nil, nil, err
	}
	return userSubscriptionEntitiesToService(rows), paginationResultFromTotal(int64(total), params), nil
}

func (r *userSubscriptionRepository) List(ctx context.Context, params pagination.PaginationParams, userID, planID *int64, status, sortBy, sortOrder string) ([]service.UserSubscription, *pagination.PaginationResult, error) {
	client := clientFromContext(ctx, r.client)
	query := client.UserSubscription.Query()
	includeDeleted := status == "" || status == service.SubscriptionStatusRevoked
	if userID != nil {
		query = query.Where(usersubscription.UserIDEQ(*userID))
	}
	if planID != nil {
		query = query.Where(usersubscription.PlanIDEQ(*planID))
	}
	now := time.Now()
	switch status {
	case service.SubscriptionStatusActive:
		query = query.Where(usersubscription.StatusEQ(service.SubscriptionStatusActive), usersubscription.StartsAtLTE(now), usersubscription.ExpiresAtGT(now))
	case service.SubscriptionStatusExpired:
		query = query.Where(usersubscription.Or(
			usersubscription.StatusEQ(service.SubscriptionStatusExpired),
			usersubscription.And(usersubscription.StatusEQ(service.SubscriptionStatusActive), usersubscription.ExpiresAtLTE(now)),
		))
	case service.SubscriptionStatusRevoked:
		query = query.Where(usersubscription.DeletedAtNotNil())
	case "":
	default:
		query = query.Where(usersubscription.StatusEQ(status))
	}
	queryCtx := ctx
	if includeDeleted {
		queryCtx = mixins.SkipSoftDelete(ctx)
	}
	total, err := query.Clone().Count(queryCtx)
	if err != nil {
		return nil, nil, err
	}
	if !includeDeleted {
		query = withSubscriptionRelations(query)
	}
	field := usersubscription.FieldCreatedAt
	switch sortBy {
	case "expires_at":
		field = usersubscription.FieldExpiresAt
	case "status":
		field = usersubscription.FieldStatus
	}
	if sortOrder == "asc" && sortBy != "" {
		query = query.Order(dbent.Asc(field))
	} else {
		query = query.Order(dbent.Desc(field))
	}
	rows, err := query.Offset(params.Offset()).Limit(params.Limit()).All(queryCtx)
	if err != nil {
		return nil, nil, err
	}
	result := userSubscriptionEntitiesToService(rows)
	if includeDeleted {
		if err := r.attachUserSubscriptionRelations(ctx, result); err != nil {
			return nil, nil, err
		}
	}
	return result, paginationResultFromTotal(int64(total), params), nil
}

func (r *userSubscriptionRepository) ExistsByUserIDAndPlanID(ctx context.Context, userID, planID int64) (bool, error) {
	client := clientFromContext(ctx, r.client)
	return client.UserSubscription.Query().Where(usersubscription.UserIDEQ(userID), usersubscription.PlanIDEQ(planID)).Exist(ctx)
}

func (r *userSubscriptionRepository) ExistsActiveByUserIDAndPlanVersionID(ctx context.Context, userID, planVersionID int64) (bool, error) {
	now := time.Now()
	client := clientFromContext(ctx, r.client)
	return client.UserSubscription.Query().Where(
		usersubscription.UserIDEQ(userID), usersubscription.PlanVersionIDEQ(planVersionID),
		usersubscription.StatusEQ(service.SubscriptionStatusActive), usersubscription.StartsAtLTE(now), usersubscription.ExpiresAtGT(now),
	).Exist(ctx)
}

func (r *userSubscriptionRepository) ExtendExpiry(ctx context.Context, id int64, expiresAt time.Time) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(id).SetExpiresAt(expiresAt).Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

func (r *userSubscriptionRepository) UpdateStatus(ctx context.Context, id int64, status string) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(id).SetStatus(status).Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

func (r *userSubscriptionRepository) UpdateNotes(ctx context.Context, id int64, notes string) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(id).SetNotes(notes).Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

func (r *userSubscriptionRepository) ActivateWindows(ctx context.Context, id int64, dailyStart, periodicStart time.Time) error {
	client := clientFromContext(ctx, r.client)
	n, err := client.UserSubscription.Update().Where(
		usersubscription.IDEQ(id), usersubscription.DailyWindowStartIsNil(),
		usersubscription.WeeklyWindowStartIsNil(), usersubscription.MonthlyWindowStartIsNil(),
	).SetDailyWindowStart(dailyStart).SetWeeklyWindowStart(periodicStart).SetMonthlyWindowStart(periodicStart).Save(ctx)
	return r.translateConditionalWindowReset(ctx, client, id, n, err)
}

func (r *userSubscriptionRepository) ResetUsageWindows(ctx context.Context, id int64, resetDaily, resetWeekly, resetMonthly bool, dailyStart, periodicStart time.Time) error {
	client := clientFromContext(ctx, r.client)
	update := client.UserSubscription.Update().Where(usersubscription.IDEQ(id))
	if resetDaily {
		update.Where(usersubscription.DailyReservedUsdEQ(0))
		update.SetDailyUsageUsd(0).SetDailyWindowStart(dailyStart)
	}
	if resetWeekly {
		update.Where(usersubscription.WeeklyReservedUsdEQ(0))
		update.SetWeeklyUsageUsd(0).SetWeeklyWindowStart(periodicStart)
	}
	if resetMonthly {
		update.Where(usersubscription.MonthlyReservedUsdEQ(0))
		update.SetMonthlyUsageUsd(0).SetMonthlyWindowStart(periodicStart)
	}
	n, err := update.Save(ctx)
	if err != nil {
		return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	}
	if n == 1 {
		return nil
	}
	exists, err := client.UserSubscription.Query().Where(usersubscription.IDEQ(id)).Exist(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return service.ErrSubscriptionNotFound
	}
	return service.ErrSubscriptionWindowBusy
}

func (r *userSubscriptionRepository) ResetDailyUsage(ctx context.Context, id int64, expected *time.Time, start time.Time) error {
	client := clientFromContext(ctx, r.client)
	query := client.UserSubscription.Update().Where(usersubscription.IDEQ(id), usersubscription.DailyReservedUsdEQ(0))
	if expected == nil {
		query = query.Where(usersubscription.DailyWindowStartIsNil())
	} else {
		query = query.Where(usersubscription.DailyWindowStartEQ(*expected))
	}
	n, err := query.SetDailyUsageUsd(0).SetDailyWindowStart(start).Save(ctx)
	return r.translateConditionalWindowReset(ctx, client, id, n, err)
}

func (r *userSubscriptionRepository) ResetWeeklyUsage(ctx context.Context, id int64, expected *time.Time, start time.Time) error {
	client := clientFromContext(ctx, r.client)
	query := client.UserSubscription.Update().Where(usersubscription.IDEQ(id), usersubscription.WeeklyReservedUsdEQ(0))
	if expected == nil {
		query = query.Where(usersubscription.WeeklyWindowStartIsNil())
	} else {
		query = query.Where(usersubscription.WeeklyWindowStartEQ(*expected))
	}
	n, err := query.SetWeeklyUsageUsd(0).SetWeeklyWindowStart(start).Save(ctx)
	return r.translateConditionalWindowReset(ctx, client, id, n, err)
}

func (r *userSubscriptionRepository) ResetMonthlyUsage(ctx context.Context, id int64, expected *time.Time, start time.Time) error {
	client := clientFromContext(ctx, r.client)
	query := client.UserSubscription.Update().Where(usersubscription.IDEQ(id), usersubscription.MonthlyReservedUsdEQ(0))
	if expected == nil {
		query = query.Where(usersubscription.MonthlyWindowStartIsNil())
	} else {
		query = query.Where(usersubscription.MonthlyWindowStartEQ(*expected))
	}
	n, err := query.SetMonthlyUsageUsd(0).SetMonthlyWindowStart(start).Save(ctx)
	return r.translateConditionalWindowReset(ctx, client, id, n, err)
}

func (r *userSubscriptionRepository) translateConditionalWindowReset(ctx context.Context, client *dbent.Client, id int64, affected int, err error) error {
	if err != nil {
		return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	}
	if affected > 0 {
		return nil
	}
	exists, err := client.UserSubscription.Query().Where(usersubscription.IDEQ(id)).Exist(ctx)
	if err != nil {
		return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	}
	if !exists {
		return service.ErrSubscriptionNotFound
	}
	return nil
}

func (r *userSubscriptionRepository) BatchUpdateExpiredStatus(ctx context.Context) (int64, error) {
	client := clientFromContext(ctx, r.client)
	n, err := client.UserSubscription.Update().Where(
		usersubscription.StatusEQ(service.SubscriptionStatusActive), usersubscription.ExpiresAtLTE(time.Now()),
	).SetStatus(service.SubscriptionStatusExpired).Save(ctx)
	return int64(n), err
}

func (r *userSubscriptionRepository) attachUserSubscriptionRelations(ctx context.Context, subs []service.UserSubscription) error {
	if len(subs) == 0 {
		return nil
	}
	userIDs, assignedIDs := make([]int64, 0, len(subs)), make([]int64, 0, len(subs))
	planIDs, versionIDs := make([]int64, 0, len(subs)), make([]int64, 0, len(subs))
	for i := range subs {
		userIDs = append(userIDs, subs[i].UserID)
		planIDs = append(planIDs, subs[i].PlanID)
		versionIDs = append(versionIDs, subs[i].PlanVersionID)
		if subs[i].AssignedBy != nil {
			assignedIDs = append(assignedIDs, *subs[i].AssignedBy)
		}
	}
	client := clientFromContext(ctx, r.client)
	users, err := client.User.Query().Where(user.IDIn(uniqueInt64s(userIDs)...)).All(ctx)
	if err != nil {
		return err
	}
	userByID := make(map[int64]*service.User, len(users))
	for _, row := range users {
		userByID[row.ID] = userEntityToService(row)
	}
	plans, err := client.SubscriptionPlan.Query().Where(subscriptionplan.IDIn(uniqueInt64s(planIDs)...)).All(ctx)
	if err != nil {
		return err
	}
	planByID := make(map[int64]*dbent.SubscriptionPlan, len(plans))
	for _, row := range plans {
		planByID[row.ID] = row
	}
	versions, err := client.SubscriptionPlanVersion.Query().Where(subscriptionplanversion.IDIn(uniqueInt64s(versionIDs)...)).All(ctx)
	if err != nil {
		return err
	}
	versionByID := make(map[int64]*dbent.SubscriptionPlanVersion, len(versions))
	for _, row := range versions {
		versionByID[row.ID] = row
	}
	assignedByID := map[int64]*service.User{}
	if len(assignedIDs) > 0 {
		rows, err := client.User.Query().Where(user.IDIn(uniqueInt64s(assignedIDs)...)).All(ctx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			assignedByID[row.ID] = userEntityToService(row)
		}
	}
	for i := range subs {
		subs[i].User = userByID[subs[i].UserID]
		subs[i].Plan = subscriptionPlanEntityToService(planByID[subs[i].PlanID], versionByID[subs[i].PlanVersionID])
		if subs[i].AssignedBy != nil {
			subs[i].AssignedByUser = assignedByID[*subs[i].AssignedBy]
		}
	}
	return nil
}

func uniqueInt64s(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	out := make([]int64, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func userSubscriptionEntityToService(m *dbent.UserSubscription) *service.UserSubscription {
	return userSubscriptionEntityToServiceWithStatusMapping(m, true)
}

func userSubscriptionEntityToServicePreserveStatus(m *dbent.UserSubscription) *service.UserSubscription {
	return userSubscriptionEntityToServiceWithStatusMapping(m, false)
}

func userSubscriptionEntityToServiceWithStatusMapping(m *dbent.UserSubscription, mapDeletedToRevoked bool) *service.UserSubscription {
	if m == nil {
		return nil
	}
	status := m.Status
	if mapDeletedToRevoked && m.DeletedAt != nil {
		status = service.SubscriptionStatusRevoked
	}
	out := &service.UserSubscription{
		ID: m.ID, UserID: m.UserID, PlanID: m.PlanID, PlanVersionID: m.PlanVersionID,
		StartsAt: m.StartsAt, ExpiresAt: m.ExpiresAt, Status: status,
		DailyWindowStart: m.DailyWindowStart, WeeklyWindowStart: m.WeeklyWindowStart, MonthlyWindowStart: m.MonthlyWindowStart,
		DailyUsageUSD: m.DailyUsageUsd, WeeklyUsageUSD: m.WeeklyUsageUsd, MonthlyUsageUSD: m.MonthlyUsageUsd,
		DailyReservedUSD: m.DailyReservedUsd, WeeklyReservedUSD: m.WeeklyReservedUsd, MonthlyReservedUSD: m.MonthlyReservedUsd,
		AssignedBy: m.AssignedBy, AssignedAt: m.AssignedAt, Notes: derefString(m.Notes),
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt, DeletedAt: m.DeletedAt,
	}
	if m.Edges.User != nil {
		out.User = userEntityToService(m.Edges.User)
	}
	out.Plan = subscriptionPlanEntityToService(m.Edges.Plan, m.Edges.PlanVersion)
	if m.Edges.AssignedByUser != nil {
		out.AssignedByUser = userEntityToService(m.Edges.AssignedByUser)
	}
	return out
}

func subscriptionPlanEntityToService(plan *dbent.SubscriptionPlan, version *dbent.SubscriptionPlanVersion) *service.SubscriptionPlan {
	if plan == nil || version == nil {
		return nil
	}
	return &service.SubscriptionPlan{
		ID: plan.ID, PublishedVersionID: version.ID, Version: version.Version,
		Name: plan.Name, Description: plan.Description, Features: plan.Features, ProductName: plan.ProductName,
		ForSale: plan.ForSale, SortOrder: plan.SortOrder, CreatedAt: plan.CreatedAt, UpdatedAt: plan.UpdatedAt,
		Price: version.Price, OriginalPrice: version.OriginalPrice, Currency: version.Currency,
		ValidityDays: version.ValidityDays, ValidityUnit: version.ValidityUnit,
		DailyLimitUSD: version.DailyLimitUsd, WeeklyLimitUSD: version.WeeklyLimitUsd, MonthlyLimitUSD: version.MonthlyLimitUsd,
	}
}

func userSubscriptionEntitiesToService(rows []*dbent.UserSubscription) []service.UserSubscription {
	out := make([]service.UserSubscription, 0, len(rows))
	for _, row := range rows {
		if sub := userSubscriptionEntityToService(row); sub != nil {
			out = append(out, *sub)
		}
	}
	return out
}

func applyUserSubscriptionEntityToService(dst *service.UserSubscription, src *dbent.UserSubscription) {
	if dst == nil || src == nil {
		return
	}
	dst.ID = src.ID
	dst.CreatedAt = src.CreatedAt
	dst.UpdatedAt = src.UpdatedAt
}
