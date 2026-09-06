package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

var builtInModelPlatforms = []string{
	PlatformAnthropic,
	PlatformGemini,
	PlatformOpenAI,
	PlatformAntigravity,
	PlatformGrok,
	PlatformKimi,
	PlatformZhipu,
	PlatformDeepseek,
}

// BuiltInModelIDsForPlatform returns the public model IDs that sub2api exposes
// when no live account catalog is available. Callers receive a fresh slice.
// A nil result means the platform is unknown.
func BuiltInModelIDsForPlatform(platform string) []string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case PlatformOpenAI:
		return openai.DefaultModelIDs()
	case PlatformGemini:
		ids := make([]string, 0, len(geminicli.DefaultModels))
		for _, model := range geminicli.DefaultModels {
			ids = append(ids, model.ID)
		}
		return ids
	case PlatformAntigravity:
		models := antigravity.DefaultModels()
		ids := make([]string, 0, len(models))
		for _, model := range models {
			ids = append(ids, model.ID)
		}
		return ids
	case PlatformAnthropic, PlatformKimi, PlatformZhipu, PlatformDeepseek:
		// CN providers expose Claude-compatible public names by default. Account
		// model mappings translate these names to provider-specific upstream IDs.
		return claude.DefaultModelIDs()
	case PlatformGrok:
		return xai.DefaultModelIDs()
	case PlatformComposite:
		return compositeBuiltInModelIDs()
	default:
		return nil
	}
}

// BuiltInCodexModelIDsForPlatform returns the static Codex manifest fallback.
// DeepSeek has native public model names instead of Claude-compatible aliases.
func BuiltInCodexModelIDsForPlatform(platform string) []string {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform == PlatformDeepseek {
		return []string{"deepseek-v4-pro", "deepseek-v4-flash"}
	}
	return BuiltInModelIDsForPlatform(platform)
}

func compositeBuiltInModelIDs() []string {
	seen := make(map[string]struct{})
	ids := make([]string, 0)
	for _, platform := range builtInModelPlatforms {
		for _, id := range BuiltInModelIDsForPlatform(platform) {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids
}
