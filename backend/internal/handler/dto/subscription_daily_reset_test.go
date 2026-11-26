package dto

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionDTOIncludesAuthoritativeDailyResetTime(t *testing.T) {
	start := time.Date(2026, 8, 6, 20, 0, 0, 0, time.UTC)
	midnight := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	sub := &service.UserSubscription{
		StartsAt: start, ExpiresAt: start.Add(48 * time.Hour), RenewalCount: 1,
		DailyWindowStart: &midnight,
		Plan:             &service.SubscriptionPlan{ValidityDays: 1, ValidityUnit: "days"},
	}
	dto := UserSubscriptionFromService(sub)
	require.NotNil(t, dto.DailyResetAt)
	require.Equal(t, start.Add(24*time.Hour), *dto.DailyResetAt)
}
