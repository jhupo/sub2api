package repository

import (
	"context"
	"regexp"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodexQuotaUpdateExtraRejectsOlderObservationAtomically(t *testing.T) {
	for _, affected := range []int64{0, 1} {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
		t.Cleanup(func() { _ = client.Close() })
		repo := &accountRepository{client: client}
		mock.ExpectExec(`UPDATE accounts SET extra = .*`+regexp.QuoteMeta("AND COALESCE((extra->>'codex_usage_observed_at_us')::bigint, 0) < $3")).
			WithArgs(sqlmock.AnyArg(), int64(1), int64(200)).WillReturnResult(sqlmock.NewResult(0, affected))
		if affected == 0 {
			mock.ExpectQuery(`SELECT .*accounts.*`).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(1)))
		}
		require.NoError(t, repo.UpdateExtra(context.Background(), 1, map[string]any{"codex_usage_observed_at_us": int64(200), "codex_5h_used_percent": float64(3)}))
		require.NoError(t, mock.ExpectationsWereMet())
	}
}

func TestCodexQuotaUpdateExtraRejectsInvalidObservation(t *testing.T) {
	repo := &accountRepository{}
	for _, value := range []any{nil, float64(200), "200", int64(0), int64(-1)} {
		err := repo.UpdateExtra(context.Background(), 1, map[string]any{"codex_usage_observed_at_us": value})
		require.ErrorContains(t, err, "positive int64 timestamp")
	}
}

func TestCodexQuotaAccountEditPreservesLockedObservations(t *testing.T) {
	for _, current := range []string{
		`{"codex_usage_observed_at_us":1788920000000000,"codex_usage_updated_at":"2026-09-09T04:00:00Z","codex_5h_used_percent":82,"codex_7d_used_percent":40}`, "{}",
	} {
		t.Run(current, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			t.Cleanup(func() { _ = client.Close() })
			mock.ExpectQuery(`(?s)SELECT.*extra\s+FROM accounts.*FOR NO KEY UPDATE`).
				WithArgs(int64(1), service.PlatformOpenAI, service.AccountTypeOAuth, `{"access_token":"refreshed"}`, nil).
				WillReturnRows(sqlmock.NewRows([]string{"identity_unchanged", "ollama_group_unchanged", "ollama_proxy_unchanged", "enabled", "rate_sync_enabled", "snapshot", "ollama_session", "ollama_auto", "ollama_snapshot", "extra"}).
					AddRow(false, false, true, nil, nil, nil, nil, nil, nil, []byte(current)))
			account := &service.Account{
				ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
				Credentials: map[string]any{"access_token": "refreshed"},
				Extra: map[string]any{
					"codex_usage_observed_at_us": float64(1788910000000000),
					"codex_usage_updated_at":     "2026-09-09T03:00:00Z", "codex_5h_used_percent": float64(10),
					"codex_5h_reset_at": "stale", "codex_cli_only": true, "auto_pause_5h_threshold": 0.95,
				},
			}
			got, err := lockAndMergeAccountExtra(context.Background(), client, account, nil, nil)
			require.NoError(t, err)
			require.True(t, got["codex_cli_only"].(bool))
			require.Equal(t, 0.95, got["auto_pause_5h_threshold"])
			require.NotContains(t, got, "codex_5h_reset_at")
			if current == "{}" {
				require.NotContains(t, got, "codex_usage_observed_at_us")
				require.NotContains(t, got, "codex_usage_updated_at")
				require.NotContains(t, got, "codex_5h_used_percent")
			} else {
				require.Equal(t, float64(1788920000000000), got["codex_usage_observed_at_us"])
				require.Equal(t, "2026-09-09T04:00:00Z", got["codex_usage_updated_at"])
				require.Equal(t, float64(82), got["codex_5h_used_percent"])
				require.Equal(t, float64(40), got["codex_7d_used_percent"])
			}
			require.Equal(t, float64(10), account.Extra["codex_5h_used_percent"])
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
