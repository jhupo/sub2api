//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCacheUsageForwardSnapshotConsistency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name                              string
		start                             string
		delta                             string
		input, creation, cache5m, cache1h int
		cost                              float64
	}{
		{"prompt total late TTL", `"input_tokens":100,"prompt_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":20}`, `{"cache_creation":{"ephemeral_1h_input_tokens":30}}`, 50, 50, 20, 30, 0.16},
		{"independent input late TTL", `"input_tokens":80,"cache_creation":{"ephemeral_5m_input_tokens":20}`, `{"cache_creation":{"ephemeral_1h_input_tokens":30}}`, 80, 50, 20, 30, 0.16},
		{"aggregate grows beyond partial TTL", `"input_tokens":10,"cache_creation_input_tokens":3000,"cache_creation":{"ephemeral_1h_input_tokens":3000}`, `{"cache_creation_input_tokens":11000}`, 10, 11000, 0, 3000, 28},
	} {
		for _, route := range []string{"messages", "passthrough", "responses", "buffered responses", "chat", "buffered chat", "native responses", "buffered native responses", "native chat", "buffered native chat"} {
			t.Run(test.name+"/"+route, func(t *testing.T) {
				payload := "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_cache","type":"message","role":"assistant","model":"claude-sonnet-4","content":[],"usage":{` + test.start + `}}}` + "\n\n"
				for range 2 {
					payload += "event: message_delta\ndata: " + `{"type":"message_delta","usage":` + test.delta + `}` + "\n\n"
				}
				payload += "event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}` + "\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(payload))}
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				svc := &GatewayService{cfg: &config.Config{}, rateLimitService: &RateLimitService{}}
				native := &OpenAIGatewayService{cfg: &config.Config{}}
				var result *ForwardResult
				var nativeResult *OpenAIForwardResult
				var err error
				switch route {
				case "messages", "passthrough":
					var streamed *streamingResult
					if route == "messages" {
						streamed, err = svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 701}, time.Now(), "claude-sonnet-4", "claude-sonnet-4", false)
					} else {
						streamed, err = svc.handleStreamingResponseAnthropicAPIKeyPassthrough(context.Background(), resp, c, &Account{ID: 701}, time.Now(), "claude-sonnet-4")
					}
					require.NotNil(t, streamed)
					result = &ForwardResult{Usage: *streamed.usage}
				case "responses":
					result, err = svc.handleResponsesStreamingResponse(resp, c, "claude-sonnet-4", "claude-sonnet-4", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
				case "buffered responses":
					result, err = svc.handleResponsesBufferedStreamingResponse(resp, c, "claude-sonnet-4", "claude-sonnet-4", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
				case "chat":
					result, err = svc.handleCCStreamingFromAnthropic(resp, c, "claude-sonnet-4", "claude-sonnet-4", nil, time.Now(), true)
				case "buffered chat":
					result, err = svc.handleCCBufferedFromAnthropic(resp, c, "claude-sonnet-4", "claude-sonnet-4", nil, time.Now())
				case "native responses":
					nativeResult, err = native.handleResponsesStreamingFromNativeAnthropic(resp, c, "claude-sonnet-4", "claude-sonnet-4", "claude-sonnet-4", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
				case "buffered native responses":
					nativeResult, err = native.handleResponsesBufferedFromNativeAnthropic(resp, c, "claude-sonnet-4", "claude-sonnet-4", "claude-sonnet-4", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
				case "native chat":
					nativeResult, err = native.handleCCStreamingFromNativeAnthropic(resp, c, "claude-sonnet-4", "claude-sonnet-4", "claude-sonnet-4", nil, time.Now(), true)
				case "buffered native chat":
					nativeResult, err = native.handleCCBufferedFromNativeAnthropic(resp, c, "claude-sonnet-4", "claude-sonnet-4", "claude-sonnet-4", nil, time.Now())
				}
				require.NoError(t, err)
				var tokens UsageTokens
				if nativeResult != nil {
					tokens = nativeResult.Usage.billingUsageTokens()
				} else {
					require.NotNil(t, result)
					tokens = result.Usage.billingUsageTokens()
				}
				require.Equal(t, test.input, tokens.InputTokens)
				require.Equal(t, 2, tokens.OutputTokens)
				require.Equal(t, test.creation, tokens.CacheCreationTokens)
				require.Equal(t, test.cache5m, tokens.CacheCreation5mTokens)
				require.Equal(t, test.cache1h, tokens.CacheCreation1hTokens)
				price := &ModelPricing{SupportsCacheBreakdown: true, CacheCreation5mPrice: 0.002, CacheCreation1hPrice: 0.004}
				require.InDelta(t, test.cost, (&BillingService{}).computeCacheCreationCost(price, tokens, 0.002, 1), 1e-12)
				if route == "messages" || route == "passthrough" {
					return
				}
				var clientUsage gjson.Result
				if strings.HasPrefix(route, "buffered") {
					clientUsage = gjson.GetBytes(recorder.Body.Bytes(), "usage")
				} else {
					count := 0
					for _, line := range strings.Split(recorder.Body.String(), "\n") {
						data, ok := extractOpenAISSEDataLine(line)
						if !ok {
							continue
						}
						if gjson.Get(data, "type").String() == "response.completed" {
							clientUsage = gjson.Get(data, "response.usage")
							count++
						} else if gjson.Get(data, "usage").Exists() {
							clientUsage = gjson.Get(data, "usage")
							count++
						}
					}
					require.Equal(t, 1, count)
				}
				require.True(t, clientUsage.Exists())
				inputPath, creationPath := "input_tokens", "cache_creation_input_tokens"
				if strings.HasSuffix(route, "chat") {
					inputPath, creationPath = "prompt_tokens", "prompt_tokens_details.cache_creation_tokens"
				}
				require.EqualValues(t, test.input+test.creation, clientUsage.Get(inputPath).Int())
				require.EqualValues(t, test.creation, clientUsage.Get(creationPath).Int())
			})
		}
	}
}

func TestCacheCreationPartialDetailsBillingAndStats(t *testing.T) {
	tokens := UsageTokens{CacheCreationTokens: 11000, CacheCreation1hTokens: 3000}
	cache5m, cache1h := allocateCacheCreationBillingTokens(tokens)
	require.Equal(t, 8000, cache5m)
	require.Equal(t, 3000, cache1h)
	price5m, price1h := 0.002, 0.004
	stats := calculateStatsCost(&ChannelModelPricing{BillingMode: BillingModeToken, CacheWritePrice: &price5m, CacheWrite1hPrice: &price1h}, tokens, 1)
	require.NotNil(t, stats)
	require.InDelta(t, 28, *stats, 1e-12)
}

func TestRecordUsageAccountStatsPreservesCacheCreationTTL(t *testing.T) {
	const groupID = int64(701)
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	svc.cfg.RunMode = config.RunModeSimple
	price5m, price1h := 0.002, 0.004
	svc.channelService = newTestChannelServiceForStats(t, &Channel{
		ID: 1, Status: StatusActive,
		AccountStatsPricingRules: []AccountStatsPricingRule{{
			GroupIDs: []int64{groupID},
			Pricing: []ChannelModelPricing{{
				Models: []string{"claude-sonnet-4"}, BillingMode: BillingModeToken,
				CacheWritePrice: &price5m, CacheWrite1hPrice: &price1h,
			}},
		}},
	}, groupID, PlatformAnthropic)
	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "cache-ttl-account-stats", Model: "claude-sonnet-4", UpstreamModel: "claude-sonnet-4",
			Usage: ClaudeUsage{CacheCreationInputTokens: 11000, CacheCreation1hTokens: 3000},
		},
		APIKey: &APIKey{ID: 1, GroupID: i64p(groupID), Group: &Group{ID: groupID, Platform: PlatformAnthropic, RateMultiplier: 1}},
		User:   &User{ID: 1}, Account: &Account{ID: 1, Platform: PlatformAnthropic},
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.NotNil(t, usageRepo.lastLog.AccountStatsCost)
	require.InDelta(t, 28, *usageRepo.lastLog.AccountStatsCost, 1e-12)
}

func TestNonStreamingAnthropicCacheReadAliasPreservesIndependentInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"messages", "passthrough"} {
		t.Run(route, func(t *testing.T) {
			body := `{"id":"msg_cache","type":"message","usage":{"input_tokens":80,"output_tokens":2,"prompt_tokens_details":{"cached_tokens":20}}}`
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			svc := &GatewayService{cfg: &config.Config{}, rateLimitService: &RateLimitService{}}
			var usage *ClaudeUsage
			var err error
			if route == "messages" {
				usage, err = svc.handleNonStreamingResponse(context.Background(), resp, c, &Account{ID: 701}, "claude-sonnet-4", "claude-sonnet-4")
			} else {
				usage, err = svc.handleNonStreamingResponseAnthropicAPIKeyPassthrough(context.Background(), resp, c, &Account{ID: 701})
			}
			require.NoError(t, err)
			require.NotNil(t, usage)
			require.Equal(t, 80, usage.InputTokens)
			require.Equal(t, 20, usage.CacheReadInputTokens)
			require.Equal(t, 2, usage.OutputTokens)
			require.JSONEq(t, body, recorder.Body.String())
		})
	}
}
