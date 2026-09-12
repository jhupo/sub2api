package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type streamTopUpWallet struct {
	*preauthorizationWalletStub
	onTopUp func()
}

func (w *streamTopUpWallet) TopUpLiveBalanceAtSnapshotWatermark(ctx context.Context, userID int64, attemptID string, watermark int64, target float64) (LiveBalanceResult, error) {
	if w.onTopUp != nil {
		w.onTopUp()
	}
	return w.preauthorizationWalletStub.TopUpLiveBalanceAtSnapshotWatermark(ctx, userID, attemptID, watermark, target)
}

type streamTopUpAllowanceRepo struct {
	SubscriptionAllowanceRepository
	onTopUp      func()
	topUpErr     error
	topUpCalls   int
	captureCalls int
	captured     float64
	actual       float64
}

func (r *streamTopUpAllowanceRepo) TopUpSubscriptionAllowance(ctx context.Context, cmd *SubscriptionAllowanceCommand) (*SubscriptionAllowanceReservation, error) {
	r.topUpCalls++
	if r.onTopUp != nil {
		r.onTopUp()
	}
	if r.topUpErr != nil {
		return nil, r.topUpErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &SubscriptionAllowanceReservation{AuthorizedAmount: cmd.Amount}, nil
}

func (r *streamTopUpAllowanceRepo) CaptureSubscriptionAllowance(ctx context.Context, cmd *SubscriptionAllowanceCommand, _ string) (*SubscriptionAllowanceReservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.captureCalls++
	r.captured = cmd.Amount
	if cmd.ActualAmount != nil {
		r.actual = *cmd.ActualAmount
	} else {
		r.actual = cmd.Amount
	}
	return &SubscriptionAllowanceReservation{}, nil
}

func newStreamTopUpGuard(t *testing.T, source string, onTopUp func(), topUpErr error) (*BalancePreauthorizationGuard, *preauthorizationFixture, *streamTopUpAllowanceRepo) {
	t.Helper()
	fixture := newPreauthorizationFixture()
	guard := streamingPreauthorizationGuard(t, fixture)
	if source == FundingSourceWallet {
		fixture.wallet.topUpErr = topUpErr
		fixture.service.watermarkWallet = &streamTopUpWallet{preauthorizationWalletStub: fixture.wallet, onTopUp: onTopUp}
		return guard, fixture, nil
	}
	repo := &streamTopUpAllowanceRepo{onTopUp: onTopUp, topUpErr: topUpErr}
	guard.core.reservation = &subscriptionPreauthorizationReservation{
		repo: repo,
		cmd:  SubscriptionAllowanceCommand{RequestID: guard.RequestID(), APIKeyID: 1, SubscriptionID: 7},
	}
	return guard, fixture, repo
}

func TestOpenAIStreamTopUpCancellationPreservesUsageAndSettles(t *testing.T) {
	for _, mode := range []string{"native", "native_async", "passthrough"} {
		for _, source := range []string{FundingSourceWallet, FundingSourceSubscription} {
			for _, hasUsage := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/usage=%t", mode, source, hasUsage), func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					guard, fixture, allowance := newStreamTopUpGuard(t, source, cancel, nil)
					ctx = ContextWithBalancePreauthorizationGuard(ctx, guard)
					body := []byte(`{"model":"gpt-5.4","input":"hello","stream":true}`)
					c, rec := newOpenAIPartialUsageContext(t, body)
					c.Request = c.Request.WithContext(ctx)
					usage := ""
					if hasUsage {
						usage = `,"usage":{"input_tokens":17,"output_tokens":4,"input_tokens_details":{"cached_tokens":9}}`
					}
					stream := &passthroughCloseTrackingReadCloser{Reader: strings.NewReader(fmt.Sprintf(
						"data: {\"type\":\"response.output_text.delta\",\"response_id\":\"resp_cancel\",\"delta\":\"%s\"%s}\n\n",
						strings.Repeat("x", 1024), usage,
					))}
					upstream := &httpUpstreamRecorder{resp: &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
						Body:       stream,
					}}
					svc := newOpenAIPartialUsageService(upstream)
					if mode == "native_async" {
						svc.cfg.Gateway.StreamKeepaliveInterval = 1
					}
					result, err := svc.Forward(ctx, c, newOpenAIPartialUsageAccount(mode == "passthrough"), body)
					require.ErrorIs(t, err, context.Canceled)
					require.NotContains(t, err.Error(), "top-up failed")
					require.NotNil(t, result)
					require.True(t, result.ClientDisconnect)
					require.False(t, result.SucceededForScheduling())
					require.Empty(t, rec.Body.String(), "canceled output must not be delivered")
					require.True(t, stream.closed)
					require.Len(t, upstream.requests, 1)
					_, hasUpstreamError := c.Get(OpsUpstreamStatusCodeKey)
					require.False(t, hasUpstreamError)
					if hasUsage {
						require.Equal(t, 17, result.Usage.InputTokens)
						require.Equal(t, 4, result.Usage.OutputTokens)
						require.Equal(t, 9, result.Usage.CacheReadInputTokens)
					} else {
						require.Equal(t, OpenAIUsage{}, result.Usage)
					}

					actual := 0.0
					if hasUsage {
						actual = 0.012
					}
					// Exercise the same ownership handoff used by the usage worker.
					task, ok := TransferBalancePreauthorizationToUsageTask(guard, func(taskCtx context.Context) {
						owner, exists := BalancePreauthorizationGuardFromContext(taskCtx)
						require.True(t, exists)
						require.NoError(t, owner.Finalize(taskCtx, actual, "canceled-request"))
						require.NoError(t, owner.Finalize(taskCtx, actual, "canceled-request"))
					})
					require.True(t, ok)
					require.NoError(t, guard.Refund(context.Background()), "stale handler must not refund worker-owned funds")
					task(context.Background())
					if source == FundingSourceWallet {
						require.Equal(t, 1, fixture.wallet.topUpCalls)
						require.NoError(t, fixture.wallet.topUpContextErr)
						if hasUsage {
							require.Equal(t, 1, fixture.wallet.finalizeCalls)
							require.InDelta(t, actual, fixture.wallet.lastActual, 1e-12)
						} else {
							require.Equal(t, 1, fixture.wallet.refundCalls)
							require.Zero(t, fixture.wallet.finalizeCalls)
						}
					} else {
						require.Equal(t, 1, allowance.topUpCalls)
						require.Equal(t, 1, allowance.captureCalls)
						require.InDelta(t, actual, allowance.captured, 1e-12)
					}
				})
			}
		}
	}
}

func TestOpenAIStreamTopUpDoesNotMisclassifyBillingFailures(t *testing.T) {
	for _, source := range []string{FundingSourceWallet, FundingSourceSubscription} {
		for _, cause := range []error{context.Canceled, context.DeadlineExceeded, ErrBalanceWithholdingFailed, errors.New("database unavailable")} {
			t.Run(source+"/"+cause.Error(), func(t *testing.T) {
				guard, _, _ := newStreamTopUpGuard(t, source, nil, cause)
				c, _ := newOpenAIPartialUsageContext(t, nil)
				svc := &OpenAIGatewayService{}
				canceled, err := svc.reserveOpenAIStreamingOutput(context.Background(), c, nil, "test", guard, 1024)
				require.False(t, canceled)
				require.Error(t, err)
				require.ErrorIs(t, err, cause)
				require.Contains(t, err.Error(), "top-up failed")
			})
		}
	}
}

func TestOpenAIStreamTopUpDoesNotHideFailureWhenRequestAlsoCancels(t *testing.T) {
	for _, source := range []string{FundingSourceWallet, FundingSourceSubscription} {
		t.Run(source, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cause := errors.New("billing database unavailable")
			guard, _, _ := newStreamTopUpGuard(t, source, cancel, cause)
			svc := &OpenAIGatewayService{}
			canceled, err := svc.reserveOpenAIStreamingOutput(ctx, nil, nil, "test", guard, 1024)
			require.False(t, canceled, "a real billing failure must remain observable")
			require.ErrorIs(t, err, cause)
		})
	}
}

func TestOpenAIStreamTopUpSkipsAlreadyCanceledRequest(t *testing.T) {
	for _, source := range []string{FundingSourceWallet, FundingSourceSubscription} {
		t.Run(source, func(t *testing.T) {
			guard, fixture, allowance := newStreamTopUpGuard(t, source, nil, nil)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			svc := &OpenAIGatewayService{}
			canceled, err := svc.reserveOpenAIStreamingOutput(ctx, nil, nil, "test", guard, 1024)
			require.True(t, canceled)
			require.ErrorIs(t, err, context.Canceled)
			require.Zero(t, fixture.wallet.topUpCalls)
			if allowance != nil {
				require.Zero(t, allowance.topUpCalls)
			}
		})
	}
}
