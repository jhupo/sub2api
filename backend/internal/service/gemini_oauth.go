package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
)

const (
	GeminiOAuthTypeAntigravity = "antigravity"
	GeminiOAuthTypeCodeAssist  = "code_assist"
	GeminiOAuthTypeAIStudio    = "ai_studio"
)

var ErrGeminiProjectIDRequired = errors.New("missing project_id for Gemini Cloud Code OAuth")

// NormalizeGeminiOAuthType validates the authorization/runtime identity used
// by Gemini accounts. Personal Google AI subscriptions default to Antigravity;
// enterprise Code Assist and custom AI Studio clients remain explicit modes.
func NormalizeGeminiOAuthType(raw string) (string, error) {
	oauthType := strings.ToLower(strings.TrimSpace(raw))
	if oauthType == "" {
		return GeminiOAuthTypeAntigravity, nil
	}
	switch oauthType {
	case GeminiOAuthTypeAntigravity, GeminiOAuthTypeCodeAssist, GeminiOAuthTypeAIStudio:
		return oauthType, nil
	default:
		return "", fmt.Errorf("unsupported Gemini OAuth type %q", raw)
	}
}

// GeminiOAuthClient performs Google OAuth token exchange/refresh for Gemini integration.
type GeminiOAuthClient interface {
	ExchangeCode(ctx context.Context, oauthType, code, codeVerifier, redirectURI, proxyURL string) (*geminicli.TokenResponse, error)
	RefreshToken(ctx context.Context, oauthType, refreshToken, proxyURL string) (*geminicli.TokenResponse, error)
	GetEmail(ctx context.Context, accessToken, proxyURL string) (string, error)
}
