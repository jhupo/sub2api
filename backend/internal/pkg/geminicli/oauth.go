package geminicli

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	Scopes       string
}

type OAuthSession struct {
	State        string `json:"state"`
	CodeVerifier string `json:"code_verifier"`
	ProxyURL     string `json:"proxy_url,omitempty"`
	RedirectURI  string `json:"redirect_uri"`
	ProjectID    string `json:"project_id,omitempty"`
	// TierID is a user-selected fallback tier.
	// For OAuth types that support auto detection, the server will prefer
	// the detected tier and fall back to TierID when detection fails.
	TierID    string    `json:"tier_id,omitempty"`
	OAuthType string    `json:"oauth_type"`
	CreatedAt time.Time `json:"created_at"`
}

type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*OAuthSession
	stopCh   chan struct{}
}

func NewSessionStore() *SessionStore {
	store := &SessionStore{
		sessions: make(map[string]*OAuthSession),
		stopCh:   make(chan struct{}),
	}
	go store.cleanup()
	return store
}

func (s *SessionStore) Set(sessionID string, session *OAuthSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = session
}

func (s *SessionStore) Get(sessionID string) (*OAuthSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, false
	}
	if time.Since(session.CreatedAt) > SessionTTL {
		return nil, false
	}
	return session, true
}

func (s *SessionStore) Delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
}

func (s *SessionStore) Stop() {
	select {
	case <-s.stopCh:
		return
	default:
		close(s.stopCh)
	}
}

func (s *SessionStore) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.mu.Lock()
			for id, session := range s.sessions {
				if time.Since(session.CreatedAt) > SessionTTL {
					delete(s.sessions, id)
				}
			}
			s.mu.Unlock()
		}
	}
}

func GenerateRandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func GenerateState() (string, error) {
	bytes, err := GenerateRandomBytes(32)
	if err != nil {
		return "", err
	}
	return base64URLEncode(bytes), nil
}

func GenerateSessionID() (string, error) {
	bytes, err := GenerateRandomBytes(16)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// GenerateCodeVerifier returns an RFC 7636 compatible code verifier (43+ chars).
func GenerateCodeVerifier() (string, error) {
	bytes, err := GenerateRandomBytes(32)
	if err != nil {
		return "", err
	}
	return base64URLEncode(bytes), nil
}

func GenerateCodeChallenge(verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return base64URLEncode(hash[:])
}

func base64URLEncode(data []byte) string {
	return strings.TrimRight(base64.URLEncoding.EncodeToString(data), "=")
}

// EffectiveOAuthConfig applies the credential policy for Gemini CLI OAuth.
// Code Assist always uses the built-in CLI client. AI Studio always uses the
// explicitly configured custom client. Antigravity is handled by its own package.
func EffectiveOAuthConfig(cfg OAuthConfig, oauthType string) (OAuthConfig, error) {
	oauthType = strings.ToLower(strings.TrimSpace(oauthType))
	if oauthType != "code_assist" && oauthType != "ai_studio" {
		return OAuthConfig{}, infraerrors.Newf(http.StatusBadRequest, "GEMINI_OAUTH_TYPE_INVALID", "unsupported Gemini CLI OAuth type %q", oauthType)
	}

	effective := OAuthConfig{
		ClientID:     strings.TrimSpace(cfg.ClientID),
		ClientSecret: strings.TrimSpace(cfg.ClientSecret),
		Scopes:       strings.TrimSpace(cfg.Scopes),
	}

	// Normalize scopes: allow comma-separated input but send space-delimited scopes to Google.
	if effective.Scopes != "" {
		effective.Scopes = strings.Join(strings.Fields(strings.ReplaceAll(effective.Scopes, ",", " ")), " ")
	}

	if oauthType == "code_assist" {
		secret := strings.TrimSpace(GeminiCLIOAuthClientSecret)
		if secret == "" {
			if v, ok := os.LookupEnv(GeminiCLIOAuthClientSecretEnv); ok {
				secret = strings.TrimSpace(v)
			}
		}
		if secret == "" {
			return OAuthConfig{}, infraerrors.Newf(http.StatusBadRequest, "GEMINI_CLI_OAUTH_CLIENT_SECRET_MISSING", "built-in Gemini CLI OAuth client_secret is not configured; set %s", GeminiCLIOAuthClientSecretEnv)
		}
		effective.ClientID = GeminiCLIOAuthClientID
		effective.ClientSecret = secret
	} else if effective.ClientID == "" || effective.ClientSecret == "" {
		return OAuthConfig{}, infraerrors.New(http.StatusBadRequest, "GEMINI_OAUTH_CLIENT_NOT_CONFIGURED", "AI Studio OAuth requires a custom OAuth Client; set both client_id and client_secret")
	} else if effective.ClientID == GeminiCLIOAuthClientID {
		return OAuthConfig{}, infraerrors.New(http.StatusBadRequest, "GEMINI_AI_STUDIO_CUSTOM_CLIENT_REQUIRED", "AI Studio OAuth requires a custom OAuth client")
	}

	if effective.Scopes == "" {
		if oauthType == "code_assist" {
			effective.Scopes = DefaultCodeAssistScopes
		} else {
			effective.Scopes = DefaultAIStudioScopes
		}
	} else if oauthType == "code_assist" {
		// Google's built-in CLI client rejects Drive and Generative Language scopes.
		parts := strings.Fields(effective.Scopes)
		filtered := make([]string, 0, len(parts))
		for _, s := range parts {
			if hasRestrictedScope(s) {
				continue
			}
			filtered = append(filtered, s)
		}
		if len(filtered) == 0 {
			effective.Scopes = DefaultCodeAssistScopes
		} else {
			effective.Scopes = strings.Join(filtered, " ")
		}
	}

	return effective, nil
}

func hasRestrictedScope(scope string) bool {
	return strings.HasPrefix(scope, "https://www.googleapis.com/auth/generative-language") ||
		strings.HasPrefix(scope, "https://www.googleapis.com/auth/drive")
}

func BuildAuthorizationURL(cfg OAuthConfig, state, codeChallenge, redirectURI, projectID, oauthType string) (string, error) {
	effectiveCfg, err := EffectiveOAuthConfig(cfg, oauthType)
	if err != nil {
		return "", err
	}
	redirectURI = strings.TrimSpace(redirectURI)
	if redirectURI == "" {
		return "", fmt.Errorf("redirect_uri is required")
	}

	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", effectiveCfg.ClientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("scope", effectiveCfg.Scopes)
	params.Set("state", state)
	params.Set("code_challenge", codeChallenge)
	params.Set("code_challenge_method", "S256")
	params.Set("access_type", "offline")
	params.Set("prompt", "consent")
	params.Set("include_granted_scopes", "true")
	if strings.TrimSpace(projectID) != "" {
		params.Set("project_id", strings.TrimSpace(projectID))
	}

	return fmt.Sprintf("%s?%s", AuthorizeURL, params.Encode()), nil
}
