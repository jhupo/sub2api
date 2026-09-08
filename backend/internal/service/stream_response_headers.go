package service

import (
	"net/http"
	"strings"
)

const streamingCacheControlValue = "no-cache, no-transform"

// SetEventStreamHeaders applies the gateway-owned headers shared by SSE paths.
func SetEventStreamHeaders(header http.Header) {
	if header == nil {
		return
	}
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", streamingCacheControlValue)
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
}

// ensureStreamingNoTransform preserves upstream cache directives while
// preventing intermediaries from transforming a streaming response.
func ensureStreamingNoTransform(header http.Header) {
	if header == nil {
		return
	}
	value := strings.TrimSpace(strings.Join(header.Values("Cache-Control"), ", "))
	if value == "" {
		header.Set("Cache-Control", streamingCacheControlValue)
		return
	}
	for _, directive := range strings.Split(value, ",") {
		name := strings.TrimSpace(strings.SplitN(directive, "=", 2)[0])
		if strings.EqualFold(name, "no-transform") {
			return
		}
	}
	header.Set("Cache-Control", value+", no-transform")
}
