package service

import (
	"context"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
)

// Keep configured retries inside the admitted account slot. Returning to the
// scheduler between attempts can select another account even with affinity.
func (s *OpenAIGatewayService) forwardWithOpenAIOAuthRetries(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	forward func() (*OpenAIForwardResult, error),
) (*OpenAIForwardResult, error) {
	if !account.IsOpenAIOAuthRetryEnabled() {
		return forward()
	}
	started := time.Now()
	limit := account.GetOpenAIOAuthRetryCount()
	for retries := 0; ; retries++ {
		attemptStarted := time.Now()
		writtenBefore := OpenAICompactKeepaliveAdjustedWrittenSize(c)
		result, err := forward()
		var failure *UpstreamFailoverError
		if !errors.As(err, &failure) || !account.IsOpenAIOAuthRetryableStatus(failure.StatusCode) {
			if result != nil && retries > 0 {
				result.Duration = time.Since(started)
				if result.FirstTokenMs != nil {
					ms := *result.FirstTokenMs + int(attemptStarted.Sub(started).Milliseconds())
					result.FirstTokenMs = &ms
				}
			}
			return result, err
		}
		// This layer owns the configured budget. Never start another copy of it
		// in the handler after returning the final error for account failover.
		finalFailure := *failure
		finalFailure.RetryableOnSameAccount = false
		if retries >= limit || result != nil || !failure.RetryableOnSameAccount ||
			!failure.ShouldRetryNextAccount() || IsResponseCommitted(c) ||
			OpenAICompactKeepaliveAdjustedWrittenSize(c) != writtenBefore {
			return result, &finalFailure
		}
		if !failure.SameAccountRetryDeadline.IsZero() && !time.Now().Before(failure.SameAccountRetryDeadline) {
			return result, &finalFailure
		}
		if failure.SameAccountRetryMax > 0 && retries >= failure.SameAccountRetryMax {
			return result, &finalFailure
		}
		clientCtx := ctx
		if c != nil && c.Request != nil {
			clientCtx = c.Request.Context()
		}
		if clientCtx.Err() != nil {
			return result, clientCtx.Err()
		}
		delay := 500 * time.Millisecond
		if failure.SameAccountRetryDelay > delay {
			delay = failure.SameAccountRetryDelay
		}
		logger.LegacyPrintf("service.openai_gateway", "OpenAI OAuth same-account retry: account=%d status=%d retry=%d/%d delay=%s", account.ID, failure.StatusCode, retries+1, limit, delay)
		timer := time.NewTimer(delay)
		select {
		case <-clientCtx.Done():
			timer.Stop()
			return nil, clientCtx.Err()
		case <-timer.C:
		}
		if clientCtx.Err() != nil {
			return nil, clientCtx.Err()
		}
		if !failure.SameAccountRetryDeadline.IsZero() && !time.Now().Before(failure.SameAccountRetryDeadline) {
			return result, &finalFailure
		}
		authorizeOpenAIConfiguredRetry(ctx)
	}
}
