package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type apiKeyGroupRateDetailsRepoStub struct {
	UserGroupRateRepository
	details map[int64]UserGroupRate
	err     error
	userID  int64
}

func (s *apiKeyGroupRateDetailsRepoStub) GetUserGroupRates(_ context.Context, userID int64) (map[int64]UserGroupRate, error) {
	s.userID = userID
	return s.details, s.err
}

func (s *apiKeyGroupRateDetailsRepoStub) GetEffectiveByUserID(_ context.Context, _ int64) (map[int64]float64, error) {
	rates := make(map[int64]float64, len(s.details))
	for id, rate := range s.details {
		rates[id] = rate.BaseRate()
	}
	return rates, s.err
}

func TestAPIKeyService_GetUserGroupRateDetails(t *testing.T) {
	personal, invite := 0.8, 0.9
	repo := &apiKeyGroupRateDetailsRepoStub{details: map[int64]UserGroupRate{7: {
		GroupRateMultiplier: 2, UserRateMultiplier: &personal, InviteeDiscountFactor: &invite,
	}}}
	svc := &APIKeyService{userGroupRateRepo: repo}
	details, err := svc.GetUserGroupRateDetails(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, int64(42), repo.userID)
	require.Equal(t, repo.details, details)
	legacy, err := svc.GetUserGroupRates(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, map[int64]float64{7: 1.8}, legacy)

	repo.err = context.DeadlineExceeded
	_, err = svc.GetUserGroupRateDetails(context.Background(), 42)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestAPIKeyService_GetUserGroupRateDetails_OptionalRepository(t *testing.T) {
	svc := &APIKeyService{}
	details, err := svc.GetUserGroupRateDetails(context.Background(), 42)
	require.NoError(t, err)
	require.Nil(t, details)
}
