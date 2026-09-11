package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func newWSIdentityTestPool(t *testing.T) (*openAIWSConnPool, *openAIWSCountingDialer) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	pool := newOpenAIWSConnPool(cfg)
	t.Cleanup(pool.Close)
	dialer := &openAIWSCountingDialer{}
	pool.setClientDialerForTest(dialer)
	return pool, dialer
}

func TestOpenAIWSIdentityChangeReplacesIdleConnection(t *testing.T) {
	for _, mode := range []string{"off", "device", "session", "full"} {
		for _, name := range []string{"User-Agent", "originator", "version"} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				pool, dialer := newWSIdentityTestPool(t)
				account := activeCodexFingerprintPoolAccountForTest(401)
				account.Extra[codexFingerprintModeExtraKey] = mode
				req := openAIWSAcquireRequest{Account: account, WSURL: "wss://example.com/responses", Headers: stableOpenAIWSIdentityHeadersForTest()}
				req.Headers.Set(name, "old")
				first, err := pool.Acquire(context.Background(), req)
				require.NoError(t, err)
				first.Release()
				req.Headers.Set(name, "new")
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				second, err := pool.Acquire(ctx, req)
				require.NoError(t, err)
				defer second.Release()
				require.False(t, second.Reused())
				require.NotEqual(t, first.ConnID(), second.ConnID())
				require.Equal(t, 2, dialer.DialCount())
				select {
				case <-first.conn.closedCh:
				default:
					t.Fatal("obsolete idle connection was not retired")
				}
			})
		}
	}
}

func TestOpenAIWSIdentityChangeDoesNotInterruptActiveLease(t *testing.T) {
	pool, dialer := newWSIdentityTestPool(t)
	req := openAIWSAcquireRequest{Account: &Account{ID: 402, Concurrency: 1}, WSURL: "wss://example.com/responses", Headers: make(http.Header)}
	req.Headers.Set("version", "old")
	first, err := pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	defer first.Release()
	req.Headers.Set("version", "new")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err = pool.Acquire(ctx, req)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, 1, dialer.DialCount())
	select {
	case <-first.conn.closedCh:
		t.Fatal("identity update closed an active connection")
	default:
	}
	first.Release()
	second, err := pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	defer second.Release()
	require.False(t, second.Reused())
}

func TestOpenAIWSIdentityChangePreservesOwnedContinuation(t *testing.T) {
	pool, dialer := newWSIdentityTestPool(t)
	account := activeCodexFingerprintPoolAccountForTest(403)
	req := openAIWSAcquireRequest{Account: account, WSURL: "wss://example.com/responses", Headers: stableOpenAIWSIdentityHeadersForTest()}
	req.Headers.Set("User-Agent", "codex-tui/0.146.0")
	first, err := pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	require.True(t, pool.PinConn(account.ID, first.ConnID()))
	defer pool.UnpinConn(account.ID, first.ConnID())
	first.Release()
	req.Headers.Set("User-Agent", "codex-tui/0.200.0")
	req.Headers.Set("version", "0.200.0")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err = pool.Acquire(ctx, req)
	require.ErrorIs(t, err, context.DeadlineExceeded, "new identity must not evict a pinned continuation")
	req.PreferredConnID, req.ForcePreferredConn = first.ConnID(), true
	second, err := pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, first.ConnID(), second.ConnID())
	second.Release()
	req.Headers.Set("thread-id", "other-thread")
	_, err = pool.Acquire(context.Background(), req)
	require.ErrorIs(t, err, errOpenAIWSPreferredConnUnavailable)
	require.Equal(t, 1, dialer.DialCount())
}

func TestOpenAIWSIdentityChangesPrewarmTarget(t *testing.T) {
	account := activeCodexFingerprintPoolAccountForTest(406)
	a := openAIWSAcquireRequest{Account: account, WSURL: "wss://example.com/responses", Headers: stableOpenAIWSIdentityHeadersForTest()}
	for _, name := range []string{"User-Agent", "originator", "version"} {
		b := cloneOpenAIWSAcquireRequest(a)
		b.Headers.Set(name, "changed")
		require.False(t, sameOpenAIWSPrewarmTarget(a, b), name)
	}
}

func TestOpenAIWSIdentityUsesActualDialHeaders(t *testing.T) {
	pool, _ := newWSIdentityTestPool(t)
	req := openAIWSAcquireRequest{Account: &Account{ID: 404, Concurrency: 1}, WSURL: "wss://example.com/responses", Headers: make(http.Header)}
	req.Headers.Set("version", "before")
	req.HeadersFactory = func(_ context.Context, h http.Header) (http.Header, error) { h.Set("version", "actual"); return h, nil }
	first, err := pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "actual", first.conn.handshakeCompatibility.identity.version)
	first.Release()
	req.HeadersFactory = nil
	req.Headers.Set("version", "actual")
	second, err := pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	defer second.Release()
	require.True(t, second.Reused())
}

func TestOpenAIWSIdentityHardBoundariesApplyToOwnedContinuation(t *testing.T) {
	for _, change := range []string{"language", "principal", "endpoint", "proxy", "api_key"} {
		t.Run(change, func(t *testing.T) {
			pool, _ := newWSIdentityTestPool(t)
			account := activeCodexFingerprintPoolAccountForTest(407)
			account.Extra[codexFingerprintModeExtraKey] = "device"
			if change == "api_key" {
				account.Type = AccountTypeAPIKey
			}
			req := openAIWSAcquireRequest{Account: account, WSURL: "wss://example.com/responses", Headers: stableOpenAIWSIdentityHeadersForTest()}
			req.Headers.Set("Accept-Language", "en-US")
			first, err := pool.Acquire(context.Background(), req)
			require.NoError(t, err)
			first.Release()
			req.PreferredConnID, req.ForcePreferredConn = first.ConnID(), true
			switch change {
			case "language":
				req.Headers.Set("Accept-Language", "zh-CN")
			case "principal":
				req.identity = &codexAttemptIdentity{scope: codexAccountIdentityScope{namespace: "different-principal"}}
			case "endpoint":
				req.WSURL = "wss://other.example/responses"
			case "proxy":
				req.ProxyURL = "http://proxy.example:8080"
			case "api_key":
				req.Headers.Set("Authorization", "Bearer replacement-test-key")
			}
			_, err = pool.Acquire(context.Background(), req)
			require.ErrorIs(t, err, errOpenAIWSPreferredConnUnavailable)
			req.ForcePreferredConn, req.PreferredConnID = false, ""
			second, err := pool.Acquire(context.Background(), req)
			require.NoError(t, err)
			defer second.Release()
			require.False(t, second.Reused())
		})
	}
}

func TestOpenAIWSIdentityOAuthRefreshKeepsStablePrincipal(t *testing.T) {
	req := openAIWSAcquireRequest{
		Account:  activeCodexFingerprintPoolAccountForTest(408),
		Headers:  make(http.Header),
		identity: &codexAttemptIdentity{scope: codexAccountIdentityScope{namespace: "chatgpt:test-principal"}},
	}
	req.Headers.Set("Authorization", "Bearer first-test-token")
	first := req.handshakeCompatibility(req.Headers)
	req.Headers.Set("Authorization", "Bearer refreshed-test-token")
	require.Equal(t, first, req.handshakeCompatibility(req.Headers))
}
