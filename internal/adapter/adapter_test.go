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
	for _, n := range []string{"codex", "docker", "gcloud", "gh", "neonctl", "railway", "vercel", "wrangler"} {
		_, ok := r.Get(n)
		require.True(t, ok, n)
		require.Equal(t, "builtin", r.Source(n), n)
	}
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
		"bad kind":      {replace(valid, `kind = "dir-overlay"`, `kind = "magic"`), "not supported"},
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

const rootHome = `name = "rh"
bin  = "rh"
kind = "dir-overlay"
experimental = true
[dir]
global      = "{home}"
inject      = { env = { HOME = "{dir}" } }
auth_files  = [".rh/config.json"]
helper_keys = ["credsStore", "credHelpers"]
[login]
cmd = ["rh", "login"]
`

const token = `name = "tok"
bin  = "tok"
kind = "env-token"
unset_env = ["TOK_LEGACY"]

[[secret]]
env   = "TOK_KEY"
label = "Tok API Key"

[[secret]]
env      = "TOK_HOST"
label    = "Tok Host"
plain    = true
optional = true

[login]
hint = "Create one at https://example.com/tokens"
`

func TestRootOverlays(t *testing.T) {
	a, err := Parse([]byte(rootHome), "rh")
	require.NoError(t, err)
	require.True(t, a.Experimental)
	require.Equal(t, []string{"credsStore", "credHelpers"}, a.Dir.HelperKeys)
	require.Equal(t, "/h", a.GlobalDir(Vars{Home: "/h"}))

	xdg := replace(replace(rootHome, `global      = "{home}"`, `global      = "{xdg_config}"`), `HOME = "{dir}"`, `XDG_CONFIG_HOME = "{dir}"`)
	_, err = Parse([]byte(xdg), "xdg")
	require.NoError(t, err)

	for name, c := range map[string]struct{ body, errSubstr string }{
		"root without its var":  {replace(rootHome, `HOME = "{dir}"`, `RH_DIR = "{dir}"`), `requires inject env HOME`},
		"root with wrong var":   {replace(rootHome, `HOME = "{dir}"`, `XDG_CONFIG_HOME = "{dir}"`), `requires inject env HOME`},
		"root var on subdir":    {replace(rootHome, `global      = "{home}"`, `global      = "{home}/.rh"`), `only allowed with dir.global = {home}`},
		"root var not plain":    {replace(rootHome, `HOME = "{dir}"`, `HOME = "{dir}/x"`), `requires inject env HOME`},
		"bad helper key":        {replace(rootHome, `"credsStore", "credHelpers"`, `"creds.Store"`), `plain top-level JSON key`},
		"secret on dir-overlay": {rootHome + "[[secret]]\nenv = \"X\"\n", `only valid for env-token`},
	} {
		_, err := Parse([]byte(c.body), name)
		require.ErrorContains(t, err, c.errSubstr, name)
	}
}

func TestEnvTokenAdapter(t *testing.T) {
	a, err := Parse([]byte(token), "tok")
	require.NoError(t, err)
	require.Equal(t, KindEnvToken, a.Kind)
	require.Equal(t, []Secret{
		{Env: "TOK_KEY", Label: "Tok API Key"},
		{Env: "TOK_HOST", Label: "Tok Host", Plain: true, Optional: true},
	}, a.Secrets)
	require.Equal(t, "Tok API Key (TOK_KEY)", a.Secrets[0].PromptLabel())
	require.Equal(t, "X", Secret{Env: "X"}.PromptLabel())

	lower := replace(token, `env   = "TOK_KEY"`, `env   = "TF_TOKEN_app_terraform_io"`)
	_, err = Parse([]byte(lower), "lowercase env")
	require.NoError(t, err, "env var names may contain lowercase")

	for name, c := range map[string]struct{ body, errSubstr string }{
		"dir section":     {token + "[dir]\nglobal = \"{home}/.t\"\n", "[dir] is not valid"},
		"login cmd":       {replace(token, `hint = "Create one at https://example.com/tokens"`, `cmd = ["tok", "login"]`), "login.cmd is not valid"},
		"no secrets":      {"name = \"tok\"\nbin = \"tok\"\nkind = \"env-token\"\n", "at least one [[secret]]"},
		"duplicate env":   {replace(token, `env      = "TOK_HOST"`, `env      = "TOK_KEY"`), "declared twice"},
		"bad env":         {replace(token, `env   = "TOK_KEY"`, `env   = "tok-key"`), "secret env name"},
		"secret in unset": {replace(token, `unset_env = ["TOK_LEGACY"]`, `unset_env = ["TOK_KEY"]`), "cannot also be in unset_env"},
	} {
		_, err := Parse([]byte(c.body), name)
		require.ErrorContains(t, err, c.errSubstr, name)
	}
}

func TestSecretEnv(t *testing.T) {
	a, err := Parse([]byte(token), "tok")
	require.NoError(t, err)
	base := []string{"PATH=/bin", "TOK_LEGACY=old", "TOK_KEY=inherited", "TOK_KEYX=keep"}
	got := a.SecretEnv(base, map[string]string{"TOK_KEY": "k1", "TOK_HOST": ""})
	require.Equal(t, []string{"PATH=/bin", "TOK_KEYX=keep", "TOK_KEY=k1"}, got)
}

func TestRegistrySource(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tok.toml"), []byte(token), 0o600))
	r, err := LoadRegistry(dir)
	require.NoError(t, err)
	require.Equal(t, "user", r.Source("tok"))
	require.Equal(t, "builtin", r.Source("gh"))
	require.Equal(t, "", r.Source("nope"))
}

func TestDirOverlayBuiltins(t *testing.T) {
	r, err := LoadRegistry(filepath.Join(t.TempDir(), "none"))
	require.NoError(t, err)
	v := Vars{Home: "/h", XDGConfig: "/h/.config", XDGData: "/h/.local/share"}

	vercel, _ := r.Get("vercel")
	require.Equal(t, "/h/.local/share/com.vercel.cli", vercel.GlobalDir(v))
	require.Equal(t, []string{"--global-config", "/o"}, vercel.InjectArgs("/o"))
	require.Equal(t, []string{"auth.json", "config.json"}, vercel.Dir.AuthFiles)

	wrangler, _ := r.Get("wrangler")
	require.Equal(t, "/h/.config", wrangler.GlobalDir(v))
	require.Equal(t, []string{"XDG_CONFIG_HOME=/o"}, wrangler.InjectEnv("/o"))

	railway, _ := r.Get("railway")
	require.True(t, railway.Experimental)
	require.Equal(t, "/h", railway.GlobalDir(v))
	require.Equal(t, []string{"HOME=/o"}, railway.InjectEnv("/o"))

	docker, _ := r.Get("docker")
	require.Equal(t, []string{"credsStore", "credHelpers"}, docker.Dir.HelperKeys)
	require.Equal(t, []string{"DOCKER_CONFIG=/o"}, docker.InjectEnv("/o"))
}

func TestTokenBuiltins(t *testing.T) {
	r, err := LoadRegistry(filepath.Join(t.TempDir(), "none"))
	require.NoError(t, err, "every built-in must load, validate and have a unique bin")

	tokens := 0
	for _, n := range r.Names() {
		a, _ := r.Get(n)
		if a.Kind != KindEnvToken {
			continue
		}
		tokens++
		required := 0
		for _, s := range a.Secrets {
			if !s.Optional {
				required++
			}
		}
		require.Positive(t, required, "%s needs at least one required secret", n)
	}
	require.GreaterOrEqual(t, tokens, 60)

	supabase, ok := r.Get("supabase")
	require.True(t, ok)
	require.Equal(t, []Secret{{Env: "SUPABASE_ACCESS_TOKEN", Label: "Supabase Access Token"}}, supabase.Secrets)
	terraform, _ := r.Get("terraform")
	require.Equal(t, "TF_TOKEN_app_terraform_io", terraform.Secrets[0].Env)
	stripe, _ := r.Get("stripe")
	require.Equal(t, "STRIPE_API_KEY", stripe.Secrets[0].Env)
	for _, n := range []string{"claude", "agent", "psql"} {
		_, ok := r.Get(n)
		require.False(t, ok, "%s is intentionally not a built-in", n)
	}
}
