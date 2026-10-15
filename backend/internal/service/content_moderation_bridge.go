package service

import "github.com/Wei-Shaw/sub2api/internal/pkg/auditbridge"

func moderationBridgeMetadata(in ContentModerationCheckInput) auditbridge.Metadata {
	return (auditbridge.Metadata{RequestID: in.RequestID, UserID: in.UserID, Username: in.Username, APIKeyID: in.APIKeyID, APIKeyName: in.APIKeyName, Stage: "moderation", ClientModel: in.Model}).WithID(auditbridge.Hash(in.Body), "/v1/moderations")
}
