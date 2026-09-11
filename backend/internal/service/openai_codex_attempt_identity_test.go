package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexAttemptIdentityBodyOnlySessionAndProjectionParity(t *testing.T) {
	for _, mode := range []string{"off", "device", "session", "full"} {
		t.Run(mode, func(t *testing.T) {
			account := newTestOAuthAccount(601, map[string]any{codexFingerprintModeExtraKey: mode, codexFingerprintSeedExtraKey: testCodexFingerprintSeed})
			body := []byte(`{"input":"keep","prompt_cache_key":"body-session","client_metadata":{"installation_id":"old","x-codex-installation-id":"old","session_id":"body-session","session-id":"body-session","thread_id":"thread","thread-id":"thread","x-codex-turn-metadata":"{\"installation_id\":\"old\",\"x-codex-installation-id\":\"old\",\"timezone\":\"Asia/Shanghai\"}"}}`)
			input := extractCodexIdentityInput(nil, body)
			require.Equal(t, "body-session", input.clientSession)
			identity := (&OpenAIGatewayService{}).resolveCodexAttemptIdentity(account, account, input, 42, false)
			if mode == "session" {
				require.Equal(t, resolveConvergedThreadID(testCodexFingerprintSeed, isolateOpenAISessionID(42, "body-session")), identity.fingerprint.threadID)
			}
			var decoded map[string]any
			require.NoError(t, json.Unmarshal(body, &decoded))
			require.True(t, identity.applyBody(decoded))
			raw, changed, err := identity.applyBodyRaw(body)
			require.NoError(t, err)
			require.True(t, changed)
			encoded, err := json.Marshal(decoded)
			require.NoError(t, err)
			require.JSONEq(t, string(encoded), string(raw))
			again, _, err := identity.applyBodyRaw(body)
			require.NoError(t, err)
			require.Equal(t, raw, again, "same-attempt retries use the original input without rehashing")
			headers := make(http.Header)
			headers.Set("originator", "client")
			headers.Set("Accept-Language", "zh-CN,en;q=0.8")
			headers.Set("x-codex-installation-id", "old")
			headers.Set("x-codex-turn-metadata", `{"installation_id":"old","x-codex-installation-id":"old","timezone":"Asia/Shanghai"}`)
			identity.applyHeaders(headers)
			require.Equal(t, "zh-CN,en;q=0.8", headers.Get("Accept-Language"))
			require.Equal(t, headers.Get("x-codex-installation-id"), gjson.GetBytes(raw, "client_metadata.x-codex-installation-id").String())
			require.Equal(t, gjson.GetBytes(raw, "client_metadata.installation_id").String(), gjson.GetBytes(raw, "client_metadata.x-codex-installation-id").String())
			embedded := gjson.GetBytes(raw, "client_metadata.x-codex-turn-metadata").String()
			require.Equal(t, "Asia/Shanghai", gjson.Get(embedded, "timezone").String())
			require.Equal(t, gjson.Get(embedded, "installation_id").String(), gjson.Get(embedded, "x-codex-installation-id").String())
			require.Equal(t, "keep", gjson.GetBytes(raw, "input").String())
			if identity.fingerprint != nil {
				require.False(t, identity.fingerprint.originalBodySessionIDCaptured, "projection must not mutate the snapshot")
			}
		})
	}
}

func TestCodexAttemptIdentityFrozenUntilNextAttempt(t *testing.T) {
	previousEnforcement := codexIdentityEnforcement.Load()
	codexCanonicalUAMu.RLock()
	previousResolver := codexCanonicalUAResolver
	codexCanonicalUAMu.RUnlock()
	t.Cleanup(func() {
		SetCodexIdentityEnforcementEnabled(previousEnforcement)
		SetCodexCanonicalUserAgentResolver(previousResolver)
	})
	SetCodexIdentityEnforcementEnabled(true)
	var reads atomic.Int32
	version := "0.146.0"
	SetCodexCanonicalUserAgentResolver(func() string {
		reads.Add(1)
		return buildCodexCLIUserAgent(version)
	})
	account := newTestOAuthAccount(602, map[string]any{codexFingerprintModeExtraKey: "device"})
	svc := &OpenAIGatewayService{}
	c := newFingerprintStageTestContext(t)
	body := []byte(`{"prompt_cache_key":"cache","client_metadata":{"session_id":"session"}}`)
	require.NoError(t, svc.prepareCodexAttemptIdentity(context.Background(), c, account, body))
	first := stagedCodexAttemptIdentity(c, account)
	firstBody, _, err := first.applyBodyRaw(body)
	require.NoError(t, err)
	version = "0.147.0"
	account.Credentials = map[string]any{"access_token": "rotated-token"}
	for n := 0; n < 3; n++ {
		req, err := svc.buildUpstreamRequest(context.Background(), c, account, firstBody, "fresh-token", true, "cache", false)
		require.NoError(t, err)
		require.Equal(t, "0.146.0", req.Header.Get("version"))
		require.Equal(t, "Bearer fresh-token", req.Header.Get("Authorization"))
	}
	require.Equal(t, int32(1), reads.Load())
	require.NoError(t, svc.prepareCodexAttemptIdentity(context.Background(), c, account, body))
	next := stagedCodexAttemptIdentity(c, account)
	require.Equal(t, "0.147.0", next.client.version)
	require.Equal(t, first.fingerprint.installationID, next.fingerprint.installationID)
	nextBody, _, err := next.applyBodyRaw(body)
	require.NoError(t, err)
	require.Equal(t, firstBody, nextBody, "version and token changes must not change device-mode cache keys")
	apiKey := &Account{ID: 603, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	require.NoError(t, svc.prepareCodexAttemptIdentity(context.Background(), c, apiKey, body))
	require.Nil(t, stagedCodexAttemptIdentity(c, account))
	require.Nil(t, stagedCodexAttemptIdentity(c, apiKey))
}

func TestCodexAttemptIdentityShadowUsesCredentialSeedAndSelectedPolicy(t *testing.T) {
	parent := newTestOAuthAccount(604, map[string]any{codexFingerprintModeExtraKey: "off", codexFingerprintSeedExtraKey: testCodexFingerprintSeed})
	parent.Extra["openai_device_id"] = "credential-device"
	shadow := newTestOAuthAccount(605, map[string]any{codexFingerprintModeExtraKey: "device"})
	shadow.ParentAccountID = &parent.ID
	shadow.Extra[codexFingerprintSeedExtraKey] = "22222222-2222-4222-8222-222222222222"
	svc := &OpenAIGatewayService{accountRepo: &codexAccountIdentityRepoStub{account: parent}}
	c := newFingerprintStageTestContext(t)
	require.NoError(t, svc.prepareCodexAttemptIdentity(context.Background(), c, shadow, nil))
	identity := stagedCodexAttemptIdentity(c, shadow)
	require.Equal(t, shadow.ID, identity.accountID)
	require.Equal(t, newCodexAccountIdentityScope(parent, 0), identity.scope)
	require.NotNil(t, identity.fingerprint)
	require.Equal(t, resolveConvergedInstallationID(parent, testCodexFingerprintSeed), identity.fingerprint.installationID)
	parent.Extra["openai_device_id"] = "next-attempt-device"
	body := map[string]any{}
	require.True(t, identity.applyInstallationFallback(body))
	require.Equal(t, "credential-device", body["client_metadata"].(map[string]any)["x-codex-installation-id"])
}

func TestCodexAttemptIdentityAccountFailoverRebuildsFromOriginalInput(t *testing.T) {
	first := newTestOAuthAccount(612, map[string]any{codexFingerprintModeExtraKey: "device"})
	second := newTestOAuthAccount(613, map[string]any{codexFingerprintModeExtraKey: "device", codexFingerprintSeedExtraKey: "22222222-2222-4222-8222-222222222222"})
	c := newFingerprintStageTestContext(t)
	svc := &OpenAIGatewayService{}
	body := []byte(`{"prompt_cache_key":"client-cache","client_metadata":{"session_id":"client-session"}}`)
	project := func(account *Account) []byte {
		t.Helper()
		require.NoError(t, svc.prepareCodexAttemptIdentity(context.Background(), c, account, body))
		identity := stagedCodexAttemptIdentity(c, account)
		raw, _, err := identity.applyBodyRaw(body)
		require.NoError(t, err)
		return raw
	}
	firstBody := project(first)
	secondBody := project(second)
	require.Nil(t, stagedCodexAttemptIdentity(c, first))
	require.NotEqual(t, gjson.GetBytes(firstBody, "prompt_cache_key").String(), gjson.GetBytes(secondBody, "prompt_cache_key").String())
	require.NotEqual(t, gjson.GetBytes(firstBody, "client_metadata.x-codex-installation-id").String(), gjson.GetBytes(secondBody, "client_metadata.x-codex-installation-id").String())
	require.Equal(t, firstBody, project(first), "returning to a credential restores its stable identity, not a hash of the prior attempt")
}

func TestCodexAttemptIdentityNativeAndPassthroughHeadersAgree(t *testing.T) {
	for _, mode := range []string{"off", "device", "session", "full"} {
		t.Run(mode, func(t *testing.T) {
			account := newTestOAuthAccount(614, map[string]any{codexFingerprintModeExtraKey: mode})
			c := newFingerprintStageTestContext(t)
			svc := &OpenAIGatewayService{}
			body := []byte(`{"model":"gpt-5.4","stream":true,"prompt_cache_key":"client-cache","client_metadata":{"session_id":"body-session"}}`)
			require.NoError(t, svc.prepareCodexAttemptIdentity(context.Background(), c, account, body))
			scopedBody, _, err := stagedCodexAttemptIdentity(c, account).applyBodyRaw(body)
			require.NoError(t, err)
			native, err := svc.buildUpstreamRequest(context.Background(), c, account, scopedBody, "test-token", true, "client-cache", true)
			require.NoError(t, err)
			passthrough, err := svc.buildUpstreamRequestOpenAIPassthrough(context.Background(), c, account, scopedBody, "test-token")
			require.NoError(t, err)
			for _, name := range []string{"User-Agent", "originator", "version", "session_id", "conversation_id", "x-codex-installation-id", "thread-id", "x-client-request-id"} {
				require.Equal(t, native.Header.Get(name), passthrough.Header.Get(name), name)
			}
		})
	}
}

func TestCodexAttemptIdentityConcurrentUsersAccountsAndCacheStability(t *testing.T) {
	accounts := []*Account{
		newTestOAuthAccount(606, map[string]any{codexFingerprintModeExtraKey: "device"}),
		newTestOAuthAccount(607, map[string]any{codexFingerprintModeExtraKey: "device", codexFingerprintSeedExtraKey: "22222222-2222-4222-8222-222222222222"}),
	}
	body := []byte(`{"prompt_cache_key":"cache","client_metadata":{"session_id":"same-client-session"}}`)
	const users = 32
	type observation struct {
		account, user                int
		installation, session, cache string
		err                          error
	}
	results := make(chan observation, len(accounts)*users)
	var workers sync.WaitGroup
	for a, account := range accounts {
		for u := 0; u < users; u++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				identity := (&OpenAIGatewayService{}).resolveCodexAttemptIdentity(account, account, extractCodexIdentityInput(nil, body), int64(u+1), false)
				raw, _, err := identity.applyBodyRaw(body)
				results <- observation{a, u, identity.fingerprint.installationID, gjson.GetBytes(raw, "client_metadata.session_id").String(), gjson.GetBytes(raw, "prompt_cache_key").String(), err}
			}()
		}
	}
	workers.Wait()
	close(results)
	seenSessions, seenKeys := map[string]bool{}, map[string]bool{}
	devices := map[int]string{}
	for result := range results {
		require.NoError(t, result.err)
		require.False(t, seenSessions[result.session], fmt.Sprintf("account=%d user=%d", result.account, result.user))
		require.False(t, seenKeys[result.cache])
		seenSessions[result.session], seenKeys[result.cache] = true, true
		if device := devices[result.account]; device != "" {
			require.Equal(t, device, result.installation)
		}
		devices[result.account] = result.installation
	}
	require.NotEqual(t, devices[0], devices[1])
}

func TestCodexAttemptIdentityCompactAndNonCodexRemainScoped(t *testing.T) {
	account := newTestOAuthAccount(608, map[string]any{codexFingerprintModeExtraKey: "session"})
	svc := &OpenAIGatewayService{}
	identity := svc.resolveCodexAttemptIdentity(account, account, codexIdentityInput{}, 1, true)
	require.Nil(t, identity.fingerprint, "compact must not acquire normal Responses metadata")
	for _, platform := range []string{PlatformGemini, PlatformAnthropic} {
		other := &Account{ID: 609, Platform: platform, Type: AccountTypeOAuth}
		require.Nil(t, svc.resolveCodexAttemptIdentity(other, other, codexIdentityInput{}, 1, false))
	}
}

func TestCodexAttemptIdentityWSTurnSeparatesTurnButPreservesCache(t *testing.T) {
	for _, mode := range []string{"off", "device", "session", "full"} {
		t.Run(mode, func(t *testing.T) {
			account := newTestOAuthAccount(610, map[string]any{codexFingerprintModeExtraKey: mode})
			body := []byte(`{"prompt_cache_key":"custom-cache","client_metadata":{"session_id":"client-session"}}`)
			first := (&OpenAIGatewayService{}).resolveCodexAttemptIdentity(account, account, extractCodexIdentityInput(nil, body), 12, false)
			next := first.forTurn(2)
			require.Same(t, next, next.forTurn(2), "retry is not a new logical turn")
			firstBody, _, err := first.applyBodyRaw(body)
			require.NoError(t, err)
			nextBody, _, err := next.applyBodyRaw(body)
			require.NoError(t, err)
			for _, path := range []string{"prompt_cache_key", "client_metadata.session_id", "client_metadata.thread_id", "client_metadata.x-codex-installation-id"} {
				require.Equal(t, gjson.GetBytes(firstBody, path).String(), gjson.GetBytes(nextBody, path).String(), path)
			}
			if mode == "session" || mode == "full" {
				require.NotEqual(t, first.fingerprint.turnID, next.fingerprint.turnID)
			}
		})
	}
}

func BenchmarkCodexAttemptIdentityRawProjection(b *testing.B) {
	account := newTestOAuthAccount(611, map[string]any{codexFingerprintModeExtraKey: "device"})
	for _, size := range []int{1024, 4 << 20} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			body := []byte(`{"input":"` + strings.Repeat("x", size) + `","prompt_cache_key":"cache","client_metadata":{"session_id":"client-session"}}`)
			identity := (&OpenAIGatewayService{}).resolveCodexAttemptIdentity(account, account, extractCodexIdentityInput(nil, body), 12, false)
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, _, err := identity.applyBodyRaw(body); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
