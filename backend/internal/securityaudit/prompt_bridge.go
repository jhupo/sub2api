package securityaudit

import "github.com/Wei-Shaw/sub2api/internal/pkg/auditbridge"

func bridgeMetadata(s PromptSnapshot) auditbridge.Metadata {
	return (auditbridge.Metadata{RequestID: s.RequestID, UserID: s.UserID, Username: s.UsernameSnapshot, APIKeyID: s.APIKeyID, APIKeyName: s.APIKeyNameSnapshot, Stage: s.Stage, ClientModel: s.Model}).WithID(s.PromptHash, "/v1/chat/completions")
}
