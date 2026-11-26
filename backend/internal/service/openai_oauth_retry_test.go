package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIOAuthRetriesShareBudgetAcrossStatuses(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		openAIOAuthRetryEnabledKey: true, openAIOAuthRetryCountKey: 2,
		openAIOAuthRetryStatusCodesKey: []int{502, 503},
	}}
	attempts := 0
	_, err := (&OpenAIGatewayService{}).forwardWithOpenAIOAuthRetries(context.Background(), c, account, func() (*OpenAIForwardResult, error) {
		attempts++
		status := 503
		if attempts == 2 {
			status = 502
		}
		return nil, &UpstreamFailoverError{StatusCode: status, RetryableOnSameAccount: true}
	})
	require.Equal(t, 3, attempts)
	var failure *UpstreamFailoverError
	require.ErrorAs(t, err, &failure)
	require.False(t, failure.RetryableOnSameAccount)
	require.True(t, failure.ShouldRetryNextAccount())
}

func TestOpenAIOAuthRetriesStopOnZeroOutputOrCancellation(t *testing.T) {
	for _, test := range []string{"zero", "output", "canceled", "disabled", "unselected", "account_disabled"} {
		t.Run(test, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
				openAIOAuthRetryEnabledKey: true, openAIOAuthRetryCountKey: 2,
				openAIOAuthRetryStatusCodesKey: []int{503},
			}}
			if test == "zero" {
				account.Credentials[openAIOAuthRetryCountKey] = 0
			}
			if test == "disabled" {
				account.Credentials[openAIOAuthRetryEnabledKey] = false
			}
			attempts := 0
			_, err := (&OpenAIGatewayService{}).forwardWithOpenAIOAuthRetries(ctx, c, account, func() (*OpenAIForwardResult, error) {
				attempts++
				if test == "output" {
					_, _ = c.Writer.WriteString("data: partial output\n\n")
				}
				if test == "canceled" {
					cancel()
				}
				status := 503
				if test == "unselected" {
					status = 500
				}
				return nil, &UpstreamFailoverError{StatusCode: status, RetryableOnSameAccount: test != "account_disabled"}
			})
			require.Error(t, err)
			require.Equal(t, 1, attempts)
			if test == "canceled" {
				require.True(t, errors.Is(err, context.Canceled))
			}
		})
	}
}

func TestOpenAIOAuthRetriesRecoverWithoutReturningTheFirstError(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		openAIOAuthRetryEnabledKey: true, openAIOAuthRetryCountKey: 2,
		openAIOAuthRetryStatusCodesKey: []int{503},
	}}
	attempts := 0
	result, err := (&OpenAIGatewayService{}).forwardWithOpenAIOAuthRetries(context.Background(), c, account, func() (*OpenAIForwardResult, error) {
		attempts++
		if attempts == 1 {
			return nil, &UpstreamFailoverError{StatusCode: 503, RetryableOnSameAccount: true}
		}
		ms := 10
		return &OpenAIForwardResult{FirstTokenMs: &ms}, nil
	})
	require.NoError(t, err)
	require.Equal(t, 2, attempts)
	require.GreaterOrEqual(t, result.Duration, 500*time.Millisecond)
	require.GreaterOrEqual(t, *result.FirstTokenMs, 500)
}
