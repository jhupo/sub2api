package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type responseAffinityProbeKey struct{}

type responseAffinityProbeCache struct {
	stubGatewayCache
	errors    []error
	deadlines []time.Time
	values    []any
}

func (p *responseAffinityProbeCache) SetSessionAccountID(ctx context.Context, groupID int64, key string, accountID int64, ttl time.Duration) error {
	p.errors = append(p.errors, ctx.Err())
	deadline, _ := ctx.Deadline()
	p.deadlines = append(p.deadlines, deadline)
	p.values = append(p.values, ctx.Value(responseAffinityProbeKey{}))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return p.stubGatewayCache.SetSessionAccountID(ctx, groupID, key, accountID, ttl)
}

func TestBindHTTPResponseAccountSurvivesCancellationWithBoundedContext(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	groupID := int64(4201)
	c.Set("api_key", &APIKey{ID: 501, GroupID: &groupID})
	SetOpenAIHTTPResponseOwner(c, 601, 501)
	cache := &responseAffinityProbeCache{}
	svc := &OpenAIGatewayService{cache: cache}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), responseAffinityProbeKey{}, "request-value"))
	cancel()
	started := time.Now()
	svc.bindHTTPResponseAccount(ctx, c, &Account{ID: 701}, "resp_after_cancel")
	require.Len(t, cache.errors, 3)
	for i, err := range cache.errors {
		require.NoError(t, err)
		require.Equal(t, "request-value", cache.values[i])
		require.True(t, cache.deadlines[i].After(started))
		require.LessOrEqual(t, cache.deadlines[i].Sub(started), openAIWSStateStoreRedisTimeout+100*time.Millisecond)
		require.Equal(t, cache.deadlines[0], cache.deadlines[i])
	}
	owned, err := svc.ValidateOpenAIHTTPResponseOwner(context.Background(), groupID, "resp_after_cancel", 601, 501)
	require.NoError(t, err)
	require.True(t, owned)
	owned, err = svc.ValidateOpenAIHTTPResponseOwner(context.Background(), groupID, "resp_after_cancel", 602, 501)
	require.NoError(t, err)
	require.False(t, owned)
}

func TestFirstOutputSpoolCreateFailureKeepsBoundedMemory(t *testing.T) {
	for _, writeString := range []bool{false, true} {
		t.Run(fmt.Sprint(writeString), func(t *testing.T) {
			stage := newOpenAIFirstOutputStage(70 * 1024)
			stage.memoryOnly = false
			t.Cleanup(func() { require.NoError(t, stage.Close()) })
			calls := 0
			stage.createTemp = func() (*os.File, error) {
				calls++
				return nil, os.ErrPermission
			}
			stage.removeFile = func(string) error {
				t.Fatal("failed creation must not leave a spool")
				return nil
			}
			write := func(value string) (int, error) {
				if writeString {
					return stage.WriteString(value)
				}
				return stage.Write([]byte(value))
			}
			payload := ""
			for _, chunk := range []string{strings.Repeat("a", 32*1024), strings.Repeat("b", 36*1024), strings.Repeat("c", 2*1024)} {
				n, err := write(chunk)
				require.NoError(t, err)
				require.Equal(t, len(chunk), n)
				payload += chunk
			}
			require.Equal(t, 1, calls)
			n, err := write("overflow")
			require.Zero(t, n)
			require.ErrorIs(t, err, errOpenAIFirstOutputStageLimit)
			var downstream bytes.Buffer
			require.NoError(t, stage.CommitTo(&downstream))
			require.Equal(t, payload, downstream.String())
			require.Zero(t, stage.Buffered())
		})
	}
}

func TestResponsesOversizedItemIDsPreserveRealCallPair(t *testing.T) {
	validID := "ws_" + strings.Repeat("x", 61)
	oversizedID := validID + "x"
	callID := "call_" + strings.Repeat("y", 100)
	body := []byte(`{"input":[{"type":"web_search_call","id":"` + validID + `"},{"type":"web_search_call","id":"` + oversizedID + `"},{"type":"function_call","call_id":"` + callID + `","name":"read_file","arguments":"{}"},{"type":"function_call_output","call_id":"` + callID + `","output":"result"}]}`)
	got, changed, err := sanitizeOpenAIResponsesInputItemIDs(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, validID, gjson.GetBytes(got, "input.0.id").String())
	require.False(t, gjson.GetBytes(got, "input.1.id").Exists())
	require.Equal(t, callID, gjson.GetBytes(got, "input.2.call_id").String())
	require.Equal(t, callID, gjson.GetBytes(got, "input.3.call_id").String())
}

func TestDeepSeekCacheReadFallbackPreservesExplicitNestedZero(t *testing.T) {
	for _, tc := range []struct {
		usage string
		want  int
	}{
		{`{"prompt_tokens":100,"completion_tokens":5,"prompt_cache_hit_tokens":80,"prompt_cache_miss_tokens":20}`, 80},
		{`{"prompt_tokens":100,"completion_tokens":5,"prompt_cache_hit_tokens":80,"prompt_tokens_details":{"cached_tokens":64}}`, 64},
		{`{"prompt_tokens":100,"completion_tokens":5,"prompt_cache_hit_tokens":80,"prompt_tokens_details":{"cached_tokens":0}}`, 0},
		{`{"prompt_tokens":100,"completion_tokens":5,"prompt_cache_hit_tokens":80,"input_tokens_details":{"cached_tokens":0}}`, 0},
		{`{"prompt_tokens":100,"completion_tokens":5,"prompt_cache_hit_tokens":80,"cached_tokens":30}`, 30},
	} {
		usage, ok := openAIUsageFromGJSON(gjson.Parse(tc.usage))
		require.True(t, ok)
		require.Equal(t, 100, usage.InputTokens)
		require.Equal(t, tc.want, usage.CacheReadInputTokens)
	}
}

func TestAlphaSearchForbiddenDistinguishesCapabilityAndAccountState(t *testing.T) {
	for _, pat := range []bool{false, true} {
		for _, accessState := range []bool{false, true} {
			t.Run(fmt.Sprintf("pat=%t/account_state=%t", pat, accessState), func(t *testing.T) {
				body := []byte(`{"id":"search-session","model":"gpt-6.2-astra","commands":{"literature_search_query":[{"q":"protein folding"}]}}`)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/alpha/search", bytes.NewReader(body))
				errorBody := `{"detail":"Literature search is unavailable."}`
				if accessState {
					errorBody = `{"error":{"code":"deactivated_workspace","message":"Workspace is deactivated"}}`
				}
				upstream := &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusForbidden,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(errorBody)),
				}}
				repo := &openAIAuthPolicyAccountRepo{}
				cfg := &config.Config{}
				limits := NewRateLimitService(repo, nil, cfg, nil, nil)
				counter := &openAIAuthPolicy403Counter{counts: []int64{3}}
				limits.openAI403CounterCache = counter
				svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, accountRepo: repo, rateLimitService: limits}
				credentials := map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-account"}
				if pat {
					credentials["access_token"] = "at-test-token"
					credentials["auth_mode"] = OpenAIAuthModePersonalAccessToken
				}
				result, err := svc.ForwardAlphaSearch(context.Background(), c, &Account{
					ID: 45, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: credentials,
				}, body)
				require.Nil(t, result)
				var failover *UpstreamFailoverError
				require.ErrorAs(t, err, &failover)
				require.Equal(t, http.StatusForbidden, failover.StatusCode)
				require.Zero(t, repo.tempCalls)
				if accessState {
					require.Equal(t, 1, repo.setErrorCalls)
				} else {
					require.Zero(t, repo.setErrorCalls)
					require.Len(t, counter.counts, 1, "tool denial must not increment the account counter")
				}
				require.False(t, c.Writer.Written())
			})
		}
	}
}
