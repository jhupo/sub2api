package repository

import (
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestUpstreamTimeRatesPostgresSort(t *testing.T) {
	dsn := os.Getenv("SUB2API_FEATURE_TEST_DSN")
	if dsn == "" {
		t.Skip("SUB2API_FEATURE_TEST_DSN is required")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	for _, tc := range []struct {
		name, at, timezone string
		night              bool
		want               float64
		valid              bool
	}{
		{"night before midnight", "2026-11-07T23:00:00Z", "UTC", true, 0.4, true},
		{"night after midnight", "2026-11-08T07:59:00Z", "UTC", true, 0.4, true},
		{"night end exclusive", "2026-11-08T08:00:00Z", "UTC", true, 0.8, true},
		{"peak independent", "2026-11-08T15:00:00Z", "UTC", true, 1.6, true},
		{"disabled night", "2026-11-08T01:00:00Z", "UTC", false, 0.8, true},
		{"invalid timezone", "2026-11-08T01:00:00Z", "Invalid/Zone", true, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := map[string]any{"status": "ok", "data": map[string]any{"billing_scope": "token", "resolved_rate_multiplier": 0.8, "peak_rate_enabled": true, "peak_start": "14:00", "peak_end": "18:00", "peak_rate_multiplier": 2, "night_rate_enabled": tc.night, "night_start": "22:00", "night_end": "08:00", "night_rate_multiplier": 0.5, "timezone": tc.timezone}}
			raw, err := json.Marshal(map[string]any{"upstream_billing_probe": data})
			require.NoError(t, err)
			expression := strings.ReplaceAll(upstreamBillingRateSortExpression("extra"), "CURRENT_TIMESTAMP", "TIMESTAMPTZ '"+tc.at+"'")
			var rate sql.NullFloat64
			err = db.QueryRow("WITH sample AS (SELECT $1::jsonb AS extra) SELECT "+expression+" FROM sample", string(raw)).Scan(&rate)
			require.NoError(t, err)
			require.Equal(t, tc.valid, rate.Valid)
			if tc.valid {
				require.InDelta(t, tc.want, rate.Float64, 0.000001)
			}
		})
	}
}
