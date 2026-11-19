package service

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

type affiliateGroupDiscountStoreStub struct {
	AffiliateRepository
	input       AffiliateUserSettingsInput
	updateCalls int
}

func (s *affiliateGroupDiscountStoreStub) SetInviteeGroupDiscountFactors(context.Context, int64, map[int64]float64) error {
	return nil
}

func (s *affiliateGroupDiscountStoreStub) ClearUserAffiliateSettings(context.Context, int64) error {
	return nil
}

func (s *affiliateGroupDiscountStoreStub) UpdateUserAffiliateSettings(_ context.Context, _ int64, input AffiliateUserSettingsInput) error {
	s.input = input
	s.updateCalls++
	return nil
}

func TestAffiliateGroupDiscountValidation(t *testing.T) {
	for _, factors := range []map[int64]float64{nil, {}, {1: 0.01, 2: 0.9, 3: 1}} {
		require.NoError(t, validateInviteeGroupDiscountFactors(factors))
	}
	for _, factors := range []map[int64]float64{
		{0: 0.9}, {-1: 0.9}, {1: 0}, {1: -0.1}, {1: 1.01},
		{1: math.NaN()}, {1: math.Inf(1)},
	} {
		require.Error(t, validateInviteeGroupDiscountFactors(factors))
	}
}

func TestAffiliateUpdateUserSettings_ValidatesBeforeWriting(t *testing.T) {
	repo := &affiliateGroupDiscountStoreStub{}
	svc := &AffiliateService{repo: repo}
	code := " vip2026 "
	factors := map[int64]float64{1: 0}
	input := AffiliateUserSettingsInput{AffCode: &code, InviteeGroupDiscountFactors: &factors}

	require.Error(t, svc.AdminUpdateUserSettings(context.Background(), 1, input))
	require.Zero(t, repo.updateCalls)

	factors[1] = 0.9
	require.NoError(t, svc.AdminUpdateUserSettings(context.Background(), 1, input))
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, "VIP2026", *repo.input.AffCode)
	require.Equal(t, factors, *repo.input.InviteeGroupDiscountFactors)

	require.NoError(t, svc.AdminUpdateUserSettings(context.Background(), 1, AffiliateUserSettingsInput{}))
	require.Nil(t, repo.input.InviteeGroupDiscountFactors, "omission must preserve existing factors")

	emptyFactors := map[int64]float64{}
	require.NoError(t, svc.AdminUpdateUserSettings(context.Background(), 1, AffiliateUserSettingsInput{InviteeGroupDiscountFactors: &emptyFactors}))
	require.NotNil(t, repo.input.InviteeGroupDiscountFactors)
	require.Empty(t, *repo.input.InviteeGroupDiscountFactors, "an explicit empty map clears factors")
}
