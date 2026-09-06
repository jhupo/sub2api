package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuiltInModelIDsForPlatform_KnownPlatformsAreUnique(t *testing.T) {
	platforms := append(append([]string(nil), builtInModelPlatforms...), PlatformComposite)
	for _, platform := range platforms {
		models := BuiltInModelIDsForPlatform(platform)
		require.NotEmpty(t, models, "platform=%s", platform)

		seen := make(map[string]struct{}, len(models))
		for _, model := range models {
			require.NotEmpty(t, model, "platform=%s", platform)
			_, duplicate := seen[model]
			require.False(t, duplicate, "duplicate model %q for platform=%s", model, platform)
			seen[model] = struct{}{}
		}
	}
}

func TestBuiltInModelIDsForPlatform_NormalizesInputAndReturnsCopy(t *testing.T) {
	first := BuiltInModelIDsForPlatform(" OpenAI ")
	require.NotEmpty(t, first)
	wantFirst := first[0]

	first[0] = "caller-mutated"
	second := BuiltInModelIDsForPlatform(PlatformOpenAI)
	require.Equal(t, wantFirst, second[0])
}

func TestBuiltInModelIDsForPlatform_UnknownPlatformReturnsNil(t *testing.T) {
	require.Nil(t, BuiltInModelIDsForPlatform("unknown"))
}

func TestBuiltInCodexModelIDsForPlatform_DeepseekUsesNativeModels(t *testing.T) {
	require.Equal(
		t,
		[]string{"deepseek-v4-pro", "deepseek-v4-flash"},
		BuiltInCodexModelIDsForPlatform(PlatformDeepseek),
	)
}
