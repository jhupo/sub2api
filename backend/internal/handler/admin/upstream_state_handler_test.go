package admin

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestPublicUpstreamStateSettingsNeverReturnsWebshareAPIKey(t *testing.T) {
	public := publicUpstreamStateSettings(service.UpstreamStateSettings{
		Enabled: true, AutoReplaceEnabled: true, TTLMinutes: 40, ExpectedLength: 292,
		WebshareEnabled: true, WebshareAPIKey: "secret-api-key", WebshareCountryMode: "random",
	})
	require.True(t, public.WebshareAPIKeyConfigured)
	raw, err := json.Marshal(public)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "secret-api-key")
	require.NotContains(t, string(raw), "webshare_api_key\"")
	require.Contains(t, string(raw), `"webshare_countries":[]`)
	require.Contains(t, string(raw), `"pairs":[]`)
}
