//go:build unit

package service

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestValidateOrderInputRejectsUnknownOrderTypeBeforeBalanceGate(t *testing.T) {
	svc := &PaymentService{}
	for _, orderType := range []string{"x", "Balance", " balance", "subscription "} {
		for _, disabled := range []bool{false, true} {
			cfg := &PaymentConfig{BalanceDisabled: disabled}
			plan, err := svc.validateOrderInput(context.Background(), CreateOrderRequest{OrderType: orderType, Amount: 10}, cfg)
			require.Nil(t, plan)
			require.Error(t, err, "order type %q must be rejected", orderType)
			require.Equal(t, "INVALID_ORDER_TYPE", infraerrors.Reason(err), "order type %q", orderType)
		}
	}
}

func TestValidateOrderInputKeepsBalanceDisabledSwitch(t *testing.T) {
	svc := &PaymentService{}
	_, err := svc.validateOrderInput(context.Background(), CreateOrderRequest{OrderType: payment.OrderTypeBalance, Amount: 10}, &PaymentConfig{BalanceDisabled: true})
	require.Equal(t, "BALANCE_PAYMENT_DISABLED", infraerrors.Reason(err))

	plan, err := svc.validateOrderInput(context.Background(), CreateOrderRequest{OrderType: payment.OrderTypeBalance, Amount: 10}, &PaymentConfig{})
	require.NoError(t, err)
	require.Nil(t, plan)
}

func TestFulfillmentRejectsUnknownOrderType(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, OrderStatusPaid, time.Now())
	order, err := client.PaymentOrder.UpdateOneID(order.ID).
		SetOrderType("x").
		ClearPlanID().
		Save(ctx)
	require.NoError(t, err)

	userRepo := &mockUserRepo{getByIDUser: &User{ID: order.UserID}}
	userRepo.updateBalanceFn = func(context.Context, int64, float64) error {
		t.Fatal("unknown order type must not credit balance")
		return nil
	}
	svc := &PaymentService{entClient: client, userRepo: userRepo}

	err = svc.ExecuteBalanceFulfillment(ctx, order.ID)
	require.Equal(t, "INVALID_ORDER_TYPE", infraerrors.Reason(err))

	// 不履约，但标记 FAILED 并正常返回，使支付回调得到确认而不是无限重试。
	require.NoError(t, svc.executeFulfillment(ctx, order.ID))
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusFailed, reloaded.Status)
	require.NotNil(t, reloaded.FailedReason)
	require.Contains(t, *reloaded.FailedReason, "unsupported order type")

	// 回调重试经 alreadyProcessed 再次进入履约：幂等，不重复写审计。
	require.NoError(t, svc.executeFulfillment(ctx, order.ID))
	audits, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("FULFILLMENT_FAILED")).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, audits)

	err = svc.RetryFulfillment(ctx, order.ID)
	require.Equal(t, "INVALID_ORDER_TYPE", infraerrors.Reason(err))
}

func TestFulfillmentUnknownOrderTypeRetriesFailedStatusWrite(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, OrderStatusPaid, time.Now())
	order, err := client.PaymentOrder.UpdateOneID(order.ID).SetOrderType("x").Save(ctx)
	require.NoError(t, err)

	writeErr := errors.New("status write unavailable")
	failWrite := true
	client.PaymentOrder.Use(func(next dbent.Mutator) dbent.Mutator {
		return dbent.MutateFunc(func(ctx context.Context, m dbent.Mutation) (dbent.Value, error) {
			if failWrite && m.Op().Is(dbent.OpUpdate) {
				return nil, writeErr
			}
			return next.Mutate(ctx, m)
		})
	})
	svc := &PaymentService{entClient: client}
	require.ErrorIs(t, svc.executeFulfillment(ctx, order.ID), writeErr)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPaid, reloaded.Status)

	failWrite = false
	require.NoError(t, svc.executeFulfillment(ctx, order.ID))
	reloaded, err = client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusFailed, reloaded.Status)
}
