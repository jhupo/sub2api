package apicompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeAnthropicInputSchemaBudgetLeavesSchemaUnchanged(t *testing.T) {
	deep := `{"type":"object","properties":{"value":{"type":"string"}}}`
	for i := 0; i <= anthropicRootUnionMaxDepth; i++ {
		deep = `{"oneOf":[` + deep + `]}`
	}
	large := `{"type":null,"description":"` + strings.Repeat("x", anthropicToolSchemaMaxBytes) + `"}`
	wide := `{"anyOf":[` + strings.TrimSuffix(strings.Repeat(`{"type":"object"},`, anthropicRootUnionMaxBranches+1), ",") + `]}`
	for _, schema := range []string{deep, large, wide} {
		require.Equal(t, schema, string(normalizeAnthropicInputSchema(json.RawMessage(schema))))
	}
}

func TestNormalizeAnthropicInputSchemaPreservesInstanceFields(t *testing.T) {
	schema := json.RawMessage(`{"oneOf":[{"type":"object","properties":{"value":{"type":"string","default":{"oneOf":[{"required":null}]},"examples":[{"anyOf":[{"type":"string"}]}]}}}]}`)
	normalized := normalizeAnthropicInputSchema(schema)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(normalized, &fields))
	require.NotContains(t, fields, "oneOf")
	require.JSONEq(t, `{"value":{"type":"string","default":{"oneOf":[{"required":null}]},"examples":[{"anyOf":[{"type":"string"}]}]}}`, string(fields["properties"]))
}
