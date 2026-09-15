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

// One state owns the retry budget for an account attempt, including HTTP and
// pre-output SSE failures. The handler keeps the account concurrency slot.
type openAI503RetryState struct {
	service *OpenAIGatewayService
	account *Account
	ctx     context.Context
	retries int
}

func (s *OpenAIGatewayService) newOpenAI503RetryState(ctx context.Context, account *Account) (*openAI503RetryState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	state := &openAI503RetryState{service: s, account: account, ctx: ctx}
	if s == nil || account == nil || account.ID <= 0 || account.Platform != PlatformOpenAI {
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
	if s.account == nil || s.account.Platform != PlatformOpenAI || !isOpenAI503RetrySignal(status, body) {
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
