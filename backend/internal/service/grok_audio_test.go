package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestBuildGrokVoiceURL_UsesAPIDefaultForCLIProxyBase(t *testing.T) {
	account := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"base_url": xai.DefaultCLIBaseURL,
		},
	}
	url, err := buildGrokVoiceURL(account, nil, "tts")
	require.NoError(t, err)
	require.Equal(t, xai.DefaultBaseURL+"/tts", url)

	url, err = buildGrokVoiceURL(account, nil, "realtime")
	require.NoError(t, err)
	require.Equal(t, xai.DefaultBaseURL+"/realtime", url)
}

func TestBuildGrokVoiceURL_EmptyBaseFallsBackToAPI(t *testing.T) {
	account := &Account{
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{},
	}
	url, err := buildGrokVoiceURL(account, nil, "stt")
	require.NoError(t, err)
	require.Equal(t, xai.DefaultBaseURL+"/stt", url)
}

func TestBuildGrokVoiceURL_RequiresEndpoint(t *testing.T) {
	account := &Account{Platform: PlatformGrok, Type: AccountTypeOAuth}
	_, err := buildGrokVoiceURL(account, nil, "  ")
	require.Error(t, err)
}

func TestBuildGrokVoiceURL_EncodesCustomVoicePathSegments(t *testing.T) {
	account := &Account{Platform: PlatformGrok, Type: AccountTypeOAuth}
	got, err := buildGrokVoiceURL(account, nil, "custom-voices/nlbqfwie/audio")
	require.NoError(t, err)
	require.Equal(t, xai.DefaultBaseURL+"/custom-voices/nlbqfwie/audio", got)

	_, err = buildGrokVoiceURL(account, nil, "custom-voices/../audio")
	require.Error(t, err)
}

func TestForwardGrokVoice_RejectsNonGrok(t *testing.T) {
	svc := &OpenAIGatewayService{}
	_, err := svc.ForwardGrokVoice(context.Background(), nil, &Account{Platform: PlatformOpenAI}, "tts", []byte(`{}`), "application/json")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not supported")
}

func TestAwaitGrokRealtimeAudioObservedReadsFlagAfterRelayExits(t *testing.T) {
	errCh := make(chan error, 1)
	var observed atomic.Bool
	go func() {
		observed.Store(true)
		errCh <- io.EOF
	}()
	got, err := awaitGrokRealtimeAudioObserved(errCh, &observed)
	require.ErrorIs(t, err, io.EOF)
	require.True(t, got, "audioObserved must be read after the relay returns, not before <-errCh")
}

func TestAwaitGrokRealtimeAudioObservedWithReservationTicksUntilRelayEnds(t *testing.T) {
	errCh := make(chan error, 1)
	ticks := make(chan time.Time, 2)
	var observed atomic.Bool
	var calls atomic.Int32
	started := time.Unix(1000, 0)
	ticks <- started.Add(5 * time.Second)
	ticks <- started.Add(10 * time.Second)

	got, err := awaitGrokRealtimeAudioObservedWithReservation(context.Background(), errCh, &observed, ticks, started, func(_ context.Context, elapsed time.Duration) error {
		if calls.Add(1) == 2 {
			errCh <- io.EOF
		}
		require.Greater(t, elapsed, time.Duration(0))
		return nil
	})
	require.ErrorIs(t, err, io.EOF)
	require.False(t, got)
	require.Equal(t, int32(2), calls.Load())
}

func TestAwaitGrokRealtimeAudioObservedWithReservationFailsPromptly(t *testing.T) {
	ticks := make(chan time.Time, 1)
	started := time.Unix(1000, 0)
	ticks <- started.Add(5 * time.Second)
	var observed atomic.Bool
	observed.Store(true)

	got, err := awaitGrokRealtimeAudioObservedWithReservation(context.Background(), make(chan error), &observed, ticks, started, func(context.Context, time.Duration) error {
		return errors.New("insufficient balance")
	})
	require.True(t, got)
	require.ErrorIs(t, err, ErrGrokRealtimeReservationFailed)
}

func TestGrokRealtimeEventHasAudio(t *testing.T) {
	require.False(t, grokRealtimeEventHasAudio([]byte(`{"type":"session.created"}`)))
	require.False(t, grokRealtimeEventHasAudio([]byte(`{"type":"response.audio_transcript.delta","delta":"hi"}`)))
	require.False(t, grokRealtimeEventHasAudio([]byte(`{"type":"response.audio.delta","delta":""}`)))
	require.True(t, grokRealtimeEventHasAudio([]byte(`{"type":"response.audio.delta","delta":"abc"}`)))
	require.True(t, grokRealtimeEventHasAudio([]byte(`{"type":"response.output_audio.delta","audio":"abc"}`)))
}

func TestForwardGrokVoice_RejectsUnknownEndpoint(t *testing.T) {
	svc := &OpenAIGatewayService{}
	_, err := svc.ForwardGrokVoice(context.Background(), nil, &Account{Platform: PlatformGrok}, "unknown", []byte(`{}`), "application/json")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported")
}

func TestForwardGrokVoiceBuffersSuccessfulResponseUntilCommit(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"input":"hello"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/tts", strings.NewReader(string(body)))
	c.Request.Header.Set("Content-Type", "application/json")

	account := &Account{
		ID:          70,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "api-key",
			"base_url": "https://xai.test/v1",
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusCreated,
		Header: http.Header{
			"Content-Type":   []string{"audio/mpeg"},
			"Xai-Request-Id": []string{"voice-request-123"},
		},
		Body: io.NopCloser(strings.NewReader("voice-bytes")),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.ForwardGrokVoice(context.Background(), c, account, "tts", body, "application/json")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.AudioUsage)
	require.False(t, c.Writer.Written())
	require.Empty(t, recorder.Body.String())

	require.NoError(t, svc.CommitGrokVoiceResponse(c, result))
	require.Equal(t, http.StatusCreated, recorder.Code)
	require.Equal(t, "audio/mpeg", recorder.Header().Get("Content-Type"))
	require.Equal(t, "voice-bytes", recorder.Body.String())
	require.Error(t, svc.CommitGrokVoiceResponse(c, result))
}

func TestEstimateGrokVoiceAudioUsageUsesRequestFloors(t *testing.T) {
	tts := EstimateGrokVoiceAudioUsage("tts", []byte(`{"input":"hello"}`), "application/json", nil, 0)
	require.NotNil(t, tts)
	require.Equal(t, "tts", tts.Mode)
	require.InDelta(t, 5.0/1_000_000.0, tts.DurationOrUnits, 1e-12)

	// 32KB is a two-second request-size floor. A client claim of one second
	// must not lower the preauthorization estimate.
	body := append([]byte(`{"duration_seconds":1,"audio":"`), bytes.Repeat([]byte("a"), 32_000)...)
	body = append(body, []byte(`"}`)...)
	stt := EstimateGrokVoiceAudioUsage("stt", body, "application/json", nil, 0)
	require.NotNil(t, stt)
	require.GreaterOrEqual(t, stt.DurationOrUnits, 2.0/3600.0)

	post := EstimateGrokVoiceAudioUsage("stt", body, "application/json", []byte(`{"duration_seconds":3}`), time.Second)
	require.GreaterOrEqual(t, post.DurationOrUnits, 3.0/3600.0)
}
