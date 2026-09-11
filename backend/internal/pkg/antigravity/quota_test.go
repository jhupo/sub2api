package antigravity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRetrieveUserQuotaSummary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"windows", 200, `{"groups":[{"displayName":"Gemini","buckets":[{"bucketId":"five-hour","window":"5h","remainingFraction":0.8},{"bucketId":"weekly","window":"7d","remainingFraction":0}]}]}`, ""},
		{"empty", 200, `{"groups":[]}`, ""},
		{"missing", 200, `{}`, "missing groups"},
		{"missing buckets", 200, `{"groups":[{"displayName":"Gemini"}]}`, "missing buckets"},
		{"null buckets", 200, `{"groups":[{"buckets":null}]}`, "missing buckets"},
		{"rate limited", 429, `{}`, "HTTP 429"},
		{"unauthenticated", 401, `{}`, "HTTP 401"},
		{"unsupported", 404, `{}`, "endpoint is unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				require.Equal(t, "/v1internal:retrieveUserQuotaSummary", r.URL.Path)
				require.Equal(t, "Bearer test", r.Header.Get("Authorization"))
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			oldURLs, oldAvailability := BaseURLs, DefaultURLAvailability
			BaseURLs = []string{server.URL, server.URL}
			DefaultURLAvailability = NewURLAvailability(time.Minute)
			t.Cleanup(func() { BaseURLs, DefaultURLAvailability = oldURLs, oldAvailability })
			client, err := NewClient("")
			require.NoError(t, err)
			summary, err := client.RetrieveUserQuotaSummary(context.Background(), "test", "project", 4096)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
				require.NotNil(t, summary.Groups)
			}
			if tc.status == 429 || tc.status == 401 {
				require.Equal(t, 1, calls)
			}
		})
	}
}

func TestQuotaBucketMissingIsNotExhausted(t *testing.T) {
	zero, invalid := 0.0, 1.2
	require.False(t, (QuotaBucket{}).Valid())
	require.True(t, (QuotaBucket{RemainingFraction: &zero}).Valid())
	require.False(t, (QuotaBucket{RemainingFraction: &invalid}).Valid())
}

func TestModelCatalogDoesNotMergeDifferentEndpoints(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"models":{"gemini-pro-agent":{"displayName":"Pro"}}}`))
	}))
	defer server.Close()
	oldURLs, oldAvailability := BaseURLs, DefaultURLAvailability
	BaseURLs = []string{server.URL, server.URL + "/other"}
	DefaultURLAvailability = NewURLAvailability(time.Minute)
	t.Cleanup(func() { BaseURLs, DefaultURLAvailability = oldURLs, oldAvailability })
	client, err := NewClient("")
	require.NoError(t, err)
	models, _, err := client.FetchAvailableModels(context.Background(), "token", "project", 4096)
	require.NoError(t, err)
	require.Contains(t, models.Models, "gemini-pro-agent")
	require.Equal(t, 1, calls)
}

func TestModelCatalogRateLimitDoesNotFanOut(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	oldURLs, oldAvailability := BaseURLs, DefaultURLAvailability
	BaseURLs = []string{server.URL, server.URL + "/other"}
	DefaultURLAvailability = NewURLAvailability(time.Minute)
	t.Cleanup(func() { BaseURLs, DefaultURLAvailability = oldURLs, oldAvailability })
	client, err := NewClient("")
	require.NoError(t, err)
	_, _, err = client.FetchAvailableModels(context.Background(), "token", "project", 4096)
	require.ErrorContains(t, err, "HTTP 429")
	require.Equal(t, 1, calls)
}
