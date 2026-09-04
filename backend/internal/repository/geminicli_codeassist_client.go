package repository

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/googleapi"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/imroc/req/v3"
)

type geminiCliCodeAssistClient struct {
	baseURL string
}

func NewGeminiCliCodeAssistClient() service.GeminiCliCodeAssistClient {
	return &geminiCliCodeAssistClient{baseURL: geminicli.GeminiCliBaseURL}
}

func (c *geminiCliCodeAssistClient) endpointURLs() []string {
	if c == nil {
		return nil
	}
	// Keep an explicitly injected endpoint usable by tests and private
	// gateways; the production default follows the current Code Assist set.
	if endpoint := strings.TrimSpace(c.baseURL); endpoint != "" && endpoint != geminicli.GeminiCliBaseURL {
		return []string{endpoint}
	}
	return antigravity.CodeAssistBaseURLs()
}

func shouldTryNextCodeAssistEndpoint(statusCode int) bool {
	return statusCode == http.StatusRequestTimeout ||
		statusCode == http.StatusNotFound ||
		statusCode == http.StatusTooManyRequests ||
		statusCode >= http.StatusInternalServerError
}

func setCodeAssistHeaders(request *req.Request, ctx context.Context, accessToken, accept string) {
	if request == nil {
		return
	}
	for key, values := range antigravity.CodeAssistRequestHeaders(ctx, accessToken, accept) {
		if len(values) > 0 {
			request.SetHeader(key, values[0])
		}
	}
}

func (c *geminiCliCodeAssistClient) LoadCodeAssist(ctx context.Context, accessToken, proxyURL string, reqBody *geminicli.LoadCodeAssistRequest) (*geminicli.LoadCodeAssistResponse, error) {
	if reqBody == nil {
		reqBody = defaultLoadCodeAssistRequest()
	}

	client, err := createGeminiCliReqClient(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}

	var lastErr error
	var lastBody string
	for _, endpoint := range c.endpointURLs() {
		var out geminicli.LoadCodeAssistResponse
		request := client.R().SetContext(ctx).SetBody(reqBody).SetSuccessResult(&out)
		setCodeAssistHeaders(request, ctx, accessToken, "application/json")
		resp, requestErr := request.Post(strings.TrimRight(endpoint, "/") + "/v1internal:loadCodeAssist")
		if requestErr != nil {
			fmt.Printf("[CodeAssist] LoadCodeAssist request error (%s): %v\n", endpoint, requestErr)
			lastErr = fmt.Errorf("request failed: %w", requestErr)
			continue
		}
		if resp.IsSuccessState() {
			fmt.Printf("[CodeAssist] LoadCodeAssist success: status %d, response: %+v\n", resp.StatusCode, out)
			return &out, nil
		}
		lastBody = resp.String()
		sanitizedBody := geminicli.SanitizeBodyForLogs(lastBody)
		fmt.Printf("[CodeAssist] LoadCodeAssist failed (%s): status %d, body: %s\n", endpoint, resp.StatusCode, sanitizedBody)
		lastErr = fmt.Errorf("loadCodeAssist failed: status %d, body: %s", resp.StatusCode, sanitizedBody)
		if !shouldTryNextCodeAssistEndpoint(resp.StatusCode) {
			break
		}
	}
	if googleapi.IsServiceDisabledError(lastBody) {
		if activationURL := googleapi.ExtractActivationURL(lastBody); activationURL != "" {
			return nil, fmt.Errorf("gemini API not enabled for this project, please enable it by visiting: %s\n\nAfter enabling it, wait a few minutes for propagation", activationURL)
		}
		return nil, fmt.Errorf("gemini API not enabled for this project, please enable it in the Google Cloud Console at: https://console.cloud.google.com/apis/library/cloudaicompanion.googleapis.com")
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("loadCodeAssist failed: no endpoint available")
	}
	return nil, lastErr
}

func (c *geminiCliCodeAssistClient) OnboardUser(ctx context.Context, accessToken, proxyURL string, reqBody *geminicli.OnboardUserRequest) (*geminicli.OnboardUserResponse, error) {
	if reqBody == nil {
		reqBody = defaultOnboardUserRequest()
	}

	fmt.Printf("[CodeAssist] OnboardUser request body: %+v\n", reqBody)

	client, err := createGeminiCliReqClient(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}

	var lastErr error
	var lastBody string
	for _, endpoint := range c.endpointURLs() {
		var out geminicli.OnboardUserResponse
		request := client.R().SetContext(ctx).SetBody(reqBody).SetSuccessResult(&out)
		setCodeAssistHeaders(request, ctx, accessToken, "application/json")
		resp, requestErr := request.Post(strings.TrimRight(endpoint, "/") + "/v1internal:onboardUser")
		if requestErr != nil {
			fmt.Printf("[CodeAssist] OnboardUser request error (%s): %v\n", endpoint, requestErr)
			lastErr = fmt.Errorf("request failed: %w", requestErr)
			continue
		}
		if resp.IsSuccessState() {
			fmt.Printf("[CodeAssist] OnboardUser success: status %d, response: %+v\n", resp.StatusCode, out)
			return &out, nil
		}
		lastBody = resp.String()
		sanitizedBody := geminicli.SanitizeBodyForLogs(lastBody)
		fmt.Printf("[CodeAssist] OnboardUser failed (%s): status %d, body: %s\n", endpoint, resp.StatusCode, sanitizedBody)
		lastErr = fmt.Errorf("onboardUser failed: status %d, body: %s", resp.StatusCode, sanitizedBody)
		if !shouldTryNextCodeAssistEndpoint(resp.StatusCode) {
			break
		}
	}
	if googleapi.IsServiceDisabledError(lastBody) {
		if activationURL := googleapi.ExtractActivationURL(lastBody); activationURL != "" {
			return nil, fmt.Errorf("gemini API not enabled for this project, please enable it by visiting: %s\n\nAfter enabling it, wait a few minutes for propagation", activationURL)
		}
		return nil, fmt.Errorf("gemini API not enabled for this project, please enable it in the Google Cloud Console at: https://console.cloud.google.com/apis/library/cloudaicompanion.googleapis.com")
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("onboardUser failed: no endpoint available")
	}
	return nil, lastErr
}

func createGeminiCliReqClient(proxyURL string) (*req.Client, error) {
	return getSharedReqClient(reqClientOptions{
		ProxyURL: proxyURL,
		Timeout:  30 * time.Second,
	})
}

func defaultLoadCodeAssistRequest() *geminicli.LoadCodeAssistRequest {
	return &geminicli.LoadCodeAssistRequest{
		Metadata: geminicli.LoadCodeAssistMetadata{
			IDEType:    "ANTIGRAVITY",
			Platform:   "PLATFORM_UNSPECIFIED",
			PluginType: "GEMINI",
		},
	}
}

func defaultOnboardUserRequest() *geminicli.OnboardUserRequest {
	return &geminicli.OnboardUserRequest{
		TierID: "LEGACY",
		Metadata: geminicli.LoadCodeAssistMetadata{
			IDEType:    "ANTIGRAVITY",
			Platform:   "PLATFORM_UNSPECIFIED",
			PluginType: "GEMINI",
		},
	}
}
