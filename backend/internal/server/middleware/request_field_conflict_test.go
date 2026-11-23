package middleware

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRejectConflictingRequestFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, body, contentType string
		accepted                bool
	}{
		{name: "ordinary", body: `{"model":"test","input":"hi"}`, accepted: true},
		{name: "same model", body: `{"model":"test","model":"test"}`},
		{name: "different model", body: `{"model":"cheap","model":"expensive"}`},
		{name: "case variant", body: `{"model":"test","Model":"other"}`},
		{name: "nested", body: `{"reasoning":{"effort":"low","Effort":"high"}}`},
		{name: "escaped name", body: `{"model":"test","mo\u0064el":"other"}`},
		{name: "JSON mislabeled multipart", contentType: "multipart/form-data; boundary=test", body: `{"model":"test","model":"other"}`},
		{name: "BOM duplicate", body: "\xef\xbb\xbf" + `{"model":"test","model":"other"}`},
		{name: "freeform case", body: `{"tools":[{"parameters":{"properties":{"id":{"type":"string"},"ID":{"type":"string"}}}}]}`, accepted: true},
		{name: "freeform duplicate", body: `{"tools":[{"parameters":{"properties":{"id":{},"id":{}}}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"/v1/responses", "/v1/messages", "/v1beta/models/test:generateContent"} {
				reached := false
				router := gin.New()
				router.POST(path, RequestBodyLimit(1024), RejectConflictingRequestFields(), func(c *gin.Context) {
					reached = true
					body, err := io.ReadAll(c.Request.Body)
					require.NoError(t, err)
					require.Equal(t, tc.body, string(body))
					c.Status(http.StatusNoContent)
				})
				request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(tc.body))
				request.Header.Set("Content-Type", tc.contentType)
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				require.Equal(t, tc.accepted, reached, "path=%s: %s", path, recorder.Body.String())
				if tc.accepted {
					require.Equal(t, http.StatusNoContent, recorder.Code)
				} else {
					require.Equal(t, http.StatusBadRequest, recorder.Code)
					require.Contains(t, recorder.Body.String(), "more than once")
				}
			}
		})
	}
}

func TestRejectConflictingRequestFieldsRestoresDecodedBodyAndBOM(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, compressed := range []bool{false, true} {
		const payload = `{"model":"test","input":"hello"}`
		body := []byte("\xef\xbb\xbf" + payload)
		if compressed {
			var encoded bytes.Buffer
			writer := gzip.NewWriter(&encoded)
			_, err := writer.Write(body)
			require.NoError(t, err)
			require.NoError(t, writer.Close())
			body = encoded.Bytes()
		}
		router := gin.New()
		router.POST("/responses", RequestBodyLimit(1024), RejectConflictingRequestFields(), func(c *gin.Context) {
			got, err := io.ReadAll(c.Request.Body)
			require.NoError(t, err)
			require.Equal(t, payload, string(got))
			require.Equal(t, int64(len(payload)), c.Request.ContentLength)
			require.Empty(t, c.GetHeader("Content-Encoding"))
			c.Status(http.StatusNoContent)
		})
		request := httptest.NewRequest(http.MethodPost, "/responses", bytes.NewReader(body))
		if compressed {
			request.Header.Set("Content-Encoding", "gzip")
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusNoContent, recorder.Code)
	}
}

func TestRejectConflictingRequestFieldsEnforcesDecodedLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var encoded bytes.Buffer
	writer := gzip.NewWriter(&encoded)
	_, err := writer.Write([]byte(`{"input":"` + strings.Repeat("x", 2048) + `"}`))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.Less(t, encoded.Len(), 128)
	router := gin.New()
	router.POST("/v1/responses", RequestBodyLimit(128), RejectConflictingRequestFields(), func(c *gin.Context) {
		t.Fatal("oversized decoded body reached handler")
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", &encoded)
	request.Header.Set("Content-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	require.Contains(t, recorder.Body.String(), "limit is 128B")
}
