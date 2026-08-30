//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestListRecoverableBalancePreauthorizationsExecutesAgainstPostgres(t *testing.T) {
	resetIntegrationDB(t)
	t.Cleanup(func() { resetIntegrationDB(t) })

	repo := &usageBillingRepository{db: integrationDB}
	cutoff := time.Now().UTC()
	records, err := repo.ListRecoverableBalancePreauthorizations(
		context.Background(), cutoff, cutoff, 1,
	)

	require.NoError(t, err)
	require.Empty(t, records)
}
