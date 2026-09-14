package service

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexCustomIdentityPreservesBothVersions(t *testing.T) {
	for _, ua := range []string{
		"Codex Desktop/0.153.4 (Mac OS 26.6.1; arm64) unknown (Codex Desktop; 26.903.61454)",
		"codex-tui/0.153.4 (Linux; x86_64) xterm (codex-tui; 0.149.0)",
	} {
		require.NoError(t, ValidateOpenAICodexUserAgent(ua))
		identity := resolveCodexOutboundIdentityWithCanonical(ua, buildCodexCLIUserAgent("0.200.1"))
		require.Equal(t, ua, identity.userAgent)
		require.Equal(t, "0.153.4", identity.version)
		h := make(http.Header)
		identity.applyHeaders(h)
		pairCodexIdentityHeadersWithCanonical(h, buildCodexCLIUserAgent("0.201.0"))
		require.Equal(t, ua, h.Get("User-Agent"))
		require.Equal(t, "0.153.4", h.Get("version"))
	}
}

func TestValidateOpenAICodexUserAgent(t *testing.T) {
	for _, ua := range []string{"", "   ", codexCLIUserAgent} {
		require.NoError(t, ValidateOpenAICodexUserAgent(ua))
	}
	for _, ua := range []string{
		"\n", "codex-tui/0.153.4\n", "\rcodex-tui/0.153.4",
		"codex-tui/0.125.0 (Linux; x86_64)",
		"codex-tui/latest (Linux; x86_64)",
		"codex-tui/0.153.4\t (Linux; x86_64)",
		"Codex Desktop/0.153.4 (Mac OS; arm64) unknown (Codex Desktop; latest)",
		"codex-tui/0.153.4 " + strings.Repeat("x", 512),
	} {
		require.Error(t, ValidateOpenAICodexUserAgent(ua), ua)
		identity := resolveCodexOutboundIdentityWithCanonical(ua, codexCLIUserAgent)
		if len(ua) <= 512 {
			require.Equal(t, codexCLIUserAgent, identity.userAgent)
		}
	}
}

func TestCodexCustomUASettingsCacheTransition(t *testing.T) {
	repo := &codexVersionSettingRepoStub{values: map[string]string{
		SettingKeyOpenAICodexUserAgent:           codexCLIUserAgent,
		SettingKeyOpenAICodexClientVersionSynced: "0.200.1",
	}}
	svc := NewSettingService(repo, nil)
	ctx := context.Background()
	// Explicitly entering even the built-in UA pins its complete version.
	require.Equal(t, codexCLIUserAgent, svc.GetOpenAICodexCanonicalUserAgent(ctx))
	repo.values[SettingKeyOpenAICodexClientVersionSynced] = "0.201.0"
	svc.InvalidateOpenAICodexClientVersionCache()
	require.Equal(t, codexCLIUserAgent, svc.GetOpenAICodexCanonicalUserAgent(ctx))

	repo.values[SettingKeyOpenAICodexUserAgent] = ""
	svc.refreshCachedSettings(&SystemSettings{OpenAICodexVersionAutoSyncEnabled: true})
	require.Empty(t, svc.GetOpenAICodexUserAgent(ctx))
	require.Equal(t, buildCodexCLIUserAgent("0.201.0"), svc.GetOpenAICodexCanonicalUserAgent(ctx))

	repo.values[SettingKeyOpenAICodexVersionAutoSyncEnabled] = "false"
	svc.refreshCachedSettings(&SystemSettings{})
	require.Equal(t, codexCLIUserAgent, svc.GetOpenAICodexCanonicalUserAgent(ctx))
}

func TestCodexStoredUARejectsControlBytesOnCacheRefresh(t *testing.T) {
	const invalidUA = "codex-tui/0.200.1 (Linux)\n"
	svc := NewSettingService(&codexVersionSettingRepoStub{values: map[string]string{
		SettingKeyOpenAICodexUserAgent: invalidUA,
	}}, nil)
	require.Equal(t, codexCLIUserAgent, svc.GetOpenAICodexCanonicalUserAgent(context.Background()))
	svc.refreshCachedSettings(&SystemSettings{OpenAICodexUserAgent: invalidUA})
	require.Equal(t, codexCLIUserAgent, svc.GetOpenAICodexCanonicalUserAgent(context.Background()))
}

func TestCodexDesktopIdentityFrozenAcrossRetries(t *testing.T) {
	const desktopUA = "Codex Desktop/0.153.4 (Mac OS 26.6.1; arm64) unknown (Codex Desktop; 26.903.61454)"
	currentUA := desktopUA
	SetCodexCanonicalUserAgentResolver(func() string { return currentUA })
	t.Cleanup(func() { SetCodexCanonicalUserAgentResolver(nil) })
	account := newTestOAuthAccount(620, nil)
	svc := &OpenAIGatewayService{}
	c := newFingerprintStageTestContext(t)
	body := []byte(`{"input":"test"}`)
	require.NoError(t, svc.prepareCodexAttemptIdentity(context.Background(), c, account, body))
	currentUA = buildCodexCLIUserAgent("0.201.0")
	for n := 0; n < 2; n++ {
		req, err := svc.buildUpstreamRequest(context.Background(), c, account, body, "token", true, "cache", false)
		require.NoError(t, err)
		require.Equal(t, desktopUA, req.Header.Get("User-Agent"))
		require.Equal(t, "Codex Desktop", req.Header.Get("originator"))
		require.Equal(t, "0.153.4", req.Header.Get("version"))
	}
	require.NoError(t, svc.prepareCodexAttemptIdentity(context.Background(), c, account, body))
	require.Equal(t, currentUA, stagedCodexAttemptIdentity(c, account).client.userAgent)
}
