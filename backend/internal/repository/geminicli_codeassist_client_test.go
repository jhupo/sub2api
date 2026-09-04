package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
)

func TestGeminiCliCodeAssistClientEndpointURLs(t *testing.T) {
	client := &geminiCliCodeAssistClient{baseURL: geminicli.GeminiCliBaseURL}
	urls := client.endpointURLs()
	if len(urls) != 3 {
		t.Fatalf("default endpoint count = %d, want 3", len(urls))
	}
	if urls[0] != "https://daily-cloudcode-pa.googleapis.com" {
		t.Fatalf("default first endpoint = %q, want daily Code Assist endpoint", urls[0])
	}

	custom := &geminiCliCodeAssistClient{baseURL: "https://private-code-assist.example"}
	customURLs := custom.endpointURLs()
	if len(customURLs) != 1 || customURLs[0] != custom.baseURL {
		t.Fatalf("custom endpoints = %#v, want %#v", customURLs, []string{custom.baseURL})
	}
}

func TestShouldTryNextCodeAssistEndpoint(t *testing.T) {
	for _, status := range []int{408, 404, 429, 500, 503} {
		if !shouldTryNextCodeAssistEndpoint(status) {
			t.Errorf("status %d should try the next endpoint", status)
		}
	}
	for _, status := range []int{400, 401, 403} {
		if shouldTryNextCodeAssistEndpoint(status) {
			t.Errorf("status %d should not try the next endpoint", status)
		}
	}
}
