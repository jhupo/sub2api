package apicompat

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func collectRecoveredAnthropicText(events []AnthropicStreamEvent) string {
	var text string
	for _, event := range events {
		if event.Type == "content_block_delta" && event.Delta != nil && event.Delta.Type == "text_delta" {
			text += event.Delta.Text
		}
	}
	return text
}

func TestResponsesEventToAnthropicEvents_RecoversDoneTextWithoutDuplicate(t *testing.T) {
	state := NewResponsesEventToAnthropicState()
	var events []AnthropicStreamEvent
	for _, input := range []*ResponsesStreamEvent{
		{Type: "response.created", Response: &ResponsesResponse{ID: "resp_1", Model: "gpt-5.5"}},
		{Type: "response.output_text.delta", OutputIndex: 0, ContentIndex: 0, Delta: "Hello"},
		{Type: "response.output_text.done", OutputIndex: 0, ContentIndex: 0, Text: "Hello, world"},
		{Type: "response.completed", Response: &ResponsesResponse{Status: "completed"}},
	} {
		events = append(events, ResponsesEventToAnthropicEvents(input, state)...)
	}
	require.Equal(t, "Hello, world", collectRecoveredAnthropicText(events))
}

func TestResponsesEventToAnthropicEvents_RecoversTerminalTextWhenNoDelta(t *testing.T) {
	state := NewResponsesEventToAnthropicState()
	events := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type: "response.completed",
		Response: &ResponsesResponse{
			Status: "completed",
			Output: []ResponsesOutput{{Type: "message", Content: []ResponsesContentPart{{Type: "output_text", Text: "terminal answer"}}}},
		},
	}, state)
	require.Equal(t, "terminal answer", collectRecoveredAnthropicText(events))
}

func TestResponsesEventToAnthropicEvents_LeavesUnmatchedDoneTextAlone(t *testing.T) {
	state := NewResponsesEventToAnthropicState()
	var events []AnthropicStreamEvent
	for _, input := range []*ResponsesStreamEvent{
		{Type: "response.created", Response: &ResponsesResponse{ID: "resp_2"}},
		{Type: "response.output_text.delta", OutputIndex: 0, ContentIndex: 0, Delta: "Hello"},
		{Type: "response.output_text.done", OutputIndex: 1, ContentIndex: 0, Text: "Hello, world"},
		{Type: "response.completed", Response: &ResponsesResponse{Status: "completed"}},
	} {
		events = append(events, ResponsesEventToAnthropicEvents(input, state)...)
	}
	require.Equal(t, "Hello", collectRecoveredAnthropicText(events))
}
