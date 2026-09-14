package openai

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexUserAgentVersion(t *testing.T) {
	require.Equal(t, "0.146.0", CodexUserAgentVersion("codex_cli_rs/0.146.0 (Ubuntu 22.4.0; x86_64) xterm-256color"))
	require.Equal(t, "0.146.0", CodexUserAgentVersion("  codex_cli_rs/0.146.0  "))
	// 预发布后缀原样保留：出站 version 头必须与 UA 版本段逐字一致。
	require.Equal(t, "0.147.0-alpha.4", CodexUserAgentVersion("codex-tui/0.147.0-alpha.4 (Mac OS X 14.0; arm64) iTerm"))
	// 非 `{client}/{version}` 形态取不到版本段。
	require.Empty(t, CodexUserAgentVersion("curl 8.7.1"))
	require.Empty(t, CodexUserAgentVersion("/0.146.0"))
	require.Empty(t, CodexUserAgentVersion(""))
}
