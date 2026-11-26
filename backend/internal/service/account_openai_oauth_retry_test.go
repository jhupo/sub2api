package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIOAuthRetryPolicy(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			openAIOAuthRetryEnabledKey:     true,
			openAIOAuthRetryCountKey:       float64(2),
			openAIOAuthRetryStatusCodesKey: []any{float64(503), "520", float64(503), float64(99), "invalid"},
		},
	}

	require.True(t, account.IsOpenAIOAuthRetryEnabled())
	require.Equal(t, 2, account.GetOpenAIOAuthRetryCount())
	require.True(t, account.IsOpenAIOAuthRetryableStatus(503))
	require.True(t, account.IsOpenAIOAuthRetryableStatus(520))
	require.False(t, account.IsOpenAIOAuthRetryableStatus(502))
	require.False(t, account.IsOpenAIOAuthRetryableStatus(99))
}

func TestOpenAIOAuthRetryPolicyIsScopedAndBounded(t *testing.T) {
	base := map[string]any{
		openAIOAuthRetryEnabledKey:     true,
		openAIOAuthRetryCountKey:       float64(99),
		openAIOAuthRetryStatusCodesKey: []any{float64(503)},
	}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: base}
	require.Equal(t, maxPoolModeRetryCount, account.GetOpenAIOAuthRetryCount())

	account.Credentials[openAIOAuthRetryCountKey] = float64(0)
	require.Equal(t, 0, account.GetOpenAIOAuthRetryCount())
	require.True(t, account.IsOpenAIOAuthRetryableStatus(503), "zero attempts still switches to another account")

	account.Credentials[openAIOAuthRetryEnabledKey] = false
	require.False(t, account.IsOpenAIOAuthRetryableStatus(503))

	account.Credentials[openAIOAuthRetryEnabledKey] = true
	account.Type = AccountTypeAPIKey
	require.False(t, account.IsOpenAIOAuthRetryableStatus(503))
	account.Type = AccountTypeOAuth
	parentID := int64(10)
	account.ParentAccountID = &parentID
	require.False(t, account.IsOpenAIOAuthRetryableStatus(503))
}

func TestShouldFailoverOpenAIPassthroughResponseUsesOpenAIOAuthRetryPolicy(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			openAIOAuthRetryEnabledKey:     true,
			openAIOAuthRetryStatusCodesKey: []any{float64(503), float64(520)},
		},
	}
	require.True(t, shouldFailoverOpenAIPassthroughResponse(account, 503, nil))
	require.True(t, shouldFailoverOpenAIPassthroughResponse(account, 520, nil))
	require.False(t, shouldFailoverOpenAIPassthroughResponse(account, 502, nil))

	account.Credentials[openAIOAuthRetryEnabledKey] = false
	require.False(t, shouldFailoverOpenAIPassthroughResponse(account, 503, nil))
}
