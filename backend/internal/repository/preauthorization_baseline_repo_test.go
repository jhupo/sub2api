//go:build unit

package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestLoadPreauthorizationBaselineTreatsMissingAndIdleRowsAsZero(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(`SELECT CASE WHEN updated_at > NOW\(\)`).
		WithArgs("baseline-key", preauthorizationBaselineIdleSeconds).
		WillReturnError(sql.ErrNoRows)
	amount, err := (&usageBillingRepository{db: db}).LoadPreauthorizationBaseline(context.Background(), "baseline-key")
	require.NoError(t, err)
	require.Zero(t, amount)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRecordPreauthorizationBaselineReplacesPreviousAmount(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectExec(`INSERT INTO billing_preauthorization_baselines`).
		WithArgs("baseline-key", 0.125).
		WillReturnResult(sqlmock.NewResult(1, 1))
	require.NoError(t, (&usageBillingRepository{db: db}).RecordPreauthorizationBaseline(context.Background(), "baseline-key", 0.1250000004))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBuildBalancePreauthorizationBaselineKeySeparatesSessionWithoutPayload(t *testing.T) {
	first := service.BuildBalancePreauthorizationBaselineKey(1, 2, service.FundingSourceWallet, 0, "session-a")
	second := service.BuildBalancePreauthorizationBaselineKey(1, 2, service.FundingSourceWallet, 0, "session-b")
	require.NotEqual(t, first, second)
	require.Equal(t, first, service.BuildBalancePreauthorizationBaselineKey(1, 2, service.FundingSourceWallet, 0, "session-a"))
}
