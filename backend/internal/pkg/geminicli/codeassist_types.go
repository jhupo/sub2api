package geminicli

import (
	"bytes"
	"encoding/json"
	"strings"
)

// LoadCodeAssistRequest matches done-hub's internal Code Assist call.
type LoadCodeAssistRequest struct {
	Metadata LoadCodeAssistMetadata `json:"metadata"`
}

type LoadCodeAssistMetadata struct {
	IDEType    string `json:"ideType"`
	Platform   string `json:"platform"`
	PluginType string `json:"pluginType"`
}

type TierInfo struct {
	ID string `json:"id"`
}

// UnmarshalJSON supports both legacy string tiers and object tiers.
func (t *TierInfo) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '"' {
		var id string
		if err := json.Unmarshal(data, &id); err != nil {
			return err
		}
		t.ID = id
		return nil
	}
	type alias TierInfo
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*t = TierInfo(decoded)
	return nil
}

type LoadCodeAssistResponse struct {
	CurrentTier             *TierInfo     `json:"currentTier,omitempty"`
	PaidTier                *TierInfo     `json:"paidTier,omitempty"`
	CloudAICompanionProject string        `json:"cloudaicompanionProject,omitempty"`
	AllowedTiers            []AllowedTier `json:"allowedTiers,omitempty"`
}

// UnmarshalJSON accepts the string form used by the original Code Assist API
// and the object/nested project forms returned by newer endpoints.
func (r *LoadCodeAssistResponse) UnmarshalJSON(data []byte) error {
	var wire struct {
		CurrentTier             *TierInfo       `json:"currentTier,omitempty"`
		PaidTier                *TierInfo       `json:"paidTier,omitempty"`
		CloudAICompanionProject json.RawMessage `json:"cloudaicompanionProject,omitempty"`
		ProjectID               json.RawMessage `json:"projectId,omitempty"`
		BackendProjectID        json.RawMessage `json:"backendProjectId,omitempty"`
		UserDefinedProject      json.RawMessage `json:"userDefinedCloudaicompanionProject,omitempty"`
		Project                 json.RawMessage `json:"project,omitempty"`
		AllowedTiers            []AllowedTier   `json:"allowedTiers,omitempty"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	r.CurrentTier = wire.CurrentTier
	r.PaidTier = wire.PaidTier
	r.AllowedTiers = wire.AllowedTiers
	for _, raw := range []json.RawMessage{
		wire.CloudAICompanionProject,
		wire.ProjectID,
		wire.BackendProjectID,
		wire.UserDefinedProject,
		wire.Project,
	} {
		if projectID := projectIDFromJSON(raw); projectID != "" {
			r.CloudAICompanionProject = projectID
			break
		}
	}
	return nil
}

func projectIDFromJSON(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var direct string
	if json.Unmarshal(raw, &direct) == nil {
		return strings.TrimSpace(direct)
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	for _, key := range []string{"id", "projectId", "project", "name"} {
		if nested, ok := value[key]; ok {
			if projectID := projectIDFromJSON(nested); projectID != "" {
				return projectID
			}
		}
	}
	return ""
}

// GetTier extracts tier ID, prioritizing paidTier over currentTier
func (r *LoadCodeAssistResponse) GetTier() string {
	if r.PaidTier != nil && r.PaidTier.ID != "" {
		return r.PaidTier.ID
	}
	if r.CurrentTier != nil {
		return r.CurrentTier.ID
	}
	return ""
}

type AllowedTier struct {
	ID        string `json:"id"`
	IsDefault bool   `json:"isDefault,omitempty"`
}

type OnboardUserRequest struct {
	TierID   string                 `json:"tierId"`
	Metadata LoadCodeAssistMetadata `json:"metadata"`
}

type OnboardUserResponse struct {
	Done     bool                   `json:"done"`
	Response *OnboardUserResultData `json:"response,omitempty"`
	Name     string                 `json:"name,omitempty"`
}

type OnboardUserResultData struct {
	CloudAICompanionProject any `json:"cloudaicompanionProject,omitempty"`
}
