package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// GrokRealtime exposes xAI's native Voice Realtime WebSocket.
// Only Grok-platform API keys may use this endpoint.
func (h *OpenAIGatewayHandler) GrokRealtime(c *gin.Context) {
	if c == nil || c.Request == nil || !isOpenAIWSUpgradeRequest(c.Request) {
		h.errorResponse(c, http.StatusUpgradeRequired, "invalid_request_error", "WebSocket upgrade required (Upgrade: websocket)")
		return
	}
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey.Group == nil || apiKey.Group.Platform != service.PlatformGrok {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Realtime API is not supported for this platform")
		return
	}
	if !h.ensureResponsesDependencies(c, nil) {
		return
	}
	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey)); err != nil {
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.errorResponse(c, status, code, message)
		return
	}

	reqLog := requestLogger(c, "handler.openai_gateway.grok_realtime")
	model := c.Query("model")
	if strings.TrimSpace(model) == "" {
		model = "grok-voice-latest"
	}
	pricingCtx, pricingAt := h.gatewayService.WithOpenAIRequestPricingContext(c.Request.Context(), apiKey.GroupID)
	c.Request = c.Request.WithContext(pricingCtx)
	balanceGuard, err := preauthorizeGrokRealtimeGatewayRequest(
		c.Request.Context(), h.balancePreauthorizer, h.gatewayService, apiKey, subscription,
		model, service.ExtractClientSessionID(c), pricingAt,
	)
	if err != nil {
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.errorResponse(c, status, code, message)
		return
	}
	if balanceGuard != nil {
		defer deferBalancePreauthorizationRefund(reqLog, balanceGuard)
		c.Request = c.Request.WithContext(service.ContextWithBalancePreauthorizationGuard(c.Request.Context(), balanceGuard))
	}
	// Keep the HTTP response uncommitted while selecting and probing an account.
	// Realtime is not an HTTP streaming response; using reqStream=true here would
	// let the wait queue flush an SSE ping before the WebSocket handshake succeeds.
	failed := map[int64]struct{}{}
	var selection *service.AccountSelectionResult
	var release func()
	var token string
	var upstream *service.GrokRealtimeUpstream
	var candidateSeen bool
	for attempts := 0; attempts < 4; attempts++ {
		// Realtime's voice model is not a text-model capability. Passing a
		// concrete text model here would reject accounts mapped only to an
		// older/default text model before the upstream handshake can decide.
		// An empty requested model keeps account selection capability-based;
		// the actual voice model remains in the upstream WS query below.
		candidate, _, selectErr := h.gatewayService.SelectAccountWithSchedulerForCapability(
			c.Request.Context(), apiKey.GroupID, "", "", "", failed,
			service.OpenAIUpstreamTransportHTTPSSE,
			service.OpenAIEndpointCapabilityChatCompletions,
			false, false, false, service.PlatformGrok,
		)
		if selectErr != nil || candidate == nil || candidate.Account == nil {
			break
		}
		candidateSeen = true
		account := candidate.Account
		var streamStarted bool
		var slotStatus openAISlotAcquireResult
		release, slotStatus = h.acquireResponsesAccountSlot(c, apiKey.GroupID, "", candidate, false, &streamStarted, reqLog)
		if slotStatus != openAISlotAcquireOK {
			if slotStatus == openAISlotAcquireFailed {
				return
			}
			failed[account.ID] = struct{}{}
			continue
		}
		var credErr error
		token, _, credErr = h.gatewayService.GetRequestCredential(c.Request.Context(), c, account)
		if credErr != nil {
			release()
			release = nil
			failed[account.ID] = struct{}{}
			continue
		}
		probeCtx, cancelProbe := context.WithTimeout(c.Request.Context(), service.DefaultGrokRealtimeDialTimeout)
		candidateUpstream, openErr := h.gatewayService.OpenGrokRealtime(probeCtx, account, token, model)
		cancelProbe()
		if openErr != nil {
			reqLog.Warn("grok_realtime.pre_accept_failed", zap.Int64("account_id", account.ID), zap.Error(openErr))
			statusCode := http.StatusBadGateway
			var dialErr *service.GrokRealtimeDialError
			if errors.As(openErr, &dialErr) && dialErr.StatusCode > 0 {
				statusCode = dialErr.StatusCode
			}
			h.gatewayService.HandleGrokRealtimeUpstreamError(c.Request.Context(), account, statusCode, []byte(openErr.Error()))
			release()
			release = nil
			failed[account.ID] = struct{}{}
			continue
		}
		selection, upstream = candidate, candidateUpstream
		break
	}
	if selection == nil || selection.Account == nil || release == nil || upstream == nil {
		if !candidateSeen {
			h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "No available Grok accounts")
		} else {
			h.errorResponse(c, http.StatusBadGateway, "upstream_error", "Grok realtime upstream unavailable")
		}
		return
	}
	defer release()
	defer func() { _ = upstream.Close() }()

	conn, err := coderws.Accept(c.Writer, c.Request, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()

	started := time.Now()
	var reserve func(context.Context, time.Duration) error
	if balanceGuard != nil {
		reserve = func(ctx context.Context, elapsed time.Duration) error {
			cost, err := h.gatewayService.BalancePreauthorizationAudioCost(
				ctx, apiKey, model, "realtime", (elapsed + service.GrokRealtimeReservationForwardWindow).Minutes(), pricingAt,
			)
			if err != nil {
				return err
			}
			return balanceGuard.TopUpTo(ctx, cost)
		}
	}
	audioObserved, proxyErr := h.gatewayService.ProxyGrokRealtimeConn(c.Request.Context(), c, conn, upstream, reserve)
	elapsed := time.Since(started)
	reservationFailed := errors.Is(proxyErr, service.ErrGrokRealtimeReservationFailed)
	if proxyErr != nil {
		reqLog.Info("grok_realtime.proxy_failed", zap.Error(proxyErr))
		if reservationFailed {
			_ = conn.Close(coderws.StatusInternalError, "billing reservation failed")
		} else if !isExpectedGrokRealtimeClose(proxyErr) {
			_ = conn.Close(coderws.StatusInternalError, "upstream realtime websocket failed")
		}
	}
	if result := grokRealtimeBillingResult(model, elapsed, audioObserved); result != nil {
		h.recordGrokVoiceUsage(c, apiKey, selection.Account, subscription, "realtime", nil, result, pricingAt)
	}
}

func grokRealtimeBillingResult(model string, elapsed time.Duration, audioObserved bool) *service.OpenAIForwardResult {
	if !audioObserved || elapsed <= 0 {
		return nil
	}
	return &service.OpenAIForwardResult{
		RequestID:  service.StableGrokRealtimeBillingRequestID(""),
		Model:      model,
		Duration:   elapsed,
		AudioUsage: &service.AudioUsage{Mode: "realtime", DurationOrUnits: elapsed.Minutes()},
	}
}

func isExpectedGrokRealtimeClose(err error) bool {
	if err == nil {
		return true
	}
	switch coderws.CloseStatus(err) {
	case coderws.StatusNormalClosure, coderws.StatusGoingAway,
		coderws.StatusNoStatusRcvd, coderws.StatusAbnormalClosure:
		return true
	default:
		return false
	}
}

// GrokVoice handles xAI Voice HTTP endpoints. endpoint is "tts", "stt", or "custom-voices".
func (h *OpenAIGatewayHandler) GrokVoice(c *gin.Context, endpoint string) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey.Group == nil || apiKey.Group.Platform != service.PlatformGrok {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Voice API is not supported for this platform")
		return
	}
	if !h.ensureResponsesDependencies(c, nil) {
		return
	}
	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey)); err != nil {
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.errorResponse(c, status, code, message)
		return
	}

	body, err := readGrokVoiceGatewayBody(c)
	if err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if endpoint == "tts" {
		subject, _ := middleware2.GetAuthSubjectFromContext(c)
		reqLog := requestLogger(c, "handler.openai_gateway.grok_voice", zap.String("endpoint", endpoint))
		// TTS bodies use {"input":"..."} (and variants). Normalize to chat messages so
		// content moderation extractors see the spoken text.
		auditBody := body
		if input := extractGrokTTSInputText(body); input != "" {
			if b, err := json.Marshal(map[string]any{
				"messages": []map[string]any{{"role": "user", "content": input}},
			}); err == nil {
				auditBody = b
			}
		}
		if decision := h.checkSecurityAudit(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIChat, "grok-4.5", auditBody); decision != nil && !decision.AllowNextStage {
			h.openAISecurityAuditError(c, decision)
			return
		}
	}
	contentType := c.GetHeader("Content-Type")
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/json"
	}
	pricingCtx, pricingAt := h.gatewayService.WithOpenAIRequestPricingContext(c.Request.Context(), apiKey.GroupID)
	c.Request = c.Request.WithContext(pricingCtx)
	balanceGuard, err := preauthorizeGrokAudioGatewayRequest(
		c.Request.Context(), h.balancePreauthorizer, h.gatewayService, apiKey, subscription, body, endpoint, pricingAt,
	)
	if err != nil {
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.errorResponse(c, status, code, message)
		return
	}
	if balanceGuard != nil {
		defer deferBalancePreauthorizationRefund(requestLogger(c, "handler.openai_gateway.grok_voice"), balanceGuard)
		c.Request = c.Request.WithContext(service.ContextWithBalancePreauthorizationGuard(c.Request.Context(), balanceGuard))
	}

	failed := map[int64]struct{}{}
	var last *service.UpstreamFailoverError
	reqLog := requestLogger(c, "handler.openai_gateway.grok_voice", zap.String("endpoint", endpoint))
	selectionModel := "grok-4.5"

	for attempts := 0; attempts < 4; attempts++ {
		selection, _, selectErr := h.gatewayService.SelectAccountWithSchedulerForCapability(
			c.Request.Context(),
			apiKey.GroupID,
			"",
			"",
			selectionModel,
			failed,
			service.OpenAIUpstreamTransportHTTPSSE,
			service.OpenAIEndpointCapabilityChatCompletions,
			false,
			false,
			false,
			service.PlatformGrok,
		)
		if selectErr != nil || selection == nil || selection.Account == nil {
			if last != nil {
				h.handleFailoverExhausted(c, last, false)
			} else {
				h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "No available Grok accounts")
			}
			return
		}
		account := selection.Account
		var started bool
		release, status := h.acquireResponsesAccountSlot(c, apiKey.GroupID, "", selection, false, &started, reqLog)
		if status == openAISlotAcquireProfitVetoed {
			failed[account.ID] = struct{}{}
			continue
		}
		if status != openAISlotAcquireOK {
			// Failed already wrote error response (or transient reject).
			if status == openAISlotAcquireFailed && len(failed) == 0 {
				// Slot path wrote the response; stop.
				return
			}
			failed[account.ID] = struct{}{}
			continue
		}
		result, forwardErr := func() (*service.OpenAIForwardResult, error) {
			defer release()
			return h.gatewayService.ForwardGrokVoice(c.Request.Context(), c, account, endpoint, body, contentType)
		}()
		if forwardErr == nil {
			h.finalizeGrokVoiceForwardSuccess(c, reqLog, apiKey, account, subscription, endpoint, body, result, pricingAt)
			return
		}
		var failoverErr *service.UpstreamFailoverError
		if errors.As(forwardErr, &failoverErr) && failoverErr.ShouldRetryNextAccount() {
			failed[account.ID] = struct{}{}
			last = failoverErr
			continue
		}
		// Non-failover errors: handleGrokMediaErrorResponse / transport already wrote response.
		return
	}
	if last != nil {
		h.handleFailoverExhausted(c, last, false)
	}
}

// finalizeGrokVoiceForwardSuccess reconciles guarded Voice HTTP billing before
// exposing an upstream success response. The usage task takes ownership of a
// reconciled guard before the buffered response is committed.
func (h *OpenAIGatewayHandler) finalizeGrokVoiceForwardSuccess(
	c *gin.Context,
	reqLog *zap.Logger,
	apiKey *service.APIKey,
	account *service.Account,
	subscription *service.UserSubscription,
	endpoint string,
	body []byte,
	result *service.OpenAIForwardResult,
	pricingAt time.Time,
) {
	guard, guarded := service.BalancePreauthorizationGuardFromContext(c.Request.Context())
	if guarded && guard.IsCurrentOwner() {
		if err := h.reconcileGrokVoiceBalancePreauthorization(c, apiKey, endpoint, result, pricingAt, guard); err != nil {
			if reqLog != nil {
				reqLog.Error("grok_voice.balance_preauthorization_reconciliation_failed", zap.Error(err))
			}
			status, code, message, retryAfter := billingErrorDetails(err)
			if retryAfter > 0 {
				c.Header("Retry-After", strconv.Itoa(retryAfter))
			}
			h.errorResponse(c, status, code, message)
			return
		}
		h.recordGrokVoiceUsage(c, apiKey, account, subscription, endpoint, body, result, pricingAt)
		if err := h.gatewayService.CommitGrokVoiceResponse(c, result); err != nil {
			if reqLog != nil {
				reqLog.Error("grok_voice.commit_response_failed", zap.Error(err))
			}
			if !c.Writer.Written() && !service.IsResponseCommitted(c) {
				h.errorResponse(c, http.StatusBadGateway, "upstream_error", "Voice response failed")
			}
		}
		return
	}
	if err := h.gatewayService.CommitGrokVoiceResponse(c, result); err != nil {
		if reqLog != nil {
			reqLog.Error("grok_voice.commit_response_failed", zap.Error(err))
		}
		if !c.Writer.Written() && !service.IsResponseCommitted(c) {
			h.errorResponse(c, http.StatusBadGateway, "upstream_error", "Voice response failed")
		}
		return
	}
	h.recordGrokVoiceUsage(c, apiKey, account, subscription, endpoint, body, result, pricingAt)
}

func (h *OpenAIGatewayHandler) reconcileGrokVoiceBalancePreauthorization(
	c *gin.Context,
	apiKey *service.APIKey,
	endpoint string,
	result *service.OpenAIForwardResult,
	pricingAt time.Time,
	guard *service.BalancePreauthorizationGuard,
) error {
	mode := ""
	if result != nil && result.AudioUsage != nil {
		mode = strings.TrimSpace(result.AudioUsage.Mode)
	}
	if h == nil || h.gatewayService == nil || c == nil || apiKey == nil || result == nil || guard == nil ||
		result.AudioUsage == nil || (endpoint != "tts" && endpoint != "stt") || mode != endpoint ||
		result.AudioUsage.DurationOrUnits <= 0 || math.IsNaN(result.AudioUsage.DurationOrUnits) || math.IsInf(result.AudioUsage.DurationOrUnits, 0) {
		return service.ErrBillingServiceUnavailable.WithCause(service.ErrInvalidBillingPreauthorizationEstimate)
	}
	result.RequestID = guard.RequestID()
	model := strings.TrimSpace(result.Model)
	if model == "" {
		model = endpoint
	}
	actualCost, err := h.gatewayService.BalancePreauthorizationAudioCost(
		c.Request.Context(), apiKey, model, mode, result.AudioUsage.DurationOrUnits, pricingAt,
	)
	if err != nil {
		return err
	}
	return guard.TopUpTo(c.Request.Context(), actualCost)
}

// recordGrokVoiceUsage bills TTS/STT/realtime via group audio prices when AudioUsage is set.
func (h *OpenAIGatewayHandler) recordGrokVoiceUsage(
	c *gin.Context,
	apiKey *service.APIKey,
	account *service.Account,
	subscription *service.UserSubscription,
	endpoint string,
	body []byte,
	result *service.OpenAIForwardResult,
	pricingAt time.Time,
) {
	if h == nil || c == nil || apiKey == nil || account == nil || result == nil {
		return
	}
	if result.AudioUsage == nil {
		return
	}
	model := strings.TrimSpace(result.Model)
	if model == "" {
		model = endpoint
	}
	guard, guarded := service.BalancePreauthorizationGuardFromContext(c.Request.Context())
	if guarded && guard.IsCurrentOwner() && endpoint == "realtime" {
		// Realtime keeps its existing reconciliation path: reservation happens
		// during relay, and this final pass aligns the usage task with the hold.
		result.RequestID = guard.RequestID()
		actualCost, err := h.gatewayService.BalancePreauthorizationAudioCost(
			c.Request.Context(), apiKey, model, result.AudioUsage.Mode, result.AudioUsage.DurationOrUnits, pricingAt,
		)
		if err != nil {
			logger.L().With(
				zap.String("component", "handler.openai_gateway.grok_voice"),
				zap.String("endpoint", endpoint),
				zap.Int64("api_key_id", apiKey.ID),
			).Error("grok_voice.balance_preauthorization_audio_cost_failed", zap.Error(err))
		} else if err := guard.TopUpTo(c.Request.Context(), actualCost); err != nil {
			logger.L().With(
				zap.String("component", "handler.openai_gateway.grok_voice"),
				zap.String("endpoint", endpoint),
				zap.Int64("api_key_id", apiKey.ID),
			).Error("grok_voice.balance_preauthorization_top_up_failed", zap.Error(err))
		}
	} else if !(guarded && guard.IsCurrentOwner()) && strings.TrimSpace(result.AudioUsage.Mode) == "realtime" {
		result.RequestID = service.StableGrokRealtimeBillingRequestID(result.RequestID)
	} else if !(guarded && guard.IsCurrentOwner()) {
		result.RequestID = service.StableGrokAudioBillingRequestID(result.RequestID)
	}
	userAgent := c.GetHeader("User-Agent")
	clientIP := ip.GetClientIP(c)
	sessionID := service.ExtractClientSessionID(c)
	requestPayloadHash := service.HashUsageRequestPayload(body)
	if requestPayloadHash == "" {
		requestPayloadHash = service.HashUsageRequestPayload([]byte(endpoint))
	}
	inboundEndpoint := GetInboundEndpoint(c)
	upstreamEndpoint := GetUpstreamEndpoint(c, account.Platform)
	quotaPlatform := service.QuotaPlatform(c.Request.Context(), apiKey)
	h.submitMandatoryUsageRecordTask(c.Request.Context(), func(ctx context.Context) {
		if err := h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
			Result:             result,
			APIKey:             apiKey,
			User:               apiKey.User,
			Account:            account,
			Subscription:       subscription,
			InboundEndpoint:    inboundEndpoint,
			UpstreamEndpoint:   upstreamEndpoint,
			UserAgent:          userAgent,
			IPAddress:          clientIP,
			RequestPayloadHash: requestPayloadHash,
			APIKeyService:      h.apiKeyService,
			QuotaPlatform:      quotaPlatform,
			SessionID:          sessionID,
			PricingAt:          pricingAt,
			ChannelUsageFields: clientRequestedUsageFields(c, service.ChannelMappingResult{}, model, result.UpstreamModel),
		}); err != nil {
			logger.L().With(
				zap.String("component", "handler.openai_gateway.grok_voice"),
				zap.Int64("user_id", apiKey.User.ID),
				zap.Int64("api_key_id", apiKey.ID),
				zap.Any("group_id", apiKey.GroupID),
				zap.String("endpoint", endpoint),
				zap.Int64("account_id", account.ID),
			).Error("grok_voice.record_usage_failed", zap.Error(err))
		}
	})
}

func readGrokVoiceGatewayBody(c *gin.Context) ([]byte, error) {
	if c == nil || c.Request == nil {
		return nil, errors.New("request body is required")
	}
	if c.Request.Body == nil {
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodDelete {
			return nil, nil
		}
		return nil, errors.New("request body is required")
	}
	return io.ReadAll(c.Request.Body)
}

// extractGrokTTSInputText pulls the primary spoken text from a TTS JSON body.
func extractGrokTTSInputText(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	for _, key := range []string{"input", "text", "prompt"} {
		if v, ok := payload[key]; ok {
			if s, ok := v.(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}
