//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/stretchr/testify/require"
)

func TestGeminiExchangeIncludesGoogleEmail(t *testing.T) {
	client := &mockGeminiOAuthClient{
		exchangeCodeFunc: func(context.Context, string, string, string, string, string) (*geminicli.TokenResponse, error) {
			return &geminicli.TokenResponse{AccessToken: "issued-token", RefreshToken: "issued-refresh", ExpiresIn: 3600}, nil
		},
		getEmailFunc: func(_ context.Context, token, proxy string) (string, error) {
			require.Equal(t, "issued-token", token)
			require.Equal(t, "http://proxy.example:8080", proxy)
			return "user@example.com", nil
		},
	}
	svc := NewGeminiOAuthService(nil, client, nil, &config.Config{})
	t.Cleanup(svc.Stop)
	svc.sessionStore.Set("session", &geminicli.OAuthSession{
		State: "state", OAuthType: GeminiOAuthTypeAIStudio, CreatedAt: time.Now(), ProxyURL: "http://proxy.example:8080",
	})
	info, err := svc.ExchangeCode(context.Background(), &GeminiExchangeCodeInput{SessionID: "session", State: "state", Code: "code"})
	require.NoError(t, err)
	require.Equal(t, "user@example.com", info.Email)
	require.Equal(t, info.Email, svc.BuildAccountCredentials(info)["email"])
}

func TestGeminiRefreshEnrichesEmailWithoutLosingTokens(t *testing.T) {
	for _, tc := range []struct {
		name, existing, fetched, want string
		failure                       bool
		calls                         int
	}{
		{name: "enrich missing email", fetched: " user@example.com ", want: "user@example.com", calls: 1},
		{name: "preserve known email", existing: "saved@example.com", want: "saved@example.com"},
		{name: "userinfo failure preserves refreshed token", failure: true, calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &mockGeminiOAuthClient{
				refreshTokenFunc: func(context.Context, string, string, string) (*geminicli.TokenResponse, error) {
					return &geminicli.TokenResponse{AccessToken: "new-access", RefreshToken: "rotated-refresh", ExpiresIn: 3600}, nil
				},
				getEmailFunc: func(ctx context.Context, accessToken, proxyURL string) (string, error) {
					calls++
					require.Equal(t, "new-access", accessToken)
					_, hasDeadline := ctx.Deadline()
					require.True(t, hasDeadline)
					if tc.failure {
						return "", errors.New("userinfo unavailable")
					}
					return tc.fetched, nil
				},
			}
			svc := NewGeminiOAuthService(nil, client, nil, &config.Config{})
			t.Cleanup(svc.Stop)
			info, err := svc.RefreshAccountToken(context.Background(), &Account{
				Platform: PlatformGemini, Type: AccountTypeOAuth,
				Credentials: map[string]any{"oauth_type": GeminiOAuthTypeAIStudio, "refresh_token": "old-refresh", "email": tc.existing},
			})
			require.NoError(t, err)
			require.Equal(t, tc.want, info.Email)
			require.Equal(t, tc.calls, calls)
			creds := svc.BuildAccountCredentials(info)
			require.Equal(t, "new-access", creds["access_token"])
			require.Equal(t, "rotated-refresh", creds["refresh_token"])
			if tc.want != "" {
				require.Equal(t, tc.want, creds["email"])
			} else {
				require.NotContains(t, creds, "email")
			}
		})
	}
}
