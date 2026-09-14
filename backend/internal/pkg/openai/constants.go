// Package openai provides helpers and types for OpenAI API integration.
package openai

import (
	_ "embed"
	"strings"
)

// Model represents an OpenAI model
type Model struct {
	ID          string `json:"id"`
	Object      string `json:"object"`
	Created     int64  `json:"created"`
	OwnedBy     string `json:"owned_by"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
}

// DefaultModels OpenAI models list
var DefaultModels = []Model{
	{ID: "gpt-5.6-sol", Object: "model", Created: 1780876800, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.6 Sol"},
	{ID: "gpt-6-astra", Object: "model", Created: 1788480000, OwnedBy: "openai", Type: "model", DisplayName: "GPT-6 Astra"},
	{ID: "gpt-6", Object: "model", Created: 1788480000, OwnedBy: "openai", Type: "model", DisplayName: "GPT-6 (Astra)"},
	{ID: "gpt-5.6", Object: "model", Created: 1780876800, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.6 (Sol)"},
	{ID: "gpt-5.6-terra", Object: "model", Created: 1780876800, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.6 Terra"},
	{ID: "gpt-5.6-luna", Object: "model", Created: 1780876800, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.6 Luna"},
	{ID: "gpt-5.5", Object: "model", Created: 1776873600, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.5"},
	{ID: "gpt-5.4", Object: "model", Created: 1738368000, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.4"},
	{ID: "gpt-5.4-mini", Object: "model", Created: 1738368000, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.4 Mini"},
	{ID: "gpt-5.3-codex-spark", Object: "model", Created: 1735689600, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.3 Codex Spark"},
	{ID: "codex-auto-review", Object: "model", Created: 1776902400, OwnedBy: "openai", Type: "model", DisplayName: "Codex Auto Review"},
	{ID: "gpt-5.2", Object: "model", Created: 1733875200, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.2"},
	{ID: "gpt-image-1", Object: "model", Created: 1733875200, OwnedBy: "openai", Type: "model", DisplayName: "GPT Image 1"},
	{ID: "gpt-image-1.5", Object: "model", Created: 1735689600, OwnedBy: "openai", Type: "model", DisplayName: "GPT Image 1.5"},
	{ID: "gpt-image-2", Object: "model", Created: 1738368000, OwnedBy: "openai", Type: "model", DisplayName: "GPT Image 2"},
	{ID: "gpt-image-2.5-flare", Object: "model", Created: 1788825600, OwnedBy: "openai", Type: "model", DisplayName: "GPT Image 2.5 Flare"},
	{ID: "gpt-image-2.5-sunburst", Object: "model", Created: 1788825600, OwnedBy: "openai", Type: "model", DisplayName: "GPT Image 2.5 Sunburst"},
}

// DefaultModelIDs returns the default model ID list
func DefaultModelIDs() []string {
	ids := make([]string, len(DefaultModels))
	for i, m := range DefaultModels {
		ids[i] = m.ID
	}
	return ids
}

// DefaultTestModel default model for testing OpenAI accounts
const DefaultTestModel = "gpt-5.4"

// CodexUsageProbeModel is the model used for OAuth Codex usage probes.
const CodexUsageProbeModel = "codex-auto-review"

// DefaultInstructions default instructions for non-Codex CLI requests.
// 内容为真实 Codex CLI 的 GPT-5-Codex base prompt（codex 系模型默认）。
//
//go:embed instructions.txt
var DefaultInstructions string

// 各模型专用 instructions 均逐字同步自官方 Codex models.json。
// GPT-5.5 同时作为 fallback，覆盖尚未单独维护 prompt 的模型。
//
//go:embed instructions_gpt5_1.txt
var instructionsGPT51 string

//go:embed instructions_gpt5_2.txt
var instructionsGPT52 string

//go:embed instructions_gpt5_5.txt
var instructionsGPT55 string

//go:embed instructions_gpt5_6.txt
var instructionsGPT56 string

//go:embed instructions_gpt6_astra.txt
var instructionsGPT6Astra string

// fallbackCodexInstructions 返回未知模型使用的 GPT-5.5 base instructions；
// 若内嵌 prompt 意外为空则回退到 DefaultInstructions 保证非空。
func fallbackCodexInstructions() string {
	if v := strings.TrimSpace(instructionsGPT55); v != "" {
		return instructionsGPT55
	}
	return DefaultInstructions
}

// CodexBaseInstructionsForModel 按模型返回最匹配的官方 Codex base instructions。
// 模型名可以包含 openai/、models/ 或 global.openai. 等提供商前缀。
// 含 "codex" 的专用模型优先使用 GPT-5-Codex prompt；未知模型回退到 GPT-5.5。
//
// 任一专用 prompt 意外为空时回退链最终落到 DefaultInstructions，保证返回非空。
func CodexBaseInstructionsForModel(model string) string {
	m := normalizeCodexInstructionsModel(model)
	switch {
	case strings.Contains(m, "codex"):
		return DefaultInstructions
	case m == "gpt-6" || modelBelongsToInstructionsFamily(m, "gpt-6-astra"):
		if v := strings.TrimSpace(instructionsGPT6Astra); v != "" {
			return instructionsGPT6Astra
		}
	case modelBelongsToInstructionsFamily(m, "gpt-5.6"):
		if v := strings.TrimSpace(instructionsGPT56); v != "" {
			return instructionsGPT56
		}
	case modelBelongsToInstructionsFamily(m, "gpt-5.5"):
		return fallbackCodexInstructions()
	case modelBelongsToInstructionsFamily(m, "gpt-5.2"):
		if v := strings.TrimSpace(instructionsGPT52); v != "" {
			return instructionsGPT52
		}
	case modelBelongsToInstructionsFamily(m, "gpt-5.1"):
		if v := strings.TrimSpace(instructionsGPT51); v != "" {
			return instructionsGPT51
		}
	}
	return fallbackCodexInstructions()
}

func normalizeCodexInstructionsModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if slash := strings.LastIndexByte(m, '/'); slash >= 0 {
		m = strings.TrimSpace(m[slash+1:])
	}
	if providerPrefix := strings.LastIndex(m, ".gpt-"); providerPrefix >= 0 {
		m = m[providerPrefix+1:]
	}
	return m
}

func modelBelongsToInstructionsFamily(model, family string) bool {
	return model == family || strings.HasPrefix(model, family+"-")
}
