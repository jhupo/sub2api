package service

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexForkRetainsParentsScopedPromptCacheKey(t *testing.T) {
	account := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"chatgpt_account_id": "fork-cache-account"}}
	parentHeaders := http.Header{"Session-Id": []string{"parent-session"}}
	forkBody := []byte(`{"prompt_cache_key":"parent-session","client_metadata":{"session_id":"child-session"},"metadata":{"large":9007199254740993},"input":[{"role":"user","content":"continue"}]}`)
	input := extractCodexIdentityInput(parentHeaders, forkBody)
	require.Equal(t, "parent-session", input.clientSession)
	identity := &codexAttemptIdentity{clientSession: input.clientSession, scope: newCodexAccountIdentityScope(account, 77)}
	parent := map[string]any{"prompt_cache_key": "parent-session", "client_metadata": map[string]any{"session_id": "parent-session"}}
	require.True(t, identity.applyBody(parent))
	fork, changed, err := identity.applyBodyRaw(forkBody)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, parent["prompt_cache_key"], gjson.GetBytes(fork, "prompt_cache_key").String())
	require.NotEqual(t, gjson.GetBytes(fork, "prompt_cache_key").String(), gjson.GetBytes(fork, "client_metadata.session_id").String())
	require.Equal(t, "9007199254740993", gjson.GetBytes(fork, "metadata.large").Raw)
	require.JSONEq(t, gjson.GetBytes(forkBody, "input").Raw, gjson.GetBytes(fork, "input").Raw)
	identity.applyHeaders(parentHeaders)
	require.Equal(t, parent["prompt_cache_key"], parentHeaders.Get("session-id"))
	require.Equal(t, "parent-session", identity.clientSession)
	// Same-attempt retries project the original ingress body, not the scoped output.
	again, changed, err := identity.applyBodyRaw(forkBody)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, fork, again)
}

func TestCodexForkCacheStillIsolatesAccountsAndAPIKeys(t *testing.T) {
	account := func(id int64, credential string) *Account {
		return &Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
			Credentials: map[string]any{"chatgpt_account_id": credential}}
	}
	var keys []string
	for _, tc := range []struct {
		account *Account
		keyID   int64
	}{
		{account(11, "account-one"), 77},
		{account(12, "account-two"), 77},
		{account(11, "account-one"), 78},
	} {
		body := map[string]any{"prompt_cache_key": "parent", "client_metadata": map[string]any{"session_id": "child"}}
		identity := &codexAttemptIdentity{clientSession: "parent", scope: newCodexAccountIdentityScope(tc.account, tc.keyID)}
		require.True(t, identity.applyBody(body))
		key, ok := body["prompt_cache_key"].(string)
		require.True(t, ok)
		keys = append(keys, key)
	}
	require.NotEqual(t, keys[0], keys[1])
	require.NotEqual(t, keys[0], keys[2])
}

func TestCodexForkPreservesCustomPromptCacheNamespace(t *testing.T) {
	account := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"chatgpt_account_id": "fork-cache-account"}}
	for _, session := range []string{"parent", "different-parent"} {
		identity := &codexAttemptIdentity{clientSession: session, scope: newCodexAccountIdentityScope(account, 77)}
		body := map[string]any{"prompt_cache_key": "custom-key", "client_metadata": map[string]any{"session_id": "child"}}
		require.True(t, identity.applyBody(body))
		require.Equal(t, identity.scope.value("prompt-cache", "custom-key"), body["prompt_cache_key"])
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), `"parent"`)
	}
}
