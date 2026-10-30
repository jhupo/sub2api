package repository

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usageadjustment"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCacheReadAdjustmentUsageInsertAndRead(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := &usageLogRepository{sql: db}
	baseline, delta := 0.01, 0.005
	log := &service.UsageLog{UserID: 1, APIKeyID: 2, AccountID: 29, Model: "gpt-5.1", RequestID: "request",
		CacheReadAdjustment: &service.CacheReadAdjustmentUsage{Snapshot: usageadjustment.Snapshot{Percent: 10, OriginalCacheTokens: 8000, ShiftedTokens: 800},
			BaselineActualCost: &baseline, AdditionalActualCost: &delta}}
	prepared := prepareUsageLogInsert(log)
	require.Len(t, prepared.args, len(usageLogInsertArgTypes))
	values := []driver.Value{int64(44)}
	for _, arg := range prepared.args {
		value, err := driver.DefaultParameterConverter.ConvertValue(arg)
		require.NoError(t, err)
		values = append(values, value)
	}
	columns := strings.Split(usageLogSelectColumns, ", ")
	mock.ExpectQuery("SELECT .* FROM usage_logs WHERE id = \\$1").WithArgs(int64(44)).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(values...))
	stored, err := repo.GetByID(context.Background(), 44)
	require.NoError(t, err)
	require.Equal(t, log.CacheReadAdjustment, stored.CacheReadAdjustment)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCacheReadAdjustmentDailyUsesSnapshotsAndAccountFilter(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	mock.ExpectQuery("(?s)SELECT TO_CHAR.*cache_read_adjustment IS NOT NULL AND account_id = \\$4.*GROUP BY").
		WithArgs(start, end, "Asia/Shanghai", int64(29)).
		WillReturnRows(sqlmock.NewRows([]string{"day", "account_id", "requests", "priced", "tokens", "total", "wallet", "subscription"}).
			AddRow("2026-10-30", 29, 3, 2, 1600, 0.02, 0.012, 0.008))
	days, err := repo.GetCacheReadAdjustmentDaily(context.Background(), start, end, "Asia/Shanghai", 29)
	require.NoError(t, err)
	require.Equal(t, []service.CacheReadAdjustmentDay{{Date: "2026-10-30", AccountID: 29, Requests: 3, PricedRequests: 2,
		ShiftedTokens: 1600, AdditionalCost: 0.02, BalanceAdditionalCost: 0.012, SubscriptionAdditionalCost: 0.008}}, days)
	require.NoError(t, mock.ExpectationsWereMet())
}
