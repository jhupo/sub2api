package openai_ws_v2

import (
	"context"
	"errors"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestRelayFundingRejectionRetainsOnlyUnsettledTurnUsage(t *testing.T) {
	client := newPassthroughTestFrameConn(nil, false)
	upstream := newPassthroughTestFrameConn([]passthroughTestFrame{
		{msgType: coderws.MessageText, payload: []byte(`{"type":"response.created","response":{"id":"resp_1"}}`)},
		{msgType: coderws.MessageText, payload: []byte(`{"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":11,"output_tokens":7}}}`)},
		{msgType: coderws.MessageText, payload: []byte(`{"type":"response.created","response":{"id":"resp_2"}}`)},
		{msgType: coderws.MessageText, payload: []byte(`{"type":"response.completed","response":{"id":"resp_2","usage":{"input_tokens":19,"output_tokens":13}}}`)},
	}, false)
	denied := errors.New("top-up denied")
	completed := 0
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, exit := Relay(ctx, client, upstream, []byte(`{"type":"response.create","model":"gpt-test"}`), RelayOptions{
		OnTurnComplete: func(turn RelayTurnResult) { completed++; require.Equal(t, "resp_1", turn.RequestID) },
		BeforeWriteClient: func(_ coderws.MessageType, payload []byte, _ bool) error {
			if gjson.GetBytes(payload, "type").String() == "response.completed" && gjson.GetBytes(payload, "response.id").String() == "resp_2" {
				return denied
			}
			return nil
		},
	})
	require.NotNil(t, exit)
	require.ErrorIs(t, exit.Err, denied)
	require.Equal(t, 1, completed)
	require.NotNil(t, result.IncompleteTurn)
	require.Equal(t, "resp_2", result.IncompleteTurn.RequestID)
	require.Equal(t, 19, result.IncompleteTurn.Usage.InputTokens)
	require.Equal(t, 13, result.IncompleteTurn.Usage.OutputTokens)
}
