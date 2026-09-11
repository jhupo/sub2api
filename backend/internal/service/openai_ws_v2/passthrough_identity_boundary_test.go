package openai_ws_v2

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTerminalEventIDCannotReplaceResponseID(t *testing.T) {
	state := &relayState{}
	now := time.Now()
	clock := func() time.Time { return now }
	observeUpstreamMessage(state, []byte(`{"type":"response.created","response":{"id":"resp_a"}}`), now, clock, nil, nil)
	terminal := observeUpstreamMessage(state, []byte(`{"type":"response.completed","id":"evt_completed","response":{"usage":{"input_tokens":7,"output_tokens":2}}}`), now, clock, nil, nil)
	require.True(t, terminal.terminal)
	require.Equal(t, "resp_a", terminal.responseID)
	require.Equal(t, "resp_a", state.lastResponseID)
	require.Equal(t, 7, terminal.usage.InputTokens)
}
