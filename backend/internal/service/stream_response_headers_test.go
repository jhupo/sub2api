package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetEventStreamHeaders(t *testing.T) {
	header := http.Header{
		"Content-Type":  []string{"application/json"},
		"Cache-Control": []string{"upstream"},
	}

	SetEventStreamHeaders(header)

	require.Equal(t, "text/event-stream", header.Get("Content-Type"))
	require.Equal(t, "no-cache, no-transform", header.Get("Cache-Control"))
	require.Equal(t, "keep-alive", header.Get("Connection"))
	require.Equal(t, "no", header.Get("X-Accel-Buffering"))
}

func TestEnsureStreamingNoTransformPreservesUpstreamDirectives(t *testing.T) {
	header := http.Header{}
	header.Add("Cache-Control", "private")
	header.Add("Cache-Control", "no-cache")

	ensureStreamingNoTransform(header)
	ensureStreamingNoTransform(header)

	require.Equal(t, "private, no-cache, no-transform", header.Get("Cache-Control"))
}
