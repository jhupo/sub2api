package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// openAI503RetryState activates an account-local FIFO only after the account
// has returned an explicit capacity 503. Before that point it is dormant and
// does not serialize ordinary requests.
type openAI503RetryState struct {
	service      *OpenAIGatewayService
	account      *Account
	ctx          context.Context
	lease        *openAI503RetryLease
	retries      int
	completeOnce bool
}

func (s *OpenAIGatewayService) newOpenAI503RetryState(ctx context.Context, account *Account) (*openAI503RetryState, error) {
	state := &openAI503RetryState{service: s, account: account, ctx: ctx}
	if s == nil || account == nil || account.ID <= 0 {
		return state, nil
	}
	lease, err := s.getOpenAI503RetryQueue().enter(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	state.lease = lease
	return state, nil
}

func (s *openAI503RetryState) complete() {
	if s == nil || s.completeOnce {
		return
	}
	s.completeOnce = true
	if s.lease != nil {
		s.lease.complete()
	}
}

// do sends one or more HTTP attempts. build must create a fresh request for
// each attempt because the previous request body and context are consumed.
// Non-capacity responses are returned untouched for the caller's normal error
// and streaming handling.
func (s *openAI503RetryState) do(
	c *gin.Context,
	build func() (*http.Request, error),
	proxyURL string,
) (*http.Response, error) {
	if s == nil || s.service == nil {
		return nil, nil
	}
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
		if err != nil {
			return resp, err
		}
		if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
			return resp, nil
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

// handle503 consumes and restores the response body when it is not a queue
// signal, and returns retry=true after moving the current lease to its FIFO
// position. On exhaustion it returns the failover error to the caller.
func (s *openAI503RetryState) handle503(c *gin.Context, resp *http.Response) (bool, error) {
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, openAIUpstreamErrorBodyReadLimit))
	_ = resp.Body.Close()
	if readErr != nil {
		return false, readErr
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	message := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	if !isOpenAI503RetrySignal(resp.StatusCode, body) {
		return false, nil
	}
	settings := DefaultOpenAI503RetrySettings()
	if s.service.settingService != nil {
		if configured, getErr := s.service.settingService.GetOpenAI503RetrySettings(s.ctx); getErr == nil && configured != nil {
			settings = configured
		}
	}
	if !settings.Enabled {
		return false, nil
	}
	if s.lease == nil || !s.lease.active() {
		lease, activateErr := s.service.getOpenAI503RetryQueue().activate(s.ctx, s.account.ID)
		if activateErr != nil {
			failoverErr := s.service.newOpenAIAccountFailoverError(s.account, resp.StatusCode, resp.Header, body, message, false, false)
			failoverErr.OpenAI503QueueHandled = true
			s.complete()
			return false, failoverErr
		}
		s.lease = lease
	}
	if s.retries >= settings.MaxSameAccountRetries {
		failoverErr := s.service.newOpenAIAccountFailoverError(s.account, resp.StatusCode, resp.Header, body, message, false, false)
		failoverErr.OpenAI503QueueHandled = true
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform: s.account.Platform, AccountID: s.account.ID, AccountName: s.account.Name,
			UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: resp.Header.Get("x-request-id"), Kind: "failover", Message: message,
		})
		s.complete()
		return false, failoverErr
	}
	s.retries++
	delay := time.Duration(settings.RetryDelaySeconds) * time.Second
	if retryAfter := openAI503RetryAfter(resp.Header); retryAfter > delay {
		delay = retryAfter
	}
	_ = resp.Body.Close()
	s.lease.requeue(delay)
	if err := s.lease.waitTurn(s.ctx); err != nil {
		s.complete()
		return false, err
	}
	return true, nil
}

func isOpenAI503RetrySignal(status int, body []byte) bool {
	if status != http.StatusServiceUnavailable {
		return false
	}
	for _, path := range []string{"error.code", "response.error.code", "code"} {
		code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, path).String()))
		if code == "server_is_overloaded" || code == "slow_down" {
			return true
		}
	}
	return false
}

func openAI503RetryAfter(headers http.Header) time.Duration {
	if headers == nil {
		return 0
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(headers.Get("Retry-After")))
	if err != nil || seconds <= 0 || seconds > 120 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}
