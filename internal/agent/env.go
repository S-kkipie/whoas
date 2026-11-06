//go:build unix

package agent

import (
	"slices"
	"strings"
)

// agentEnvNames and agentEnvPrefixes allowlist what the long-lived agent may
// inherit from whoever spawned it. Spawn can run inside a "whoas <id>"
// session, whose env holds injected tokens (CLAUDE_CODE_OAUTH_TOKEN, API
// keys); the agent needs none of them, only what locates its dirs, the
// keyring/session bus and the WSL bridge.
var (
	agentEnvNames = []string{
		"HOME", "USER", "LOGNAME", "PATH", "SHELL", "TMPDIR", "TZ", "LANG",
		"DISPLAY", "WAYLAND_DISPLAY", "DBUS_SESSION_BUS_ADDRESS", "WSLENV",
	}
	agentEnvPrefixes = []string{"LC_", "XDG_", "WSL_", "WHOAS_"}
)

// agentEnv keeps the allowlisted entries of environ, in order. The result is
// never nil: a nil exec.Cmd.Env would inherit the whole environment.
func agentEnv(environ []string) []string {
	out := []string{}
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			continue
		}
		if slices.Contains(agentEnvNames, name) || slices.ContainsFunc(agentEnvPrefixes, func(p string) bool {
			return strings.HasPrefix(name, p)
		}) {
			out = append(out, kv)
		}
	}
	return out
}
