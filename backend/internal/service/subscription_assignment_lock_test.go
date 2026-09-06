package service

import (
	"context"
	"regexp"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

func TestLockSubscriptionAssignmentUsesSinglePostgresAdvisoryKey(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	mock.ExpectBegin()
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	txCtx := dbent.NewTxContext(ctx, tx)

	const userID int64 = 41
	const planVersionID int64 = 73
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1::bigint)")).
		WithArgs(subscriptionAssignmentAdvisoryLockKey(userID, planVersionID)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, lockSubscriptionAssignment(txCtx, userID, planVersionID))
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSubscriptionAssignmentAdvisoryLockKeyIncludesBothIDs(t *testing.T) {
	base := subscriptionAssignmentAdvisoryLockKey(1, 23)
	require.Equal(t, base, subscriptionAssignmentAdvisoryLockKey(1, 23))
	require.NotEqual(t, base, subscriptionAssignmentAdvisoryLockKey(12, 3))
	require.NotEqual(t, base, subscriptionAssignmentAdvisoryLockKey(2, 23))
	require.NotEqual(t, base, subscriptionAssignmentAdvisoryLockKey(1, 24))
}
