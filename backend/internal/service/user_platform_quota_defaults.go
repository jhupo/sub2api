package service

import (
	"context"

	dbent "github.com/Wei-Shaw/sub2api/ent"
)

func snapshotUserPlatformQuotaDefaults(ctx context.Context, repo UserPlatformQuotaRepository, userID int64, quotas map[string]*DefaultPlatformQuotaSetting) error {
	if repo == nil || userID <= 0 || len(quotas) == 0 {
		return nil
	}
	records := make([]UserPlatformQuotaRecord, 0, len(quotas))
	for platform, quota := range quotas {
		if quota == nil || (quota.DailyLimitUSD == nil && quota.WeeklyLimitUSD == nil && quota.MonthlyLimitUSD == nil) {
			continue
		}
		records = append(records, UserPlatformQuotaRecord{
			UserID:          userID,
			Platform:        platform,
			DailyLimitUSD:   quota.DailyLimitUSD,
			WeeklyLimitUSD:  quota.WeeklyLimitUSD,
			MonthlyLimitUSD: quota.MonthlyLimitUSD,
		})
	}
	if len(records) == 0 {
		return nil
	}
	// Best-effort initialization must not abort a caller's user-creation transaction.
	return repo.BulkInsertInitial(dbent.WithoutTx(ctx), records)
}
