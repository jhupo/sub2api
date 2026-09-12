package handler

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

type openAIWSTurnRepricer interface {
	RepriceBeforeSend(context.Context, *service.BalancePreauthorizationGuard, service.BalancePreauthorizationRequest) error
}

func (h *OpenAIGatewayHandler) validateOpenAIWSFundingFrame(ctx context.Context, key *service.APIKey, payload []byte) error {
	if requirement, ok := h.balancePreauthorizer.(balancePreauthorizationRequirement); ok && !requirement.RequiresPreauthorization(ctx, service.BalancePreauthorizationBillingType(key, nil)) {
		return nil
	}
	switch gjson.GetBytes(payload, "type").String() {
	case "response.create", "response.cancel":
		return nil
	default:
		return service.NewOpenAIWSRequestScopedClientCloseError(coderws.StatusPolicyViolation, "preauthorized Responses connections require input in response.create", nil)
	}
}

type openAIWSTurnFundingSnapshot struct {
	turn                 int
	id                   string
	guard                *service.BalancePreauthorizationGuard
	key                  *service.APIKey
	subscription         *service.UserSubscription
	fingerprint          string
	pricingAt            time.Time
	outputObserved       bool
	outputBytes          int
	outputReservation    service.OpenAIWSOutputReservation
	finished             bool
	skipPreauthorization bool
}

// One owner per business turn, not per socket or transport attempt. The relay
// is sequential; the mutex also orders opposite-direction passthrough hooks.
type openAIWSTurnFunding struct {
	mu            sync.Mutex
	active        *openAIWSTurnFundingSnapshot
	contextBounds map[string]int
}

func (f *openAIWSTurnFunding) prepare(ctx context.Context, h *OpenAIGatewayHandler, turn int, key *service.APIKey, subscription *service.UserSubscription, payload []byte, model string, pricingAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.active == nil || f.active.turn != turn {
		if f.active != nil && !f.active.finished {
			return errors.New("previous websocket funding turn is still active")
		}
		f.active = &openAIWSTurnFundingSnapshot{turn: turn, id: "ws-turn:" + uuid.NewString(), key: key, subscription: subscription, pricingAt: pricingAt, fingerprint: service.HashUsageRequestPayload(payload)}
		f.active.skipPreauthorization = h.openAIWSSimpleMode()
		if requirement, ok := h.balancePreauthorizer.(balancePreauthorizationRequirement); ok {
			f.active.skipPreauthorization = !requirement.RequiresPreauthorization(ctx, service.BalancePreauthorizationBillingType(key, subscription))
		}
	}
	a := f.active
	if a.finished || a.outputObserved {
		return errors.New("websocket funding turn cannot be replayed")
	}
	ctx = service.ContextWithOpenAIWSTurnBillingID(ctx, a.id)
	if a.skipPreauthorization {
		return nil
	}
	if h.balancePreauthorizer == nil {
		return errors.New("websocket preauthorization service unavailable")
	}
	if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(payload, "type").String()), "response.cancel") {
		return nil
	}
	estimate, estimateErr := service.EstimateBalancePreauthorizationTokensStrict(payload, true)
	if estimateErr != nil {
		return service.NewOpenAIWSRequestScopedClientCloseError(
			coderws.StatusPolicyViolation,
			"unable to estimate request tokens before forwarding",
			service.ErrBalancePreauthorizationEstimateUnavailable.WithCause(estimateErr),
		)
	}
	if previous := gjson.GetBytes(payload, "previous_response_id").String(); previous != "" {
		bound, found := f.contextBounds[previous]
		if !found {
			var err error
			bound, found, err = h.gatewayService.OpenAIContinuationInputBound(ctx, key, previous)
			if err != nil {
				return err
			}
			if !found {
				return service.NewOpenAIWSRequestScopedClientCloseError(coderws.StatusPolicyViolation, "previous response billing context unavailable; reconnect with full input and no previous_response_id", nil)
			}
		}
		if bound > math.MaxInt-estimate.InputTokens {
			return service.ErrInvalidBillingPreauthorizationEstimate
		}
		estimate.InputTokens += bound
	}
	request := service.BalancePreauthorizationRequest{
		RequestID: a.id, APIKeyID: a.key.ID, UserID: a.key.UserID,
		SubscriptionID:           subscriptionPreauthorizationID(a.key, a.subscription),
		BillingType:              service.BalancePreauthorizationBillingType(a.key, a.subscription),
		AuthorizationFingerprint: a.fingerprint, BillableInputBytes: len(payload),
		EstimatedInputTokens: estimate.InputTokens, EstimatedImageInputTokens: estimate.ImageInputTokens,
		EstimatedAudioInputTokens: estimate.AudioInputTokens, EstimatedImageOutputTokens: estimate.ImageOutputTokens,
		InitialOutputWindowTokens: estimate.OutputTokens,
		PerRequestEstimate:        service.PerRequestPreauthorizationEstimate{RequestCount: 1},
		CostInput: h.gatewayService.BalancePreauthorizationCostInput(ctx, a.key, model, a.pricingAt,
			service.BalancePreauthorizationServiceTier(ctx, a.key, gjson.GetBytes(payload, "service_tier").String()), service.BalancePreauthorizationRateText),
	}
	if a.guard != nil {
		repricer, ok := h.balancePreauthorizer.(openAIWSTurnRepricer)
		if !ok {
			return errors.New("websocket preauthorization repricing unavailable")
		}
		return repricer.RepriceBeforeSend(ctx, a.guard, request)
	}
	guard, err := h.balancePreauthorizer.Preauthorize(ctx, request)
	if err != nil {
		return err
	}
	a.guard = guard
	return nil
}

func (f *openAIWSTurnFunding) observe(ctx context.Context, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.active == nil || f.active.finished {
		var unowned service.OpenAIWSOutputReservation
		if unowned.UpperBound(payload) > 0 {
			return errors.New("websocket output has no active funding turn")
		}
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Terminal usage includes invisible reasoning tokens. Reserve that measured
	// floor before forwarding the terminal frame; it is not a second delta.
	n := f.active.outputReservation.UpperBound(payload) - f.active.outputBytes
	if n <= 0 {
		return nil
	}
	if f.active.guard != nil {
		if err := f.active.guard.ObserveStreamingOutput(ctx, n); err != nil {
			return err
		}
	}
	f.active.outputObserved = true
	f.active.outputBytes += n
	return nil
}

func (f *openAIWSTurnFunding) finish(turn int, result *service.OpenAIForwardResult) *openAIWSTurnFundingSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.active
	if a == nil || a.turn != turn || a.finished || result == nil {
		return nil
	}
	a.finished = true
	if result.RequestID != "" {
		if f.contextBounds == nil {
			f.contextBounds = make(map[string]int)
		}
		// Bound connection memory; older references remain available in Redis.
		if len(f.contextBounds) >= 256 {
			clear(f.contextBounds)
		}
		f.contextBounds[result.RequestID] = result.Usage.InputTokens + result.Usage.OutputTokens
	}
	snapshot := *a
	snapshot.outputReservation = service.OpenAIWSOutputReservation{}
	return &snapshot
}

func (a *openAIWSTurnFundingSnapshot) context(parent context.Context) context.Context {
	return service.ContextWithBalancePreauthorizationGuard(service.ContextWithOpenAIWSTurnBillingID(parent, a.id), a.guard)
}

func (f *openAIWSTurnFunding) refund() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.active == nil || f.active.guard == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return f.active.guard.Refund(ctx)
}
