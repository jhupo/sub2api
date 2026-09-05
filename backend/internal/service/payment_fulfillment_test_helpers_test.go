//go:build unit

package service

import (
	"context"
	"strconv"
)

type subscriptionUserSubRepoStub struct {
	userSubRepoNoop
	nextID      int64
	byID        map[int64]*UserSubscription
	byUserPlan  map[string]*UserSubscription
	createCalls int
}

func newSubscriptionUserSubRepoStub() *subscriptionUserSubRepoStub {
	return &subscriptionUserSubRepoStub{
		nextID:     1,
		byID:       make(map[int64]*UserSubscription),
		byUserPlan: make(map[string]*UserSubscription),
	}
}

func subscriptionPlanKey(userID, planVersionID int64) string {
	return strconv.FormatInt(userID, 10) + ":" + strconv.FormatInt(planVersionID, 10)
}

func (s *subscriptionUserSubRepoStub) seed(sub *UserSubscription) {
	if sub == nil {
		return
	}
	cp := *sub
	if cp.ID == 0 {
		cp.ID = s.nextID
		s.nextID++
	}
	if cp.PlanVersionID == 0 {
		cp.PlanVersionID = cp.PlanID
	}
	s.byID[cp.ID] = &cp
	s.byUserPlan[subscriptionPlanKey(cp.UserID, cp.PlanVersionID)] = &cp
}

func (s *subscriptionUserSubRepoStub) GetByUserIDAndPlanVersionID(_ context.Context, userID, planVersionID int64) (*UserSubscription, error) {
	sub := s.byUserPlan[subscriptionPlanKey(userID, planVersionID)]
	if sub == nil {
		return nil, ErrSubscriptionNotFound
	}
	cp := *sub
	return &cp, nil
}

func (s *subscriptionUserSubRepoStub) ExistsByUserIDAndPlanID(_ context.Context, userID, planID int64) (bool, error) {
	for _, sub := range s.byID {
		if sub.UserID == userID && sub.PlanID == planID {
			return true, nil
		}
	}
	return false, nil
}

func (s *subscriptionUserSubRepoStub) ExistsActiveByUserIDAndPlanVersionID(_ context.Context, userID, planVersionID int64) (bool, error) {
	sub, err := s.GetByUserIDAndPlanVersionID(context.Background(), userID, planVersionID)
	return err == nil && sub.Status == SubscriptionStatusActive, nil
}

func (s *subscriptionUserSubRepoStub) Create(_ context.Context, sub *UserSubscription) error {
	if sub == nil {
		return ErrSubscriptionNilInput
	}
	s.createCalls++
	cp := *sub
	if cp.ID == 0 {
		cp.ID = s.nextID
		s.nextID++
	}
	s.byID[cp.ID] = &cp
	s.byUserPlan[subscriptionPlanKey(cp.UserID, cp.PlanVersionID)] = &cp
	sub.ID = cp.ID
	return nil
}

func (s *subscriptionUserSubRepoStub) GetByID(_ context.Context, id int64) (*UserSubscription, error) {
	sub := s.byID[id]
	if sub == nil {
		return nil, ErrSubscriptionNotFound
	}
	cp := *sub
	return &cp, nil
}

func (s *subscriptionUserSubRepoStub) GetByIDForUpdate(ctx context.Context, id int64) (*UserSubscription, error) {
	return s.GetByID(ctx, id)
}

func (s *subscriptionUserSubRepoStub) Update(_ context.Context, sub *UserSubscription) error {
	if sub == nil {
		return ErrSubscriptionNilInput
	}
	if _, ok := s.byID[sub.ID]; !ok {
		return ErrSubscriptionNotFound
	}
	cp := *sub
	s.byID[cp.ID] = &cp
	s.byUserPlan[subscriptionPlanKey(cp.UserID, cp.PlanVersionID)] = &cp
	return nil
}
