//go:build unix

package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const valid = `name = "fake"
bin  = "fake"
kind = "dir-overlay"
[dir]
global     = "{home}/.fake"
inject     = { env = { FAKE_DIR = "{dir}" } }
auth_files = ["auth.json", "nested/dir"]
[login]
cmd = ["fake", "login"]
[whoami]
cmd  = ["fake", "whoami"]
json = "user.email"
`

func TestBuiltinsLoad(t *testing.T) {
	r, err := LoadRegistry(filepath.Join(t.TempDir(), "none"))
	require.NoError(t, err)
	require.Equal(t, []string{"codex", "gcloud", "gh", "neonctl"}, r.Names())
	gh, ok := r.ByBin("gh")
	require.True(t, ok)
	require.Equal(t, []string{"GH_CONFIG_DIR=/o"}, gh.InjectEnv("/o"))
	neon, _ := r.Get("neonctl")
	require.Equal(t, []string{"--config-dir", "/o"}, neon.InjectArgs("/o"))
}

func TestUserAdapterOverridesAndExpands(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fake.toml"), []byte(valid), 0o600))
	r, err := LoadRegistry(dir)
	require.NoError(t, err)
	a, ok := r.Get("fake")
	require.True(t, ok)
	require.Equal(t, "/h/.fake", a.GlobalDir(Vars{Home: "/h"}))
	require.Equal(t, []string{"FAKE_DIR=/run/x"}, a.InjectEnv("/run/x"))
	require.Empty(t, a.InjectArgs("/run/x"))
	require.Equal(t, "user.email", a.Whoami.JSON)
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"unknown key":   valid + "colour = \"blue\"\n",
		"bad kind":      replace(valid, `kind = "dir-overlay"`, `kind = "env-token"`),
		"abs auth":      replace(valid, `"auth.json", "nested/dir"`, `"/etc/passwd"`),
		"dotdot auth":   replace(valid, `"auth.json", "nested/dir"`, `"../x"`),
		"no dir ref":    replace(valid, `FAKE_DIR = "{dir}"`, `FAKE_DIR = "/tmp"`),
		"both injects":  replace(valid, `inject     = { env = { FAKE_DIR = "{dir}" } }`, `inject     = { env = { FAKE_DIR = "{dir}" }, flag = ["--d", "{dir}"] }`),
		"login bin":     replace(valid, `cmd = ["fake", "login"]`, `cmd = ["sh", "-c", "evil"]`),
		"bin with path": replace(valid, `bin  = "fake"`, `bin  = "/usr/bin/fake"`),
		"reserved name": replace(valid, `name = "fake"`, `name = "run"`),
	}
	for name, body := range cases {
		_, err := Parse([]byte(body), name)
		require.Error(t, err, name)
	}
}

func replace(s, old, new string) string { return strings.Replace(s, old, new, 1) }
