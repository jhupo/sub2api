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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type blockingOpenAIResponseHeaderUpstream struct {
	canceled chan struct{}
	once     sync.Once
}

type firstOutputCloseTrackingBody struct {
	io.ReadCloser
	closed chan struct{}
	once   sync.Once
}

func withCodexAdaptiveTestPolicy(ctx context.Context, normalSeconds, highSeconds int, compact bool) context.Context {
	return context.WithValue(ctx, codexAdaptiveRequestContextKey{}, &codexAdaptiveRequestState{
		compact: compact,
		settings: CodexAdaptiveSchedulingSettings{
			Enabled:                             true,
			NormalFirstOutputTimeoutSeconds:     normalSeconds,
			HighEffortFirstOutputTimeoutSeconds: highSeconds,
		},
		pressures: make(map[codexAdaptivePressureScope]int),
	})
}

func openAIFirstOutputTestAccount(id int64) *Account {
	return &Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
}

func (b *firstOutputCloseTrackingBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return b.ReadCloser.Close()
}

func (u *blockingOpenAIResponseHeaderUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	select {
	case <-req.Context().Done():
		u.once.Do(func() { close(u.canceled) })
		return nil, req.Context().Err()
	case <-time.After(1500 * time.Millisecond):
		return nil, errors.New("test upstream was not canceled before response headers")
	}
}

func (u *blockingOpenAIResponseHeaderUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, "", 0, 0)
}

func TestOpenAIForwardFirstOutputTimeoutIncludesResponseHeaderWait(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &blockingOpenAIResponseHeaderUpstream{canceled: make(chan struct{})}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{
			MaxLineSize: defaultMaxLineSize,
		}},
		httpUpstream: upstream,
	}
	body := []byte(`{"model":"gpt-5.5","stream":true,"reasoning":{"effort":"low"},"input":"hello"}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	account := &Account{
		ID: 1, Name: "oauth-test", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token", "chatgpt_account_id": "test-account"},
	}

	started := time.Now()
	ctx := withCodexAdaptiveTestPolicy(context.Background(), 1, 1, false)
	_, err := svc.Forward(ctx, c, account, body)

	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusGatewayTimeout, failoverErr.StatusCode)
	require.Contains(t, string(failoverErr.ResponseBody), "first_output_timeout")
	require.True(t, failoverErr.SafeToFailoverAfterWrite)
	require.Less(t, time.Since(started), 1300*time.Millisecond)
	require.Empty(t, rec.Body.String())
	select {
	case <-upstream.canceled:
	default:
		t.Fatal("response-header timeout did not cancel the upstream request context")
	}
}

func TestOpenAIPassthroughFirstOutputTimeoutIncludesResponseHeaderWait(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &blockingOpenAIResponseHeaderUpstream{canceled: make(chan struct{})}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{
			MaxLineSize: defaultMaxLineSize,
		}},
		httpUpstream: upstream,
	}
	body := []byte(`{"model":"gpt-5.5","stream":true,"reasoning":{"effort":"low"},"input":"hello"}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	account := &Account{
		ID: 2, Name: "oauth-passthrough-test", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token", "chatgpt_account_id": "test-account"},
	}
	effort := "low"

	started := time.Now()
	ctx := withCodexAdaptiveTestPolicy(context.Background(), 1, 1, false)
	_, err := svc.forwardOpenAIPassthrough(
		ctx, c, account, body, body, "gpt-5.5", false, &effort, true, started,
	)

	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, CodexFirstOutputTimeoutReason, failoverErr.Reason)
	require.Less(t, time.Since(started), 1300*time.Millisecond)
	require.Empty(t, rec.Body.String())
	select {
	case <-upstream.canceled:
	default:
		t.Fatal("passthrough response-header timeout did not cancel the upstream request context")
	}
}

func TestOpenAICompatFirstOutputTimeoutIncludesResponseHeaderWait(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		body []byte
		call func(*OpenAIGatewayService, context.Context, *gin.Context, *Account, []byte) (*OpenAIForwardResult, error)
	}{
		{
			name: "chat_completions",
			path: "/v1/chat/completions",
			body: []byte(`{"model":"gpt-5.5","stream":true,"messages":[{"role":"user","content":"hello"}]}`),
			call: func(svc *OpenAIGatewayService, ctx context.Context, c *gin.Context, account *Account, body []byte) (*OpenAIForwardResult, error) {
				return svc.ForwardAsChatCompletions(ctx, c, account, body, "", "")
			},
		},
		{
			name: "anthropic_messages",
			path: "/v1/messages",
			body: []byte(`{"model":"gpt-5.5","stream":true,"max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`),
			call: func(svc *OpenAIGatewayService, ctx context.Context, c *gin.Context, account *Account, body []byte) (*OpenAIForwardResult, error) {
				return svc.ForwardAsAnthropic(ctx, c, account, body, "", "")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			upstream := &blockingOpenAIResponseHeaderUpstream{canceled: make(chan struct{})}
			svc := &OpenAIGatewayService{
				cfg:          &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
				httpUpstream: upstream,
			}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(tc.body))
			account := &Account{
				ID: 3, Name: "oauth-compat-test", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
				Status: StatusActive, Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{"access_token": "test-token", "chatgpt_account_id": "test-account"},
			}

			started := time.Now()
			_, err := tc.call(svc, withCodexAdaptiveTestPolicy(context.Background(), 1, 1, false), c, account, tc.body)

			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, CodexFirstOutputTimeoutReason, failoverErr.Reason)
			require.Less(t, time.Since(started), 1300*time.Millisecond)
			require.Empty(t, rec.Body.String())
			select {
			case <-upstream.canceled:
			default:
				t.Fatal("response-header timeout did not cancel the compat upstream request")
			}
		})
	}
}

func TestOpenAICompatFirstOutputTimeoutStagesProtocolPreamble(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		call func(*OpenAIGatewayService, context.Context, *http.Response, *gin.Context, *Account, time.Time) (*OpenAIForwardResult, error)
	}{
		{
			name: "chat_completions",
			path: "/v1/chat/completions",
			call: func(svc *OpenAIGatewayService, ctx context.Context, resp *http.Response, c *gin.Context, account *Account, started time.Time) (*OpenAIForwardResult, error) {
				return svc.handleChatStreamingResponse(ctx, resp, c, account, "gpt-5.5", "gpt-5.5", "gpt-5.5", started, started, "low", 0)
			},
		},
		{
			name: "anthropic_messages",
			path: "/v1/messages",
			call: func(svc *OpenAIGatewayService, ctx context.Context, resp *http.Response, c *gin.Context, account *Account, started time.Time) (*OpenAIForwardResult, error) {
				return svc.handleAnthropicStreamingResponse(ctx, resp, c, account, "gpt-5.5", "gpt-5.5", "gpt-5.5", started, started, "low")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			reader, writer := io.Pipe()
			writerDone := make(chan struct{})
			go func() {
				defer close(writerDone)
				defer func() { _ = writer.Close() }()
				_, _ = io.WriteString(writer, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_slow\"}}\n\n")
				time.Sleep(1200 * time.Millisecond)
			}()

			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, nil)
			body := &firstOutputCloseTrackingBody{ReadCloser: reader, closed: make(chan struct{})}
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: body}
			started := time.Now()

			result, err := tc.call(svcForCompatFirstOutputTests(), withCodexAdaptiveTestPolicy(c.Request.Context(), 1, 1, false), resp, c, openAIFirstOutputTestAccount(4), started)

			require.Nil(t, result)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, CodexFirstOutputTimeoutReason, failoverErr.Reason)
			require.Empty(t, rec.Body.String(), "protocol preamble must remain staged so failover stays safe")
			select {
			case <-body.closed:
			default:
				t.Fatal("compat first-output timeout did not close the upstream body")
			}
			select {
			case <-writerDone:
			case <-time.After(time.Second):
				t.Fatal("compat upstream writer did not exit after timeout")
			}
		})
	}
}

func TestOpenAICompatTTFTStartsAtVisibleOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want string
		call func(*OpenAIGatewayService, context.Context, *http.Response, *gin.Context, *Account, time.Time) (*OpenAIForwardResult, error)
	}{
		{
			name: "chat_completions",
			path: "/v1/chat/completions",
			want: "delayed output",
			call: func(svc *OpenAIGatewayService, ctx context.Context, resp *http.Response, c *gin.Context, account *Account, started time.Time) (*OpenAIForwardResult, error) {
				return svc.handleChatStreamingResponse(ctx, resp, c, account, "gpt-5.5", "gpt-5.5", "gpt-5.5", started, started, "low", 0)
			},
		},
		{
			name: "anthropic_messages",
			path: "/v1/messages",
			want: "delayed output",
			call: func(svc *OpenAIGatewayService, ctx context.Context, resp *http.Response, c *gin.Context, account *Account, started time.Time) (*OpenAIForwardResult, error) {
				return svc.handleAnthropicStreamingResponse(ctx, resp, c, account, "gpt-5.5", "gpt-5.5", "gpt-5.5", started, started, "low")
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

			result, err := tc.call(svcForCompatFirstOutputTests(), withCodexAdaptiveTestPolicy(c.Request.Context(), 1, 1, false), resp, c, openAIFirstOutputTestAccount(5), started)

			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotNil(t, result.FirstTokenMs)
			require.GreaterOrEqual(t, *result.FirstTokenMs, 100)
			require.Contains(t, rec.Body.String(), tc.want)
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
				return svc.handleChatStreamingResponse(ctx, resp, c, account, "gpt-5.5", "gpt-5.5", "gpt-5.5", started, started, "low", 0)
			},
		},
		{
			name: "anthropic_messages",
			path: "/v1/messages",
			call: func(svc *OpenAIGatewayService, ctx context.Context, resp *http.Response, c *gin.Context, account *Account, started time.Time) (*OpenAIForwardResult, error) {
				return svc.handleAnthropicStreamingResponse(ctx, resp, c, account, "gpt-5.5", "gpt-5.5", "gpt-5.5", started, started, "low")
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
			started := time.Now()

			result, err := tc.call(svcForCompatFirstOutputTests(), withCodexAdaptiveTestPolicy(c.Request.Context(), 5, 5, false), resp, c, openAIFirstOutputTestAccount(6), started)

			require.Nil(t, result)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.True(t, failoverErr.IsOpenAICapacityShed())
			require.Empty(t, rec.Body.String(), "capacity shedding must remain replayable before semantic output")
		})
	}
}

func TestOpenAIFirstOutputDeadlineUsesAbsoluteCutoff(t *testing.T) {
	expired := newOpenAIFirstOutputDeadlineGuard(time.Now().Add(-time.Millisecond))
	defer expired.stop()
	require.False(t, expired.claimOutput())
	require.True(t, expired.timedOut())
	select {
	case <-expired.timeoutC():
	default:
		t.Fatal("expired guard did not publish its timeout")
	}

	claimed := newOpenAIFirstOutputDeadlineGuard(time.Now().Add(50 * time.Millisecond))
	require.True(t, claimed.claimOutput())
	time.Sleep(75 * time.Millisecond)
	require.False(t, claimed.timedOut())
	claimed.stop()
}

func TestOpenAIFirstOutputDeadlineCallbackFollowsWinningState(t *testing.T) {
	timedOut := make(chan struct{})
	expired := newOpenAIFirstOutputDeadlineGuardWithCallback(
		time.Now().Add(-time.Millisecond),
		func() { close(timedOut) },
	)
	defer expired.stop()
	require.False(t, expired.claimOutput())
	select {
	case <-timedOut:
	case <-time.After(time.Second):
		t.Fatal("deadline callback did not run after timeout won")
	}

	claimedCallback := make(chan struct{})
	claimed := newOpenAIFirstOutputDeadlineGuardWithCallback(
		time.Now().Add(40*time.Millisecond),
		func() { close(claimedCallback) },
	)
	require.True(t, claimed.claimOutput())
	time.Sleep(60 * time.Millisecond)
	select {
	case <-claimedCallback:
		t.Fatal("deadline callback ran after output had already claimed the guard")
	default:
	}
	claimed.stop()
}

func svcForCompatFirstOutputTests() *OpenAIGatewayService {
	return &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
}

func TestOpenAINativeFirstOutputTimeoutDisabledPreservesSynchronousStream(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		MaxLineSize: defaultMaxLineSize,
	}}}
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_disabled"}}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_disabled","usage":{"input_tokens":1,"output_tokens":1}}}`,
		"",
	}, "\n")))}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	result, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, openAIFirstOutputTestAccount(1), time.Now(), "model", "model")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), "response.completed")
}

func TestOpenAICompactionBypassesAdaptiveFirstOutputTimeout(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_compact"}}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_compact","usage":{"input_tokens":1,"output_tokens":1}}}`,
		"",
	}, "\n")))}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx := withCodexAdaptiveTestPolicy(c.Request.Context(), 1, 1, true)

	result, err := svc.handleStreamingResponse(ctx, resp, c, openAIFirstOutputTestAccount(1), time.Now().Add(-2*time.Second), "model", "model")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), "response.completed")
}

func TestOpenAINativeFirstOutputTimeoutIgnoresPreambleAndCleansReader(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		MaxLineSize: defaultMaxLineSize,
	}}}
	pr, pw := io.Pipe()
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_slow\"}}\n\n"))
		_, _ = pw.Write([]byte("data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_slow\"}}\n\n"))
		time.Sleep(200 * time.Millisecond)
	}()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	body := &firstOutputCloseTrackingBody{ReadCloser: pr, closed: make(chan struct{})}
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: body}

	ctx := withCodexAdaptiveTestPolicy(c.Request.Context(), 1, 1, false)
	_, err := svc.handleStreamingResponse(ctx, resp, c, openAIFirstOutputTestAccount(1), time.Now().Add(-2*time.Second), "model", "model")

	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusGatewayTimeout, failoverErr.StatusCode)
	require.Contains(t, string(failoverErr.ResponseBody), "first_output_timeout")
	require.True(t, failoverErr.SafeToFailoverAfterWrite)
	require.Empty(t, rec.Body.String())
	select {
	case <-body.closed:
	default:
		t.Fatal("first-output timeout did not close the upstream response body")
	}
	select {
	case <-writerDone:
	case <-time.After(time.Second):
		t.Fatal("stream reader/writer goroutine did not exit after first-output timeout")
	}
}

func TestOpenAIFirstOutputTimeoutForReasoningEffort(t *testing.T) {
	ctx := withCodexAdaptiveTestPolicy(context.Background(), 120, 300, false)
	account := openAIFirstOutputTestAccount(1)

	require.Equal(t, 120*time.Second, codexAdaptiveFirstOutputTimeout(ctx, account, "gpt-5.6", "low"))
	require.Equal(t, 300*time.Second, codexAdaptiveFirstOutputTimeout(ctx, account, "gpt-5.6", "high"))
	require.Equal(t, 300*time.Second, codexAdaptiveFirstOutputTimeout(ctx, account, "gpt-5.6", "xhigh"))
	require.Equal(t, 300*time.Second, codexAdaptiveFirstOutputTimeout(ctx, account, "gpt-5.6", "max"))
	require.Equal(t, 300*time.Second, codexAdaptiveFirstOutputTimeout(ctx, account, "gpt-5.6", "ultra"))
	require.Zero(t, codexAdaptiveFirstOutputTimeout(withCodexAdaptiveTestPolicy(ctx, 120, 300, true), account, "gpt-5.6", "high"))
}

func TestOpenAIFirstOutputStageDefaultLimitIsIndependentFromScannerLimit(t *testing.T) {
	stage := newDefaultOpenAIFirstOutputStage()
	defer func() { require.NoError(t, stage.Close()) }()

	require.EqualValues(t, 8*1024*1024, stage.limit)
	require.Greater(t, stage.limit, int64(68106))
	require.Less(t, stage.limit, int64(defaultMaxLineSize))
}

func TestOpenAIFirstOutputEventQueueSizeBackpressuresGuardedStreams(t *testing.T) {
	require.Equal(t, 1, openAIFirstOutputEventQueueSize(true))
	require.Equal(t, 16, openAIFirstOutputEventQueueSize(false))
}

func TestOpenAIFirstOutputDynamicScannerLimitsOnlyWhileGuardIsActive(t *testing.T) {
	var guardActive atomic.Bool
	guardActive.Store(true)
	split := openAIFirstOutputDynamicScanLines(&guardActive)
	guardLimit := openAIFirstOutputStageMaxBytes + openAIFirstOutputScannerFramingAllowance
	undelimited := bytes.Repeat([]byte("x"), guardLimit)

	_, _, err := split(undelimited, false)
	require.ErrorIs(t, err, errOpenAIFirstOutputScannerLimit)

	guardActive.Store(false)
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
	require.True(t, stage.closed)
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
	if runtime.GOOS == "windows" {
		require.Empty(t, path)
		require.Nil(t, stage.tempFile)
	} else {
		require.NotEmpty(t, path)
		require.NotNil(t, stage.tempFile)
		_, statErr := os.Stat(path)
		require.ErrorIs(t, statErr, os.ErrNotExist)
	}

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

func TestOpenAIFirstOutputStageUnlinkFailurePermanentlyFallsBackToMemoryAndRetriesCleanup(t *testing.T) {
	stage := newDefaultOpenAIFirstOutputStage()
	stage.memoryOnly = false
	t.Cleanup(func() {
		stage.removeFile = os.Remove
		_ = stage.Close()
	})
	createCalls := 0
	stage.createTemp = func() (*os.File, error) {
		createCalls++
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
	require.Equal(t, 1, createCalls)
	stat, statErr := os.Stat(stage.tempPath)
	require.NoError(t, statErr)
	require.Zero(t, stat.Size(), "failed-unlink fallback must never write plaintext to the named file")

	_, err = stage.WriteString("more")
	require.NoError(t, err)
	require.Equal(t, 1, createCalls, "memory-only fallback must not retry CreateTemp")
	path := stage.tempPath
	cleanupErr := stage.Close()
	require.ErrorContains(t, cleanupErr, "forced remove failure")
	require.Empty(t, stage.tempPath)
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, stage.Close())
}

func TestOpenAINativeFirstOutputTimeoutDisarmsAfterSemanticOutput(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{
		MaxLineSize: defaultMaxLineSize,
	}}
	svc := &OpenAIGatewayService{cfg: cfg, responseHeaderFilter: compileResponseHeaderFilter(cfg)}
	pr, pw := io.Pipe()
	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_ok\"}}\n\n"))
		_, _ = pw.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"))
		time.Sleep(1100 * time.Millisecond)
		_, _ = pw.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
	}()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{
		"X-Request-Id":                   []string{"request-winning"},
		"X-Ratelimit-Remaining-Requests": []string{"42"},
	}, Body: pr}

	ctx := withCodexAdaptiveTestPolicy(c.Request.Context(), 1, 1, false)
	result, err := svc.handleStreamingResponse(ctx, resp, c, openAIFirstOutputTestAccount(1), time.Now(), "model", "model")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.firstTokenMs)
	require.Contains(t, rec.Body.String(), "response.output_text.delta")
	require.Contains(t, rec.Body.String(), "response.completed")
	require.Equal(t, "request-winning", rec.Result().Header.Get("X-Request-Id"))
	require.Equal(t, "42", rec.Result().Header.Get("X-Ratelimit-Remaining-Requests"))
}

func TestOpenAIPassthroughFirstOutputTimeoutDisarmsAfterSemanticOutput(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	svc := &OpenAIGatewayService{cfg: cfg, responseHeaderFilter: compileResponseHeaderFilter(cfg)}
	pr, pw := io.Pipe()
	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"))
		time.Sleep(1100 * time.Millisecond)
		_, _ = pw.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
	}()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}
	ctx := withCodexAdaptiveTestPolicy(c.Request.Context(), 1, 1, false)

	started := time.Now()
	result, err := svc.handleStreamingResponsePassthrough(ctx, resp, c, openAIFirstOutputTestAccount(1), started, started, "model", "model", "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), "response.completed")
}

func TestOpenAIPassthroughFirstOutputUsesAttemptDeadlineAndHighEffortLimit(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
	stream := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_ok","usage":{"input_tokens":1,"output_tokens":1}}}`,
		"",
	}, "\n")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(stream))}
	ctx := withCodexAdaptiveTestPolicy(c.Request.Context(), 1, 2, false)
	requestStarted := time.Now().Add(-10 * time.Second)
	attemptStarted := time.Now().Add(-1500 * time.Millisecond)

	result, err := svc.handleStreamingResponsePassthrough(
		ctx, resp, c, openAIFirstOutputTestAccount(1), requestStarted, attemptStarted,
		"model", "model", "high",
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.firstTokenMs)
	require.GreaterOrEqual(t, *result.firstTokenMs, 10_000)
	require.Contains(t, rec.Body.String(), "response.completed")
}

func TestOpenAINativeFirstOutputTimeoutWaitsForCompleteSemanticEvent(t *testing.T) {
	const lineSize = 68106
	prefix := `data: {"type":"response.output_text.delta","delta":"`
	suffix := `"}`
	line := prefix + strings.Repeat("x", lineSize-len(prefix)-len(suffix)) + suffix
	require.Len(t, line, lineSize)
	assertOpenAINativeLargeOpenEventTimesOutWithoutLeak(t, line)
}

func TestOpenAINativeFirstOutputTimeoutDoesNotLeakLargePreambleEvent(t *testing.T) {
	const lineSize = 68106
	prefix := `data: {"type":"response.created","response":{"id":"resp_partial","padding":"`
	suffix := `"}}`
	line := prefix + strings.Repeat("x", lineSize-len(prefix)-len(suffix)) + suffix
	require.Len(t, line, lineSize)
	assertOpenAINativeLargeOpenEventTimesOutWithoutLeak(t, line)
}

func assertOpenAINativeLargeOpenEventTimesOutWithoutLeak(t *testing.T, line string) {
	t.Helper()
	cfg := &config.Config{Gateway: config.GatewayConfig{
		StreamKeepaliveInterval: 1,
		MaxLineSize:             defaultMaxLineSize,
	}}
	svc := &OpenAIGatewayService{cfg: cfg, responseHeaderFilter: compileResponseHeaderFilter(cfg)}
	pr, pw := io.Pipe()
	body := &firstOutputCloseTrackingBody{ReadCloser: pr, closed: make(chan struct{})}
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte(line + "\n"))
		select {
		case <-body.closed:
		case <-time.After(2 * time.Second):
		}
	}()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{
		"X-Request-Id":                   []string{"request-partial"},
		"X-Ratelimit-Remaining-Requests": []string{"1"},
	}, Body: body}

	ctx := withCodexAdaptiveTestPolicy(c.Request.Context(), 1, 1, false)
	_, err := svc.handleStreamingResponse(ctx, resp, c, openAIFirstOutputTestAccount(1), time.Now(), "model", "model")

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusGatewayTimeout, failoverErr.StatusCode)
	require.Contains(t, string(failoverErr.ResponseBody), "first_output_timeout")
	require.True(t, failoverErr.SafeToFailoverAfterWrite)
	require.NotContains(t, rec.Body.String(), "data:", "attempt JSON must remain private before the SSE boundary")
	require.NotContains(t, rec.Body.String(), `"type"`, "attempt JSON must remain private before the SSE boundary")
	for _, outputLine := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n") {
		if outputLine != "" {
			require.True(t, strings.HasPrefix(outputLine, ":"), "only keepalive comments may precede failover: %q", outputLine)
		}
	}
	require.Empty(t, rec.Header().Values("X-Request-Id"))
	require.Empty(t, rec.Header().Values("X-Ratelimit-Remaining-Requests"))
	select {
	case <-writerDone:
	case <-time.After(time.Second):
		t.Fatal("partial-event writer did not exit after timeout closed the body")
	}
}

func TestOpenAINativeFirstOutputEOFDispatchesTerminalEventWithoutBlankLine(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{
		MaxLineSize: defaultMaxLineSize,
	}}
	svc := &OpenAIGatewayService{cfg: cfg, responseHeaderFilter: compileResponseHeaderFilter(cfg)}
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

	ctx := withCodexAdaptiveTestPolicy(c.Request.Context(), 1, 1, false)
	result, err := svc.handleStreamingResponse(ctx, resp, c, openAIFirstOutputTestAccount(1), time.Now(), "model", "model")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Nil(t, result.firstTokenMs, "usage-only terminal event is not visible output")
	require.Equal(t, "resp_eof", result.responseID)
	require.Equal(t, 3, result.usage.InputTokens)
	require.Equal(t, 2, result.usage.OutputTokens)
	require.Contains(t, rec.Body.String(), `"type":"response.completed"`)
	require.Contains(t, rec.Body.String(), `"id":"resp_eof"`)
	require.True(t, strings.HasSuffix(rec.Body.String(), "\n"))
	require.False(t, strings.HasSuffix(rec.Body.String(), "\n\n"), "EOF dispatch must not synthesize a blank line")
	require.Equal(t, "request-eof", rec.Result().Header.Get("X-Request-Id"))
	require.Equal(t, "17", rec.Result().Header.Get("X-Ratelimit-Remaining-Requests"))
}

func TestOpenAINativeFirstOutputStageOverflowFailsOverWithoutAttemptBytes(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{
		MaxLineSize: 2 * 1024 * 1024,
	}}
	svc := &OpenAIGatewayService{cfg: cfg, responseHeaderFilter: compileResponseHeaderFilter(cfg)}
	const lineSize = 1024*1024 - 256
	prefix := `data: {"type":"response.output_text.delta","delta":"`
	suffix := `"}`
	line := prefix + strings.Repeat("x", lineSize-len(prefix)-len(suffix)) + suffix
	body := strings.Repeat(line+"\n", 9)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"X-Request-Id":                   []string{"request-overflow"},
			"X-Ratelimit-Remaining-Requests": []string{"1"},
		},
		Body: io.NopCloser(strings.NewReader(body)),
	}

	ctx := withCodexAdaptiveTestPolicy(c.Request.Context(), 30, 30, false)
	_, err := svc.handleStreamingResponse(ctx, resp, c, openAIFirstOutputTestAccount(1), time.Now(), "model", "model")

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.True(t, failoverErr.SafeToFailoverAfterWrite)
	require.Contains(t, string(failoverErr.ResponseBody), "staging limit exceeded")
	require.Empty(t, rec.Body.String())
	require.Empty(t, rec.Header().Values("X-Request-Id"))
	require.Empty(t, rec.Header().Values("X-Ratelimit-Remaining-Requests"))
}

func TestOpenAIPassthroughFirstOutputStageOverflowFailsOverWithoutAttemptBytes(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{
		MaxLineSize: 2 * 1024 * 1024,
	}}
	svc := &OpenAIGatewayService{cfg: cfg, responseHeaderFilter: compileResponseHeaderFilter(cfg)}
	const lineSize = 1024*1024 - 256
	prefix := `data: {"type":"response.created","response":{"id":"resp_private","padding":"`
	suffix := `"}}`
	line := prefix + strings.Repeat("x", lineSize-len(prefix)-len(suffix)) + suffix
	body := strings.Repeat(line+"\n", 9)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"X-Request-Id": []string{"request-passthrough-overflow"},
		},
		Body: io.NopCloser(strings.NewReader(body)),
	}

	ctx := withCodexAdaptiveTestPolicy(c.Request.Context(), 30, 30, false)
	_, err := svc.handleStreamingResponsePassthrough(
		ctx, resp, c, openAIFirstOutputTestAccount(1), time.Now(), time.Now(),
		"model", "model", "",
	)

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.True(t, failoverErr.SafeToFailoverAfterWrite)
	require.Contains(t, string(failoverErr.ResponseBody), "staging limit exceeded")
	require.Empty(t, rec.Body.String())
}

func TestOpenAINativeFirstOutputScannerRejectsOversizedLineWithoutLeak(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{
		MaxLineSize: defaultMaxLineSize,
	}}
	svc := &OpenAIGatewayService{cfg: cfg, responseHeaderFilter: compileResponseHeaderFilter(cfg)}
	oversizedLine := "data: " + strings.Repeat("x", openAIFirstOutputStageMaxBytes+openAIFirstOutputScannerFramingAllowance+1024)
	body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_private\"}}\n\n" + oversizedLine + "\n"
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"X-Request-Id":                   []string{"request-too-large"},
			"X-Ratelimit-Remaining-Requests": []string{"1"},
		},
		Body: io.NopCloser(strings.NewReader(body)),
	}

	ctx := withCodexAdaptiveTestPolicy(c.Request.Context(), 30, 30, false)
	_, err := svc.handleStreamingResponse(ctx, resp, c, openAIFirstOutputTestAccount(1), time.Now(), "model", "model")

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.True(t, failoverErr.SafeToFailoverAfterWrite)
	require.Contains(t, string(failoverErr.ResponseBody), "line exceeds guarded first-output limit")
	require.Empty(t, rec.Body.String())
	require.Empty(t, rec.Header().Values("X-Request-Id"))
	require.Empty(t, rec.Header().Values("X-Ratelimit-Remaining-Requests"))
}

func TestOpenAINativeFirstOutputScannerAllowsLargeEventAfterSemanticBoundary(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{
		MaxLineSize: defaultMaxLineSize,
	}}
	svc := &OpenAIGatewayService{cfg: cfg, responseHeaderFilter: compileResponseHeaderFilter(cfg)}
	largeDelta := strings.Repeat("i", openAIFirstOutputStageMaxBytes+openAIFirstOutputScannerFramingAllowance+1024)
	body := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"ready"}`,
		"",
		`data: {"type":"response.output_text.delta","delta":"` + largeDelta + `"}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_large_image","usage":{"input_tokens":4,"output_tokens":3}}}`,
		"",
	}, "\n")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"X-Request-Id": []string{"request-large-image"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	ctx := withCodexAdaptiveTestPolicy(c.Request.Context(), 30, 30, false)
	result, err := svc.handleStreamingResponse(ctx, resp, c, openAIFirstOutputTestAccount(1), time.Now(), "model", "model")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.firstTokenMs)
	require.Equal(t, "resp_large_image", result.responseID)
	require.Equal(t, 4, result.usage.InputTokens)
	require.Equal(t, 3, result.usage.OutputTokens)
	require.Contains(t, rec.Body.String(), `"delta":"ready"`)
	require.Contains(t, rec.Body.String(), `"id":"resp_large_image"`)
	require.Contains(t, rec.Body.String(), strings.Repeat("i", 1024))
	require.Equal(t, "request-large-image", rec.Result().Header.Get("X-Request-Id"))
}

func TestOpenAINativeFirstOutputTimeoutDisabledKeepsPreamblePrivateAcrossKeepalive(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		StreamKeepaliveInterval: 1,
		MaxLineSize:             defaultMaxLineSize,
	}}}
	pr, pw := io.Pipe()
	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_stalled\"}}\n\n"))
		_, _ = pw.Write([]byte("data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_stalled\"}}\n\n"))
		time.Sleep(2100 * time.Millisecond)
	}()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, openAIFirstOutputTestAccount(1), time.Now(), "model", "model")

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Contains(t, rec.Body.String(), ":\n\n")
	require.NotContains(t, rec.Body.String(), "response.created")
	require.NotContains(t, rec.Body.String(), "response.in_progress")
}

func TestOpenAINativeFirstOutputFailoverKeepsAttemptHeadersPrivateAfterKeepaliveCommit(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{
		StreamKeepaliveInterval: 1,
		MaxLineSize:             defaultMaxLineSize,
	}}
	svc := &OpenAIGatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	firstBody, firstWriter := io.Pipe()
	trackedFirstBody := &firstOutputCloseTrackingBody{ReadCloser: firstBody, closed: make(chan struct{})}
	firstWriterDone := make(chan struct{})
	go func() {
		defer close(firstWriterDone)
		defer func() { _ = firstWriter.Close() }()
		_, _ = firstWriter.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_first\"}}\n\n"))
		select {
		case <-trackedFirstBody.closed:
		case <-time.After(4 * time.Second):
		}
	}()
	firstResp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":                   []string{"text/event-stream"},
			"X-Request-Id":                   []string{"request-first"},
			"X-Ratelimit-Remaining-Requests": []string{"1"},
		},
		Body: trackedFirstBody,
	}

	ctx := withCodexAdaptiveTestPolicy(c.Request.Context(), 2, 2, false)
	_, firstErr := svc.handleStreamingResponse(ctx, firstResp, c, openAIFirstOutputTestAccount(1), time.Now(), "model", "model")
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, firstErr, &failoverErr)
	require.Contains(t, rec.Body.String(), ":\n\n", "first attempt should have committed only a stable keepalive")
	require.NotContains(t, rec.Body.String(), "resp_first")

	secondResp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":                   []string{"text/event-stream"},
			"X-Request-Id":                   []string{"request-second"},
			"X-Ratelimit-Remaining-Requests": []string{"99"},
		},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.output_text.delta","delta":"hello"}`,
			"",
			`data: {"type":"response.completed","response":{"id":"resp_second","usage":{"input_tokens":1,"output_tokens":1}}}`,
			"",
		}, "\n"))),
	}
	result, secondErr := svc.handleStreamingResponse(ctx, secondResp, c, openAIFirstOutputTestAccount(2), time.Now(), "model", "model")

	require.NoError(t, secondErr)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), "resp_second")
	wireHeaders := rec.Result().Header
	require.Empty(t, wireHeaders.Values("X-Request-Id"))
	require.Empty(t, wireHeaders.Values("X-Ratelimit-Remaining-Requests"))
	require.Empty(t, rec.Header().Values("X-Request-Id"))
	require.Empty(t, rec.Header().Values("X-Ratelimit-Remaining-Requests"))
	select {
	case <-firstWriterDone:
	case <-time.After(time.Second):
		t.Fatal("first account writer did not exit after timeout")
	}
}
