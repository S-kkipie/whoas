//go:build unix

package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgentEnv(t *testing.T) {
	kept := []string{
		"HOME=/home/u", "USER=u", "LOGNAME=u", "PATH=/usr/bin", "SHELL=/bin/zsh",
		"TMPDIR=/tmp/u", "TZ=UTC", "LANG=C.UTF-8", "DISPLAY=:0", "WAYLAND_DISPLAY=wayland-0",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus", "WSLENV=WT_SESSION",
		"LC_ALL=C", "XDG_RUNTIME_DIR=/run/user/1000", "WSL_DISTRO_NAME=Ubuntu",
		"WHOAS_RUNTIME_DIR=/tmp/wr", "WHOAS_CONFIG_DIR=/cfg",
	}
	dropped := []string{
		"CLAUDE_CODE_OAUTH_TOKEN=x", "ANTHROPIC_API_KEY=x", "POSTHOG_API_KEY=x",
		"GITHUB_TOKEN=x", "GH_TOKEN=x", "AWS_SECRET_ACCESS_KEY=x",
		"HOMEBREW_GITHUB_API_TOKEN=x", // HOME is exact, not a prefix
		"PATHEXT=x", "XDG=x", "LC=x", "WHOAS=x", "MY_WHOAS_TOKEN=x",
		"=C:=C:\\", "malformed",
	}
	require.Equal(t, kept, agentEnv(append(append([]string{}, kept...), dropped...)))
	require.Equal(t, kept, agentEnv(append(append([]string{}, dropped...), kept...)), "order kept")

	// nil Env would make exec inherit everything: an empty result must stay non-nil.
	got := agentEnv([]string{"GITHUB_TOKEN=x"})
	require.NotNil(t, got)
	require.Empty(t, got)
}
