package repository

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/imroc/req/v3"
)

type geminiOAuthClient struct {
	tokenURL string
	cfg      *config.Config
}

func NewGeminiOAuthClient(cfg *config.Config) service.GeminiOAuthClient {
	return &geminiOAuthClient{
		tokenURL: geminicli.TokenURL,
		cfg:      cfg,
	}
}

func (c *geminiOAuthClient) GetEmail(ctx context.Context, accessToken, proxyURL string) (string, error) {
	client, err := antigravity.NewClient(proxyURL)
	if err != nil {
		return "", err
	}
	info, err := client.GetUserInfo(ctx, accessToken)
	if err != nil {
		return "", err
	}
	return info.Email, nil
}

func (c *geminiOAuthClient) ExchangeCode(ctx context.Context, oauthType, code, codeVerifier, redirectURI, proxyURL string) (*geminicli.TokenResponse, error) {
	if oauthType == service.GeminiOAuthTypeAntigravity {
		client, err := antigravity.NewClient(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("create Antigravity OAuth client: %w", err)
		}
		token, err := client.ExchangeCode(ctx, code, codeVerifier)
		if err != nil {
			return nil, err
		}
		return geminiTokenResponseFromAntigravity(token), nil
	}

	client, err := createGeminiReqClient(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}

	oauthCfgInput := geminicli.OAuthConfig{
		ClientID:     c.cfg.Gemini.OAuth.ClientID,
		ClientSecret: c.cfg.Gemini.OAuth.ClientSecret,
		Scopes:       c.cfg.Gemini.OAuth.Scopes,
	}
	oauthCfg, err := geminicli.EffectiveOAuthConfig(oauthCfgInput, oauthType)
	if err != nil {
		return nil, err
	}

	formData := url.Values{}
	formData.Set("grant_type", "authorization_code")
	formData.Set("client_id", oauthCfg.ClientID)
	formData.Set("client_secret", oauthCfg.ClientSecret)
	formData.Set("code", code)
	formData.Set("code_verifier", codeVerifier)
	formData.Set("redirect_uri", redirectURI)

	var tokenResp geminicli.TokenResponse
	resp, err := client.R().
		SetContext(ctx).
		SetFormDataFromValues(formData).
		SetSuccessResult(&tokenResp).
		Post(c.tokenURL)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if !resp.IsSuccessState() {
		return nil, fmt.Errorf("token exchange failed: status %d, body: %s", resp.StatusCode, geminicli.SanitizeBodyForLogs(resp.String()))
	}
	return &tokenResp, nil
}

func (c *geminiOAuthClient) RefreshToken(ctx context.Context, oauthType, refreshToken, proxyURL string) (*geminicli.TokenResponse, error) {
	if oauthType == service.GeminiOAuthTypeAntigravity {
		client, err := antigravity.NewClient(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("create Antigravity OAuth client: %w", err)
		}
		token, err := client.RefreshToken(ctx, refreshToken)
		if err != nil {
			return nil, err
		}
		return geminiTokenResponseFromAntigravity(token), nil
	}

	client, err := createGeminiReqClient(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}

	oauthCfgInput := geminicli.OAuthConfig{
		ClientID:     c.cfg.Gemini.OAuth.ClientID,
		ClientSecret: c.cfg.Gemini.OAuth.ClientSecret,
		Scopes:       c.cfg.Gemini.OAuth.Scopes,
	}
	oauthCfg, err := geminicli.EffectiveOAuthConfig(oauthCfgInput, oauthType)
	if err != nil {
		return nil, err
	}

	formData := url.Values{}
	formData.Set("grant_type", "refresh_token")
	formData.Set("refresh_token", refreshToken)
	formData.Set("client_id", oauthCfg.ClientID)
	formData.Set("client_secret", oauthCfg.ClientSecret)

	var tokenResp geminicli.TokenResponse
	resp, err := client.R().
		SetContext(ctx).
		SetFormDataFromValues(formData).
		SetSuccessResult(&tokenResp).
		Post(c.tokenURL)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if !resp.IsSuccessState() {
		return nil, fmt.Errorf("token refresh failed: status %d, body: %s", resp.StatusCode, geminicli.SanitizeBodyForLogs(resp.String()))
	}
	return &tokenResp, nil
}

func geminiTokenResponseFromAntigravity(token *antigravity.TokenResponse) *geminicli.TokenResponse {
	if token == nil {
		return nil
	}
	return &geminicli.TokenResponse{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		TokenType:    token.TokenType,
		ExpiresIn:    token.ExpiresIn,
		Scope:        token.Scope,
	}
}

func createGeminiReqClient(proxyURL string) (*req.Client, error) {
	return getSharedReqClient(reqClientOptions{
		ProxyURL: proxyURL,
		Timeout:  60 * time.Second,
	})
}
