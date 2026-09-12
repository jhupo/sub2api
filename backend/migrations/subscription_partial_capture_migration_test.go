package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubscriptionPartialCaptureMigrationDefinesActualAmountAndTerminalState(t *testing.T) {
	content, err := FS.ReadFile("242_subscription_partial_capture.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")

	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS actual_amount DECIMAL(20, 10) NOT NULL DEFAULT 0")
	require.Contains(t, sql, "SET actual_amount = captured_amount")
	require.Contains(t, sql, "captured_amount <= actual_amount")
	require.Contains(t, sql, "'partially_captured'")
}
