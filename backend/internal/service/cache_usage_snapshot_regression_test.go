//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

func TestAnthropicHitOnlyPreservesIndependentInput(t *testing.T) {
	for _, test := range []struct {
		name      string
		snapshots []string
		wantReads []int
	}{
		{"hit in initial snapshot", []string{`{"input_tokens":80,"prompt_cache_hit_tokens":20}`, `{"output_tokens":3}`}, []int{20, 20}},
		{"hit arrives later", []string{`{"input_tokens":80}`, `{"prompt_cache_hit_tokens":20}`}, []int{0, 20}},
		{"zero input placeholder", []string{`{"input_tokens":80,"cache_read_input_tokens":20}`, `{"input_tokens":0,"prompt_cache_hit_tokens":20}`}, []int{20, 20}},
		{"hit grows later", []string{`{"input_tokens":80,"cache_read_input_tokens":20}`, `{"prompt_cache_hit_tokens":40}`}, []int{20, 40}},
	} {
		for _, parser := range []string{"typed", "raw", "generic"} {
			t.Run(test.name+"/"+parser, func(t *testing.T) {
				usage := &ClaudeUsage{}
				for index, snapshot := range test.snapshots {
					if parser == "typed" {
						var source apicompat.AnthropicUsage
						require.NoError(t, json.Unmarshal([]byte(snapshot), &source))
						mergeAnthropicUsage(usage, source)
					} else {
						data := `{"type":"message_delta","usage":` + snapshot + `}`
						if index == 0 {
							data = `{"type":"message_start","message":{"usage":` + snapshot + `}}`
						}
						if parser == "raw" {
							parseSSEUsagePassthrough(data, usage)
						} else {
							(&GatewayService{}).parseSSEUsage(data, usage)
						}
					}
					require.Equal(t, 80, usage.InputTokens)
					require.Equal(t, test.wantReads[index], usage.CacheReadInputTokens)
				}
			})
		}
	}
}

func TestAnthropicPromptTotalSnapshots(t *testing.T) {
	for _, test := range []struct {
		name   string
		start  string
		deltas []string
		inputs []int
	}{
		{"prompt total", `"prompt_tokens":100`, []string{`{"cache_creation":{"ephemeral_1h_input_tokens":30}}`, `{"cache_creation":{"ephemeral_1h_input_tokens":30}}`, `{"cache_creation":{"ephemeral_5m_input_tokens":0}}`, `{"cache_read_input_tokens":10}`, `{"prompt_tokens":120}`, `{"output_tokens":2}`}, []int{50, 50, 70, 60, 80, 80}},
		{"independent input", `"input_tokens":80`, []string{`{"cache_creation":{"ephemeral_1h_input_tokens":30}}`, `{"cache_creation":{"ephemeral_1h_input_tokens":30}}`}, []int{80, 80}},
		{"miss overrides total", `"prompt_tokens":100`, []string{`{"prompt_tokens":100,"prompt_cache_miss_tokens":40}`, `{"cache_creation":{"ephemeral_1h_input_tokens":30}}`}, []int{40, 40}},
		{"late read aliases", `"prompt_tokens":100`, []string{`{"cached_tokens":10}`, `{"cached_tokens":15}`, `{"prompt_tokens_details":{"cached_tokens":20}}`, `{"prompt_cache_hit_tokens":25}`, `{"cache_creation":{"ephemeral_1h_input_tokens":30}}`}, []int{70, 65, 60, 55, 25}},
		{"aggregate survives zero placeholder", `"prompt_tokens":100`, []string{`{"cache_creation_input_tokens":50}`, `{"cache_creation_input_tokens":0}`, `{"cache_creation":{"ephemeral_1h_input_tokens":30}}`}, []int{50, 50, 50}},
		{"cache exceeds prompt", `"prompt_tokens":100`, []string{`{"cache_creation":{"ephemeral_1h_input_tokens":100}}`, `{"cache_creation":{"ephemeral_1h_input_tokens":30}}`}, []int{0, 50}},
	} {
		for _, parser := range []string{"typed", "raw", "generic"} {
			t.Run(test.name+"/"+parser, func(t *testing.T) {
				usage := &ClaudeUsage{}
				merge := func(snapshot string, start bool) {
					if parser == "typed" {
						var source apicompat.AnthropicUsage
						require.NoError(t, json.Unmarshal([]byte(snapshot), &source))
						mergeAnthropicUsage(usage, source)
						return
					}
					data := `{"type":"message_delta","usage":` + snapshot + `}`
					if start {
						data = `{"type":"message_start","message":{"usage":` + snapshot + `}}`
					}
					if parser == "raw" {
						parseSSEUsagePassthrough(data, usage)
					} else {
						(&GatewayService{}).parseSSEUsage(data, usage)
					}
				}
				merge(`{`+test.start+`,"cache_creation":{"ephemeral_5m_input_tokens":20}}`, true)
				require.Equal(t, 80, usage.InputTokens)
				for index, delta := range test.deltas {
					merge(delta, false)
					require.Equal(t, test.inputs[index], usage.InputTokens, "delta %d", index)
				}
			})
		}
	}
}

func TestAnthropicPromptTotalCacheReadPresence(t *testing.T) {
	for _, test := range []struct {
		name  string
		delta string
		read  int
	}{
		{"canonical zero", `{"cache_read_input_tokens":0}`, 20},
		{"cached zero", `{"cached_tokens":0}`, 20},
		{"details zero", `{"prompt_tokens_details":{"cached_tokens":0}}`, 20},
		{"hit zero", `{"prompt_cache_hit_tokens":0}`, 20},
		{"missing", `{"output_tokens":2}`, 20},
		{"null", `{"cache_read_input_tokens":null,"cached_tokens":null,"prompt_tokens_details":{"cached_tokens":null},"prompt_cache_hit_tokens":null}`, 20},
		{"canonical precedence", `{"cache_read_input_tokens":12,"cached_tokens":13,"prompt_tokens_details":{"cached_tokens":14},"prompt_cache_hit_tokens":15}`, 12},
		{"zero falls back", `{"cache_read_input_tokens":0,"cached_tokens":13,"prompt_tokens_details":{"cached_tokens":14},"prompt_cache_hit_tokens":15}`, 13},
		{"alias zero falls back", `{"cached_tokens":0,"prompt_tokens_details":{"cached_tokens":14},"prompt_cache_hit_tokens":15}`, 14},
		{"details zero falls back", `{"prompt_tokens_details":{"cached_tokens":0},"prompt_cache_hit_tokens":15}`, 15},
	} {
		for _, parser := range []string{"typed", "raw", "generic"} {
			t.Run(test.name+"/"+parser, func(t *testing.T) {
				usage := &ClaudeUsage{}
				merge := func(snapshot string, start bool) {
					if parser == "typed" {
						var source apicompat.AnthropicUsage
						require.NoError(t, json.Unmarshal([]byte(snapshot), &source))
						mergeAnthropicUsage(usage, source)
						return
					}
					data := `{"type":"message_delta","usage":` + snapshot + `}`
					if start {
						data = `{"type":"message_start","message":{"usage":` + snapshot + `}}`
					}
					if parser == "raw" {
						parseSSEUsagePassthrough(data, usage)
					} else {
						(&GatewayService{}).parseSSEUsage(data, usage)
					}
				}
				merge(`{"prompt_tokens":100,"cache_read_input_tokens":20,"cache_creation_input_tokens":10}`, true)
				require.Equal(t, 70, usage.InputTokens)
				merge(test.delta, false)
				require.Equal(t, test.read, usage.CacheReadInputTokens)
				require.Equal(t, 90-test.read, usage.InputTokens)
				merge(`{"output_tokens":3}`, false)
				require.Equal(t, test.read, usage.CacheReadInputTokens)
				require.Equal(t, 90-test.read, usage.InputTokens)
			})
		}
	}
}

func TestAnthropicCacheReadZeroPreservesIndependentInput(t *testing.T) {
	for _, field := range []string{"cache_read_input_tokens", "cached_tokens"} {
		for _, parser := range []string{"typed", "generic", "raw"} {
			t.Run(field+"/"+parser, func(t *testing.T) {
				usage := &ClaudeUsage{}
				for index, snapshot := range []string{
					`{"input_tokens":80,"cache_read_input_tokens":20,"cache_creation_input_tokens":10}`,
					`{"` + field + `":0}`,
				} {
					if parser == "typed" {
						var source apicompat.AnthropicUsage
						require.NoError(t, json.Unmarshal([]byte(snapshot), &source))
						mergeAnthropicUsage(usage, source)
					} else if parser == "raw" {
						if index == 0 {
							parseSSEUsagePassthrough(`{"type":"message_start","message":{"usage":`+snapshot+`}}`, usage)
						} else {
							parseSSEUsagePassthrough(`{"type":"message_delta","usage":`+snapshot+`}`, usage)
						}
					} else if index == 0 {
						(&GatewayService{}).parseSSEUsage(`{"type":"message_start","message":{"usage":`+snapshot+`}}`, usage)
					} else {
						(&GatewayService{}).parseSSEUsage(`{"type":"message_delta","usage":`+snapshot+`}`, usage)
					}
					require.Equal(t, 80, usage.InputTokens)
				}
				require.Equal(t, 20, usage.CacheReadInputTokens)
				require.Equal(t, 10, usage.CacheCreationInputTokens)
			})
		}
	}
}

func TestAnthropicCacheCreationSnapshots(t *testing.T) {
	tests := []struct {
		name       string
		snapshots  []string
		wantTotals []int
		want5m     int
		want1h     int
		billed5m   int
		billed1h   int
	}{
		{
			name:       "aggregate 100 with 1h 200",
			snapshots:  []string{`{"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_1h_input_tokens":200}}`, `{"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_1h_input_tokens":200}}`},
			wantTotals: []int{100, 100}, want1h: 200, billed1h: 100,
		},
		{
			name:       "contradictory explicit aggregate",
			snapshots:  []string{`{"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":50,"ephemeral_1h_input_tokens":200}}`, `{"cache_creation":{"ephemeral_1h_input_tokens":300}}`},
			wantTotals: []int{100, 100}, want5m: 50, want1h: 300, billed5m: 14, billed1h: 86,
		},
		{
			name:       "partial TTL snapshots derive missing aggregate",
			snapshots:  []string{`{"cache_creation":{"ephemeral_5m_input_tokens":20}}`, `{"cache_creation":{"ephemeral_1h_input_tokens":30}}`, `{"cache_creation":{"ephemeral_5m_input_tokens":0}}`},
			wantTotals: []int{20, 50, 30}, want1h: 30, billed1h: 30,
		},
		{
			name:       "aggregate only delta replaces derived total",
			snapshots:  []string{`{"cache_creation":{"ephemeral_5m_input_tokens":20,"ephemeral_1h_input_tokens":30}}`, `{"cache_creation_input_tokens":40}`, `{"output_tokens":7}`, `{"cache_creation":{"ephemeral_1h_input_tokens":200}}`},
			wantTotals: []int{50, 40, 40, 40}, want5m: 20, want1h: 200, billed5m: 4, billed1h: 36,
		},
		{
			name:       "explicit zero falls back to TTL updates",
			snapshots:  []string{`{"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_1h_input_tokens":200}}`, `{"cache_creation":{"ephemeral_5m_input_tokens":50}}`},
			wantTotals: []int{200, 250}, want5m: 50, want1h: 200, billed5m: 50, billed1h: 200,
		},
		{
			name:       "explicit zero delta preserves aggregate",
			snapshots:  []string{`{"cache_creation_input_tokens":100}`, `{"cache_creation_input_tokens":0}`, `{"cache_creation":{"ephemeral_1h_input_tokens":200}}`},
			wantTotals: []int{100, 100, 100}, want1h: 200, billed1h: 100,
		},
		{
			name:       "repeated cumulative snapshots do not double count",
			snapshots:  []string{`{"cache_creation":{"ephemeral_5m_input_tokens":20,"ephemeral_1h_input_tokens":30}}`, `{"cache_creation":{"ephemeral_5m_input_tokens":20,"ephemeral_1h_input_tokens":30}}`, `{"cache_creation":{"ephemeral_1h_input_tokens":30}}`},
			wantTotals: []int{50, 50, 50}, want5m: 20, want1h: 30, billed5m: 20, billed1h: 30,
		},
		{
			name:       "null aggregate derives merged partial TTL",
			snapshots:  []string{`{"cache_creation_input_tokens":null,"cache_creation":{"ephemeral_5m_input_tokens":20}}`, `{"cache_creation_input_tokens":null,"cache_creation":{"ephemeral_5m_input_tokens":null,"ephemeral_1h_input_tokens":30}}`, `{"cache_creation":{"ephemeral_5m_input_tokens":null,"ephemeral_1h_input_tokens":null}}`},
			wantTotals: []int{20, 50, 50}, want5m: 20, want1h: 30, billed5m: 20, billed1h: 30,
		},
		{
			name:       "null aggregate does not clear explicit aggregate",
			snapshots:  []string{`{"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_1h_input_tokens":200}}`, `{"cache_creation_input_tokens":null,"cache_creation":{"ephemeral_1h_input_tokens":null}}`},
			wantTotals: []int{100, 100}, want1h: 200, billed1h: 100,
		},
		{
			name:       "null does not clear explicit zero",
			snapshots:  []string{`{"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_1h_input_tokens":200}}`, `{"cache_creation_input_tokens":null,"cache_creation":{"ephemeral_5m_input_tokens":20,"ephemeral_1h_input_tokens":null}}`},
			wantTotals: []int{200, 220}, want5m: 20, want1h: 200, billed5m: 20, billed1h: 200,
		},
		{
			name:       "zero to positive retains partial TTL history",
			snapshots:  []string{`{"cache_creation":{"ephemeral_5m_input_tokens":20}}`, `{"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_1h_input_tokens":30}}`, `{"cache_creation":{"ephemeral_1h_input_tokens":50}}`, `{"cache_creation_input_tokens":35}`},
			wantTotals: []int{20, 50, 70, 35}, want5m: 20, want1h: 50, billed5m: 10, billed1h: 25,
		},
		{
			name:       "all null is absent before partial TTL",
			snapshots:  []string{`{"cache_creation_input_tokens":null,"cache_creation":{"ephemeral_5m_input_tokens":null,"ephemeral_1h_input_tokens":null}}`, `{"cache_creation":{"ephemeral_5m_input_tokens":20}}`, `{"cache_creation":{"ephemeral_1h_input_tokens":30}}`},
			wantTotals: []int{0, 20, 50}, want5m: 20, want1h: 30, billed5m: 20, billed1h: 30,
		},
	}
	for _, test := range tests {
		for _, parser := range []string{"shared merge", "raw passthrough", "generic SSE"} {
			t.Run(test.name+"/"+parser, func(t *testing.T) {
				usage := &ClaudeUsage{}
				for index, snapshot := range test.snapshots {
					if parser == "shared merge" {
						var source apicompat.AnthropicUsage
						require.NoError(t, json.Unmarshal([]byte(snapshot), &source))
						mergeAnthropicUsage(usage, source)
					} else if parser == "generic SSE" {
						if index == 0 {
							(&GatewayService{}).parseSSEUsage(`{"type":"message_start","message":{"usage":`+snapshot+`}}`, usage)
						} else {
							(&GatewayService{}).parseSSEUsage(`{"type":"message_delta","usage":`+snapshot+`}`, usage)
						}
					} else if index == 0 {
						parseSSEUsagePassthrough(`{"type":"message_start","message":{"usage":`+snapshot+`}}`, usage)
					} else {
						parseSSEUsagePassthrough(`{"type":"message_delta","usage":`+snapshot+`}`, usage)
					}
					require.Equal(t, test.wantTotals[index], usage.CacheCreationInputTokens)
				}
				require.Equal(t, test.want5m, usage.CacheCreation5mTokens)
				require.Equal(t, test.want1h, usage.CacheCreation1hTokens)
				cache5m, cache1h := normalizeCacheCreationBreakdown(usage.billingUsageTokens())
				require.Equal(t, test.billed5m, cache5m)
				require.Equal(t, test.billed1h, cache1h)
				cost := (&BillingService{}).computeCacheCreationCost(&ModelPricing{
					SupportsCacheBreakdown: true, CacheCreation5mPrice: 0.002, CacheCreation1hPrice: 0.004,
				}, usage.billingUsageTokens(), 0.002, 1)
				require.InDelta(t, float64(test.billed5m)*0.002+float64(test.billed1h)*0.004, cost, 1e-12)
			})
		}
	}
}

func TestApplyCacheTTLOverrideAuthoritativeTotal(t *testing.T) {
	for _, test := range []struct {
		name    string
		usage   ClaudeUsage
		total   int
		changed bool
	}{
		{"partial details", ClaudeUsage{CacheCreationInputTokens: 11000, CacheCreation1hTokens: 3000, cacheCreationInputTokensPresent: true}, 11000, true},
		{"oversized details", ClaudeUsage{CacheCreationInputTokens: 100, CacheCreation5mTokens: 50, CacheCreation1hTokens: 200, cacheCreationInputTokensPresent: true}, 100, true},
		{"explicit zero", ClaudeUsage{CacheCreation5mTokens: 50, CacheCreation1hTokens: 200, cacheCreationInputTokensPresent: true}, 250, true},
		{"synthetic positive aggregate", ClaudeUsage{CacheCreationInputTokens: 11000, CacheCreation1hTokens: 3000}, 11000, true},
		{"synthetic detail only", ClaudeUsage{CacheCreation5mTokens: 50, CacheCreation1hTokens: 200}, 250, true},
		{"synthetic negative detail", ClaudeUsage{CacheCreation5mTokens: -50, CacheCreation1hTokens: 200}, 200, true},
		{"aggregate only", ClaudeUsage{CacheCreationInputTokens: 11000}, 11000, true},
		{"empty", ClaudeUsage{}, 0, false},
	} {
		for _, target := range []string{"5m", "1h"} {
			t.Run(test.name+"/"+target, func(t *testing.T) {
				usage := test.usage
				require.Equal(t, test.changed, applyCacheTTLOverride(&usage, target))
				want5m, want1h := 0, test.total
				if target == "5m" {
					want5m, want1h = test.total, 0
				}
				require.Equal(t, want5m, usage.CacheCreation5mTokens)
				require.Equal(t, want1h, usage.CacheCreation1hTokens)
				require.Equal(t, test.usage.CacheCreationInputTokens, usage.CacheCreationInputTokens)
				require.Equal(t, test.usage.cacheCreationInputTokensPresent, usage.cacheCreationInputTokensPresent)
				snapshot := usage
				require.False(t, applyCacheTTLOverride(&usage, target))
				require.Equal(t, snapshot, usage)
			})
		}
	}
}

func TestRewriteCacheCreationJSONPreservesAbsentDetails(t *testing.T) {
	for _, payload := range []string{
		`{"cache_creation_input_tokens":100}`,
		`{"cache_creation_input_tokens":100,"cache_creation":null}`,
		`{"cache_creation_input_tokens":0}`,
	} {
		for _, target := range []string{"5m", "1h"} {
			var usage map[string]any
			require.NoError(t, json.Unmarshal([]byte(payload), &usage))
			for range 2 {
				require.False(t, rewriteCacheCreationJSON(usage, target))
				encoded, err := json.Marshal(usage)
				require.NoError(t, err)
				require.JSONEq(t, payload, string(encoded))
			}
		}
	}
}
