package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type recoverySubscriptionRepo struct {
	SubscriptionAllowanceRepository
	released, captured int
	amount             float64
	fingerprint        string
	err                error
}

func (r *recoverySubscriptionRepo) ReleaseSubscriptionAllowance(_ context.Context, cmd *SubscriptionAllowanceCommand) (*SubscriptionAllowanceReservation, error) {
	r.released++
	r.amount = cmd.Amount
	return nil, r.err
}
func (r *recoverySubscriptionRepo) CaptureSubscriptionAllowance(_ context.Context, cmd *SubscriptionAllowanceCommand, fingerprint string) (*SubscriptionAllowanceReservation, error) {
	r.captured++
	r.amount, r.fingerprint = cmd.Amount, fingerprint
	return nil, r.err
}

func TestRecoverSubscriptionRequiresDurableMeasuredUsage(t *testing.T) {
	for _, status := range []string{BillingReservationAuthorized, BillingReservationFinalizing} {
		t.Run(status, func(t *testing.T) {
			repo := &recoverySubscriptionRepo{}
			svc := &BalancePreauthorizationService{subscriptionRepo: repo}
			record := SubscriptionAllowanceReservation{RequestID: "request", APIKeyID: 1, UserID: 2, SubscriptionID: 3,
				Status: status, AuthorizedAmount: 10, CapturedAmount: 0.25, RequestFingerprint: "durable-usage"}
			require.NoError(t, svc.RecoverSubscriptionAllowance(context.Background(), record))
			if status == BillingReservationAuthorized {
				require.Equal(t, 1, repo.released)
				require.Zero(t, repo.captured)
				require.Zero(t, repo.amount)
			} else {
				require.Equal(t, 1, repo.captured)
				require.Zero(t, repo.released)
				require.Equal(t, 0.25, repo.amount)
				require.Equal(t, "durable-usage", repo.fingerprint)
			}
			repo.err = errors.New("database unavailable")
			require.ErrorIs(t, svc.RecoverSubscriptionAllowance(context.Background(), record), repo.err)
		})
	}
}

func TestRecoverWalletFinalizingUsesActualNotHold(t *testing.T) {
	fixture := newPreauthorizationFixture()
	err := fixture.service.RecoverBalancePreauthorization(context.Background(), BalancePreauthorizationRecord{
		RequestID: "measured", APIKeyID: 7, UserID: 42, HoldAmount: 10, Amount: 0.25,
		Status: BalanceSettlementFinalizationPending, RequestFingerprint: "durable-usage",
	})
	require.NoError(t, err)
	require.Equal(t, 1, fixture.wallet.finalizeCalls)
	require.Equal(t, 0.25, fixture.wallet.lastActual)
	require.Zero(t, fixture.wallet.refundCalls)
	require.Equal(t, []string{"wallet_finalize", "repo_complete_settlement"}, fixture.recorder.snapshot())
}
