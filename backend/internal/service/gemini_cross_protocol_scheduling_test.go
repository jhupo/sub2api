package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMixedSchedulingPreservesGeminiPlatformIsolation(t *testing.T) {
	cloudCode := &Account{
		Platform:    PlatformGemini,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"oauth_type": GeminiOAuthTypeAntigravity},
	}
	aiStudio := &Account{
		Platform:    PlatformGemini,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"oauth_type": GeminiOAuthTypeAIStudio},
	}
	apiKey := &Account{Platform: PlatformGemini, Type: AccountTypeAPIKey}
	mixedAntigravity := &Account{
		Platform: PlatformAntigravity,
		Extra:    map[string]any{"mixed_scheduling": true},
	}
	isolatedAntigravity := &Account{Platform: PlatformAntigravity}

	require.Equal(t, []string{PlatformAnthropic, PlatformAntigravity}, mixedSchedulingPlatforms(PlatformAnthropic))
	require.Equal(t, []string{PlatformGemini, PlatformAntigravity}, mixedSchedulingPlatforms(PlatformGemini))
	require.False(t, isAccountAllowedForMixedPlatform(cloudCode, PlatformAnthropic))
	require.False(t, isAccountAllowedForMixedPlatform(aiStudio, PlatformAnthropic))
	require.False(t, isAccountAllowedForMixedPlatform(apiKey, PlatformAnthropic))
	require.True(t, isAccountAllowedForMixedPlatform(cloudCode, PlatformGemini))
	require.True(t, isAccountAllowedForMixedPlatform(aiStudio, PlatformGemini))
	require.True(t, isAccountAllowedForMixedPlatform(apiKey, PlatformGemini))
	require.True(t, isAccountAllowedForMixedPlatform(mixedAntigravity, PlatformAnthropic))
	require.True(t, isAccountAllowedForMixedPlatform(mixedAntigravity, PlatformGemini))
	require.False(t, isAccountAllowedForMixedPlatform(isolatedAntigravity, PlatformAnthropic))
	require.False(t, isAccountAllowedForMixedPlatform(isolatedAntigravity, PlatformGemini))
}
