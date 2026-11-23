package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAnthropicNativeLinePumpBufferedDataWinsExpiredTimer(t *testing.T) {
	for round := 0; round < 32; round++ {
		pump := &anthropicNativeLinePump{
			events:   make(chan anthropicNativeLineEvent, 1),
			done:     make(chan struct{}),
			timer:    time.NewTimer(time.Millisecond),
			interval: time.Hour,
		}
		time.Sleep(5 * time.Millisecond)
		pump.events <- anthropicNativeLineEvent{line: "buffered upstream data"}
		line, err := pump.next()
		pump.stop()
		require.NoError(t, err, "round %d: downstream work must not cause upstream idle", round)
		require.Equal(t, "buffered upstream data", line)
	}
}
