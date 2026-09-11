package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const codexAttemptIdentityContextKey = "openai_codex_attempt_identity"

// Only original identity metadata is captured, never credentials or the large
// inference payload. Extract before any account scoping or protocol conversion.
type codexIdentityInput struct {
	userAgent      string
	clientSession  string
	bodySession    string
	promptCacheKey string
}

func extractCodexIdentityInput(headers http.Header, body []byte) codexIdentityInput {
	input := codexIdentityInput{userAgent: headers.Get("User-Agent"), clientSession: extractClientSessionID(headers)}
	if value := gjson.GetBytes(body, "client_metadata.session_id"); value.Type == gjson.String {
		input.bodySession = strings.TrimSpace(value.String())
	}
	if input.clientSession == "" {
		input.clientSession = input.bodySession
	}
	if value := gjson.GetBytes(body, "prompt_cache_key"); value.Type == gjson.String {
		input.promptCacheKey = strings.TrimSpace(value.String())
	}
	return input
}

// Immutable for a logical attempt, including all same-account transport retries.
// Account failover publishes a replacement from the original ingress metadata.
// Authentication remains outside this snapshot and is resolved at send/dial time.
type codexAttemptIdentity struct {
	accountID       int64
	deviceID        string
	promptCacheKey  string
	client          codexOutboundIdentity
	bridgeSessionID string
	scope           codexAccountIdentityScope
	fingerprint     *codexFingerprintIDs
	turn            int
}

func (s *OpenAIGatewayService) resolveCodexAttemptIdentity(account, source *Account, input codexIdentityInput, apiKeyID int64, compact bool) *codexAttemptIdentity {
	if account == nil || !account.IsOpenAI() || !account.UsesOpenAICodexProtocol() {
		return nil
	}
	forceCLI := s != nil && s.cfg != nil && s.cfg.Gateway.ForceCodexCLI
	override := source.GetOpenAIUserAgent()
	if source != account && account.GetOpenAIUserAgent() != "" {
		override = account.GetOpenAIUserAgent()
	}
	client := resolveCodexRequestClientIdentity(input.userAgent, override, forceCLI)
	identity := &codexAttemptIdentity{accountID: account.ID, deviceID: source.GetOpenAIDeviceID(), promptCacheKey: input.promptCacheKey, client: client, scope: newCodexAccountIdentityScope(source, apiKeyID), turn: 1}
	if !compact {
		// The selected row owns policy; the actual credential owns its device seed.
		identity.fingerprint = resolveCodexFingerprintIDsForInput(source, input, apiKeyID, account.GetCodexFingerprintMode())
	}
	return identity
}

// A new logical WS turn gets one turn ID, shared by all its transport retries.
// Device, session, thread and cache identity remain unchanged on the socket.
func (i *codexAttemptIdentity) forTurn(turn int) *codexAttemptIdentity {
	if i == nil || turn <= i.turn {
		return i
	}
	next := *i
	next.turn = turn
	if i.fingerprint != nil && i.fingerprint.turnID != "" {
		ids := *i.fingerprint
		ids.turnID = uuid.Must(uuid.NewV7()).String()
		ids.turnStartedAtUnixMs = time.Now().UnixMilli()
		next.fingerprint = &ids
	}
	return &next
}

func (s *OpenAIGatewayService) prepareCodexAttemptIdentity(ctx context.Context, c *gin.Context, account *Account, body []byte) error {
	stageCodexAttemptIdentity(c, nil)
	source, err := s.prepareCodexAccountIdentitySource(ctx, c, account)
	if err != nil {
		return err
	}
	var headers http.Header
	if c != nil && c.Request != nil {
		headers = c.Request.Header
	}
	stageCodexAttemptIdentity(c, s.resolveCodexAttemptIdentity(account, source, extractCodexIdentityInput(headers, body), getAPIKeyIDFromContext(c), isOpenAIResponsesCompactPath(c)))
	return nil
}

func stageCodexAttemptIdentity(c *gin.Context, identity *codexAttemptIdentity) {
	if c != nil {
		c.Set(codexAttemptIdentityContextKey, identity)
	}
}

func stagedCodexAttemptIdentity(c *gin.Context, account *Account) *codexAttemptIdentity {
	if c == nil || account == nil || !account.UsesOpenAICodexProtocol() {
		return nil
	}
	value, _ := c.Get(codexAttemptIdentityContextKey)
	identity, _ := value.(*codexAttemptIdentity)
	if identity == nil || identity.accountID != account.ID {
		return nil
	}
	return identity
}

// Request builders can also be used independently of the forwarding entry
// points. Publish once there, before constructing any outbound identity fields.
func (s *OpenAIGatewayService) codexAttemptIdentity(c *gin.Context, account *Account) *codexAttemptIdentity {
	if identity := stagedCodexAttemptIdentity(c, account); identity != nil {
		return identity
	}
	var headers http.Header
	if c != nil && c.Request != nil {
		headers = c.Request.Header
	}
	identity := s.resolveCodexAttemptIdentity(account, codexAccountIdentitySource(c, account), extractCodexIdentityInput(headers, nil), getAPIKeyIDFromContext(c), isOpenAIResponsesCompactPath(c))
	stageCodexAttemptIdentity(c, identity)
	return identity
}

func (i *codexAttemptIdentity) applyHeaders(headers http.Header) {
	if i == nil || headers == nil {
		return
	}
	i.scope.applyHeaders(headers)
	if i.bridgeSessionID != "" {
		headers.Set("session_id", i.bridgeSessionID)
		if headers.Get("conversation_id") != "" {
			headers.Set("conversation_id", i.bridgeSessionID)
		}
	}
	applyCodexFingerprintHeaders(headers, i.fingerprint)
	// A protocol bridge may deliberately omit originator. Do not introduce it.
	if headers.Get("originator") != "" {
		i.applyClientHeaders(headers)
	}
}

// Bridges derive their transport session before the final identity projection.
// Optional session convergence remains the final authority over all replicas.
func (i *codexAttemptIdentity) withBridgeSession(key string) *codexAttemptIdentity {
	if i == nil || key == "" {
		return i
	}
	next := *i
	next.bridgeSessionID = generateSessionUUID(i.scope.sessionID(key))
	return &next
}

func (i *codexAttemptIdentity) applyClientHeaders(headers http.Header) {
	if i == nil || headers == nil {
		return
	}
	i.client.applyHeaders(headers)
}

func (i *codexAttemptIdentity) applyInstallationFallback(body map[string]any) bool {
	return i != nil && applyCodexClientMetadata(body, i.deviceID)
}

func (i *codexAttemptIdentity) applyBody(body map[string]any) bool {
	if i == nil {
		return false
	}
	changed := i.scope.applyBody(body)
	if i.fingerprint != nil {
		ids := *i.fingerprint
		if applyCodexFingerprintClientMetadata(body, &ids) {
			changed = true
		}
	}
	return changed
}

func (i *codexAttemptIdentity) applyBodyRaw(body []byte) ([]byte, bool, error) {
	if i == nil {
		return body, false, nil
	}
	if !gjson.ParseBytes(body).IsObject() {
		return body, false, nil
	}
	fields := make(map[string]any, 2)
	values := gjson.GetManyBytes(body, "client_metadata", "prompt_cache_key")
	if values[0].IsObject() {
		var metadata map[string]any
		if err := json.Unmarshal([]byte(values[0].Raw), &metadata); err != nil {
			return body, false, fmt.Errorf("decode Codex identity metadata: %w", err)
		}
		fields["client_metadata"] = metadata
	}
	if values[1].Type == gjson.String {
		fields["prompt_cache_key"] = values[1].String()
	}
	if !i.applyBody(fields) {
		return body, false, nil
	}
	next := body
	if metadata, exists := fields["client_metadata"]; exists {
		encoded, err := json.Marshal(metadata)
		if err != nil {
			return body, false, err
		}
		next, err = sjson.SetRawBytes(next, "client_metadata", encoded)
		if err != nil {
			return body, false, err
		}
	}
	if key, exists := fields["prompt_cache_key"]; exists && key != values[1].String() {
		var err error
		next, err = sjson.SetBytes(next, "prompt_cache_key", key)
		if err != nil {
			return body, false, err
		}
	}
	return next, true, nil
}
