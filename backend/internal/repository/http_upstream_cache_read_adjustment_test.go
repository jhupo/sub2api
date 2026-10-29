package repository

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type cacheAdjustmentSettingRepo struct {
	service.SettingRepository
	raw string
}

func (r *cacheAdjustmentSettingRepo) GetValue(context.Context, string) (string, error) {
	return r.raw, nil
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
			defer resp.Body.Close()
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
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, int64(9000), gjson.GetBytes(body, "usage.input_tokens").Int())
		require.Equal(t, int64(7200), gjson.GetBytes(body, "usage.cache_read_input_tokens").Int())
	})
}
