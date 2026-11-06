//go:build unix

package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMCPSetupCommands(t *testing.T) {
	s := mcpSetup{agent: "claude", scope: "user", server: "posthog", command: "/opt/whoas", args: []string{"mcp", "proxy", "posthog"}}
	require.Equal(t, []string{"claude", "mcp", "get", "posthog"}, s.getCmd())
	require.Equal(t, []string{"claude", "mcp", "remove", "posthog"}, s.removeCmd())
	require.Equal(t, []string{"claude", "mcp", "add-json", "--scope", "user", "posthog", `{"type":"stdio","command":"/opt/whoas","args":["mcp","proxy","posthog"]}`}, s.addCmd())

	s = mcpSetup{agent: "codex", server: "linear", command: "/opt/whoas", args: []string{"mcp", "proxy", "mcp-linear@team"}}
	require.Equal(t, []string{"codex", "mcp", "add", "linear", "--", "/opt/whoas", "mcp", "proxy", "mcp-linear@team"}, s.addCmd())
	require.Equal(t, []string{"codex", "mcp", "remove", "linear"}, s.removeCmd())

	require.Equal(t, `claude mcp add-json --scope user posthog '{"type":"stdio","command":"/opt/whoas","args":["mcp","proxy","posthog"]}'`,
		shellQuote([]string{"claude", "mcp", "add-json", "--scope", "user", "posthog", `{"type":"stdio","command":"/opt/whoas","args":["mcp","proxy","posthog"]}`}))
	require.Equal(t, `a 'it'\''s' ''`, shellQuote([]string{"a", "it's", ""}))
}

func TestPrintEntry(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, printEntry(&out, "posthog", "/opt/whoas", []string{"mcp", "proxy", "posthog"}))
	require.Equal(t, `{
  "mcpServers": {
    "posthog": {
      "command": "/opt/whoas",
      "args": [
        "mcp",
        "proxy",
        "posthog"
      ]
    }
  }
}
`, out.String())
}

// fakeAgents puts shell-script claude and codex on PATH; they log their
// argv to the returned file and keep "registered" servers as files.
func fakeAgents(t *testing.T) string {
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.log")
	script := `#!/bin/sh
name=$(basename "$0")
echo "$name $*" >> '` + log + `'
reg='` + dir + `'/"$name"-
case "$2" in
get) test -e "$reg$3" ;;
remove) rm "$reg$3" ;;
add-json) echo "$6" > "$reg$5" ;;
add) echo "$*" > "$reg$3" ;;
esac
`
	for _, name := range []string{"claude", "codex"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755))
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func setup(args ...string) (string, error) {
	var out bytes.Buffer
	root := newRoot()
	root.SetArgs(append([]string{"mcp", "setup"}, args...))
	root.SetOut(&out)
	root.SetErr(&out)
	err := root.Execute()
	return out.String(), err
}

func TestMCPSetup(t *testing.T) {
	proxyEnv(t)
	log := fakeAgents(t)
	self, err := whoasPath()
	require.NoError(t, err)
	entry := `{"type":"stdio","command":"` + self + `","args":["mcp","proxy","posthog"]}`

	out, err := setup("posthog", "--agent", "claude")
	require.NoError(t, err)
	require.Equal(t, shellQuote([]string{"claude", "mcp", "add-json", "--scope", "user", "posthog", entry})+"\n", out)

	_, err = setup("mcp-posthog", "--agent", "claude", "--scope", "project")
	require.EqualError(t, err, `claude already has an MCP server named "posthog" (see: claude mcp get posthog); use --replace to replace it`)

	out, err = setup("mcp-posthog", "--agent", "claude", "--scope", "project", "--replace", "--alias", "acme")
	require.NoError(t, err)
	require.Equal(t, "claude mcp remove posthog\n"+
		shellQuote([]string{"claude", "mcp", "add-json", "--scope", "project", "posthog", `{"type":"stdio","command":"` + self + `","args":["mcp","proxy","mcp-posthog@acme"]}`})+"\n", out)

	out, err = setup("linear", "--agent", "codex")
	require.NoError(t, err)
	require.Equal(t, shellQuote([]string{"codex", "mcp", "add", "linear", "--", self, "mcp", "proxy", "linear"})+"\n", out)
	_, err = setup("linear", "--agent", "codex")
	require.ErrorContains(t, err, `codex already has an MCP server named "linear"`)

	data, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, []string{
		"claude mcp get posthog",
		"claude mcp add-json --scope user posthog " + entry,
		"claude mcp get posthog",
		"claude mcp get posthog",
		"claude mcp remove posthog",
		"claude mcp add-json --scope project posthog " + `{"type":"stdio","command":"` + self + `","args":["mcp","proxy","mcp-posthog@acme"]}`,
		"codex mcp get linear",
		"codex mcp add linear -- " + self + " mcp proxy linear",
		"codex mcp get linear",
	}, strings.Split(strings.TrimSpace(string(data)), "\n"))

	out, err = setup("posthog", "--print", "--alias", "acme")
	require.NoError(t, err)
	require.Contains(t, out, `"command": "`+self+`"`)
	require.Contains(t, out, `"mcp-posthog@acme"`)
}

func TestMCPSetupRejects(t *testing.T) {
	proxyEnv(t)
	fakeAgents(t)
	for _, c := range []struct {
		args []string
		err  string
	}{
		{[]string{"posthog"}, "--agent claude|codex (or --print) is required"},
		{[]string{"posthog", "--agent", "cursor"}, `unknown agent "cursor" (claude|codex; --print for others)`},
		{[]string{"posthog", "--agent", "codex", "--scope", "user"}, "codex has no scopes; --scope is only for --agent claude"},
		{[]string{"posthog", "--agent", "claude", "--scope", "global"}, `unknown scope "global" (user|local|project)`},
		{[]string{"posthog", "--print", "--agent", "claude"}, "--print cannot be combined with --agent, --scope or --replace"},
		{[]string{"posthog@acme", "--agent", "claude"}, "pass the alias with --alias, not server@alias"},
		{[]string{"posthog", "--agent", "claude", "--alias", "Bad Alias"}, "invalid name"},
		{[]string{"gh", "--agent", "claude"}, `"gh" is not an MCP server or mcp adapter`},
	} {
		_, err := setup(c.args...)
		require.ErrorContains(t, err, c.err, c.args)
	}
}
