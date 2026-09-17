package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// One state owns the dedicated retry budget for an OAuth-like account attempt,
// including HTTP and pre-output SSE failures. API key accounts keep using their
// existing pool-mode retry policy.
type openAI503RetryState struct {
	service *OpenAIGatewayService
	account *Account
	ctx     context.Context
	retries int
}

func usesDedicatedOpenAI503Retry(account *Account) bool {
	return account != nil && account.IsOpenAIOAuthLike()
}

func (s *OpenAIGatewayService) newOpenAI503RetryState(ctx context.Context, account *Account) (*openAI503RetryState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	state := &openAI503RetryState{service: s, account: account, ctx: ctx}
	if s == nil || !usesDedicatedOpenAI503Retry(account) || account.ID <= 0 {
		return state, nil
	}
	if err := s.getOpenAI503RetryQueue().wait(ctx, account.ID, 0, false); err != nil {
		if errors.Is(err, ErrOpenAI503RetryQueueFull) {
			return nil, &UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable, OpenAI503QueueHandled: true}
		}
		return nil, err
	}
	return state, nil
}

// build creates a fresh request because the previous attempt consumed its body.
func (s *openAI503RetryState) do(c *gin.Context, build func() (*http.Request, error), proxyURL string) (*http.Response, error) {
	for {
		req, err := build()
		if err != nil {
			return nil, err
		}
		if s.retries > 0 {
			// The first request is part of the normal turn budget. Once it has
			// returned an OpenAI capacity 503, subsequent requests are governed
			// exclusively by this state's configured same-account retry count.
			// The marker is state-based because SSE overloads leave this method
			// and re-enter it from the outer response-processing loop.
			req = req.WithContext(withOpenAI503RetryAttempt(req.Context()))
		}
		started := time.Now()
		resp, err := s.service.doOpenAIUpstream(req, proxyURL, s.account)
		if c != nil {
			SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(started).Milliseconds())
		}
		if err != nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
			return resp, err
		}
		retry, handleErr := s.handle503(c, resp)
		if handleErr != nil {
			return nil, handleErr
		}
		if !retry {
			return resp, nil
		}
	}
}

// OpenAI503RetrySession lets OAuth-like non-HTTP ingress paths use the same
// account-local queue, delay and retry count as HTTP/SSE forwarding.
type OpenAI503RetrySession struct {
	state *openAI503RetryState
}

// NewOpenAI503RetrySession joins an active account queue and creates the retry
// state used by a client WebSocket account attempt.
func (s *OpenAIGatewayService) NewOpenAI503RetrySession(ctx context.Context, account *Account) (*OpenAI503RetrySession, error) {
	state, err := s.newOpenAI503RetryState(ctx, account)
	if err != nil {
		return nil, err
	}
	return &OpenAI503RetrySession{state: state}, nil
}

// HandleStreamError applies the configured OpenAI capacity retry policy while
// replay is still safe.
func (s *OpenAI503RetrySession) HandleStreamError(c *gin.Context, err error) (bool, error) {
	if s == nil || s.state == nil {
		return false, err
	}
	var failure *UpstreamFailoverError
	if !errors.As(err, &failure) || failure.OpenAI503QueueHandled ||
		!isOpenAI503RetrySignal(failure.StatusCode, failure.ResponseBody) {
		return false, err
	}
	// Client WebSocket handshakes commit Gin's HTTP writer before any business
	// frame is sent. The ingress forwarder returns this failure only while the
	// current response.create can still be replayed safely.
	retry, queueErr := s.state.retry(c, failure.StatusCode, failure.ResponseHeaders, failure.ResponseBody)
	if retry || queueErr != nil {
		return retry, queueErr
	}
	return false, err
}

// RetryContext exempts exactly one upstream send from the legacy generic turn
// budget after a dedicated OAuth or API pool policy charged the retry.
func (s *OpenAI503RetrySession) RetryContext(ctx context.Context) context.Context {
	return withOpenAI503RetryAttempt(ctx)
}

// Reset starts a fresh configured 503 budget after a business turn succeeds.
// A long-lived client WebSocket can carry multiple independent turns.
func (s *OpenAI503RetrySession) Reset() {
	if s == nil || s.state == nil {
		return
	}
	s.state.retries = 0
}

// Stream readers only return a typed capacity failure while replay is safe.
// Close the previous stream before waiting, so it cannot retain a connection
// or keep producing data during cooldown.
func (s *openAI503RetryState) handleStreamError(c *gin.Context, resp *http.Response, err error) (bool, error) {
	var failure *UpstreamFailoverError
	if !errors.As(err, &failure) || failure.OpenAI503QueueHandled ||
		openAIStreamClientOutputStarted(c, false) ||
		!isOpenAI503RetrySignal(failure.StatusCode, failure.ResponseBody) {
		return false, err
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	retry, queueErr := s.retry(c, failure.StatusCode, failure.ResponseHeaders, failure.ResponseBody)
	if retry || queueErr != nil {
		return retry, queueErr
	}
	return false, err
}

func (s *openAI503RetryState) handle503(c *gin.Context, resp *http.Response) (bool, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, openAIUpstreamErrorBodyReadLimit))
	_ = resp.Body.Close()
	if err != nil {
		return false, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return s.retry(c, resp.StatusCode, resp.Header, body)
}

func (s *openAI503RetryState) retry(c *gin.Context, status int, headers http.Header, body []byte) (bool, error) {
	if !usesDedicatedOpenAI503Retry(s.account) || !isOpenAI503RetrySignal(status, body) {
		return false, nil
	}
	message := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	settings := DefaultOpenAI503RetrySettings()
	if s.service.settingService != nil {
		if configured, err := s.service.settingService.GetOpenAI503RetrySettings(s.ctx); err == nil && configured != nil {
			settings = configured
		}
	}
	exhausted := func() error {
		failure := s.service.newOpenAIAccountFailoverError(s.account, status, headers, body, message, false, false)
		failure.OpenAI503QueueHandled = true
		failure.RetryableOnSameAccount = false
		return failure
	}
	if !settings.Enabled || s.retries >= settings.MaxSameAccountRetries {
		return false, exhausted()
	}
	s.retries++
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform: s.account.Platform, AccountID: s.account.ID, AccountName: s.account.Name,
		UpstreamStatusCode: status, UpstreamRequestID: headers.Get("x-request-id"), Kind: "retry", Message: message,
	})
	delay := time.Duration(settings.RetryDelaySeconds) * time.Second
	if retryAfter := openAI503RetryAfter(headers); retryAfter > delay {
		delay = retryAfter
	}
	if err := s.service.getOpenAI503RetryQueue().wait(s.ctx, s.account.ID, delay, true); err != nil {
		if errors.Is(err, ErrOpenAI503RetryQueueFull) {
			return false, exhausted()
		}
		return false, err
	}
	return true, nil
}

func isOpenAI503RetrySignal(status int, body []byte) bool {
	return status == http.StatusServiceUnavailable && isOpenAIRequestScopedCapacityShed("", body)
}

func openAI503RetryAfter(headers http.Header) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(headers.Get("Retry-After")))
	if err != nil || seconds <= 0 || seconds > 120 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}
