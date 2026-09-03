package service

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestBuildOpenAIWSCreatePayloadRawPreservesRequestValues(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	raw := []byte(` {"model":"gpt-5.1","type":"response.create","stream":false,"background":true,"input":[ {"type":"input_text","text":"keep exact","opaque":9007199254740993} ],"tools":[ {"type":"function","name":"lookup"} ]} `)

	updated, err := svc.buildOpenAIWSCreatePayloadRaw(raw, account)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(updated, "stream").Bool())
	require.False(t, gjson.GetBytes(updated, "background").Exists())
	require.Equal(t, "response.create", gjson.GetBytes(updated, "type").String())
	require.Equal(t, `[{"type":"input_text","text":"keep exact","opaque":9007199254740993}]`, compactJSONValue(t, gjson.GetBytes(updated, "input").Raw))
	require.Equal(t, `[{"type":"function","name":"lookup"}]`, compactJSONValue(t, gjson.GetBytes(updated, "tools").Raw))
	require.Equal(t, "9007199254740993", gjson.GetBytes(updated, "input.0.opaque").Raw)
}

func TestBuildOpenAIWSCreatePayloadRawOAuthForcesStoreFalse(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	updated, err := svc.buildOpenAIWSCreatePayloadRaw([]byte(`{"model":"gpt-5.1","store":true}`), account)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(updated, "store").Bool())
	require.True(t, gjson.GetBytes(updated, "stream").Bool())
}

func TestPrepareOpenAIWSForwardPayloadRawPreservesSemanticFields(t *testing.T) {
	raw := []byte(`{"model":"gpt-5.1","background":true,"include":["reasoning.encrypted_content"],"prompt_cache_key":"stable","client_metadata":{"keep":"yes"},"input":[{"opaque":9007199254740993}],"tools":[{"type":"function","name":"lookup"}]}`)

	updated, strategy, removed, err := (&OpenAIGatewayService{}).prepareOpenAIWSForwardPayloadRaw(
		raw,
		&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		2,
		`{"trace":"turn-2"}`,
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, "trim_optional_fields", strategy)
	require.Equal(t, []string{"include"}, removed)
	require.False(t, gjson.GetBytes(updated, "include").Exists())
	require.False(t, gjson.GetBytes(updated, "background").Exists())
	require.Equal(t, "stable", gjson.GetBytes(updated, "prompt_cache_key").String())
	require.Equal(t, "yes", gjson.GetBytes(updated, "client_metadata.keep").String())
	require.Equal(t, `{"trace":"turn-2"}`, gjson.GetBytes(updated, "client_metadata.x-codex-turn-metadata").String())
	require.Equal(t, "9007199254740993", gjson.GetBytes(updated, "input.0.opaque").Raw)
	require.Equal(t, "lookup", gjson.GetBytes(updated, "tools.0.name").String())
}

func TestPrepareOpenAIWSForwardPayloadRawRejectsInvalidJSON(t *testing.T) {
	_, _, _, err := (&OpenAIGatewayService{}).prepareOpenAIWSForwardPayloadRaw(
		[]byte(`{"model":`),
		&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		1,
		"",
		nil,
	)
	require.Error(t, err)
}

func TestSetOpenAIWSTurnMetadataRawPreservesExistingMetadata(t *testing.T) {
	raw := []byte(`{"model":"gpt-5.1","client_metadata":{"keep":"yes","x-codex-turn-metadata":"old"},"input":[{"opaque":9007199254740993}]}`)
	updated, err := setOpenAIWSTurnMetadataRaw(raw, ` {"trace":"new"} `)
	require.NoError(t, err)
	require.Equal(t, "yes", gjson.GetBytes(updated, "client_metadata.keep").String())
	require.Equal(t, `{"trace":"new"}`, gjson.GetBytes(updated, "client_metadata.x-codex-turn-metadata").String())
	require.Equal(t, "9007199254740993", gjson.GetBytes(updated, "input.0.opaque").Raw)
}

func compactJSONValue(t *testing.T, raw string) string {
	t.Helper()
	var compact bytes.Buffer
	require.NoError(t, json.Compact(&compact, []byte(raw)))
	return compact.String()
}
