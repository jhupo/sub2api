package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const codexAccountIdentityNamespaceVersion = "v1"

const codexAccountIdentitySourceContextKey = "openai_codex_account_identity_source"

type codexAccountIdentitySourceSnapshot struct {
	selectedID int64
	source     *Account
}

// prepareCodexAccountIdentitySource resolves credential shadows once per selected
// attempt. The handler reuses gin.Context across failover attempts, so every entry
// point overwrites the staged source before projecting outbound identity.
func (s *OpenAIGatewayService) prepareCodexAccountIdentitySource(ctx context.Context, c *gin.Context, account *Account) (*Account, error) {
	source := account
	if account != nil && account.IsShadow() {
		resolved, err := resolveCredentialAccount(ctx, s.accountRepo, account)
		if err != nil {
			return nil, err
		}
		source = resolved
	}
	if c != nil {
		staged := codexAccountIdentitySourceSnapshot{source: source}
		if account != nil {
			staged.selectedID = account.ID
		}
		c.Set(codexAccountIdentitySourceContextKey, staged)
	}
	return source, nil
}

func codexAccountIdentitySource(c *gin.Context, fallback *Account) *Account {
	if c != nil {
		if staged, ok := c.Get(codexAccountIdentitySourceContextKey); ok {
			if source, ok := staged.(codexAccountIdentitySourceSnapshot); ok && source.source != nil && fallback != nil && source.selectedID == fallback.ID {
				return source.source
			}
		}
	}
	return fallback
}

// codexAccountIdentityNamespace returns a stable, credential-scoped namespace.
// Multiple local rows that use the same ChatGPT account intentionally share the
// same namespace. Setup tokens use an irreversible bearer fingerprint because
// they have no refresh lifecycle or imported account metadata. Refreshable OAuth
// otherwise falls back only to a persistent fingerprint seed: local row IDs are
// deployment-relative and must never become upstream identity.
func codexAccountIdentityNamespace(account *Account) string {
	if account == nil || !account.IsOpenAIOAuthLike() {
		return ""
	}
	if upstreamAccountID := strings.TrimSpace(account.GetChatGPTAccountID()); upstreamAccountID != "" {
		if upstreamUserID := strings.TrimSpace(account.GetCredential("chatgpt_user_id")); upstreamUserID != "" {
			return "chatgpt:" + upstreamAccountID + ":user:" + upstreamUserID
		}
		return "chatgpt:" + upstreamAccountID
	}
	if seed, ok := codexFingerprintSeed(account.Extra); ok {
		return "seed:" + seed
	}
	if account.Type == AccountTypeSetupToken {
		if token := strings.TrimSpace(account.GetOpenAIAccessToken()); token != "" {
			sum := sha256.Sum256([]byte("openai-setup-token:" + token))
			return fmt.Sprintf("setup-token:%x", sum[:16])
		}
	}
	return ""
}

type codexAccountIdentityScope struct {
	namespace string
	apiKeyID  int64
}

func newCodexAccountIdentityScope(account *Account, apiKeyID int64) codexAccountIdentityScope {
	return codexAccountIdentityScope{namespace: codexAccountIdentityNamespace(account), apiKeyID: apiKeyID}
}

func (s codexAccountIdentityScope) sessionID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	namespace := s.namespace
	if namespace == "" {
		return isolateOpenAISessionID(s.apiKeyID, raw)
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("u%d:a%s:%s", s.apiKeyID, namespace, raw)))
	return fmt.Sprintf("%x", sum[:8])
}

func (s codexAccountIdentityScope) value(kind, raw string) string {
	raw = strings.TrimSpace(raw)
	namespace := s.namespace
	if raw == "" || namespace == "" {
		return raw
	}
	return deriveStableUUIDv4(fmt.Sprintf(
		"sub2api:codex-account-identity:%s:user:%d:account:%s:kind:%s:value:%s",
		codexAccountIdentityNamespaceVersion,
		s.apiKeyID,
		namespace,
		kind,
		raw,
	))
}

var codexAccountIdentityFields = []struct {
	name string
	kind string
}{
	{name: "installation_id", kind: "installation"},
	{name: "x-codex-installation-id", kind: "installation"},
	{name: "session_id", kind: "session"},
	{name: "session-id", kind: "session"},
	{name: "thread_id", kind: "thread"},
	{name: "thread-id", kind: "thread"},
	{name: "turn_id", kind: "turn"},
	{name: "turn-id", kind: "turn"},
	{name: "window_id", kind: "window"},
	{name: "x-codex-window-id", kind: "window"},
	{name: "x-client-request-id", kind: "request"},
}

func (s codexAccountIdentityScope) applyFields(values map[string]any) bool {
	if values == nil || s.namespace == "" {
		return false
	}
	changed := false
	for _, field := range codexAccountIdentityFields {
		raw, ok := values[field.name].(string)
		if !ok || strings.TrimSpace(raw) == "" {
			continue
		}
		next := s.value(field.kind, raw)
		if next != raw {
			values[field.name] = next
			changed = true
		}
	}
	return changed
}

func (s codexAccountIdentityScope) applyEmbeddedMetadata(values map[string]any) bool {
	raw, ok := values[openAIWSTurnMetadataHeader].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return false
	}
	metadata := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil || metadata == nil {
		return false
	}
	if !s.applyFields(metadata) {
		return false
	}
	rebuilt, err := json.Marshal(metadata)
	if err != nil {
		return false
	}
	values[openAIWSTurnMetadataHeader] = string(rebuilt)
	return true
}

func (s codexAccountIdentityScope) applyBody(requestBody map[string]any) bool {
	if requestBody == nil || s.namespace == "" {
		return false
	}
	changed := false
	clientMetadata, _ := requestBody["client_metadata"].(map[string]any)
	originalBodySessionID := ""
	if clientMetadata != nil {
		originalBodySessionID, _ = clientMetadata["session_id"].(string)
		if s.applyFields(clientMetadata) {
			changed = true
		}
		if s.applyEmbeddedMetadata(clientMetadata) {
			changed = true
		}
	}
	if raw, ok := requestBody["prompt_cache_key"].(string); ok && strings.TrimSpace(raw) != "" {
		kind := "prompt-cache"
		if strings.TrimSpace(originalBodySessionID) != "" && raw == originalBodySessionID {
			kind = "session"
		}
		next := s.value(kind, raw)
		if next != raw {
			requestBody["prompt_cache_key"] = next
			changed = true
		}
	}
	return changed
}

func (s codexAccountIdentityScope) applyHeaders(headers http.Header) {
	if headers == nil || s.namespace == "" {
		return
	}
	for _, field := range codexAccountIdentityFields {
		// Underscore session/conversation headers are rebuilt separately from the
		// prompt cache key by each request builder.
		if field.name == "session_id" {
			continue
		}
		raw := strings.TrimSpace(headers.Get(field.name))
		if raw != "" {
			headers.Set(field.name, s.value(field.kind, raw))
		}
	}
	if raw := strings.TrimSpace(headers.Get(openAIWSTurnMetadataHeader)); raw != "" {
		metadata := map[string]any{}
		if err := json.Unmarshal([]byte(raw), &metadata); err == nil && metadata != nil && s.applyFields(metadata) {
			if rebuilt, err := json.Marshal(metadata); err == nil {
				headers.Set(openAIWSTurnMetadataHeader, string(rebuilt))
			}
		}
	}
}
