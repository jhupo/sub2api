package service

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountCanBindToGroup(t *testing.T) {
	geminiOAuth := &Account{Platform: PlatformGemini, Type: AccountTypeOAuth}
	mixedAntigravity := &Account{Platform: PlatformAntigravity, Extra: map[string]any{"mixed_scheduling": true}}
	isolatedAntigravity := &Account{Platform: PlatformAntigravity}

	tests := []struct {
		name    string
		account *Account
		group   *Group
		want    bool
	}{
		{name: "same platform", account: geminiOAuth, group: &Group{Platform: PlatformGemini}, want: true},
		{name: "concrete account in composite", account: geminiOAuth, group: &Group{Platform: PlatformComposite}, want: true},
		{name: "Gemini OAuth cannot bind Anthropic", account: geminiOAuth, group: &Group{Platform: PlatformAnthropic}, want: false},
		{name: "mixed Antigravity in Gemini", account: mixedAntigravity, group: &Group{Platform: PlatformGemini}, want: true},
		{name: "mixed Antigravity in Anthropic", account: mixedAntigravity, group: &Group{Platform: PlatformAnthropic}, want: true},
		{name: "isolated Antigravity cannot cross bind", account: isolatedAntigravity, group: &Group{Platform: PlatformGemini}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, accountCanBindToGroup(tt.account, tt.group))
		})
	}
}

func TestFilterOAuthOnlyAccountIDs(t *testing.T) {
	accounts := []*Account{
		{ID: 1, Type: AccountTypeOAuth},
		{ID: 2, Type: AccountTypeAPIKey},
	}

	filtered, err := filterOAuthOnlyAccountIDs([]int64{1, 2}, accounts)
	require.NoError(t, err)
	require.Equal(t, []int64{1}, filtered)

	_, err = filterOAuthOnlyAccountIDs([]int64{1, 3}, accounts)
	require.True(t, errors.Is(err, ErrAccountNotFound))
}
