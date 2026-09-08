package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func withCodexAdaptiveTestPolicy(ctx context.Context) context.Context {
	return context.WithValue(ctx, codexAdaptiveRequestContextKey{}, &codexAdaptiveRequestState{
		pressures: make(map[codexAdaptivePressureScope]int),
	})
}

func openAIFirstOutputTestAccount(id int64) *Account {
	return &Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
}

func svcForCompatFirstOutputTests() *OpenAIGatewayService {
	return &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
}

func TestOpenAICompatTTFTStartsAtVisibleOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		call func(*OpenAIGatewayService, context.Context, *http.Response, *gin.Context, *Account, time.Time) (*OpenAIForwardResult, error)
	}{
		{
			name: "chat_completions",
			path: "/v1/chat/completions",
			call: func(svc *OpenAIGatewayService, ctx context.Context, resp *http.Response, c *gin.Context, account *Account, started time.Time) (*OpenAIForwardResult, error) {
				return svc.handleChatStreamingResponse(ctx, resp, c, account, "gpt-5.6-sol", "gpt-5.6-sol", "gpt-5.6-sol", started, 0)
			},
		},
		{
			name: "anthropic_messages",
			path: "/v1/messages",
			call: func(svc *OpenAIGatewayService, ctx context.Context, resp *http.Response, c *gin.Context, account *Account, started time.Time) (*OpenAIForwardResult, error) {
				return svc.handleAnthropicStreamingResponse(ctx, resp, c, account, "gpt-5.6-sol", "gpt-5.6-sol", "gpt-5.6-sol", started)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			reader, writer := io.Pipe()
			go func() {
				defer func() { _ = writer.Close() }()
				_, _ = io.WriteString(writer, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_visible\"}}\n\n")
				time.Sleep(120 * time.Millisecond)
				_, _ = io.WriteString(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"delayed output\"}\n\n")
				_, _ = io.WriteString(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_visible\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
			}()

			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, nil)
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: reader}
			started := time.Now()

			result, err := tc.call(svcForCompatFirstOutputTests(), withCodexAdaptiveTestPolicy(c.Request.Context()), resp, c, openAIFirstOutputTestAccount(5), started)

			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotNil(t, result.FirstTokenMs)
			require.GreaterOrEqual(t, *result.FirstTokenMs, 100)
			require.Contains(t, rec.Body.String(), "delayed output")
		})
	}
}

func TestOpenAICompatCapacityShedDoesNotCommitProtocolPreamble(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		call func(*OpenAIGatewayService, context.Context, *http.Response, *gin.Context, *Account, time.Time) (*OpenAIForwardResult, error)
	}{
		{
			name: "chat_completions",
			path: "/v1/chat/completions",
			call: func(svc *OpenAIGatewayService, ctx context.Context, resp *http.Response, c *gin.Context, account *Account, started time.Time) (*OpenAIForwardResult, error) {
				return svc.handleChatStreamingResponse(ctx, resp, c, account, "gpt-5.6-sol", "gpt-5.6-sol", "gpt-5.6-sol", started, 0)
			},
		},
		{
			name: "anthropic_messages",
			path: "/v1/messages",
			call: func(svc *OpenAIGatewayService, ctx context.Context, resp *http.Response, c *gin.Context, account *Account, started time.Time) (*OpenAIForwardResult, error) {
				return svc.handleAnthropicStreamingResponse(ctx, resp, c, account, "gpt-5.6-sol", "gpt-5.6-sol", "gpt-5.6-sol", started)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			stream := strings.Join([]string{
				`data: {"type":"response.created","response":{"id":"resp_overloaded"}}`,
				"",
				`event: error`,
				`data: {"type":"error","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}`,
				"",
			}, "\n")
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, nil)
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(stream))}

			result, err := tc.call(svcForCompatFirstOutputTests(), withCodexAdaptiveTestPolicy(c.Request.Context()), resp, c, openAIFirstOutputTestAccount(6), time.Now())

			require.Nil(t, result)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.True(t, failoverErr.IsOpenAICapacityShed())
			require.Empty(t, rec.Body.String(), "capacity shedding must remain replayable before semantic output")
		})
	}
}

func TestOpenAILongPreambleDoesNotTriggerAdaptiveFailover(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		name := "native"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			reader, writer := io.Pipe()
			go func() {
				defer func() { _ = writer.Close() }()
				_, _ = io.WriteString(writer, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_slow\"}}\n\n")
				_, _ = io.WriteString(writer, "data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_slow\"}}\n\n")
				time.Sleep(1100 * time.Millisecond)
				_, _ = io.WriteString(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ready\"}\n\n")
				_, _ = io.WriteString(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_slow\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
			}()
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: reader}
			started := time.Now()
			ctx := withCodexAdaptiveTestPolicy(c.Request.Context())

			if passthrough {
				result, err := svcForCompatFirstOutputTests().handleStreamingResponsePassthrough(ctx, resp, c, openAIFirstOutputTestAccount(1), started, "gpt-5.6-sol", "gpt-5.6-sol")
				require.NoError(t, err)
				require.NotNil(t, result)
				require.NotNil(t, result.firstTokenMs)
				require.GreaterOrEqual(t, *result.firstTokenMs, 1000)
			} else {
				result, err := svcForCompatFirstOutputTests().handleStreamingResponse(ctx, resp, c, openAIFirstOutputTestAccount(1), started, "gpt-5.6-sol", "gpt-5.6-sol")
				require.NoError(t, err)
				require.NotNil(t, result)
				require.NotNil(t, result.firstTokenMs)
				require.GreaterOrEqual(t, *result.firstTokenMs, 1000)
			}
			require.Contains(t, rec.Body.String(), `"delta":"ready"`)
		})
	}
}

func TestOpenAIFirstOutputStageDefaultLimitIsIndependentFromScannerLimit(t *testing.T) {
	stage := newDefaultOpenAIFirstOutputStage()
	defer func() { require.NoError(t, stage.Close()) }()

	require.EqualValues(t, 8*1024*1024, stage.limit)
	require.Greater(t, stage.limit, int64(68106))
	require.Less(t, stage.limit, int64(defaultMaxLineSize))
}

func TestOpenAIFirstOutputDynamicScannerLimitsOnlyWhileStaging(t *testing.T) {
	var staging atomic.Bool
	staging.Store(true)
	split := openAIFirstOutputDynamicScanLines(&staging)
	guardLimit := openAIFirstOutputStageMaxBytes + openAIFirstOutputScannerFramingAllowance
	undelimited := bytes.Repeat([]byte("x"), guardLimit)

	_, _, err := split(undelimited, false)
	require.ErrorIs(t, err, errOpenAIFirstOutputScannerLimit)

	staging.Store(false)
	advance, token, err := split(undelimited, false)
	require.NoError(t, err)
	require.Zero(t, advance)
	require.Nil(t, token)
}

func TestOpenAIFirstOutputStageOverflowIsAtomicAndCleanupRemovesSpool(t *testing.T) {
	stage := newOpenAIFirstOutputStage(70 * 1024)
	payload := bytes.Repeat([]byte("x"), 68*1024)
	n, err := stage.Write(payload)
	require.NoError(t, err)
	require.Equal(t, len(payload), n)
	if runtime.GOOS == "windows" {
		require.Nil(t, stage.tempFile)
		require.Empty(t, stage.tempPath)
	} else {
		require.NotNil(t, stage.tempFile)
		require.NotEmpty(t, stage.tempPath)
		_, err = os.Stat(stage.tempPath)
		require.ErrorIs(t, err, os.ErrNotExist)
		stat, statErr := stage.tempFile.Stat()
		require.NoError(t, statErr)
		require.Equal(t, os.FileMode(0o600), stat.Mode().Perm())
	}

	n, err = stage.Write(bytes.Repeat([]byte("y"), 3*1024))
	require.Zero(t, n)
	require.ErrorIs(t, err, errOpenAIFirstOutputStageLimit)
	require.EqualValues(t, len(payload), stage.Buffered())
	path := stage.tempPath
	require.NoError(t, stage.Close())
	require.Nil(t, stage.tempFile)
	require.Empty(t, stage.tempPath)
	if path != "" {
		_, err = os.Stat(path)
		require.ErrorIs(t, err, os.ErrNotExist)
	}
}

func TestOpenAIFirstOutputStageCommitCopiesSpoolAndRemovesTemp(t *testing.T) {
	stage := newOpenAIFirstOutputStage(80 * 1024)
	payload := bytes.Repeat([]byte("z"), 68*1024)
	_, err := stage.Write(payload)
	require.NoError(t, err)
	path := stage.tempPath

	var downstream bytes.Buffer
	require.NoError(t, stage.CommitTo(&downstream))
	require.Equal(t, payload, downstream.Bytes())
	require.Zero(t, stage.Buffered())
	if path != "" {
		_, err = os.Stat(path)
		require.ErrorIs(t, err, os.ErrNotExist)
	}
	require.NoError(t, stage.Close())
}

func TestOpenAIFirstOutputStageUnlinkFailureFallsBackToMemory(t *testing.T) {
	stage := newDefaultOpenAIFirstOutputStage()
	stage.memoryOnly = false
	t.Cleanup(func() {
		stage.removeFile = os.Remove
		_ = stage.Close()
	})
	stage.createTemp = func() (*os.File, error) {
		return os.CreateTemp("", "sub2api-openai-first-output-fallback-*")
	}
	removeCalls := 0
	stage.removeFile = func(path string) error {
		removeCalls++
		if removeCalls <= 2 {
			return errors.New("forced remove failure")
		}
		return os.Remove(path)
	}

	payload := bytes.Repeat([]byte("m"), 68*1024)
	_, err := stage.Write(payload)
	require.NoError(t, err)
	require.True(t, stage.memoryOnly)
	require.Nil(t, stage.tempFile)
	require.NotEmpty(t, stage.tempPath)
	stat, statErr := os.Stat(stage.tempPath)
	require.NoError(t, statErr)
	require.Zero(t, stat.Size(), "failed-unlink fallback must never write plaintext to the named file")

	path := stage.tempPath
	cleanupErr := stage.Close()
	require.ErrorContains(t, cleanupErr, "forced remove failure")
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestOpenAINativeFirstOutputEOFDispatchesTerminalEventWithoutBlankLine(t *testing.T) {
	svc := svcForCompatFirstOutputTests()
	payload := `data: {"type":"response.completed","response":{"id":"resp_eof","usage":{"input_tokens":3,"output_tokens":2}}}`
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"X-Request-Id":                   []string{"request-eof"},
			"X-Ratelimit-Remaining-Requests": []string{"17"},
		},
		Body: io.NopCloser(strings.NewReader(payload)),
	}

	result, err := svc.handleStreamingResponse(withCodexAdaptiveTestPolicy(c.Request.Context()), resp, c, openAIFirstOutputTestAccount(1), time.Now(), "model", "model")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Nil(t, result.firstTokenMs, "usage-only terminal event is not visible output")
	require.Equal(t, "resp_eof", result.responseID)
	require.Equal(t, 3, result.usage.InputTokens)
	require.Equal(t, 2, result.usage.OutputTokens)
	require.Contains(t, rec.Body.String(), `"type":"response.completed"`)
	require.True(t, strings.HasSuffix(rec.Body.String(), "\n"))
	require.False(t, strings.HasSuffix(rec.Body.String(), "\n\n"), "EOF dispatch must not synthesize a blank line")
}

func TestOpenAIFirstOutputStageOverflowFailsOverWithoutAttemptBytes(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		name := "native"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: 2 * 1024 * 1024}}
			svc := &OpenAIGatewayService{cfg: cfg, responseHeaderFilter: compileResponseHeaderFilter(cfg)}
			const lineSize = 1024*1024 - 256
			prefix := `data: {"type":"response.created","response":{"id":"resp_private","padding":"`
			suffix := `"}}`
			line := prefix + strings.Repeat("x", lineSize-len(prefix)-len(suffix)) + suffix
			body := strings.Repeat(line+"\n", 9)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Request-Id": []string{"request-overflow"}}, Body: io.NopCloser(strings.NewReader(body))}
			ctx := withCodexAdaptiveTestPolicy(c.Request.Context())

			var err error
			if passthrough {
				_, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, openAIFirstOutputTestAccount(1), time.Now(), "model", "model")
			} else {
				_, err = svc.handleStreamingResponse(ctx, resp, c, openAIFirstOutputTestAccount(1), time.Now(), "model", "model")
			}
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
			require.True(t, failoverErr.SafeToFailoverAfterWrite)
			require.Contains(t, string(failoverErr.ResponseBody), "staging limit exceeded")
			require.Empty(t, rec.Body.String())
		})
	}
}
