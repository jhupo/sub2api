package service

import (
	"context"
	"errors"

	"github.com/gin-gonic/gin"
)

// reserveOpenAIStreamingOutput distinguishes request cancellation from a failed
// money operation. A wallet may finish its bounded, detached top-up after the
// request is canceled; its hold still belongs to the normal settlement path.
func (s *OpenAIGatewayService) reserveOpenAIStreamingOutput(
	ctx context.Context, c *gin.Context, account *Account, origin string,
	guard *BalancePreauthorizationGuard, outputBytes int,
) (clientCanceled bool, err error) {
	ctx = nonNilContext(ctx)
	if errors.Is(ctx.Err(), context.Canceled) {
		return true, context.Canceled
	}
	if err := guard.ObserveStreamingOutput(ctx, outputBytes); err != nil {
		// A backend's own cancellation/timeout is not a client cancellation.
		if errors.Is(ctx.Err(), context.Canceled) && errors.Is(err, context.Canceled) {
			return true, context.Canceled
		}
		s.reportOpenAIStreamOutputHoldTopUpFailure(c, account, origin, err)
		return false, wrapStreamOutputHoldTopUpFailure(err)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return true, context.Canceled
	}
	return false, nil
}
