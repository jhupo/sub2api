package repository

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usageadjustment"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type cacheAdjustmentSettingRepo struct {
	service.SettingRepository
	raw string
}

func TestHTTPUpstreamCacheReadAdjustmentIdentityEncoding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "identity")
		w.Header().Set("X-Request-Id", "req_identity")
		_, _ = io.WriteString(w, "data: "+`{"type":"response.completed","response":{"id":"resp_identity","usage":{"input_tokens":10227,"input_tokens_details":{"cached_tokens":3968,"cache_write_tokens":320},"output_tokens":5,"total_tokens":10232}}}`+"\n\n")
	}))
	t.Cleanup(server.Close)
	for _, path := range []string{"/v1/responses", "/backend-api/codex/responses"} {
		for _, percent := range []int{10, 100} {
			t.Run(fmt.Sprintf("%s/%d", path, percent), func(t *testing.T) {
				settings := service.NewSettingService(&cacheAdjustmentSettingRepo{raw: fmt.Sprintf(`{"enabled":true,"percent":%d,"account_ids":[29]}`, percent)}, nil)
				upstream := NewConfiguredHTTPUpstream(nil, settings)
				ctx := usageadjustment.WithRecorder(context.Background())
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+path, strings.NewReader(`{}`))
				require.NoError(t, err)
				resp, err := upstream.Do(req, "", 29, 1)
				require.NoError(t, err)
				defer func() { require.NoError(t, resp.Body.Close()) }()
				data, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				payload := strings.TrimSpace(strings.TrimPrefix(string(data), "data: "))
				u := gjson.Get(payload, "response.usage")
				cached := 3968 - 3968*percent/100
				require.EqualValues(t, cached, u.Get("input_tokens_details.cached_tokens").Int())
				require.EqualValues(t, 320, u.Get("input_tokens_details.cache_write_tokens").Int())
				require.EqualValues(t, 10227, u.Get("input_tokens").Int())
				require.EqualValues(t, 10232, u.Get("total_tokens").Int())
				snapshot := usageadjustment.Lookup(ctx, 29, "req_identity", cached)
				require.NotNil(t, snapshot)
				require.EqualValues(t, 3968, snapshot.OriginalCacheTokens)
				require.EqualValues(t, 3968-cached, snapshot.ShiftedTokens)
			})
		}
	}
}

func (r *cacheAdjustmentSettingRepo) GetValue(context.Context, string) (string, error) {
	return r.raw, nil
}

func TestHTTPUpstreamCacheReadAdjustmentMissingContentType(t *testing.T) {
	for _, format := range []string{"sse", "json"} {
		t.Run(format, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header()["Content-Type"] = nil
				w.Header().Set("X-Request-Id", "req_missing_content_type")
				payload := `{"id":"resp_missing_type","usage":{"input_tokens":4658,"input_tokens_details":{"cached_tokens":4480},"output_tokens":5,"total_tokens":4663}}`
				if format == "sse" {
					payload = "event: response.completed\ndata: " + `{"type":"response.completed","response":` + payload + "}\n\n"
				}
				_, _ = io.WriteString(w, payload)
			}))
			t.Cleanup(server.Close)
			for _, path := range []string{"/v1/responses", "/backend-api/codex/responses"} {
				for _, percent := range []int{10, 100} {
					t.Run(fmt.Sprintf("%s/%d", path, percent), func(t *testing.T) {
						settings := service.NewSettingService(&cacheAdjustmentSettingRepo{raw: fmt.Sprintf(`{"enabled":true,"percent":%d,"account_ids":[31]}`, percent)}, nil)
						upstream := NewConfiguredHTTPUpstream(nil, settings)
						ctx := usageadjustment.WithRecorder(context.Background())
						req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+path, strings.NewReader(`{}`))
						require.NoError(t, err)
						resp, err := upstream.Do(req, "", 31, 1)
						require.NoError(t, err)
						defer func() { require.NoError(t, resp.Body.Close()) }()
						require.Empty(t, resp.Header.Get("Content-Type"))
						data, err := io.ReadAll(resp.Body)
						require.NoError(t, err)
						usagePath := "usage"
						if format == "sse" {
							data = []byte(strings.TrimSpace(strings.SplitN(string(data), "data: ", 2)[1]))
							usagePath = "response.usage"
						}
						u := gjson.GetBytes(data, usagePath)
						cached := 4480 - 4480*percent/100
						require.EqualValues(t, cached, u.Get("input_tokens_details.cached_tokens").Int())
						require.EqualValues(t, 4658, u.Get("input_tokens").Int())
						require.EqualValues(t, 4663, u.Get("total_tokens").Int())
						snapshot := usageadjustment.Lookup(ctx, 31, "req_missing_content_type", cached)
						require.NotNil(t, snapshot)
						require.EqualValues(t, 4480, snapshot.OriginalCacheTokens)
						require.EqualValues(t, 4480-cached, snapshot.ShiftedTokens)
					})
				}
			}
		})
	}
}

func TestHTTPUpstreamCacheReadAdjustmentAfterDecompression(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		zip := gzip.NewWriter(w)
		if r.URL.Path == "/v1/responses" {
			_, _ = io.WriteString(zip, `{"usage":{"input_tokens":9000,"cache_read_input_tokens":8000,"output_tokens":100}}`)
		} else {
			_, _ = io.WriteString(zip, `{"type":"message","usage":{"input_tokens":1000,"cache_read_input_tokens":8000,"output_tokens":100}}`)
		}
		_ = zip.Close()
	}))
	t.Cleanup(server.Close)
	for _, tt := range []struct {
		name         string
		raw          string
		accountID    int64
		input, cache int64
	}{
		{"selected", `{"enabled":true,"percent":10,"account_ids":[7]}`, 7, 1800, 7200},
		{"not selected", `{"enabled":true,"percent":10,"account_ids":[7]}`, 8, 1000, 8000},
		{"disabled", `{"enabled":false,"percent":10,"account_ids":[7]}`, 7, 1000, 8000},
		{"zero percent", `{"enabled":true,"percent":0,"account_ids":[7]}`, 7, 1000, 8000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			settings := service.NewSettingService(&cacheAdjustmentSettingRepo{raw: tt.raw}, nil)
			upstream := NewConfiguredHTTPUpstream(nil, settings)
			req, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{}`))
			require.NoError(t, err)
			// Explicit encoding ensures decompression happens in the upstream service.
			req.Header.Set("Accept-Encoding", "gzip")
			resp, err := upstream.Do(req, "", tt.accountID, 1)
			require.NoError(t, err)
			defer func() { require.NoError(t, resp.Body.Close()) }()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, tt.input, gjson.GetBytes(body, "usage.input_tokens").Int())
			require.Equal(t, tt.cache, gjson.GetBytes(body, "usage.cache_read_input_tokens").Int())
			require.Equal(t, int64(9000), gjson.GetBytes(body, "usage.input_tokens").Int()+gjson.GetBytes(body, "usage.cache_read_input_tokens").Int())
		})
	}
	t.Run("responses without object marker", func(t *testing.T) {
		settings := service.NewSettingService(&cacheAdjustmentSettingRepo{raw: `{"enabled":true,"percent":10,"account_ids":[7]}`}, nil)
		upstream := NewConfiguredHTTPUpstream(nil, settings)
		req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{}`))
		require.NoError(t, err)
		resp, err := upstream.Do(req, "", 7, 1)
		require.NoError(t, err)
		defer func() { require.NoError(t, resp.Body.Close()) }()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, int64(9000), gjson.GetBytes(body, "usage.input_tokens").Int())
		require.Equal(t, int64(7200), gjson.GetBytes(body, "usage.cache_read_input_tokens").Int())
	})
}
