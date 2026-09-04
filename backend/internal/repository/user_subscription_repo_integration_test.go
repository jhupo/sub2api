//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/suite"
)

type UserSubscriptionRepoSuite struct {
	suite.Suite
	ctx    context.Context
	client *dbent.Client
	repo   *userSubscriptionRepository
}

func (s *UserSubscriptionRepoSuite) SetupTest() {
	s.ctx = context.Background()
	tx := testEntTx(s.T())
	s.client = tx.Client()
	s.repo = NewUserSubscriptionRepository(s.client).(*userSubscriptionRepository)
}

func TestUserSubscriptionRepoSuite(t *testing.T) {
	suite.Run(t, new(UserSubscriptionRepoSuite))
}

func (s *UserSubscriptionRepoSuite) mustCreateUser(email string, role string) *service.User {
	s.T().Helper()

	if role == "" {
		role = service.RoleUser
	}

	u, err := s.client.User.Create().
		SetEmail(email).
		SetPasswordHash("test-password-hash").
		SetStatus(service.StatusActive).
		SetRole(role).
		Save(s.ctx)
	s.Require().NoError(err, "create user")
	return userEntityToService(u)
}

func (s *UserSubscriptionRepoSuite) mustCreatePlan(name string) *service.SubscriptionPlan {
	s.T().Helper()

	plan, err := s.client.SubscriptionPlan.Create().
		SetName(name).
		Save(s.ctx)
	s.Require().NoError(err, "create subscription plan")
	version, err := s.client.SubscriptionPlanVersion.Create().
		SetPlanID(plan.ID).
		SetVersion(1).
		SetPrice(10).
		SetValidityDays(30).
		SetValidityUnit("day").
		Save(s.ctx)
	s.Require().NoError(err, "create subscription plan version")
	plan, err = s.client.SubscriptionPlan.UpdateOneID(plan.ID).
		SetPublishedVersionID(version.ID).
		Save(s.ctx)
	s.Require().NoError(err, "publish subscription plan version")
	return subscriptionPlanEntityToService(plan, version)
}

func (s *UserSubscriptionRepoSuite) mustCreateSubscription(userID, planID int64, mutate func(*dbent.UserSubscriptionCreate)) *dbent.UserSubscription {
	s.T().Helper()

	now := time.Now()
	plan, err := s.client.SubscriptionPlan.Get(s.ctx, planID)
	s.Require().NoError(err, "load subscription plan")
	s.Require().NotNil(plan.PublishedVersionID, "subscription plan must have a published version")
	create := s.client.UserSubscription.Create().
		SetUserID(userID).
		SetPlanID(planID).
		SetPlanVersionID(*plan.PublishedVersionID).
		SetStartsAt(now.Add(-1 * time.Hour)).
		SetExpiresAt(now.Add(24 * time.Hour)).
		SetStatus(service.SubscriptionStatusActive).
		SetAssignedAt(now).
		SetNotes("")

	if mutate != nil {
		mutate(create)
	}

	sub, err := create.Save(s.ctx)
	s.Require().NoError(err, "create user subscription")
	return sub
}

// --- Create / GetByID / Update / Delete ---

func (s *UserSubscriptionRepoSuite) TestCreate() {
	user := s.mustCreateUser("sub-create@test.com", service.RoleUser)
	group := s.mustCreatePlan("g-create")

	sub := &service.UserSubscription{
		UserID:        user.ID,
		PlanID:        group.ID,
		PlanVersionID: group.PublishedVersionID,
		Status:        service.SubscriptionStatusActive,
		ExpiresAt:     time.Now().Add(24 * time.Hour),
	}

	err := s.repo.Create(s.ctx, sub)
	s.Require().NoError(err, "Create")
	s.Require().NotZero(sub.ID, "expected ID to be set")

	got, err := s.repo.GetByID(s.ctx, sub.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().Equal(sub.UserID, got.UserID)
	s.Require().Equal(sub.PlanID, got.PlanID)
}

func (s *UserSubscriptionRepoSuite) TestGetByID_WithPreloads() {
	user := s.mustCreateUser("preload@test.com", service.RoleUser)
	group := s.mustCreatePlan("g-preload")
	admin := s.mustCreateUser("admin@test.com", service.RoleAdmin)

	sub := s.mustCreateSubscription(user.ID, group.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetAssignedBy(admin.ID)
	})

	got, err := s.repo.GetByID(s.ctx, sub.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().NotNil(got.User, "expected User preload")
	s.Require().NotNil(got.Plan, "expected Group preload")
	s.Require().NotNil(got.AssignedByUser, "expected AssignedByUser preload")
	s.Require().Equal(user.ID, got.User.ID)
	s.Require().Equal(group.ID, got.Plan.ID)
	s.Require().Equal(admin.ID, got.AssignedByUser.ID)
}

func (s *UserSubscriptionRepoSuite) TestGetByID_NotFound() {
	_, err := s.repo.GetByID(s.ctx, 999999)
	s.Require().Error(err, "expected error for non-existent ID")
}

func (s *UserSubscriptionRepoSuite) TestUpdate() {
	user := s.mustCreateUser("update@test.com", service.RoleUser)
	group := s.mustCreatePlan("g-update")
	created := s.mustCreateSubscription(user.ID, group.ID, nil)

	sub, err := s.repo.GetByID(s.ctx, created.ID)
	s.Require().NoError(err, "GetByID")

	sub.Notes = "updated notes"
	s.Require().NoError(s.repo.Update(s.ctx, sub), "Update")

	got, err := s.repo.GetByID(s.ctx, sub.ID)
	s.Require().NoError(err, "GetByID after update")
	s.Require().Equal("updated notes", got.Notes)
}

func (s *UserSubscriptionRepoSuite) TestDelete() {
	user := s.mustCreateUser("delete@test.com", service.RoleUser)
	group := s.mustCreatePlan("g-delete")
	sub := s.mustCreateSubscription(user.ID, group.ID, nil)

	err := s.repo.Delete(s.ctx, sub.ID)
	s.Require().NoError(err, "Delete")

	_, err = s.repo.GetByID(s.ctx, sub.ID)
	s.Require().Error(err, "expected error after delete")
}

func (s *UserSubscriptionRepoSuite) TestGetByIDIncludeDeleted_PreservesPersistedStatus() {
	user := s.mustCreateUser("include-deleted@test.com", service.RoleUser)
	group := s.mustCreatePlan("g-include-deleted")
	sub := s.mustCreateSubscription(user.ID, group.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetStatus(service.SubscriptionStatusActive)
	})

	s.Require().NoError(s.repo.Delete(s.ctx, sub.ID), "Delete")

	got, err := s.repo.GetByIDIncludeDeleted(s.ctx, sub.ID)
	s.Require().NoError(err, "GetByIDIncludeDeleted")
	s.Require().Equal(service.SubscriptionStatusActive, got.Status)
	s.Require().NotNil(got.DeletedAt)
	s.Require().NotNil(got.User)
	s.Require().NotNil(got.Plan)
}

func (s *UserSubscriptionRepoSuite) TestRestore() {
	user := s.mustCreateUser("restore@test.com", service.RoleUser)
	group := s.mustCreatePlan("g-restore")
	sub := s.mustCreateSubscription(user.ID, group.ID, nil)

	s.Require().NoError(s.repo.Delete(s.ctx, sub.ID), "Delete")

	restored, err := s.repo.Restore(s.ctx, sub.ID, service.SubscriptionStatusExpired)
	s.Require().NoError(err, "Restore")
	s.Require().Equal(service.SubscriptionStatusExpired, restored.Status)
	s.Require().Nil(restored.DeletedAt)

	got, err := s.repo.GetByID(s.ctx, sub.ID)
	s.Require().NoError(err, "GetByID after restore")
	s.Require().Nil(got.DeletedAt)
	s.Require().Equal(service.SubscriptionStatusExpired, got.Status)
}

func (s *UserSubscriptionRepoSuite) TestDelete_Idempotent() {
	s.Require().NoError(s.repo.Delete(s.ctx, 42424242), "Delete should be idempotent")
}

// --- GetByUserIDAndPlanVersionID / GetActiveByUserIDAndPlanID ---

func (s *UserSubscriptionRepoSuite) TestGetByUserIDAndPlanVersionID() {
	user := s.mustCreateUser("byuser@test.com", service.RoleUser)
	group := s.mustCreatePlan("g-byuser")
	sub := s.mustCreateSubscription(user.ID, group.ID, nil)

	got, err := s.repo.GetByUserIDAndPlanVersionID(s.ctx, user.ID, group.PublishedVersionID)
	s.Require().NoError(err, "GetByUserIDAndPlanVersionID")
	s.Require().Equal(sub.ID, got.ID)
	s.Require().NotNil(got.Plan, "expected Group preload")
}

func (s *UserSubscriptionRepoSuite) TestGetByUserIDAndPlanVersionID_NotFound() {
	_, err := s.repo.GetByUserIDAndPlanVersionID(s.ctx, 999999, 999999)
	s.Require().Error(err, "expected error for non-existent pair")
}

func (s *UserSubscriptionRepoSuite) TestGetActiveByUserIDAndPlanID() {
	user := s.mustCreateUser("active@test.com", service.RoleUser)
	group := s.mustCreatePlan("g-active")

	active := s.mustCreateSubscription(user.ID, group.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetExpiresAt(time.Now().Add(2 * time.Hour))
	})

	got, err := s.repo.GetActiveByUserIDAndPlanID(s.ctx, user.ID, group.ID)
	s.Require().NoError(err, "GetActiveByUserIDAndPlanID")
	s.Require().Equal(active.ID, got.ID)
}

func (s *UserSubscriptionRepoSuite) TestGetActiveByUserIDAndPlanID_ExpiredIgnored() {
	user := s.mustCreateUser("expired@test.com", service.RoleUser)
	group := s.mustCreatePlan("g-expired")

	s.mustCreateSubscription(user.ID, group.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetExpiresAt(time.Now().Add(-2 * time.Hour))
	})

	_, err := s.repo.GetActiveByUserIDAndPlanID(s.ctx, user.ID, group.ID)
	s.Require().Error(err, "expected error for expired subscription")
}

// --- ListByUserID / ListActiveByUserID ---

func (s *UserSubscriptionRepoSuite) TestListByUserID() {
	user := s.mustCreateUser("listby@test.com", service.RoleUser)
	g1 := s.mustCreatePlan("g-list1")
	g2 := s.mustCreatePlan("g-list2")

	s.mustCreateSubscription(user.ID, g1.ID, nil)
	s.mustCreateSubscription(user.ID, g2.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetStatus(service.SubscriptionStatusExpired)
		c.SetExpiresAt(time.Now().Add(-24 * time.Hour))
	})

	subs, err := s.repo.ListByUserID(s.ctx, user.ID)
	s.Require().NoError(err, "ListByUserID")
	s.Require().Len(subs, 2)
	for _, sub := range subs {
		s.Require().NotNil(sub.Plan, "expected Group preload")
	}
}

func (s *UserSubscriptionRepoSuite) TestListActiveByUserID() {
	user := s.mustCreateUser("listactive@test.com", service.RoleUser)
	g1 := s.mustCreatePlan("g-act1")
	g2 := s.mustCreatePlan("g-act2")

	s.mustCreateSubscription(user.ID, g1.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetExpiresAt(time.Now().Add(24 * time.Hour))
	})
	s.mustCreateSubscription(user.ID, g2.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetStatus(service.SubscriptionStatusExpired)
		c.SetExpiresAt(time.Now().Add(-24 * time.Hour))
	})

	subs, err := s.repo.ListActiveByUserID(s.ctx, user.ID)
	s.Require().NoError(err, "ListActiveByUserID")
	s.Require().Len(subs, 1)
	s.Require().Equal(service.SubscriptionStatusActive, subs[0].Status)
}

// --- ListByPlanID ---

func (s *UserSubscriptionRepoSuite) TestListByPlanID() {
	user1 := s.mustCreateUser("u1@test.com", service.RoleUser)
	user2 := s.mustCreateUser("u2@test.com", service.RoleUser)
	group := s.mustCreatePlan("g-listgrp")

	s.mustCreateSubscription(user1.ID, group.ID, nil)
	s.mustCreateSubscription(user2.ID, group.ID, nil)

	subs, page, err := s.repo.ListByPlanID(s.ctx, group.ID, pagination.PaginationParams{Page: 1, PageSize: 10})
	s.Require().NoError(err, "ListByPlanID")
	s.Require().Len(subs, 2)
	s.Require().Equal(int64(2), page.Total)
	for _, sub := range subs {
		s.Require().NotNil(sub.User, "expected User preload")
		s.Require().NotNil(sub.Plan, "expected Group preload")
	}
}

// --- List with filters ---

func (s *UserSubscriptionRepoSuite) TestList_NoFilters() {
	user := s.mustCreateUser("list@test.com", service.RoleUser)
	group := s.mustCreatePlan("g-list")
	s.mustCreateSubscription(user.ID, group.ID, nil)

	subs, page, err := s.repo.List(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, nil, nil, "", "", "")
	s.Require().NoError(err, "List")
	s.Require().Len(subs, 1)
	s.Require().Equal(int64(1), page.Total)
}

func (s *UserSubscriptionRepoSuite) TestList_FilterByUserID() {
	user1 := s.mustCreateUser("filter1@test.com", service.RoleUser)
	user2 := s.mustCreateUser("filter2@test.com", service.RoleUser)
	group := s.mustCreatePlan("g-filter")

	s.mustCreateSubscription(user1.ID, group.ID, nil)
	s.mustCreateSubscription(user2.ID, group.ID, nil)

	subs, _, err := s.repo.List(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, &user1.ID, nil, "", "", "")
	s.Require().NoError(err)
	s.Require().Len(subs, 1)
	s.Require().Equal(user1.ID, subs[0].UserID)
}

func (s *UserSubscriptionRepoSuite) TestList_FilterByPlanID() {
	user := s.mustCreateUser("grpfilter@test.com", service.RoleUser)
	g1 := s.mustCreatePlan("g-f1")
	g2 := s.mustCreatePlan("g-f2")

	s.mustCreateSubscription(user.ID, g1.ID, nil)
	s.mustCreateSubscription(user.ID, g2.ID, nil)

	subs, _, err := s.repo.List(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, nil, &g1.ID, "", "", "")
	s.Require().NoError(err)
	s.Require().Len(subs, 1)
	s.Require().Equal(g1.ID, subs[0].PlanID)
}

func (s *UserSubscriptionRepoSuite) TestList_FilterByStatus() {
	user1 := s.mustCreateUser("statfilter1@test.com", service.RoleUser)
	user2 := s.mustCreateUser("statfilter2@test.com", service.RoleUser)
	group1 := s.mustCreatePlan("g-stat-1")
	group2 := s.mustCreatePlan("g-stat-2")

	s.mustCreateSubscription(user1.ID, group1.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetStatus(service.SubscriptionStatusActive)
		c.SetExpiresAt(time.Now().Add(24 * time.Hour))
	})
	s.mustCreateSubscription(user2.ID, group2.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetStatus(service.SubscriptionStatusExpired)
		c.SetExpiresAt(time.Now().Add(-24 * time.Hour))
	})

	subs, _, err := s.repo.List(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, nil, nil, service.SubscriptionStatusExpired, "", "")
	s.Require().NoError(err)
	s.Require().Len(subs, 1)
	s.Require().Equal(service.SubscriptionStatusExpired, subs[0].Status)
}

func (s *UserSubscriptionRepoSuite) TestList_IncludesRevokedWhenStatusEmpty() {
	user1 := s.mustCreateUser("allstatus1@test.com", service.RoleUser)
	user2 := s.mustCreateUser("allstatus2@test.com", service.RoleUser)
	user3 := s.mustCreateUser("allstatus3@test.com", service.RoleUser)
	group1 := s.mustCreatePlan("g-allstatus-1")
	group2 := s.mustCreatePlan("g-allstatus-2")
	group3 := s.mustCreatePlan("g-allstatus-3")

	s.mustCreateSubscription(user1.ID, group1.ID, nil)
	s.mustCreateSubscription(user2.ID, group2.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetStatus(service.SubscriptionStatusExpired)
		c.SetExpiresAt(time.Now().Add(-24 * time.Hour))
	})
	revoked := s.mustCreateSubscription(user3.ID, group3.ID, nil)
	s.Require().NoError(s.repo.Delete(s.ctx, revoked.ID))

	subs, pag, err := s.repo.List(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, nil, nil, "", "", "")
	s.Require().NoError(err)
	s.Require().Len(subs, 3)
	s.Require().Equal(int64(3), pag.Total)

	var gotRevoked *service.UserSubscription
	for i := range subs {
		if subs[i].ID == revoked.ID {
			gotRevoked = &subs[i]
			break
		}
	}
	s.Require().NotNil(gotRevoked, "all status should include soft-deleted subscription")
	s.Require().Equal(service.SubscriptionStatusRevoked, gotRevoked.Status)
	s.Require().NotNil(gotRevoked.DeletedAt)
	s.Require().NotNil(gotRevoked.User)
	s.Require().NotNil(gotRevoked.Plan)
}

func (s *UserSubscriptionRepoSuite) TestList_FilterByRevokedStatus() {
	user1 := s.mustCreateUser("revokedfilter1@test.com", service.RoleUser)
	user2 := s.mustCreateUser("revokedfilter2@test.com", service.RoleUser)
	group1 := s.mustCreatePlan("g-revoked-1")
	group2 := s.mustCreatePlan("g-revoked-2")

	active := s.mustCreateSubscription(user1.ID, group1.ID, nil)
	revoked := s.mustCreateSubscription(user2.ID, group2.ID, nil)
	s.Require().NoError(s.repo.Delete(s.ctx, revoked.ID))

	subs, pag, err := s.repo.List(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, nil, nil, service.SubscriptionStatusRevoked, "", "")
	s.Require().NoError(err)
	s.Require().Len(subs, 1)
	s.Require().Equal(int64(1), pag.Total)
	s.Require().Equal(revoked.ID, subs[0].ID)
	s.Require().NotEqual(active.ID, subs[0].ID)
	s.Require().Equal(service.SubscriptionStatusRevoked, subs[0].Status)
	s.Require().NotNil(subs[0].DeletedAt)
}

func (s *UserSubscriptionRepoSuite) TestActivateWindows() {
	user := s.mustCreateUser("activate@test.com", service.RoleUser)
	group := s.mustCreatePlan("g-activate")
	sub := s.mustCreateSubscription(user.ID, group.ID, nil)

	dailyStart := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	activateAt := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	err := s.repo.ActivateWindows(s.ctx, sub.ID, dailyStart, activateAt)
	s.Require().NoError(err, "ActivateWindows")

	got, err := s.repo.GetByID(s.ctx, sub.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got.DailyWindowStart)
	s.Require().NotNil(got.WeeklyWindowStart)
	s.Require().NotNil(got.MonthlyWindowStart)
	s.Require().WithinDuration(dailyStart, *got.DailyWindowStart, time.Microsecond)
	s.Require().WithinDuration(activateAt, *got.WeeklyWindowStart, time.Microsecond)
	s.Require().WithinDuration(activateAt, *got.MonthlyWindowStart, time.Microsecond)
}

func (s *UserSubscriptionRepoSuite) TestActivateWindows_StaleActivationPreservesExistingWindows() {
	user := s.mustCreateUser("activate-cas@test.com", service.RoleUser)
	group := s.mustCreatePlan("g-activate-cas")
	sub := s.mustCreateSubscription(user.ID, group.ID, nil)
	activatedAt := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	manualResetAt := activatedAt.Add(2 * time.Hour)
	manualDailyStart := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	s.Require().NoError(s.repo.ActivateWindows(s.ctx, sub.ID, activatedAt, activatedAt))
	s.Require().NoError(s.repo.ResetUsageWindows(s.ctx, sub.ID, true, true, true, manualDailyStart, manualResetAt))
	// Simulate a concurrent request carrying the original unactivated snapshot.
	s.Require().NoError(s.repo.ActivateWindows(s.ctx, sub.ID, activatedAt.Add(time.Hour), activatedAt.Add(time.Hour)))

	got, err := s.repo.GetByID(s.ctx, sub.ID)
	s.Require().NoError(err)
	s.Require().WithinDuration(manualDailyStart, *got.DailyWindowStart, time.Microsecond)
	s.Require().WithinDuration(manualResetAt, *got.WeeklyWindowStart, time.Microsecond)
	s.Require().WithinDuration(manualResetAt, *got.MonthlyWindowStart, time.Microsecond)
}

func (s *UserSubscriptionRepoSuite) TestResetDailyUsage() {
	user := s.mustCreateUser("resetd@test.com", service.RoleUser)
	group := s.mustCreatePlan("g-resetd")
	sub := s.mustCreateSubscription(user.ID, group.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetDailyUsageUsd(10.0)
		c.SetWeeklyUsageUsd(20.0)
	})

	resetAt := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	err := s.repo.ResetDailyUsage(s.ctx, sub.ID, sub.DailyWindowStart, resetAt)
	s.Require().NoError(err, "ResetDailyUsage")

	got, err := s.repo.GetByID(s.ctx, sub.ID)
	s.Require().NoError(err)
	s.Require().InDelta(0.0, got.DailyUsageUSD, 1e-6)
	s.Require().InDelta(20.0, got.WeeklyUsageUSD, 1e-6)
	s.Require().NotNil(got.DailyWindowStart)
	s.Require().WithinDuration(resetAt, *got.DailyWindowStart, time.Microsecond)
}

func (s *UserSubscriptionRepoSuite) TestResetDailyUsage_StaleResetDoesNotClearNewWindowUsage() {
	user := s.mustCreateUser("resetd-cas@test.com", service.RoleUser)
	group := s.mustCreatePlan("g-resetd-cas")
	oldWindowStart := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	sub := s.mustCreateSubscription(user.ID, group.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetDailyWindowStart(oldWindowStart)
		c.SetDailyUsageUsd(10)
	})

	newWindowStart := oldWindowStart.Add(24 * time.Hour)
	s.Require().NoError(s.repo.ResetDailyUsage(s.ctx, sub.ID, &oldWindowStart, newWindowStart))
	_, err := s.client.UserSubscription.UpdateOneID(sub.ID).
		SetDailyUsageUsd(3).
		SetWeeklyUsageUsd(3).
		SetMonthlyUsageUsd(3).
		Save(s.ctx)
	s.Require().NoError(err)
	// Simulate a second request carrying the stale old-window snapshot.
	s.Require().NoError(s.repo.ResetDailyUsage(s.ctx, sub.ID, &oldWindowStart, newWindowStart))

	got, err := s.repo.GetByID(s.ctx, sub.ID)
	s.Require().NoError(err)
	s.Require().InDelta(3, got.DailyUsageUSD, 1e-6)
	s.Require().WithinDuration(newWindowStart, *got.DailyWindowStart, time.Microsecond)
}

func (s *UserSubscriptionRepoSuite) TestResetUsageWindows_ClearsUsageAfterAutomaticWindowAdvance() {
	user := s.mustCreateUser("admin-reset-current@test.com", service.RoleUser)
	group := s.mustCreatePlan("g-admin-reset-current")
	oldWindowStart := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	sub := s.mustCreateSubscription(user.ID, group.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetDailyWindowStart(oldWindowStart)
		c.SetDailyUsageUsd(10)
	})

	newWindowStart := oldWindowStart.Add(24 * time.Hour)
	s.Require().NoError(s.repo.ResetDailyUsage(s.ctx, sub.ID, &oldWindowStart, newWindowStart))
	_, err := s.client.UserSubscription.UpdateOneID(sub.ID).
		SetDailyUsageUsd(3).
		SetWeeklyUsageUsd(3).
		SetMonthlyUsageUsd(3).
		Save(s.ctx)
	s.Require().NoError(err)
	s.Require().NoError(s.repo.ResetUsageWindows(s.ctx, sub.ID, true, false, false, newWindowStart, newWindowStart))

	got, err := s.repo.GetByID(s.ctx, sub.ID)
	s.Require().NoError(err)
	s.Require().InDelta(0, got.DailyUsageUSD, 1e-6)
	s.Require().WithinDuration(newWindowStart, *got.DailyWindowStart, time.Microsecond)
}

func (s *UserSubscriptionRepoSuite) TestResetWeeklyUsage() {
	user := s.mustCreateUser("resetw@test.com", service.RoleUser)
	group := s.mustCreatePlan("g-resetw")
	sub := s.mustCreateSubscription(user.ID, group.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetWeeklyUsageUsd(15.0)
		c.SetMonthlyUsageUsd(30.0)
	})

	resetAt := time.Date(2025, 1, 6, 0, 0, 0, 0, time.UTC)
	err := s.repo.ResetWeeklyUsage(s.ctx, sub.ID, sub.WeeklyWindowStart, resetAt)
	s.Require().NoError(err, "ResetWeeklyUsage")

	got, err := s.repo.GetByID(s.ctx, sub.ID)
	s.Require().NoError(err)
	s.Require().InDelta(0.0, got.WeeklyUsageUSD, 1e-6)
	s.Require().InDelta(30.0, got.MonthlyUsageUSD, 1e-6)
	s.Require().NotNil(got.WeeklyWindowStart)
	s.Require().WithinDuration(resetAt, *got.WeeklyWindowStart, time.Microsecond)
}

func (s *UserSubscriptionRepoSuite) TestResetMonthlyUsage() {
	user := s.mustCreateUser("resetm@test.com", service.RoleUser)
	group := s.mustCreatePlan("g-resetm")
	sub := s.mustCreateSubscription(user.ID, group.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetMonthlyUsageUsd(25.0)
	})

	resetAt := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	err := s.repo.ResetMonthlyUsage(s.ctx, sub.ID, sub.MonthlyWindowStart, resetAt)
	s.Require().NoError(err, "ResetMonthlyUsage")

	got, err := s.repo.GetByID(s.ctx, sub.ID)
	s.Require().NoError(err)
	s.Require().InDelta(0.0, got.MonthlyUsageUSD, 1e-6)
	s.Require().NotNil(got.MonthlyWindowStart)
	s.Require().WithinDuration(resetAt, *got.MonthlyWindowStart, time.Microsecond)
}

// --- UpdateStatus / ExtendExpiry / UpdateNotes ---

func (s *UserSubscriptionRepoSuite) TestUpdateStatus() {
	user := s.mustCreateUser("status@test.com", service.RoleUser)
	group := s.mustCreatePlan("g-status")
	sub := s.mustCreateSubscription(user.ID, group.ID, nil)

	err := s.repo.UpdateStatus(s.ctx, sub.ID, service.SubscriptionStatusExpired)
	s.Require().NoError(err, "UpdateStatus")

	got, err := s.repo.GetByID(s.ctx, sub.ID)
	s.Require().NoError(err)
	s.Require().Equal(service.SubscriptionStatusExpired, got.Status)
}

func (s *UserSubscriptionRepoSuite) TestExtendExpiry() {
	user := s.mustCreateUser("extend@test.com", service.RoleUser)
	group := s.mustCreatePlan("g-extend")
	sub := s.mustCreateSubscription(user.ID, group.ID, nil)

	newExpiry := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	err := s.repo.ExtendExpiry(s.ctx, sub.ID, newExpiry)
	s.Require().NoError(err, "ExtendExpiry")

	got, err := s.repo.GetByID(s.ctx, sub.ID)
	s.Require().NoError(err)
	s.Require().WithinDuration(newExpiry, got.ExpiresAt, time.Microsecond)
}

func (s *UserSubscriptionRepoSuite) TestUpdateNotes() {
	user := s.mustCreateUser("notes@test.com", service.RoleUser)
	group := s.mustCreatePlan("g-notes")
	sub := s.mustCreateSubscription(user.ID, group.ID, nil)

	err := s.repo.UpdateNotes(s.ctx, sub.ID, "VIP user")
	s.Require().NoError(err, "UpdateNotes")

	got, err := s.repo.GetByID(s.ctx, sub.ID)
	s.Require().NoError(err)
	s.Require().Equal("VIP user", got.Notes)
}

// --- Expired listing / BatchUpdateExpiredStatus ---

func (s *UserSubscriptionRepoSuite) TestList_FilterByExpiredWindow() {
	user := s.mustCreateUser("listexp@test.com", service.RoleUser)
	groupActive := s.mustCreatePlan("g-listexp-active")
	groupExpired := s.mustCreatePlan("g-listexp-expired")

	s.mustCreateSubscription(user.ID, groupActive.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetExpiresAt(time.Now().Add(24 * time.Hour))
	})
	s.mustCreateSubscription(user.ID, groupExpired.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetExpiresAt(time.Now().Add(-24 * time.Hour))
	})

	expired, _, err := s.repo.List(
		s.ctx,
		pagination.PaginationParams{Page: 1, PageSize: 10},
		nil,
		nil,
		service.SubscriptionStatusExpired,
		"",
		"",
	)
	s.Require().NoError(err, "List expired")
	s.Require().Len(expired, 1)
}

func (s *UserSubscriptionRepoSuite) TestBatchUpdateExpiredStatus() {
	user := s.mustCreateUser("batch@test.com", service.RoleUser)
	groupFuture := s.mustCreatePlan("g-batch-future")
	groupPast := s.mustCreatePlan("g-batch-past")

	active := s.mustCreateSubscription(user.ID, groupFuture.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetExpiresAt(time.Now().Add(24 * time.Hour))
	})
	expiredActive := s.mustCreateSubscription(user.ID, groupPast.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetExpiresAt(time.Now().Add(-24 * time.Hour))
	})

	affected, err := s.repo.BatchUpdateExpiredStatus(s.ctx)
	s.Require().NoError(err, "BatchUpdateExpiredStatus")
	s.Require().Equal(int64(1), affected)

	gotActive, _ := s.repo.GetByID(s.ctx, active.ID)
	s.Require().Equal(service.SubscriptionStatusActive, gotActive.Status)

	gotExpired, _ := s.repo.GetByID(s.ctx, expiredActive.ID)
	s.Require().Equal(service.SubscriptionStatusExpired, gotExpired.Status)
}

// --- ExistsByUserIDAndPlanID ---

func (s *UserSubscriptionRepoSuite) TestExistsByUserIDAndPlanID() {
	user := s.mustCreateUser("exists@test.com", service.RoleUser)
	group := s.mustCreatePlan("g-exists")

	s.mustCreateSubscription(user.ID, group.ID, nil)

	exists, err := s.repo.ExistsByUserIDAndPlanID(s.ctx, user.ID, group.ID)
	s.Require().NoError(err, "ExistsByUserIDAndPlanID")
	s.Require().True(exists)

	notExists, err := s.repo.ExistsByUserIDAndPlanID(s.ctx, user.ID, 999999)
	s.Require().NoError(err)
	s.Require().False(notExists)
}

func (s *UserSubscriptionRepoSuite) TestExistsActiveByUserIDAndPlanVersionID_IgnoresSoftDeletedRows() {
	user := s.mustCreateUser("exists-active@test.com", service.RoleUser)
	group := s.mustCreatePlan("g-exists-active")
	sub := s.mustCreateSubscription(user.ID, group.ID, nil)

	exists, err := s.repo.ExistsActiveByUserIDAndPlanVersionID(s.ctx, user.ID, group.PublishedVersionID)
	s.Require().NoError(err, "ExistsActiveByUserIDAndPlanVersionID")
	s.Require().True(exists)

	s.Require().NoError(s.repo.Delete(s.ctx, sub.ID), "Delete")

	exists, err = s.repo.ExistsActiveByUserIDAndPlanVersionID(s.ctx, user.ID, group.PublishedVersionID)
	s.Require().NoError(err, "ExistsActiveByUserIDAndPlanVersionID after delete")
	s.Require().False(exists)
}

// --- Combined scenario ---

func (s *UserSubscriptionRepoSuite) TestActiveExpiredBoundaries_UsageAndReset_BatchUpdateExpiredStatus() {
	user := s.mustCreateUser("subr@example.com", service.RoleUser)
	groupActive := s.mustCreatePlan("g-subr-active")
	groupExpired := s.mustCreatePlan("g-subr-expired")

	active := s.mustCreateSubscription(user.ID, groupActive.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetExpiresAt(time.Now().Add(2 * time.Hour))
	})
	expiredActive := s.mustCreateSubscription(user.ID, groupExpired.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetExpiresAt(time.Now().Add(-2 * time.Hour))
	})

	got, err := s.repo.GetActiveByUserIDAndPlanID(s.ctx, user.ID, groupActive.ID)
	s.Require().NoError(err, "GetActiveByUserIDAndPlanID")
	s.Require().Equal(active.ID, got.ID, "expected active subscription")

	activateAt := time.Now().Add(-25 * time.Hour)
	s.Require().NoError(s.repo.ActivateWindows(s.ctx, active.ID, activateAt, activateAt), "ActivateWindows")
	_, err = s.client.UserSubscription.UpdateOneID(active.ID).
		SetDailyUsageUsd(1.25).
		SetWeeklyUsageUsd(1.25).
		SetMonthlyUsageUsd(1.25).
		Save(s.ctx)
	s.Require().NoError(err, "seed usage")

	after, err := s.repo.GetByID(s.ctx, active.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().InDelta(1.25, after.DailyUsageUSD, 1e-6)
	s.Require().InDelta(1.25, after.WeeklyUsageUSD, 1e-6)
	s.Require().InDelta(1.25, after.MonthlyUsageUSD, 1e-6)
	s.Require().NotNil(after.DailyWindowStart, "expected DailyWindowStart activated")
	s.Require().NotNil(after.WeeklyWindowStart, "expected WeeklyWindowStart activated")
	s.Require().NotNil(after.MonthlyWindowStart, "expected MonthlyWindowStart activated")

	resetAt := time.Now().Truncate(time.Microsecond) // truncate to microsecond for DB precision
	s.Require().NoError(s.repo.ResetDailyUsage(s.ctx, active.ID, after.DailyWindowStart, resetAt), "ResetDailyUsage")
	afterReset, err := s.repo.GetByID(s.ctx, active.ID)
	s.Require().NoError(err, "GetByID after reset")
	s.Require().InDelta(0.0, afterReset.DailyUsageUSD, 1e-6)
	s.Require().NotNil(afterReset.DailyWindowStart)
	s.Require().WithinDuration(resetAt, *afterReset.DailyWindowStart, time.Microsecond)

	affected, err := s.repo.BatchUpdateExpiredStatus(s.ctx)
	s.Require().NoError(err, "BatchUpdateExpiredStatus")
	s.Require().Equal(int64(1), affected, "expected 1 affected row")

	updated, err := s.repo.GetByID(s.ctx, expiredActive.ID)
	s.Require().NoError(err, "GetByID expired")
	s.Require().Equal(service.SubscriptionStatusExpired, updated.Status, "expected status expired")
}

// --- nil 入参测试 ---

func (s *UserSubscriptionRepoSuite) TestCreate_NilInput() {
	err := s.repo.Create(s.ctx, nil)
	s.Require().Error(err, "Create should fail with nil input")
	s.Require().ErrorIs(err, service.ErrSubscriptionNilInput)
}

func (s *UserSubscriptionRepoSuite) TestUpdate_NilInput() {
	err := s.repo.Update(s.ctx, nil)
	s.Require().Error(err, "Update should fail with nil input")
	s.Require().ErrorIs(err, service.ErrSubscriptionNilInput)
}

func (s *UserSubscriptionRepoSuite) TestTxContext_RollbackIsolation() {
	baseClient := testEntClient(s.T())
	tx, err := baseClient.Tx(context.Background())
	s.Require().NoError(err, "begin tx")
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	txCtx := dbent.NewTxContext(context.Background(), tx)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	userEnt, err := tx.Client().User.Create().
		SetEmail("tx-user-" + suffix + "@example.com").
		SetPasswordHash("test").
		Save(txCtx)
	s.Require().NoError(err, "create user in tx")

	planEnt, err := tx.Client().SubscriptionPlan.Create().
		SetName("tx-plan-" + suffix).
		Save(txCtx)
	s.Require().NoError(err, "create plan in tx")
	versionEnt, err := tx.Client().SubscriptionPlanVersion.Create().
		SetPlanID(planEnt.ID).
		SetVersion(1).
		SetPrice(10).
		SetValidityDays(30).
		SetValidityUnit("day").
		Save(txCtx)
	s.Require().NoError(err, "create plan version in tx")
	_, err = tx.Client().SubscriptionPlan.UpdateOneID(planEnt.ID).
		SetPublishedVersionID(versionEnt.ID).
		Save(txCtx)
	s.Require().NoError(err, "publish plan version in tx")

	repo := NewUserSubscriptionRepository(baseClient)
	sub := &service.UserSubscription{
		UserID:        userEnt.ID,
		PlanID:        planEnt.ID,
		PlanVersionID: versionEnt.ID,
		ExpiresAt:     time.Now().AddDate(0, 0, 30),
		Status:        service.SubscriptionStatusActive,
		AssignedAt:    time.Now(),
		Notes:         "tx",
	}
	s.Require().NoError(repo.Create(txCtx, sub), "create subscription in tx")
	s.Require().NoError(repo.UpdateNotes(txCtx, sub.ID, "tx-note"), "update subscription in tx")

	s.Require().NoError(tx.Rollback(), "rollback tx")
	tx = nil

	_, err = repo.GetByID(context.Background(), sub.ID)
	s.Require().ErrorIs(err, service.ErrSubscriptionNotFound)
}
