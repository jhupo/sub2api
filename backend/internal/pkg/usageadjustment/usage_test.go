package usageadjustment

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestRewriteUsageProtocols(t *testing.T) {
	tests := []struct {
		name string
		body string
		want map[string]int64
	}{
		{"anthropic", `{"usage":{"input_tokens":1000,"output_tokens":200,"cache_read_input_tokens":8000,"cache_creation_input_tokens":90}}`, map[string]int64{"usage.input_tokens": 1800, "usage.cache_read_input_tokens": 7200, "usage.output_tokens": 200, "usage.cache_creation_input_tokens": 90}},
		{"responses", `{"response":{"usage":{"input_tokens":9000,"output_tokens":200,"total_tokens":9200,"input_tokens_details":{"cached_tokens":8000}}}}`, map[string]int64{"response.usage.input_tokens": 9000, "response.usage.input_tokens_details.cached_tokens": 7200, "response.usage.total_tokens": 9200}},
		{"responses-alias", `{"object":"response","usage":{"input_tokens":9000,"cache_read_input_tokens":8000,"cached_tokens":8000}}`, map[string]int64{"usage.input_tokens": 9000, "usage.cache_read_input_tokens": 7200, "usage.cached_tokens": 7200}},
		{"chat", `{"usage":{"prompt_tokens":9000,"completion_tokens":200,"prompt_tokens_details":{"cached_tokens":8000}}}`, map[string]int64{"usage.prompt_tokens": 9000, "usage.prompt_tokens_details.cached_tokens": 7200, "usage.completion_tokens": 200}},
		{"deepseek", `{"usage":{"prompt_tokens":9000,"prompt_cache_hit_tokens":8000,"prompt_cache_miss_tokens":1000,"prompt_tokens_details":{"cached_tokens":8000}}}`, map[string]int64{"usage.prompt_tokens": 9000, "usage.prompt_cache_hit_tokens": 7200, "usage.prompt_cache_miss_tokens": 1800, "usage.prompt_tokens_details.cached_tokens": 7200}},
		{"gemini", `{"usageMetadata":{"promptTokenCount":9000,"cachedContentTokenCount":8000,"candidatesTokenCount":200,"totalTokenCount":9200}}`, map[string]int64{"usageMetadata.promptTokenCount": 9000, "usageMetadata.cachedContentTokenCount": 7200, "usageMetadata.totalTokenCount": 9200}},
		{"antigravity", `{"response":{"usageMetadata":{"promptTokenCount":9000,"cachedContentTokenCount":8000}}}`, map[string]int64{"response.usageMetadata.cachedContentTokenCount": 7200, "response.usageMetadata.promptTokenCount": 9000}},
		{"array", `[{"usageMetadata":{"promptTokenCount":9000,"cachedContentTokenCount":8000}}]`, map[string]int64{"0.usageMetadata.cachedContentTokenCount": 7200}},
		{"kimi-alias", `{"type":"message","usage":{"input_tokens":1000,"cache_read_input_tokens":0,"cached_tokens":8000}}`, map[string]int64{"usage.input_tokens": 1800, "usage.cached_tokens": 7200}},
		{"gemini-audio", `{"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":18,"cacheTokensDetails":[{"modality":"AUDIO","tokenCount":9},{"modality":"TEXT","tokenCount":9}]}}`, map[string]int64{"usageMetadata.promptTokenCount": 100, "usageMetadata.cachedContentTokenCount": 17, "usageMetadata.cacheTokensDetails.0.tokenCount": 8, "usageMetadata.cacheTokensDetails.1.tokenCount": 9}},
		{"openai-audio", `{"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":18,"cached_tokens_details":{"text_tokens":9,"audio_tokens":9}}}}`, map[string]int64{"usage.input_tokens": 100, "usage.input_tokens_details.cached_tokens": 17, "usage.input_tokens_details.cached_tokens_details.text_tokens": 8, "usage.input_tokens_details.cached_tokens_details.audio_tokens": 9}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Rewriter{Percent: 10}
			got := r.Rewrite([]byte(tt.body))
			for path, want := range tt.want {
				if n := gjson.GetBytes(got, path).Int(); n != want {
					t.Errorf("%s = %d, want %d; body=%s", path, n, want, got)
				}
			}
		})
	}
}

func TestRewriteNoOpAndRounding(t *testing.T) {
	for _, body := range []string{
		`{"usage": {"input_tokens":1000,"cache_read_input_tokens":0}}`,
		`{"usage":{"input_tokens":1000}}`,
		`{"usage":{"cache_read_input_tokens":8000}}`,
		`{"usage":{"input_tokens":1000,"cache_read_input_tokens":-1}}`,
		`{"usage":{"input_tokens":1000,"cache_read_input_tokens":"8000"}}`,
		`{"content":[{"usage":{"input_tokens":1000,"cache_read_input_tokens":8000}}]}`,
		`{"usage":`,
	} {
		r := Rewriter{Percent: 10}
		if got := string(r.Rewrite([]byte(body))); got != body {
			t.Errorf("unexpected mutation: %s", got)
		}
	}
	for _, percent := range []int{-1, 0, 101} {
		r := Rewriter{Percent: percent}
		body := `{"usage":{"input_tokens":1000,"cache_read_input_tokens":8000}}`
		if got := string(r.Rewrite([]byte(body))); got != body {
			t.Errorf("invalid/disabled percent mutated body: %d", percent)
		}
	}
	for _, tt := range []struct {
		percent int
		cached  int64
		want    int64
	}{{10, 9, 9}, {10, 19, 18}, {100, 19, 0}, {33, 100, 67}, {100, 9223372036854775807, 0}} {
		r := Rewriter{Percent: tt.percent}
		if got := tt.cached - r.shift(tt.cached); got != tt.want {
			t.Errorf("rounding %d%% of %d: got %d want %d", tt.percent, tt.cached, got, tt.want)
		}
	}
}

func TestAnthropicCumulativeUsageDoesNotCompound(t *testing.T) {
	r := Rewriter{Percent: 10}
	start := []byte(`{"type":"message_start","message":{"usage":{"input_tokens":1000,"cache_read_input_tokens":8000}}}`)
	delta := []byte(`{"type":"message_delta","usage":{"output_tokens":200,"cache_read_input_tokens":8000}}`)
	inputDelta := []byte(`{"type":"message_delta","usage":{"input_tokens":1000,"output_tokens":200}}`)
	for _, body := range [][]byte{start, delta, delta, inputDelta} {
		got := r.Rewrite(body)
		path := "usage"
		if gjson.GetBytes(got, "type").String() == "message_start" {
			path = "message.usage"
		}
		if gjson.GetBytes(got, path+".input_tokens").Int() != 1800 || gjson.GetBytes(got, path+".cache_read_input_tokens").Int() != 7200 {
			t.Fatalf("unexpected usage: %s", got)
		}
	}
}

func TestRewriteResponsesCacheAttributionConservesTotals(t *testing.T) {
	const body = `{"type":"response.completed","response":{"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":19,"cache_write_tokens":80},"attribution":{"items":{"msg.1":{"input_tokens":60,"cached_tokens":9,"cache_write_tokens":50,"content":[{"cached_tokens":4,"cache_write_tokens":20},{"cached_tokens":5,"cache_write_tokens":30}]}},"request_fields":{"instructions":{"input_tokens":40,"cached_tokens":10,"cache_write_tokens":30}}}}}}`
	for _, percent := range []int{10, 25, 100} {
		r := Rewriter{Percent: percent}
		got := r.Rewrite([]byte(body))
		u := gjson.GetBytes(got, "response.usage")
		item := u.Get(`attribution.items.msg\.1`)
		instructions := u.Get("attribution.request_fields.instructions")
		cached := u.Get("input_tokens_details.cached_tokens").Int()
		if cached != 19-int64(19*percent/100) || cached != item.Get("cached_tokens").Int()+instructions.Get("cached_tokens").Int() {
			t.Fatalf("percent=%d inconsistent attribution: %s", percent, got)
		}
		if item.Get("cached_tokens").Int() != item.Get("content.0.cached_tokens").Int()+item.Get("content.1.cached_tokens").Int() {
			t.Fatalf("inconsistent content attribution: %s", got)
		}
		if item.Get("cache_write_tokens").Int() != 50 || item.Get("content.0.cache_write_tokens").Int() != 20 || instructions.Get("cache_write_tokens").Int() != 30 || u.Get("input_tokens_details.cache_write_tokens").Int() != 80 || u.Get("input_tokens").Int() != 100 {
			t.Fatalf("creation or total input changed: %s", got)
		}
	}
}
