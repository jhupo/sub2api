package usageadjustment

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type chunkBody struct {
	io.Reader
	closed bool
}

func (b *chunkBody) Read(p []byte) (int, error) {
	if len(p) > 3 {
		p = p[:3]
	}
	return b.Reader.Read(p)
}
func (b *chunkBody) Close() error { b.closed = true; return nil }

func TestWrapResponseJSONAndSSE(t *testing.T) {
	json := `{"usage":{"input_tokens":1000,"cache_read_input_tokens":8000},"id":9007199254740993}`
	for _, tt := range []struct{ typ, body, want string }{
		{"application/json", json, strings.ReplaceAll(strings.ReplaceAll(json, `"input_tokens":1000`, `"input_tokens":1800`), `"cache_read_input_tokens":8000`, `"cache_read_input_tokens":7200`)},
		{"text/event-stream", ": ping\n\nevent: message\ndata: " + json + "\n\ndata: [DONE]\n\n", ": ping\n\nevent: message\ndata: " + strings.ReplaceAll(strings.ReplaceAll(json, `"input_tokens":1000`, `"input_tokens":1800`), `"cache_read_input_tokens":8000`, `"cache_read_input_tokens":7200`) + "\n\ndata: [DONE]\n\n"},
		{"text/event-stream", "event: message\r\ndata: {\r\ndata: \"usage\":{\"input_tokens\":1000,\"cache_read_input_tokens\":8000}}\r\n\r\n", "event: message\r\ndata: {\ndata: \"usage\":{\"input_tokens\":1800,\"cache_read_input_tokens\":7200}}\r\n\r\n"},
	} {
		source := &chunkBody{Reader: strings.NewReader(tt.body)}
		resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {tt.typ}, "Content-Length": {"99"}}, Body: source}
		WrapResponse(resp, 10, 1<<20, false)
		got, err := io.ReadAll(resp.Body)
		if err != nil || string(got) != tt.want {
			t.Fatalf("type=%s got=%q err=%v want=%q", tt.typ, got, err, tt.want)
		}
		if resp.ContentLength != -1 || resp.Header.Get("Content-Length") != "" {
			t.Fatal("stale content length")
		}
		_ = resp.Body.Close()
		if !source.closed {
			t.Fatal("source not closed")
		}
	}
}

func TestWrapResponseLimitsAndErrors(t *testing.T) {
	body := `{"usage":{"input_tokens":1000,"cache_read_input_tokens":8000}}`
	for _, typ := range []string{"application/json", "text/event-stream"} {
		input := body
		if typ == "text/event-stream" {
			input = "data: " + strings.Repeat("x", 70000) + "\n\ndata: " + body + "\n\n"
		}
		resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {typ}}, Body: io.NopCloser(strings.NewReader(input))}
		WrapResponse(resp, 10, 32, false)
		got, err := io.ReadAll(resp.Body)
		if err != nil || !bytes.Equal(got, []byte(input)) {
			t.Fatalf("oversize response changed or truncated: %s %v", typ, err)
		}
	}
	for _, status := range []int{400, 500} {
		source := io.NopCloser(strings.NewReader(body))
		resp := &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: source}
		WrapResponse(resp, 10, 1<<20, false)
		if resp.Body != source {
			t.Fatal("error response wrapped")
		}
	}
	errSource := errors.New("upstream disconnected")
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(io.MultiReader(strings.NewReader(body), errorReader{errSource}))}
	WrapResponse(resp, 10, 1<<20, false)
	got, err := io.ReadAll(resp.Body)
	if !errors.Is(err, errSource) || string(got) != body {
		t.Fatalf("lost upstream error: %s %v", got, err)
	}
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }
