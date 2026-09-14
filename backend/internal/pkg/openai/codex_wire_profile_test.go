package openai

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseCodexWireProfile(t *testing.T) {
	for _, tc := range []struct {
		name, ua, originator, core, clientName, clientVersion string
	}{
		{"cli", "codex_cli_rs/0.153.4 (Linux; x86_64) xterm", "codex_cli_rs", "0.153.4", "", ""},
		{"tui", "codex-tui/0.153.4 (Windows NT 10.0; x86_64) WindowsTerminal (codex-tui; 0.153.4)", "codex-tui", "0.153.4", "codex-tui", "0.153.4"},
		{"desktop_mac", "Codex Desktop/0.153.4 (Mac OS 26.6.1; arm64) unknown (Codex Desktop; 26.903.61454)", "Codex Desktop", "0.153.4", "Codex Desktop", "26.903.61454"},
		{"desktop_windows", "Codex Desktop/0.153.4 (Windows NT 10.0; x86_64) unknown (Codex Desktop; 26.903.61454)", "Codex Desktop", "0.153.4", "Codex Desktop", "26.903.61454"},
		{"remote_tui", "codex-tui/0.153.4 (Linux; x86_64) xterm (codex-tui; 0.149.0)", "codex-tui", "0.153.4", "codex-tui", "0.149.0"},
		{"no_runtime", "codex-tui/0.153.4", "codex-tui", "0.153.4", "", ""},
		{"os_only", "codex-tui/0.153.4 (Linux; x86_64)", "codex-tui", "0.153.4", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile, ok := ParseCodexWireProfile(tc.ua)
			require.True(t, ok)
			require.Equal(t, tc.ua, profile.UserAgent)
			require.Equal(t, tc.originator, profile.Originator)
			require.Equal(t, tc.core, profile.CoreVersion)
			if tc.clientName == "" {
				require.Nil(t, profile.ClientInfo)
			} else {
				require.Equal(t, &CodexClientInfo{Name: tc.clientName, Version: tc.clientVersion}, profile.ClientInfo)
			}
		})
	}
}

func TestParseCodexWireProfileRejectsIncompleteClientInfo(t *testing.T) {
	for _, ua := range []string{
		"Codex Desktop/0.153.4 (Mac OS; arm64) unknown (Codex Desktop)",
		"Codex Desktop/0.153.4 (Mac OS; arm64) unknown (Codex Desktop; )",
		"Codex Desktop/0.153.4 (Mac OS; arm64) unknown (Codex Desktop; 26.903.61454",
		"Codex Desktop/0.153.4 (Mac OS; arm64) unknown (Codex Desktop; 26.903.61454) extra",
		"codex-tui/0.153.4\r\n",
	} {
		_, ok := ParseCodexWireProfile(ua)
		require.False(t, ok, ua)
	}
}
