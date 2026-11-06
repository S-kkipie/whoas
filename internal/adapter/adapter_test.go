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
	_, err := Parse([]byte(valid), "valid")
	require.NoError(t, err)

	cases := map[string]struct {
		body      string
		errSubstr string
	}{
		"unknown key":   {valid + "colour = \"blue\"\n", "unknown keys"},
		"bad kind":      {replace(valid, `kind = "dir-overlay"`, `kind = "env-token"`), "not supported"},
		"abs auth":      {replace(valid, `"auth.json", "nested/dir"`, `"/etc/passwd"`), "clean relative slash path"},
		"dotdot auth":   {replace(valid, `"auth.json", "nested/dir"`, `"../x"`), "clean relative slash path"},
		"no dir ref":    {replace(valid, `FAKE_DIR = "{dir}"`, `FAKE_DIR = "/tmp"`), "must reference {dir}"},
		"both injects":  {replace(valid, `inject     = { env = { FAKE_DIR = "{dir}" } }`, `inject     = { env = { FAKE_DIR = "{dir}" }, flag = ["--d", "{dir}"] }`), "exactly one"},
		"login bin":     {replace(valid, `cmd = ["fake", "login"]`, `cmd = ["sh", "-c", "evil"]`), "must start with"},
		"bin with path": {replace(valid, `bin  = "fake"`, `bin  = "/usr/bin/fake"`), "bare command name"},
		"reserved name": {replace(valid, `name = "fake"`, `name = "run"`), "reserved"},
		"bin -x":        {replace(valid, `bin  = "fake"`, `bin  = "-x"`), "bare command name"},
		"bin ..":        {replace(valid, `bin  = "fake"`, `bin  = ".."`), "bare command name"},
		"env not {dir}": {replace(valid, `inject     = { env = { FAKE_DIR = "{dir}" } }`, `inject     = { env = { FAKE_DIR = "{dir}", LD_PRELOAD = "/tmp/x.so" } }`), "must reference {dir}"},
		"global /etc":   {replace(valid, `global     = "{home}/.fake"`, `global     = "/etc/fake"`), "must start with"},
		"global rel":    {replace(valid, `global     = "{home}/.fake"`, `global     = "relative/x"`), "must start with"},
	}
	for name, tc := range cases {
		_, err := Parse([]byte(tc.body), name)
		require.ErrorContains(t, err, tc.errSubstr, name)
	}
}

func replace(s, old, new string) string { return strings.Replace(s, old, new, 1) }

func TestUserOverridesBuiltinByName(t *testing.T) {
	dir := t.TempDir()
	userGH := `name = "gh"
bin  = "gh"
kind = "dir-overlay"
[dir]
global     = "{xdg_config}/my-gh"
inject     = { env = { GH_CONFIG_DIR = "{dir}" } }
auth_files = ["hosts.yml"]
[login]
cmd = ["gh", "auth", "login", "--insecure-storage"]
[whoami]
cmd  = ["gh", "api", "user"]
json = "login"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gh.toml"), []byte(userGH), 0o600))
	r, err := LoadRegistry(dir)
	require.NoError(t, err)
	a, ok := r.Get("gh")
	require.True(t, ok)
	require.Equal(t, "{xdg_config}/my-gh", a.Dir.Global)
}

func TestDuplicateBinRejected(t *testing.T) {
	dir := t.TempDir()
	aaa := `name = "aaa"
bin  = "gh"
kind = "dir-overlay"
[dir]
global     = "{xdg_config}/aaa"
inject     = { env = { AAA_CONFIG = "{dir}" } }
auth_files = ["config.json"]
[login]
cmd = ["gh", "auth", "login"]
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "aaa.toml"), []byte(aaa), 0o600))
	_, err := LoadRegistry(dir)
	require.ErrorContains(t, err, "both use bin")
}

func TestChildEnv(t *testing.T) {
	a, err := Parse([]byte(strings.Replace(valid, `kind = "dir-overlay"`, "kind = \"dir-overlay\"\nunset_env = [\"FAKE_TOKEN\"]", 1)), "t")
	require.NoError(t, err)
	got := a.ChildEnv([]string{"PATH=/bin", "FAKE_TOKEN=leak", "FAKE_DIR=/global", "FAKE_TOKENX=keep"}, "/ov")
	require.Equal(t, []string{"PATH=/bin", "FAKE_DIR=/global", "FAKE_TOKENX=keep", "FAKE_DIR=/ov"}, got)

	_, err = Parse([]byte(strings.Replace(valid, `kind = "dir-overlay"`, "kind = \"dir-overlay\"\nunset_env = [\"bad-name\"]", 1)), "t")
	require.ErrorContains(t, err, "unset_env name")
}

func TestBuiltinUnsetEnv(t *testing.T) {
	r, err := LoadRegistry(filepath.Join(t.TempDir(), "none"))
	require.NoError(t, err)
	gh, _ := r.Get("gh")
	require.Contains(t, gh.UnsetEnv, "GH_TOKEN")
	neon, _ := r.Get("neonctl")
	require.Equal(t, []string{"NEON_API_KEY"}, neon.UnsetEnv)
}
