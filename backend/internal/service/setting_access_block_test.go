package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccessBlockSettingsDefaults(t *testing.T) {
	svc := newPanelRateLimitTestService(&panelRateLimitSettingRepo{})
	settings, err := svc.GetAccessBlockSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, DefaultAccessBlockSettings(), settings)
}

func TestAccessBlockSettingsRoundTripAndCacheRefresh(t *testing.T) {
	repo := &panelRateLimitSettingRepo{}
	svc := newPanelRateLimitTestService(repo)
	want := DefaultAccessBlockSettings()
	want.Enabled = false
	want.LoginFailureThreshold = 20
	want.BlockedHeaders = []AccessBlockedHeaderRule{{Name: " user-agent ", Value: " scanner "}}

	require.NoError(t, svc.SetAccessBlockSettings(context.Background(), want))
	require.Equal(t, []AccessBlockedHeaderRule{{Name: "User-Agent", Value: "scanner"}}, want.BlockedHeaders)
	require.Equal(t, *want, svc.GetAccessBlockSettingsCached(context.Background()))

	stored, err := svc.GetAccessBlockSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, want, stored)
}

func TestAccessBlockSettingsValidation(t *testing.T) {
	svc := newPanelRateLimitTestService(&panelRateLimitSettingRepo{})
	require.Error(t, svc.SetAccessBlockSettings(context.Background(), nil))

	invalidLogin := DefaultAccessBlockSettings()
	invalidLogin.LoginFailureThreshold = 0
	require.Error(t, svc.SetAccessBlockSettings(context.Background(), invalidLogin))

	invalidHeader := DefaultAccessBlockSettings()
	invalidHeader.BlockedHeaders = []AccessBlockedHeaderRule{{Name: "Bad Header", Value: "scanner"}}
	require.Error(t, svc.SetAccessBlockSettings(context.Background(), invalidHeader))

	invalidPanel := DefaultAccessBlockSettings()
	invalidPanel.PanelBlacklistEnabled = true
	invalidPanel.PanelBlacklistThreshold = 0
	require.Error(t, svc.SetAccessBlockSettings(context.Background(), invalidPanel))
}

func TestAccessBlockSettingsNormalizesHeadersFromStorage(t *testing.T) {
	repo := &panelRateLimitSettingRepo{values: map[string]string{
		SettingKeyAccessBlockSettings: `{"enabled":true,"login_protection_enabled":true,"login_failure_threshold":10,"login_failure_window_seconds":600,"login_temporary_block_seconds":3600,"blocked_headers":[{"name":" user-agent ","value":" scanner "},{"name":"User-Agent","value":"scanner"},{"name":"Bad Header","value":"ignored"}],"panel_blacklist_enabled":false,"panel_blacklist_threshold":10,"panel_blacklist_window_seconds":600}`,
	}}
	svc := newPanelRateLimitTestService(repo)
	settings, err := svc.GetAccessBlockSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, []AccessBlockedHeaderRule{{Name: "User-Agent", Value: "scanner"}}, settings.BlockedHeaders)
}
