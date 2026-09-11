package handler

import (
	"context"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
)

type openAIWSTurnKeyReader interface {
	GetByID(context.Context, int64) (*service.APIKey, error)
}

// A connection may outlive key revocation, quota exhaustion or a group change.
// Routing and funding changes require a reconnect, never an in-place rebinding.
func refreshOpenAIWSTurnKey(ctx context.Context, reader openAIWSTurnKeyReader, initial *service.APIKey, clientIP string, simple bool) (*service.APIKey, error) {
	if reader == nil || initial == nil {
		return nil, service.NewOpenAIWSRequestScopedClientCloseError(coderws.StatusInternalError, "API key validation unavailable", nil)
	}
	key, err := reader.GetByID(ctx, initial.ID)
	if err != nil {
		return nil, service.NewOpenAIWSRequestScopedClientCloseError(coderws.StatusPolicyViolation, "API key validation failed; please reconnect", err)
	}
	deny := func(message string) (*service.APIKey, error) {
		return nil, service.NewOpenAIWSRequestScopedClientCloseError(coderws.StatusPolicyViolation, message, nil)
	}
	if key == nil || key.User == nil || key.ID != initial.ID || key.UserID != initial.UserID || key.User.ID != key.UserID || !key.User.IsActive() || !key.IsActive() {
		return deny("API key or user is no longer active")
	}
	if !sameOpenAIWSOptionalID(key.GroupID, initial.GroupID) || key.FundingSource != initial.FundingSource || !sameOpenAIWSOptionalID(key.SubscriptionID, initial.SubscriptionID) {
		return deny("API key routing or payment changed; please reconnect")
	}
	if key.GroupID != nil && (key.Group == nil || key.Group.ID != *key.GroupID || !key.Group.IsActive() || !key.User.CanBindGroup(key.Group.ID, key.Group.IsExclusive)) {
		return deny("API key group is no longer available")
	}
	if key.Group != nil && initial.Group != nil && key.Group.Platform != initial.Group.Platform {
		return deny("API key routing platform changed; please reconnect")
	}
	if len(key.IPWhitelist) > 0 || len(key.IPBlacklist) > 0 {
		if allowed, _ := ip.CheckIPRestrictionWithCompiledRules(clientIP, key.CompiledIPWhitelist, key.CompiledIPBlacklist); !allowed {
			return deny("IP access denied")
		}
	}
	if !simple {
		if key.IsExpired() || key.IsQuotaExhausted() {
			return deny("API key expired or quota exhausted")
		}
		// The previous turn has committed before this read. Do not let delayed
		// cache updates reopen already-exhausted monetary windows.
		if (key.RateLimit5h > 0 && key.EffectiveUsage5h() >= key.RateLimit5h) ||
			(key.RateLimit1d > 0 && key.EffectiveUsage1d() >= key.RateLimit1d) ||
			(key.RateLimit7d > 0 && key.EffectiveUsage7d() >= key.RateLimit7d) {
			return deny("API key rate limit exceeded")
		}
	}
	return key, nil
}

func sameOpenAIWSOptionalID(a, b *int64) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

type openAIWSUsageCompletion struct {
	done chan struct{}
	err  error
}

// Serialize only this connection's admission behind its previous settlement.
// No account/user concurrency slot is held while waiting for the usage worker.
type openAIWSUsageBarrier struct {
	mu      sync.Mutex
	pending *openAIWSUsageCompletion
}

func (b *openAIWSUsageBarrier) start() func(error) {
	completion := &openAIWSUsageCompletion{done: make(chan struct{})}
	b.mu.Lock()
	b.pending = completion
	b.mu.Unlock()
	var once sync.Once
	return func(err error) {
		once.Do(func() {
			completion.err = err
			close(completion.done)
		})
	}
}

func (b *openAIWSUsageBarrier) wait(ctx context.Context) error {
	b.mu.Lock()
	pending := b.pending
	b.mu.Unlock()
	if pending == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-pending.done:
		return pending.err
	}
}

func (h *OpenAIGatewayHandler) checkOpenAIWSTurnBilling(ctx context.Context, key *service.APIKey) error {
	if h.billingCacheService == nil {
		return service.NewOpenAIWSRequestScopedClientCloseError(coderws.StatusInternalError, "websocket billing service unavailable", nil)
	}
	if key.UsesSubscription() {
		subscription, err := h.apiKeyService.GetGatewaySubscription(ctx, key)
		if err != nil {
			return service.NewOpenAIWSRequestScopedClientCloseError(coderws.StatusPolicyViolation, "subscription validation failed", err)
		}
		key.Subscription = subscription
	}
	if err := h.billingCacheService.CheckBillingEligibility(ctx, key.User, key, key.Group, key.Subscription, service.QuotaPlatform(ctx, key)); err != nil {
		return service.NewOpenAIWSRequestScopedClientCloseError(coderws.StatusPolicyViolation, "billing check failed", err)
	}
	return nil
}

func (h *OpenAIGatewayHandler) openAIWSSimpleMode() bool {
	return h.cfg != nil && h.cfg.RunMode == config.RunModeSimple
}
