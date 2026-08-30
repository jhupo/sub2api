package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestListRecoverableBalancePreauthorizationsReturnsFinalizationData(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	authorizationCutoff := time.Now().UTC()
	finalizationCutoff := authorizationCutoff.Add(-30 * time.Second)
	expiresAt := authorizationCutoff.Add(-time.Minute)
	updatedAt := finalizationCutoff.Add(-time.Second)
	mock.ExpectQuery(`(?s)WITH candidates AS .*FROM billing_balance_settlements.*expires_at <= \$1.*updated_at <= NOW\(\) - \(\$7 \* INTERVAL '1 second'\).*updated_at <= \$2.*ORDER BY CASE.*FOR UPDATE SKIP LOCKED.*UPDATE billing_balance_settlements AS settlement.*SET updated_at = NOW\(\).*COALESCE\(settlement\.async_task_id, ''\) AS async_task_id.*FROM leased`).
		WithArgs(authorizationCutoff, finalizationCutoff, 500, int16(0), int16(1), int16(2), int64(60)).
		WillReturnRows(sqlmock.NewRows([]string{
			"request_id", "api_key_id", "user_id", "request_fingerprint", "authorization_fingerprint",
			"hold_usd", "amount_usd", "status", "expires_at", "async_task_id", "updated_at",
		}).AddRow("request", 7, 42, "actual", "authorization", "0.50", "0.25", 2, expiresAt, "", updatedAt))

	repo := &usageBillingRepository{db: db}
	records, err := repo.ListRecoverableBalancePreauthorizations(
		context.Background(), authorizationCutoff, finalizationCutoff, 0,
	)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "request", records[0].RequestID)
	require.Equal(t, "actual", records[0].RequestFingerprint)
	require.Equal(t, "authorization", records[0].AuthorizationFingerprint)
	require.Equal(t, service.BalanceSettlementFinalizationPending, records[0].Status)
	require.InDelta(t, 0.25, records[0].Amount, 1e-12)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestListRecoverableBalancePreauthorizationsClampsBatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	cutoff := time.Now().UTC()
	mock.ExpectQuery(`(?s)FROM billing_balance_settlements.*LIMIT \$3.*FOR UPDATE SKIP LOCKED.*UPDATE billing_balance_settlements`).
		WithArgs(cutoff, cutoff, 5000, int16(0), int16(1), int16(2), int64(60)).
		WillReturnRows(sqlmock.NewRows([]string{
			"request_id", "api_key_id", "user_id", "request_fingerprint", "authorization_fingerprint",
			"hold_usd", "amount_usd", "status", "expires_at", "async_task_id", "updated_at",
		}))

	repo := &usageBillingRepository{db: db}
	records, err := repo.ListRecoverableBalancePreauthorizations(context.Background(), cutoff, cutoff, 99999)
	require.NoError(t, err)
	require.Empty(t, records)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestListRecoverableBalancePreauthorizationsReturnsOriginalExpiryAfterLease(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	cutoff := time.Now().UTC()
	originalExpiry := cutoff.Add(-time.Minute)
	mock.ExpectQuery(`(?s)WITH candidates AS \(\s*SELECT id, expires_at.*UPDATE billing_balance_settlements AS settlement.*SET updated_at = NOW\(\).*settlement\.expires_at.*async_task_id`).
		WithArgs(cutoff, cutoff, 1, int16(0), int16(1), int16(2), int64(60)).
		WillReturnRows(sqlmock.NewRows([]string{
			"request_id", "api_key_id", "user_id", "request_fingerprint", "authorization_fingerprint",
			"hold_usd", "amount_usd", "status", "expires_at", "async_task_id", "updated_at",
		}).AddRow("grok-video:hold:request", 7, 42, "", "authorization", "0.50", "0", 1, originalExpiry, "", cutoff))

	repo := &usageBillingRepository{db: db}
	records, err := repo.ListRecoverableBalancePreauthorizations(context.Background(), cutoff, cutoff, 1)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, originalExpiry, records[0].ExpiresAt)
	require.NoError(t, mock.ExpectationsWereMet())
}
