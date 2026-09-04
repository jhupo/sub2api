package antigravity

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
)

const codeAssistGoogAPIClient = "google-cloud-sdk vscode_cloudshelleditor/0.1"

// ApplyCodeAssistRequestHeaders applies the client identity used by current
// Antigravity Code Assist requests.
func ApplyCodeAssistRequestHeaders(req *http.Request, ctx context.Context, accessToken, accept string) {
	if req == nil {
		return
	}
	for key, values := range CodeAssistRequestHeaders(ctx, accessToken, accept) {
		if len(values) > 0 {
			req.Header.Set(key, values[0])
		}
	}
}

// CodeAssistRequestHeaders returns the Code Assist client identity headers in
// a transport-neutral form for HTTP clients that do not expose *http.Request.
func CodeAssistRequestHeaders(ctx context.Context, accessToken, accept string) http.Header {
	platform := "LINUX"
	switch runtime.GOOS {
	case "darwin":
		platform = "MACOS"
	case "windows":
		platform = "WINDOWS"
	}
	metadata, _ := json.Marshal(map[string]string{
		"ideType":    "ANTIGRAVITY",
		"platform":   platform,
		"pluginType": "GEMINI",
	})

	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+accessToken)
	headers.Set("Content-Type", "application/json")
	if accept != "" {
		headers.Set("Accept", accept)
	}
	headers.Set("User-Agent", GetUserAgentForContext(ctx))
	headers.Set("X-Goog-Api-Client", codeAssistGoogAPIClient)
	headers.Set("Client-Metadata", string(metadata))
	return headers
}
