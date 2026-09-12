package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	openAIWSThreadIDHeader = "thread-id"
	openAIWSWindowIDHeader = "x-codex-window-id"

	// Codex puts request_kind in x-codex-turn-metadata. The main turn lane also
	// covers prewarm and compaction because those operations intentionally reuse
	// the same upstream connection and ordered state. Independent kinds get a
	// separate lane so background work cannot preempt an active user turn.
	openAIWSRequestKindTurn       = "turn"
	openAIWSRequestKindPrewarm    = "prewarm"
	openAIWSRequestKindCompaction = "compaction"
)

// resolveOpenAIWSClientThreadID extracts the client thread identity. Codex
// multi-agent requests share one session-id, so thread-id is the identity that
// separates parent and child agents. Headers take precedence over embedded
// metadata, followed by the window id and body metadata.
func resolveOpenAIWSClientThreadID(c *gin.Context, body []byte) string {
	if c != nil && c.Request != nil {
		if id := strings.TrimSpace(c.GetHeader(openAIWSThreadIDHeader)); id != "" {
			return id
		}
		if id := codexTurnMetadataThreadID(c.GetHeader(openAIWSTurnMetadataHeader)); id != "" {
			return id
		}
		if window := strings.TrimSpace(c.GetHeader(openAIWSWindowIDHeader)); window != "" {
			if id := strings.TrimSpace(strings.SplitN(window, ":", 2)[0]); id != "" {
				return id
			}
		}
	}
	if len(body) == 0 {
		return ""
	}
	if id := strings.TrimSpace(gjson.GetBytes(body, "client_metadata.thread_id").String()); id != "" {
		return id
	}
	return codexTurnMetadataThreadID(gjson.GetBytes(body, "client_metadata."+openAIWSTurnMetadataHeader).String())
}

type codexTurnMetadata struct {
	ThreadID    string `json:"thread_id"`
	RequestKind string `json:"request_kind"`
}

func parseCodexTurnMetadata(raw string) (codexTurnMetadata, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return codexTurnMetadata{}, false
	}
	var metadata codexTurnMetadata
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return codexTurnMetadata{}, false
	}
	metadata.ThreadID = strings.TrimSpace(metadata.ThreadID)
	metadata.RequestKind = strings.ToLower(strings.TrimSpace(metadata.RequestKind))
	return metadata, true
}

func codexTurnMetadataThreadID(raw string) string {
	metadata, _ := parseCodexTurnMetadata(raw)
	return metadata.ThreadID
}

func openAIWSExecutionTurnMetadata(c *gin.Context, body []byte) codexTurnMetadata {
	if c != nil && c.Request != nil {
		if metadata, ok := parseCodexTurnMetadata(c.GetHeader(openAIWSTurnMetadataHeader)); ok {
			return metadata
		}
	}
	if len(body) == 0 {
		return codexTurnMetadata{}
	}
	metadata, _ := parseCodexTurnMetadata(gjson.GetBytes(body, "client_metadata."+openAIWSTurnMetadataHeader).String())
	return metadata
}

func openAIWSExecutionSubagent(c *gin.Context, body []byte) string {
	if c != nil && c.Request != nil {
		if subagent := strings.TrimSpace(c.GetHeader(openAISubagentHeader)); subagent != "" {
			return strings.ToLower(subagent)
		}
	}
	if len(body) == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "client_metadata."+openAISubagentHeader).String()))
}

func resolveOpenAIWSExecutionLane(c *gin.Context, body []byte) string {
	metadata := openAIWSExecutionTurnMetadata(c, body)
	switch metadata.RequestKind {
	case "", openAIWSRequestKindTurn, openAIWSRequestKindPrewarm, openAIWSRequestKindCompaction:
	default:
		return "kind=" + metadata.RequestKind
	}
	if metadata.ThreadID == "" {
		if subagent := openAIWSExecutionSubagent(c, body); subagent != "" {
			return "subagent=" + subagent
		}
	}
	return ""
}

func openAIWSExecutionScopeSeed(apiKeyID int64, identity, value, lane string) string {
	seed := fmt.Sprintf("openai_ws_exec:%d|%s=%s", apiKeyID, identity, value)
	if lane != "" {
		seed += "|" + lane
	}
	return seed
}

// resolveOpenAIWSExecutionScope produces the key used for WS preemption and
// session-level state. It intentionally uses declared client identity only;
// content-derived affinity remains a routing hint and is not safe for
// preemption. Parent and child agents that share session-id therefore diverge
// by thread-id while reconnects of one thread still replace one another.
func resolveOpenAIWSExecutionScope(c *gin.Context, body []byte, apiKeyID int64) (scope, threadID string) {
	lane := resolveOpenAIWSExecutionLane(c, body)
	if threadID = resolveOpenAIWSClientThreadID(c, body); threadID != "" {
		scope, _ = deriveOpenAISessionHashes(openAIWSExecutionScopeSeed(apiKeyID, "thread", threadID, lane))
		return scope, threadID
	}
	// A plain session-id already has a stable content/session hash used by the
	// existing WS state store. Only independent request lanes need an additional
	// execution key when the client did not provide a thread identity; leaving
	// the ordinary lane empty preserves turn-state and connection continuity.
	if lane != "" {
		if sessionID := strings.TrimSpace(explicitOpenAIRequestSessionID(c, body)); sessionID != "" {
			scope, _ = deriveOpenAISessionHashes(openAIWSExecutionScopeSeed(apiKeyID, "session", sessionID, lane))
			return scope, ""
		}
	}
	return "", ""
}
