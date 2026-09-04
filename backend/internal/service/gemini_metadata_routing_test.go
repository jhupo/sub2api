//go:build unit

package service

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCollapseGeminiCodeAssistModelsKeepsEveryAuthorizedPublicModel(t *testing.T) {
	models := collapseGeminiCodeAssistModels(map[string]antigravity.ModelInfo{
		"gemini-3.1-flash-image": {DisplayName: "Gemini 3.1 Flash Image"},
		"veo-3.1-generate":       {DisplayName: "Veo 3.1"},
	})

	require.Len(t, models, 2)
	require.Equal(t, "gemini-3.1-flash-image", models[0].ID)
	require.Equal(t, "veo-3.1-generate", models[1].ID)
}

func TestPrepareGeminiImageGenerationRequestAddsOnlyMissingDefaults(t *testing.T) {
	body, err := prepareGeminiImageGenerationRequest(
		[]byte(`{"contents":[{"parts":[{"text":"draw"}]}],"generationConfig":{"imageConfig":{"aspectRatio":"16:9"}}}`),
		"gemini-3.1-flash-image",
	)
	require.NoError(t, err)

	var request map[string]any
	require.NoError(t, json.Unmarshal(body, &request))
	generationConfig, ok := request["generationConfig"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, []any{"TEXT", "IMAGE"}, generationConfig["responseModalities"])
	imageConfig, ok := generationConfig["imageConfig"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "16:9", imageConfig["aspectRatio"])
}

func TestPrepareGeminiImageGenerationRequestDefaultsEmptyImageConfig(t *testing.T) {
	body, err := prepareGeminiImageGenerationRequest(
		[]byte(`{"contents":[{"parts":[{"text":"draw"}]}],"generationConfig":{"imageConfig":{}}}`),
		"gemini-3.1-flash-image",
	)
	require.NoError(t, err)

	var request map[string]any
	require.NoError(t, json.Unmarshal(body, &request))
	generationConfig := request["generationConfig"].(map[string]any)
	imageConfig := generationConfig["imageConfig"].(map[string]any)
	require.Equal(t, "1:1", imageConfig["aspectRatio"])
}

func TestGeminiInlineImageMarkdownSupportsCloudCodeResponse(t *testing.T) {
	markdown, _, ok := geminiInlineImageMarkdown(map[string]any{
		"inlineData": map[string]any{
			"mimeType": "image/png",
			"data":     "aW1hZ2U=",
		},
	})

	require.True(t, ok)
	require.Equal(t, "![image](data:image/png;base64,aW1hZ2U=)", markdown)
}

func TestGeminiInlineImageMarkdownRejectsInvalidPayload(t *testing.T) {
	_, _, ok := geminiInlineImageMarkdown(map[string]any{
		"inlineData": map[string]any{
			"mimeType": "image/png",
			"data":     "not-base64",
		},
	})

	require.False(t, ok)
}

func TestGeminiProjectIDErrorUsesStableIdentity(t *testing.T) {
	_, err := buildGeminiCodeAssistRequestBody("", "gemini-3.1-pro", []byte(`{"contents":[]}`))
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrGeminiProjectIDRequired))
}

func TestGeminiImageStreamingIsExposedByOpenAIAndClaudeProtocols(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const upstream = "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"inlineData\":{\"mimeType\":\"image/png\",\"data\":\"aW1hZ2U=\"}}]},\"finishReason\":\"STOP\"}]}}\n\ndata: [DONE]\n\n"

	tests := []struct {
		name    string
		forward func(*GeminiMessagesCompatService, *gin.Context, *http.Response) error
	}{
		{
			name: "OpenAI Chat Completions",
			forward: func(svc *GeminiMessagesCompatService, c *gin.Context, resp *http.Response) error {
				_, err := svc.handleOpenAIStreamingResponseFromGemini(c, resp, time.Now(), "gemini-3.1-flash-image", true, false, geminiOpenAICompatChatCompletions)
				return err
			},
		},
		{
			name: "Claude Messages",
			forward: func(svc *GeminiMessagesCompatService, c *gin.Context, resp *http.Response) error {
				_, err := svc.handleStreamingResponse(c, resp, time.Now(), "gemini-3.1-flash-image", true)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(upstream)),
			}

			err := tt.forward(&GeminiMessagesCompatService{}, c, resp)

			require.NoError(t, err)
			require.Contains(t, recorder.Body.String(), "data:image/png;base64,aW1hZ2U=")
		})
	}
}
