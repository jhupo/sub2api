package repository

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodexOverdraftProbeUpdatesUseSafeTimestampExpressions(t *testing.T) {
	var observedQueries []string
	matcher := sqlmock.QueryMatcherFunc(func(expected, actual string) error {
		normalized := normalizeSQLWhitespace(strings.ToLower(actual))
		observedQueries = append(observedQueries, normalized)
		require.NotContains(t, normalized, "pg_input_is_valid")
		require.Contains(t, normalized, "make_date")
		require.Contains(t, normalized, "extract(year from")
		matched, err := regexp.MatchString(expected, actual)
		if err != nil {
			return err
		}
		if !matched {
			return fmt.Errorf("query %q does not match %q", actual, expected)
		}
		return nil
	})
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	recoverAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	state := &service.CodexQuotaOverdraftProbeState{
		Status:    "pending",
		CycleKey:  "cycle-1",
		RecoverAt: &recoverAt,
	}

	mock.ExpectExec(`(?s)\s*UPDATE accounts`).
		WithArgs(service.CodexQuotaOverdraftProbeExtraKey, sqlmock.AnyArg(), int64(7), "cycle-1", recoverAt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	claimed, err := repo.ClaimCodexQuotaOverdraftProbe(context.Background(), 7, state)
	require.NoError(t, err)
	require.True(t, claimed)

	state.Status = "passed"
	mock.ExpectExec(`(?s)\s*UPDATE accounts`).
		WithArgs(service.CodexQuotaOverdraftProbeExtraKey, sqlmock.AnyArg(), int64(7), "cycle-1", recoverAt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	persisted, err := repo.PersistCodexQuotaOverdraftProbeUnlessFailed(context.Background(), 7, state)
	require.NoError(t, err)
	require.True(t, persisted)
	require.Len(t, observedQueries, 2)
	require.NoError(t, mock.ExpectationsWereMet())
}
