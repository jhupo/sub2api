//go:build unit

package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageBillingDeletedKeyStillSettlesBalanceAndAccount(t *testing.T) {
	for _, preauthorized := range []bool{false, true} {
		name := "queued balance"
		if preauthorized {
			name = "preauthorized balance"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			mock.ExpectBegin()
			tx, err := db.BeginTx(ctx, nil)
			require.NoError(t, err)
			cmd := &service.UsageBillingCommand{
				RequestID: "deleted-key", RequestFingerprint: "final-fingerprint",
				APIKeyID: 7, UserID: 42, AccountID: 13, AccountType: service.AccountTypeAPIKey,
				BalanceCost: 1.25, BalancePreauthorized: preauthorized,
				APIKeyQuotaCost: 1.25, APIKeyRateLimitCost: 1.25, AccountQuotaCost: 0.8,
			}
			if preauthorized {
				mock.ExpectExec("(?s)UPDATE billing_balance_settlements").
					WithArgs(cmd.RequestID, cmd.APIKeyID, cmd.BalanceCost, cmd.RequestFingerprint,
						service.BalanceSettlementFinalizationPending, service.BalanceSettlementAuthorized).
					WillReturnResult(sqlmock.NewResult(0, 1))
			} else {
				mock.ExpectQuery(enqueueUsageBalanceSQL).
					WithArgs(cmd.RequestID, cmd.APIKeyID, cmd.RequestFingerprint, cmd.UserID, cmd.BalanceCost, service.BalanceSettlementPending).
					WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(service.BalanceSettlementPending))
			}
			mock.ExpectQuery("(?s)UPDATE api_keys SET quota_used").
				WithArgs(cmd.APIKeyQuotaCost, cmd.APIKeyID, service.StatusAPIKeyActive, service.StatusAPIKeyQuotaExhausted).
				WillReturnError(sql.ErrNoRows)
			mock.ExpectExec("(?s)UPDATE api_keys SET usage_5h").
				WithArgs(cmd.APIKeyRateLimitCost, cmd.APIKeyID).
				WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectQuery("(?s)UPDATE accounts SET extra").
				WithArgs(cmd.AccountQuotaCost, cmd.AccountID).
				WillReturnRows(sqlmock.NewRows([]string{"total_used", "total_limit", "daily_used", "daily_limit", "weekly_used", "weekly_limit"}).
					AddRow(4.0, 100.0, 1.0, 10.0, 1.0, 10.0))
			mock.ExpectCommit()

			result := &service.UsageBillingApplyResult{}
			require.NoError(t, (&usageBillingRepository{}).applyUsageBillingEffects(ctx, tx, cmd, result))
			require.Equal(t, preauthorized, result.BalanceFinalizationPending)
			require.False(t, result.APIKeyQuotaExhausted)
			require.NotNil(t, result.QuotaState)
			require.Equal(t, 4.0, result.QuotaState.TotalUsed)
			require.NoError(t, tx.Commit())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestUsageBillingKeyCounterStorageErrorStillFailsTransaction(t *testing.T) {
	for _, counter := range []string{"quota", "rate"} {
		t.Run(counter, func(t *testing.T) {
			ctx := context.Background()
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			mock.ExpectBegin()
			tx, err := db.BeginTx(ctx, nil)
			require.NoError(t, err)
			storageErr := errors.New("storage unavailable")
			cmd := &service.UsageBillingCommand{APIKeyID: 7}
			if counter == "quota" {
				cmd.APIKeyQuotaCost = 1
				mock.ExpectQuery("(?s)UPDATE api_keys SET quota_used").WillReturnError(storageErr)
			} else {
				cmd.APIKeyRateLimitCost = 1
				mock.ExpectExec("(?s)UPDATE api_keys SET usage_5h").WillReturnError(storageErr)
			}
			mock.ExpectRollback()
			err = (&usageBillingRepository{}).applyUsageBillingEffects(ctx, tx, cmd, &service.UsageBillingApplyResult{})
			require.ErrorIs(t, err, storageErr)
			require.NoError(t, tx.Rollback())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
