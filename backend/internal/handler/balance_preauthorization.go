package handler

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"go.uber.org/zap"
)

type balancePreauthorizer interface {
	Preauthorize(context.Context, service.BalancePreauthorizationRequest) (*service.BalancePreauthorizationGuard, error)
}

type balancePreauthorizationResumer interface {
	Resume(context.Context, service.BalancePreauthorizationResumeRequest) (*service.BalancePreauthorizationGuard, error)
}

type balancePreauthorizationPricingProvider interface {
	BalancePreauthorizationCostInput(context.Context, *service.APIKey, string, time.Time, string, service.BalancePreauthorizationRateKind) service.CostInput
}

type balancePreauthorizationAudioPricingProvider interface {
	BalancePreauthorizationAudioCost(context.Context, *service.APIKey, string, string, float64, time.Time) (float64, error)
}

type balancePreauthorizationRequirement interface {
	RequiresPreauthorization(context.Context, int8) bool
}

type balancePreauthorizationWebSearchPricingProvider interface {
	BalancePreauthorizationWebSearchCost(context.Context, *service.APIKey, time.Time) (float64, error)
}

type balancePreauthorizationSearchPricingProvider interface {
	BalancePreauthorizationSearchCost(context.Context, *service.APIKey, time.Time) (float64, error)
}

func preauthorizeTextGatewayRequest(
	ctx context.Context,
	preauthorizer balancePreauthorizer,
	pricing balancePreauthorizationPricingProvider,
	apiKey *service.APIKey,
	subscription *service.UserSubscription,
	body []byte,
	billingModel string,
	pricingAt time.Time,
	serviceTier string,
) (*service.BalancePreauthorizationGuard, error) {
	return preauthorizeTokenGatewayRequest(
		ctx, preauthorizer, pricing, apiKey, subscription, body,
		billingModel, pricingAt, serviceTier, false,
	)
}

func preauthorizeInputOnlyGatewayRequest(
	ctx context.Context,
	preauthorizer balancePreauthorizer,
	pricing balancePreauthorizationPricingProvider,
	apiKey *service.APIKey,
	subscription *service.UserSubscription,
	body []byte,
	billingModel string,
	pricingAt time.Time,
	serviceTier string,
) (*service.BalancePreauthorizationGuard, error) {
	return preauthorizeTokenGatewayRequest(
		ctx, preauthorizer, pricing, apiKey, subscription, body,
		billingModel, pricingAt, serviceTier, true,
	)
}

func preauthorizeTokenGatewayRequest(
	ctx context.Context,
	preauthorizer balancePreauthorizer,
	pricing balancePreauthorizationPricingProvider,
	apiKey *service.APIKey,
	subscription *service.UserSubscription,
	body []byte,
	billingModel string,
	pricingAt time.Time,
	serviceTier string,
	disableOutputReservation bool,
) (*service.BalancePreauthorizationGuard, error) {
	if preauthorizer == nil || pricing == nil || apiKey == nil {
		return nil, nil
	}
	billingType := service.BalancePreauthorizationBillingType(apiKey, subscription)
	if requirement, ok := preauthorizer.(balancePreauthorizationRequirement); ok &&
		!requirement.RequiresPreauthorization(ctx, billingType) {
		return nil, nil
	}
	userID := apiKey.UserID
	if userID <= 0 && apiKey.User != nil {
		userID = apiKey.User.ID
	}
	payloadHash := service.HashUsageRequestPayload(body)
	tokenEstimate := service.EstimateBalancePreauthorizationTokens(body)
	if disableOutputReservation {
		tokenEstimate.OutputTokens = 0
	}
	return preauthorizer.Preauthorize(ctx, service.BalancePreauthorizationRequest{
		RequestID:                 service.ResolveBalancePreauthorizationRequestID(ctx),
		APIKeyID:                  apiKey.ID,
		UserID:                    userID,
		SubscriptionID:            subscriptionPreauthorizationID(apiKey, subscription),
		AuthorizationFingerprint:  payloadHash,
		BillingType:               billingType,
		BillableInputBytes:        len(body),
		EstimatedInputTokens:      tokenEstimate.InputTokens,
		InitialOutputWindowTokens: tokenEstimate.OutputTokens,
		DisableOutputReservation:  disableOutputReservation,
		PerRequestEstimate:        service.PerRequestPreauthorizationEstimate{RequestCount: 1},
		CostInput: pricing.BalancePreauthorizationCostInput(
			ctx, apiKey, billingModel, pricingAt,
			service.BalancePreauthorizationServiceTier(ctx, apiKey, serviceTier),
			service.BalancePreauthorizationRateText,
		),
	})
}

// preauthorizePerRequestGatewayRequest reserves balance for count/size/duration
// -metered endpoints (images, video, standalone search) before an upstream
// account is selected. Unlike the text path it prices the request once from the
// explicit billing units in estimate, holding the exact request price; usage
// settlement later refunds any positive difference through the shared guard.
func preauthorizePerRequestGatewayRequest(
	ctx context.Context,
	preauthorizer balancePreauthorizer,
	pricing balancePreauthorizationPricingProvider,
	apiKey *service.APIKey,
	subscription *service.UserSubscription,
	body []byte,
	billingModel string,
	pricingAt time.Time,
	estimate service.PerRequestPreauthorizationEstimate,
	rateKind service.BalancePreauthorizationRateKind,
) (*service.BalancePreauthorizationGuard, error) {
	if preauthorizer == nil || pricing == nil || apiKey == nil {
		return nil, nil
	}
	billingType := service.BalancePreauthorizationBillingType(apiKey, subscription)
	if requirement, ok := preauthorizer.(balancePreauthorizationRequirement); ok &&
		!requirement.RequiresPreauthorization(ctx, billingType) {
		return nil, nil
	}
	userID := apiKey.UserID
	if userID <= 0 && apiKey.User != nil {
		userID = apiKey.User.ID
	}
	payloadHash := service.HashUsageRequestPayload(body)
	tokenEstimate := service.EstimateBalancePreauthorizationTokens(body)
	return preauthorizer.Preauthorize(ctx, service.BalancePreauthorizationRequest{
		RequestID:                 service.ResolveBalancePreauthorizationRequestID(ctx),
		APIKeyID:                  apiKey.ID,
		UserID:                    userID,
		SubscriptionID:            subscriptionPreauthorizationID(apiKey, subscription),
		AuthorizationFingerprint:  payloadHash,
		BillingType:               billingType,
		BillableInputBytes:        len(body),
		EstimatedInputTokens:      tokenEstimate.InputTokens,
		InitialOutputWindowTokens: tokenEstimate.OutputTokens,
		EstimateKind:              service.PreauthorizationEstimatePerRequest,
		PerRequestEstimate:        estimate,
		CostInput: pricing.BalancePreauthorizationCostInput(
			ctx, apiKey, billingModel, pricingAt, "", rateKind,
		),
	})
}

func preauthorizeWebSearchGatewayRequest(
	ctx context.Context,
	preauthorizer balancePreauthorizer,
	pricing balancePreauthorizationWebSearchPricingProvider,
	apiKey *service.APIKey,
	subscription *service.UserSubscription,
	body []byte,
	pricingAt time.Time,
	requestID string,
) (*service.BalancePreauthorizationGuard, error) {
	if preauthorizer == nil || pricing == nil || apiKey == nil {
		return nil, nil
	}
	billingType := service.BalancePreauthorizationBillingType(apiKey, subscription)
	if requirement, ok := preauthorizer.(balancePreauthorizationRequirement); ok &&
		!requirement.RequiresPreauthorization(ctx, billingType) {
		return nil, nil
	}
	fixedAmount, err := pricing.BalancePreauthorizationWebSearchCost(ctx, apiKey, pricingAt)
	if err != nil {
		return nil, err
	}
	return preauthorizeFixedGatewayRequest(ctx, preauthorizer, apiKey, subscription, body, requestID, fixedAmount)
}

func preauthorizeSearchGatewayRequest(
	ctx context.Context,
	preauthorizer balancePreauthorizer,
	pricing balancePreauthorizationSearchPricingProvider,
	apiKey *service.APIKey,
	subscription *service.UserSubscription,
	body []byte,
	pricingAt time.Time,
	requestID string,
) (*service.BalancePreauthorizationGuard, error) {
	if preauthorizer == nil || pricing == nil || apiKey == nil {
		return nil, nil
	}
	billingType := service.BalancePreauthorizationBillingType(apiKey, subscription)
	if requirement, ok := preauthorizer.(balancePreauthorizationRequirement); ok &&
		!requirement.RequiresPreauthorization(ctx, billingType) {
		return nil, nil
	}
	fixedAmount, err := pricing.BalancePreauthorizationSearchCost(ctx, apiKey, pricingAt)
	if err != nil {
		return nil, err
	}
	return preauthorizeFixedGatewayRequest(ctx, preauthorizer, apiKey, subscription, body, requestID, fixedAmount)
}

func preauthorizeFixedGatewayRequest(
	ctx context.Context,
	preauthorizer balancePreauthorizer,
	apiKey *service.APIKey,
	subscription *service.UserSubscription,
	body []byte,
	requestID string,
	fixedAmount float64,
) (*service.BalancePreauthorizationGuard, error) {
	if requestID == "" {
		requestID = service.ResolveBalancePreauthorizationRequestID(ctx)
	}
	billingType := service.BalancePreauthorizationBillingType(apiKey, subscription)
	userID := apiKey.UserID
	if userID <= 0 && apiKey.User != nil {
		userID = apiKey.User.ID
	}
	return preauthorizer.Preauthorize(ctx, service.BalancePreauthorizationRequest{
		RequestID:                requestID,
		APIKeyID:                 apiKey.ID,
		UserID:                   userID,
		SubscriptionID:           subscriptionPreauthorizationID(apiKey, subscription),
		AuthorizationFingerprint: service.HashUsageRequestPayload(body),
		BillingType:              billingType,
		EstimateKind:             service.PreauthorizationEstimateFixed,
		FixedAmount:              fixedAmount,
	})
}

func preauthorizeGrokVideoGatewayRequest(
	ctx context.Context,
	preauthorizer balancePreauthorizer,
	pricing balancePreauthorizationPricingProvider,
	apiKey *service.APIKey,
	subscription *service.UserSubscription,
	body []byte,
	billingModel string,
	pricingAt time.Time,
	durationSeconds int,
	resolution string,
) (*service.BalancePreauthorizationGuard, error) {
	if preauthorizer == nil || pricing == nil || apiKey == nil {
		return nil, nil
	}
	billingType := service.BalancePreauthorizationBillingType(apiKey, subscription)
	if requirement, ok := preauthorizer.(balancePreauthorizationRequirement); ok && !requirement.RequiresPreauthorization(ctx, billingType) {
		return nil, nil
	}
	userID := apiKey.UserID
	if userID <= 0 && apiKey.User != nil {
		userID = apiKey.User.ID
	}
	return preauthorizer.Preauthorize(ctx, service.BalancePreauthorizationRequest{
		RequestID:                service.NewGrokVideoHoldRequestID(),
		APIKeyID:                 apiKey.ID,
		UserID:                   userID,
		SubscriptionID:           subscriptionPreauthorizationID(apiKey, subscription),
		AuthorizationFingerprint: service.HashUsageRequestPayload(body),
		BillingType:              billingType,
		EstimateKind:             service.PreauthorizationEstimatePerRequest,
		PerRequestEstimate: service.PerRequestPreauthorizationEstimate{
			UsageUnits: float64(durationSeconds),
			SizeTier:   service.NormalizeVideoBillingResolutionOrDefault(resolution),
		},
		CostInput: pricing.BalancePreauthorizationCostInput(
			ctx, apiKey, billingModel, pricingAt, "", service.BalancePreauthorizationRateVideo,
		),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	})
}

func preauthorizeGrokAudioGatewayRequest(
	ctx context.Context,
	preauthorizer balancePreauthorizer,
	pricing balancePreauthorizationAudioPricingProvider,
	apiKey *service.APIKey,
	subscription *service.UserSubscription,
	body []byte,
	endpoint string,
	pricingAt time.Time,
) (*service.BalancePreauthorizationGuard, error) {
	if preauthorizer == nil || pricing == nil || apiKey == nil {
		return nil, nil
	}
	billingType := service.BalancePreauthorizationBillingType(apiKey, subscription)
	if requirement, ok := preauthorizer.(balancePreauthorizationRequirement); ok && !requirement.RequiresPreauthorization(ctx, billingType) {
		return nil, nil
	}
	usage := service.EstimateGrokVoiceAudioUsage(endpoint, body, "", nil, 0)
	if usage == nil {
		return nil, nil
	}
	fixedAmount, err := pricing.BalancePreauthorizationAudioCost(ctx, apiKey, endpoint, usage.Mode, usage.DurationOrUnits, pricingAt)
	if err != nil {
		return nil, err
	}
	userID := apiKey.UserID
	if userID <= 0 && apiKey.User != nil {
		userID = apiKey.User.ID
	}
	return preauthorizer.Preauthorize(ctx, service.BalancePreauthorizationRequest{
		RequestID:                service.StableGrokAudioBillingRequestID(""),
		APIKeyID:                 apiKey.ID,
		UserID:                   userID,
		SubscriptionID:           subscriptionPreauthorizationID(apiKey, subscription),
		AuthorizationFingerprint: service.HashUsageRequestPayload(body),
		BillingType:              billingType,
		EstimateKind:             service.PreauthorizationEstimateFixed,
		FixedAmount:              fixedAmount,
	})
}

func preauthorizeGrokRealtimeGatewayRequest(
	ctx context.Context,
	preauthorizer balancePreauthorizer,
	pricing balancePreauthorizationAudioPricingProvider,
	apiKey *service.APIKey,
	subscription *service.UserSubscription,
	model string,
	sessionID string,
	pricingAt time.Time,
) (*service.BalancePreauthorizationGuard, error) {
	if preauthorizer == nil || pricing == nil || apiKey == nil {
		return nil, nil
	}
	billingType := service.BalancePreauthorizationBillingType(apiKey, subscription)
	if requirement, ok := preauthorizer.(balancePreauthorizationRequirement); ok && !requirement.RequiresPreauthorization(ctx, billingType) {
		return nil, nil
	}
	fixedAmount, err := pricing.BalancePreauthorizationAudioCost(ctx, apiKey, model, "realtime", 1, pricingAt)
	if err != nil {
		return nil, err
	}
	userID := apiKey.UserID
	if userID <= 0 && apiKey.User != nil {
		userID = apiKey.User.ID
	}
	fingerprint := service.HashUsageRequestPayload([]byte("grok_realtime:" + strings.TrimSpace(model) + ":" + strings.TrimSpace(sessionID)))
	return preauthorizer.Preauthorize(ctx, service.BalancePreauthorizationRequest{
		RequestID:                service.StableGrokRealtimeBillingRequestID(""),
		APIKeyID:                 apiKey.ID,
		UserID:                   userID,
		SubscriptionID:           subscriptionPreauthorizationID(apiKey, subscription),
		AuthorizationFingerprint: fingerprint,
		BillingType:              billingType,
		EstimateKind:             service.PreauthorizationEstimateFixed,
		FixedAmount:              fixedAmount,
	})
}

func subscriptionPreauthorizationID(apiKey *service.APIKey, subscription *service.UserSubscription) int64 {
	if apiKey != nil && apiKey.UsesSubscription() {
		if apiKey.SubscriptionID == nil {
			return 0
		}
		return *apiKey.SubscriptionID
	}
	return 0
}

func deferBalancePreauthorizationRefund(reqLog *zap.Logger, guard *service.BalancePreauthorizationGuard) {
	if guard == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := guard.Refund(ctx); err != nil &&
		err != service.ErrBalancePreauthorizationAlreadyFinalized {
		if reqLog != nil {
			reqLog.Error("billing.balance_preauthorization_refund_failed", zap.Error(err))
		}
	}
}

func transferBalancePreauthorizationUsageTask(
	parent context.Context,
	task service.UsageRecordTask,
) (service.UsageRecordTask, bool) {
	guard, ok := service.BalancePreauthorizationGuardFromContext(parent)
	if !ok {
		return task, task != nil
	}
	return service.TransferBalancePreauthorizationToUsageTask(guard, task)
}

func shouldRecordStandaloneCyberUsage(forwardErr error, hasUsageResult bool) bool {
	return forwardErr != nil && !hasUsageResult
}

var errDuplicateBalancePreauthorizationUsageTask = errors.New("duplicate balance preauthorization usage task")
