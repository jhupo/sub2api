package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponsesUsageNestedCachePresenceSurvivesConversion(t *testing.T) {
	for _, test := range []struct {
		name     string
		details  string
		creation int
	}{
		{"missing write falls back", `"input_tokens_details":{"cache_creation_tokens":4}`, 4},
		{"null write suppresses creation", `"input_tokens_details":{"cache_write_tokens":null,"cache_creation_tokens":4}`, 0},
		{"zero write suppresses creation", `"input_tokens_details":{"cache_write_tokens":0,"cache_creation_tokens":4}`, 0},
		{"negative write suppresses creation", `"input_tokens_details":{"cache_write_tokens":-2,"cache_creation_tokens":4}`, 0},
		{"positive write wins", `"input_tokens_details":{"cache_write_tokens":2,"cache_creation_tokens":4}`, 2},
		{"null creation suppresses aggregate", `"cache_creation_input_tokens":4,"input_tokens_details":{"cache_creation_tokens":null}`, 0},
		{"prompt null write suppresses creation", `"prompt_tokens_details":{"cache_write_tokens":null,"cache_creation_tokens":4}`, 0},
		{"input null write precedes prompt write", `"input_tokens_details":{"cache_write_tokens":null},"prompt_tokens_details":{"cache_write_tokens":4}`, 0},
		{"prompt null write precedes input creation", `"input_tokens_details":{"cache_creation_tokens":4},"prompt_tokens_details":{"cache_write_tokens":null}`, 0},
		{"prompt positive write precedes input creation", `"input_tokens_details":{"cache_creation_tokens":4},"prompt_tokens_details":{"cache_write_tokens":2}`, 2},
		{"input null creation precedes prompt creation", `"input_tokens_details":{"cache_creation_tokens":null},"prompt_tokens_details":{"cache_creation_tokens":4}`, 0},
		{"null details allow aggregate", `"cache_creation_input_tokens":4,"input_tokens_details":null`, 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := []byte(`{"input_tokens":10,"output_tokens":2,` + test.details + `}`)
			for range 3 {
				var usage ResponsesUsage
				require.NoError(t, json.Unmarshal(payload, &usage))
				require.Equal(t, test.creation, usage.CacheCreationInputTokens)
				assertUsage := func(converted AnthropicUsage) {
					require.Equal(t, 10-test.creation, converted.InputTokens)
					require.Equal(t, test.creation, converted.CacheCreationInputTokens)
					require.Equal(t, 2, converted.OutputTokens)
				}
				assertUsage(ResponsesToAnthropic(&ResponsesResponse{Usage: &usage}, "test-model").Usage)
				state := NewResponsesEventToAnthropicState()
				events := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
					Type: "response.completed", Response: &ResponsesResponse{Status: "completed", Usage: &usage},
				}, state)
				usageEvents := 0
				for _, event := range events {
					if event.Usage != nil {
						assertUsage(*event.Usage)
						usageEvents++
					}
				}
				require.Equal(t, 1, usageEvents)
				var err error
				payload, err = json.Marshal(usage)
				require.NoError(t, err)
			}
		})
	}
}
