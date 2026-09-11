package service

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func enforceCodexIdentityHeaders(headers http.Header) {
	enforceCodexIdentityHeadersWithUA(headers, "")
}

func enforceCodexIdentityHeadersWithUA(headers http.Header, override string) {
	if headers != nil && headers.Get("originator") != "" {
		resolveCodexRequestClientIdentity(headers.Get("User-Agent"), override, false).applyHeaders(headers)
	}
}

func pairCodexIdentityHeaders(headers http.Header) {
	pairCodexIdentityHeadersWithCanonical(headers, codexCanonicalUserAgent())
}

func resolveCodexFingerprintIDsFromRequestAndBodyForAPIKey(account *Account, headers http.Header, body []byte, apiKeyID int64) *codexFingerprintIDs {
	return resolveCodexFingerprintIDsForInput(account, extractCodexIdentityInput(headers, body), apiKeyID, account.GetCodexFingerprintMode())
}

func resolveCodexFingerprintIDsFromRequestAndBody(account *Account, headers http.Header, body []byte) *codexFingerprintIDs {
	return resolveCodexFingerprintIDsFromRequestAndBodyForAPIKey(account, headers, body, 0)
}

func isolateOpenAIUpstreamSessionID(apiKeyID int64, account *Account, raw string) string {
	return newCodexAccountIdentityScope(account, apiKeyID).sessionID(raw)
}

func scopeCodexAccountIdentityValue(account *Account, apiKeyID int64, kind, raw string) string {
	return newCodexAccountIdentityScope(account, apiKeyID).value(kind, raw)
}

func applyCodexAccountIdentityClientMetadataRaw(body []byte, account *Account, apiKeyID int64) ([]byte, bool, error) {
	return (&codexAttemptIdentity{scope: newCodexAccountIdentityScope(account, apiKeyID)}).applyBodyRaw(body)
}

// Low-level projection fixtures explicitly choose their fingerprint IDs. Real
// forwarders publish only the complete attempt identity at the ingress boundary.
func stageTestCodexFingerprintIDs(c *gin.Context, account *Account, ids *codexFingerprintIDs) {
	var headers http.Header
	if c.Request != nil {
		headers = c.Request.Header
	}
	identity := (&OpenAIGatewayService{}).resolveCodexAttemptIdentity(account, account, extractCodexIdentityInput(headers, nil), getAPIKeyIDFromContext(c), false)
	if identity != nil {
		identity.fingerprint = ids
	}
	stageCodexAttemptIdentity(c, identity)
}

func stagedCodexFingerprintIDs(c *gin.Context, account *Account) *codexFingerprintIDs {
	if identity := stagedCodexAttemptIdentity(c, account); identity != nil {
		return identity.fingerprint
	}
	return nil
}

func applyStagedCodexFingerprintHeaders(c *gin.Context, account *Account, headers http.Header) {
	applyCodexFingerprintHeaders(headers, stagedCodexFingerprintIDs(c, account))
}

func applyStagedCodexFingerprintClientMetadata(c *gin.Context, account *Account, body map[string]any) bool {
	return applyCodexFingerprintClientMetadata(body, stagedCodexFingerprintIDs(c, account))
}
