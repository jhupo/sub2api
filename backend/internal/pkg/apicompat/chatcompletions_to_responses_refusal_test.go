package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChatCompletionsToResponsesReplaysRefusalHistory(t *testing.T) {
	for _, text := range []string{"", "I can help with another topic."} {
		name := "refusal only"
		if text != "" {
			name = "text and refusal"
		}
		t.Run(name, func(t *testing.T) {
			content := []ResponsesContentPart{{Type: "refusal", Refusal: refusalText}}
			if text != "" {
				content = append([]ResponsesContentPart{{Type: "output_text", Text: text}}, content...)
			}
			chat := ResponsesToChatCompletions(&ResponsesResponse{
				ID: "refusal-history", Status: "completed",
				Output: []ResponsesOutput{{Type: "message", Role: "assistant", Content: content}},
			}, "gpt-5")
			encoded, err := json.Marshal(chat.Choices[0].Message)
			require.NoError(t, err)
			var message ChatMessage
			require.NoError(t, json.Unmarshal(encoded, &message))
			converted, err := ChatCompletionsToResponses(&ChatCompletionsRequest{
				Model: "gpt-5", Messages: []ChatMessage{
					message,
					{Role: "user", Content: json.RawMessage(`"Tell me more."`)},
				},
			})
			require.NoError(t, err)
			var input []ResponsesInputItem
			require.NoError(t, json.Unmarshal(converted.Input, &input))
			require.Len(t, input, 2)
			require.Equal(t, "message", input[0].Type)
			require.Equal(t, "assistant", input[0].Role)
			var replay []ResponsesContentPart
			require.NoError(t, json.Unmarshal(input[0].Content, &replay))
			require.Equal(t, content, replay)
			require.Equal(t, "user", input[1].Role)
		})
	}
}
