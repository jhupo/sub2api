//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/stretchr/testify/require"
)

func TestAntigravityQuotaUsesForwardEndpoint(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "summary", true: "model fallback"}[fallback], func(t *testing.T) {
			var mu sync.Mutex
			var quotaCalls []string
			server := func(name string) *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch {
					case strings.HasSuffix(r.URL.Path, ":retrieveUserQuotaSummary"):
						mu.Lock()
						quotaCalls = append(quotaCalls, name+":summary")
						mu.Unlock()
						if fallback {
							w.WriteHeader(http.StatusNotFound)
							return
						}
						_, _ = w.Write([]byte(`{"groups":[]}`))
					case strings.HasSuffix(r.URL.Path, ":fetchAvailableModels"):
						mu.Lock()
						quotaCalls = append(quotaCalls, name+":models")
						mu.Unlock()
						_, _ = w.Write([]byte(`{"models":{}}`))
					default:
						_, _ = w.Write([]byte(`{"currentTier":{"id":"free-tier"}}`))
					}
				}))
			}
			prod, daily := server("prod"), server("daily")
			t.Cleanup(prod.Close)
			t.Cleanup(daily.Close)
			oldURLs, oldAvailability := antigravity.BaseURLs, antigravity.DefaultURLAvailability
			t.Cleanup(func() { antigravity.BaseURLs = oldURLs; antigravity.DefaultURLAvailability = oldAvailability })
			antigravity.BaseURLs = []string{prod.URL, daily.URL}
			antigravity.DefaultURLAvailability = antigravity.NewURLAvailability(time.Minute)
			t.Setenv(antigravityForwardBaseURLEnv, "daily")
			result, err := NewAntigravityQuotaFetcher(nil, nil, nil, nil).FetchQuota(context.Background(), &Account{
				Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test-token", "project_id": "test-project"},
			}, "")
			require.NoError(t, err)
			require.NotNil(t, result)
			expected := []string{"daily:summary"}
			if fallback {
				expected = append(expected, "daily:models")
			}
			mu.Lock()
			defer mu.Unlock()
			require.Equal(t, expected, quotaCalls)
		})
	}
}
