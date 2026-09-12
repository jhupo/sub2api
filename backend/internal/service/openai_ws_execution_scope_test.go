package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newOpenAIWSExecutionScopeTestContext(headers map[string]string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	for key, value := range headers {
		c.Request.Header.Set(key, value)
	}
	return c
}

func TestResolveOpenAIWSExecutionScopeSeparatesCodexThreads(t *testing.T) {
	parent := newOpenAIWSExecutionScopeTestContext(map[string]string{
		"session-id":               "root-session",
		openAIWSThreadIDHeader:     "parent-thread",
		openAIWSTurnMetadataHeader: `{"thread_id":"parent-thread","request_kind":"turn"}`,
	})
	child := newOpenAIWSExecutionScopeTestContext(map[string]string{
		"session-id":               "root-session",
		openAIWSThreadIDHeader:     "child-thread",
		openAIWSTurnMetadataHeader: `{"thread_id":"child-thread","request_kind":"turn"}`,
	})

	parentScope, parentThread := resolveOpenAIWSExecutionScope(parent, nil, 42)
	childScope, childThread := resolveOpenAIWSExecutionScope(child, nil, 42)
	require.NotEmpty(t, parentScope)
	require.NotEmpty(t, childScope)
	require.NotEqual(t, parentScope, childScope)
	require.Equal(t, "parent-thread", parentThread)
	require.Equal(t, "child-thread", childThread)

	reconnectScope, _ := resolveOpenAIWSExecutionScope(parent, nil, 42)
	require.Equal(t, parentScope, reconnectScope)
	require.NotEqual(t, parentScope, mustExecutionScope(t, parent, 43), "API key namespaces must remain isolated")
}

func TestResolveOpenAIWSExecutionScopeSeparatesIndependentRequestKinds(t *testing.T) {
	turn := newOpenAIWSExecutionScopeTestContext(map[string]string{
		"session-id":               "root-session",
		openAIWSThreadIDHeader:     "thread-1",
		openAIWSTurnMetadataHeader: `{"thread_id":"thread-1","request_kind":"turn"}`,
	})
	memory := newOpenAIWSExecutionScopeTestContext(map[string]string{
		"session-id":               "root-session",
		openAIWSThreadIDHeader:     "thread-1",
		openAIWSTurnMetadataHeader: `{"thread_id":"thread-1","request_kind":"memory"}`,
	})
	turnScope, _ := resolveOpenAIWSExecutionScope(turn, nil, 42)
	memoryScope, _ := resolveOpenAIWSExecutionScope(memory, nil, 42)
	require.NotEqual(t, turnScope, memoryScope)

	noIdentity := newOpenAIWSExecutionScopeTestContext(nil)
	require.Empty(t, mustExecutionScope(t, noIdentity, 42))
}

func mustExecutionScope(t *testing.T, c *gin.Context, apiKeyID int64) string {
	t.Helper()
	scope, _ := resolveOpenAIWSExecutionScope(c, nil, apiKeyID)
	return scope
}
