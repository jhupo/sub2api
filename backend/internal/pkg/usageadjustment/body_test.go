package usageadjustment

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

type chunkBody struct {
	io.Reader
	closed bool
}

func TestWrapResponseCacheReadOnlyWithIdentityEncoding(t *testing.T) {
	const payload = `{"type":"response.completed","response":{"id":"resp_probe","usage":{"input_tokens":10227,"input_tokens_details":{"cached_tokens":3968,"cache_write_tokens":320},"output_tokens":5,"total_tokens":10232}}}`
	for _, percent := range []int{0, 10, 100} {
		for _, encoding := range []string{"", "identity", " Identity "} {
			resp := &http.Response{StatusCode: 200, Header: http.Header{
				"Content-Type": {"text/event-stream; charset=utf-8"}, "Content-Encoding": {encoding},
			}, Body: io.NopCloser(strings.NewReader("event: response.completed\ndata: " + payload + "\n\n"))}
			WrapResponse(resp, percent, 1<<20, true)
			data, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			event := strings.TrimSpace(strings.SplitN(string(data), "data: ", 2)[1])
			if got := gjson.Get(event, "response.usage.input_tokens_details.cached_tokens").Int(); got != 3968-int64(3968*percent/100) {
				t.Fatalf("encoding=%q percent=%d cache=%d", encoding, percent, got)
			}
			for path, want := range map[string]int64{
				"input_tokens": 10227, "input_tokens_details.cache_write_tokens": 320, "output_tokens": 5, "total_tokens": 10232,
			} {
				if got := gjson.Get(event, "response.usage."+path).Int(); got != want {
					t.Fatalf("%s changed to %d", path, got)
				}
			}
		}
	}
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

func TestWrapResponseWithoutContentType(t *testing.T) {
	const original = `{"usage":{"input_tokens":1000,"cache_read_input_tokens":8000}}`
	const adjusted = `{"usage":{"input_tokens":1800,"cache_read_input_tokens":7200}}`
	for _, tt := range []struct{ name, input, want string }{
		{"json", original, adjusted},
		{"json whitespace", " \n\t" + original, " \n\t" + adjusted},
		{"data event", "data: " + original + "\n\n", "data: " + adjusted + "\n\n"},
		{"named event", "event: message\ndata: " + original + "\n\n", "event: message\ndata: " + adjusted + "\n\n"},
		{"heartbeat", ": ping\n\ndata: " + original + "\n\n", ": ping\n\ndata: " + adjusted + "\n\n"},
		{"binary", "\x89PNG\r\n\x1a\n", "\x89PNG\r\n\x1a\n"},
		{"short prefix", "dat", "dat"},
		{"plain text", "example text", "example text"},
		{"whitespace limit", strings.Repeat(" ", 513) + original, strings.Repeat(" ", 513) + original},
		{"empty", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			source := &chunkBody{Reader: strings.NewReader(tt.input)}
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: source}
			WrapResponse(resp, 10, 1<<20, false)
			got, err := io.ReadAll(resp.Body)
			if err != nil || string(got) != tt.want {
				t.Fatalf("got=%q err=%v want=%q", got, err, tt.want)
			}
			_ = resp.Body.Close()
			if !source.closed {
				t.Fatal("source not closed")
			}
		})
	}
	failure := errors.New("upstream read failed")
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(errorReader{failure})}
	WrapResponse(resp, 10, 1<<20, false)
	_, err := io.ReadAll(resp.Body)
	if !errors.Is(err, failure) {
		t.Fatalf("lost sniffing error: %v", err)
	}
}
