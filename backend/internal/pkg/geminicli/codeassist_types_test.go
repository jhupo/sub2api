package geminicli

import (
	"encoding/json"
	"testing"
)

func TestLoadCodeAssistResponseProjectIDForms(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "string", body: `{"cloudaicompanionProject":"project-a"}`, want: "project-a"},
		{name: "object", body: `{"cloudaicompanionProject":{"id":"project-b"}}`, want: "project-b"},
		{name: "projectId fallback", body: `{"projectId":"project-c"}`, want: "project-c"},
		{name: "nested project", body: `{"project":{"id":"project-d"}}`, want: "project-d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got LoadCodeAssistResponse
			if err := json.Unmarshal([]byte(tt.body), &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got.CloudAICompanionProject != tt.want {
				t.Fatalf("project id = %q, want %q", got.CloudAICompanionProject, tt.want)
			}
		})
	}
}
