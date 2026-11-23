package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/require"
)

func TestNormalizeAnthropicInputSchemaPreservesConjunctiveConstraints(t *testing.T) {
	for _, tc := range []struct {
		name    string
		schema  string
		valid   []string
		invalid []string
	}{
		{
			name: "allOf properties intersect",
			schema: `{"type":"object","allOf":[
				{"type":"object","properties":{"x":{"type":"string","minLength":5}},"required":["x"]},
				{"type":"object","properties":{"x":{"type":"string","pattern":"^[0-9]+$"}},"required":["x"]}
			]}`,
			valid: []string{`{"x":"12345"}`}, invalid: []string{`{"x":"abcde"}`, `{"x":"1234"}`, `{}`},
		},
		{
			name: "root properties intersect anyOf branches",
			schema: `{"type":"object","properties":{"x":{"type":"string","pattern":"^[a-z]+$"}},"required":["x"],"anyOf":[
				{"type":"object","properties":{"x":{"minLength":3}}},
				{"type":"object","properties":{"x":{"const":"ab"}}}
			]}`,
			valid: []string{`{"x":"abc"}`, `{"x":"ab"}`}, invalid: []string{`{"x":"ABCDE"}`, `{"x":"a"}`, `{"x":12}`},
		},
		{
			name: "sibling union required sets combine",
			schema: `{"type":"object",
				"oneOf":[{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}],
				"anyOf":[{"type":"object","properties":{"b":{"type":"string"}},"required":["b"]}]
			}`,
			valid: []string{`{"a":"a","b":"b"}`}, invalid: []string{`{"a":"a"}`, `{"b":"b"}`, `{}`},
		},
		{
			name: "sibling union property constraints intersect",
			schema: `{"type":"object","required":["x"],
				"oneOf":[{"type":"object","properties":{"x":{"type":"string","minLength":5}}}],
				"anyOf":[{"type":"object","properties":{"x":{"pattern":"^[0-9]+$"}}}]
			}`,
			valid: []string{`{"x":"12345"}`}, invalid: []string{`{"x":"abcde"}`, `{"x":"1234"}`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			normalized := normalizeAnthropicInputSchema(json.RawMessage(tc.schema))
			fields := requireObjectInputSchema(t, normalized)
			for _, keyword := range anthropicInputSchemaUnionKeywords {
				require.NotContains(t, fields, keyword)
			}
			for name, raw := range map[string][]byte{"original": []byte(tc.schema), "normalized": normalized} {
				var schema jsonschema.Schema
				require.NoError(t, json.Unmarshal(raw, &schema))
				resolved, err := schema.Resolve(nil)
				require.NoError(t, err, name)
				for _, rawInstance := range tc.valid {
					var instance any
					require.NoError(t, json.Unmarshal([]byte(rawInstance), &instance))
					require.NoError(t, resolved.Validate(instance), "%s should accept %s", name, rawInstance)
				}
				for _, rawInstance := range tc.invalid {
					var instance any
					require.NoError(t, json.Unmarshal([]byte(rawInstance), &instance))
					require.Error(t, resolved.Validate(instance), "%s must reject %s", name, rawInstance)
				}
			}
		})
	}
}
