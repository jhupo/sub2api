package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexQuotaObservationKeysCoverPublishedSnapshot(t *testing.T) {
	percent, seconds, minutes := 50.0, 600, 300
	snapshot := &OpenAICodexUsageSnapshot{
		PrimaryUsedPercent: &percent, PrimaryResetAfterSeconds: &seconds, PrimaryWindowMinutes: &minutes,
		SecondaryUsedPercent: &percent, SecondaryResetAfterSeconds: &seconds, SecondaryWindowMinutes: &minutes,
		PrimaryOverSecondaryPercent: &percent,
	}
	for key := range buildCodexUsageExtraUpdates(snapshot, time.Now()) {
		require.True(t, IsCodexQuotaObservationExtraKey(key), key)
	}
	for _, key := range []string{"codex_cli_only", "codex_fingerprint_mode", "auto_pause_5h_threshold", "auto_pause_7d_disabled", "quota_used"} {
		require.False(t, IsCodexQuotaObservationExtraKey(key), key)
	}
}

func TestCodexQuotaAdminExtraUpdatesDropObservations(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		name := "single"
		if bulk {
			name = "bulk"
		}
		t.Run(name, func(t *testing.T) {
			repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{
				1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"codex_5h_used_percent": 82.0}},
			}}
			svc := &adminServiceImpl{accountRepo: repo}
			updates := map[string]any{
				"codex_usage_observed_at_us": float64(1788910000000000), "codex_usage_updated_at": "stale",
				"codex_5h_used_percent": 0.0, "auto_pause_5h_threshold": 0.95,
			}
			var written map[string]any
			if bulk {
				result, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1}, Extra: updates})
				require.NoError(t, err)
				require.Equal(t, 1, result.Success)
				require.Len(t, repo.bulkUpdates, 1)
				written = repo.bulkUpdates[0].Extra
				require.NotContains(t, written, "codex_5h_used_percent")
			} else {
				require.NoError(t, svc.UpdateAccountExtra(context.Background(), 1, updates))
				written = repo.accounts[1].Extra
				require.Equal(t, 82.0, written["codex_5h_used_percent"])
			}
			require.NotContains(t, written, "codex_usage_observed_at_us")
			require.NotContains(t, written, "codex_usage_updated_at")
			require.Equal(t, 0.95, written["auto_pause_5h_threshold"])
		})
	}
}
