//go:build unit

package service

import (
	"context"
	"errors"
	"math"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

func TestTrustedRedeemNeverMutatesPublicFailureWindow(t *testing.T) {
	for _, entry := range []string{"payment", "admin"} {
		for _, status := range []string{StatusUsed, StatusExpired, "missing"} {
			t.Run(entry+"/"+status, func(t *testing.T) {
				cache := &paymentFulfillmentRedeemCacheStub{count: redeemMaxFailedAttempts}
				repo := &redeemRejectRepo{code: RedeemCode{Code: "CODE", Type: RedeemTypeBalance, Status: status}}
				svc := &RedeemService{redeemRepo: repo, cache: cache}
				redeem := svc.redeemForPaymentFulfillment
				if entry == "admin" {
					redeem = svc.RedeemForAdminFulfillment
				}
				code := "CODE"
				if status == "missing" {
					code = "MISSING"
				}
				_, err := redeem(context.Background(), 42, code)
				require.Error(t, err)
				require.NotErrorIs(t, err, ErrRedeemRateLimited)
				require.Zero(t, cache.getCalls)
				require.Zero(t, cache.incrementCalls)
				require.Equal(t, 1, cache.acquireCalls)
				require.Equal(t, 1, cache.releaseCalls)
			})
		}
	}
}

type paymentLookupFailureRepo struct {
	RedeemCodeRepository
	lookupErr error
}

func (r *paymentLookupFailureRepo) GetByCode(context.Context, string) (*RedeemCode, error) {
	return nil, r.lookupErr
}

func TestPaymentLookupFailureDoesNotCreateRedeemCode(t *testing.T) {
	dbErr := errors.New("database unavailable")
	svc := &PaymentService{redeemService: &RedeemService{redeemRepo: &paymentLookupFailureRepo{lookupErr: dbErr}}}
	err := svc.doBalance(context.Background(), &dbent.PaymentOrder{RechargeCode: "PAY"}, nil)
	require.ErrorIs(t, err, dbErr)
}

func TestPaymentRedeemAmountValidationPrecision(t *testing.T) {
	order := &dbent.PaymentOrder{UserID: 42, RechargeCode: "PAY", Amount: 20.00000001}
	code := &RedeemCode{Code: "PAY", Type: RedeemTypeBalance, Status: StatusUnused, Value: order.Amount}
	require.NoError(t, validatePaymentRedeemCode(order, code))
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), 20.0000001} {
		code.Value = value
		require.Error(t, validatePaymentRedeemCode(order, code))
	}
	code.Value = order.Amount
	code.UsedBy = &order.UserID
	require.Error(t, validatePaymentRedeemCode(order, code), "unused code must not have a redeemer")
}
