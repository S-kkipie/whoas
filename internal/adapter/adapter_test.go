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

// userAdapter is a minimal user adapter named name that runs bin.
func userAdapter(name, bin string) string {
	return `name = "` + name + `"
bin  = "` + bin + `"
kind = "dir-overlay"
[dir]
global     = "{xdg_config}/` + name + `"
inject     = { env = { AAA_CONFIG = "{dir}" } }
auth_files = ["config.json"]
[login]
cmd = ["` + bin + `", "login"]
`
}

func TestDuplicateBinRejected(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "aaa.toml"), []byte(userAdapter("aaa", "mytool")), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bbb.toml"), []byte(userAdapter("bbb", "mytool")), 0o600))
	_, err := LoadRegistry(dir)
	require.ErrorContains(t, err, `adapters "aaa" and "bbb" both use bin "mytool"`)
}

func TestUserReplacesBuiltinByBin(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cloudflare.toml"), []byte(userAdapter("cloudflare", "wrangler")), 0o600))
	r, err := LoadRegistry(dir)
	require.NoError(t, err)
	_, ok := r.Get("wrangler")
	require.False(t, ok, "the built-in with the same bin is dropped")
	require.Equal(t, "", r.Source("wrangler"))
	require.Equal(t, "user", r.Source("cloudflare"))
	a, ok := r.ByBin("wrangler")
	require.True(t, ok)
	require.Equal(t, "cloudflare", a.Name)
	gh, ok := r.Get("gh")
	require.True(t, ok, "other built-ins stay")
	require.Equal(t, "builtin", r.Source(gh.Name))
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
		"login cmd bin":   {replace(token, `hint = "Create one at https://example.com/tokens"`, `cmd = ["sh", "-c", "evil"]`), `login.cmd must start with "tok"`},
		"no secrets":      {"name = \"tok\"\nbin = \"tok\"\nkind = \"env-token\"\n", "at least one [[secret]]"},
		"duplicate env":   {replace(token, `env      = "TOK_HOST"`, `env      = "TOK_KEY"`), "declared twice"},
		"bad env":         {replace(token, `env   = "TOK_KEY"`, `env   = "tok-key"`), "secret env name"},
		"secret in unset": {replace(token, `unset_env = ["TOK_LEGACY"]`, `unset_env = ["TOK_KEY"]`), "cannot also be in unset_env"},
		"only optional":   {replace(token, "label = \"Tok API Key\"\n", "label = \"Tok API Key\"\noptional = true\n"), "at least one non-optional [[secret]]"},
		"secret HOME":     {replace(token, `env   = "TOK_KEY"`, `env   = "HOME"`), "secret env HOME is a root variable"},
		"secret XDG cfg":  {replace(token, `env   = "TOK_KEY"`, `env   = "XDG_CONFIG_HOME"`), "secret env XDG_CONFIG_HOME is a root variable"},
		"secret XDG data": {replace(token, `env      = "TOK_HOST"`, `env      = "XDG_DATA_HOME"`), "secret env XDG_DATA_HOME is a root variable"},
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
		if a, _ := r.Get(n); a.Kind == KindEnvToken {
			tokens++
		}
	}
	require.GreaterOrEqual(t, tokens, 60)

	supabase, ok := r.Get("supabase")
	require.True(t, ok)
	require.Equal(t, []Secret{{Env: "SUPABASE_ACCESS_TOKEN", Label: "Supabase Access Token"}}, supabase.Secrets)
	terraform, _ := r.Get("terraform")
	require.Equal(t, "TF_TOKEN_app_terraform_io", terraform.Secrets[0].Env)
	stripe, _ := r.Get("stripe")
	require.Equal(t, "STRIPE_API_KEY", stripe.Secrets[0].Env)
	for _, n := range []string{"claude", "agent", "psql", "forge"} {
		_, ok := r.Get(n)
		require.False(t, ok, "%s is intentionally not a built-in", n)
	}
	_, ok = r.ByBin("forge")
	require.False(t, ok, "forge is Foundry's bin too")

	// competing variables that would outrank the injected token are removed
	hf, _ := r.Get("hf")
	require.Equal(t, "HF_TOKEN", hf.Secrets[0].Env)
	require.Equal(t, []string{"HUGGING_FACE_HUB_TOKEN"}, hf.UnsetEnv)
	for _, n := range []string{"fly", "flyctl"} {
		a, _ := r.Get(n)
		require.Equal(t, []string{"FLY_API_TOKEN"}, a.UnsetEnv, n)
	}

	// hints point at a real page, without placeholders
	for _, n := range r.Names() {
		a, _ := r.Get(n)
		require.NotContains(t, a.Login.Hint, "[account]", n)
		require.NotContains(t, a.Login.Hint, "mycorp", n)
	}
	postman, _ := r.Get("postman")
	require.Equal(t, "Postman API Key", postman.Secrets[0].Label)
}

func TestConflicts(t *testing.T) {
	body := replace(valid, `auth_files = ["auth.json", "nested/dir"]`, "auth_files = [\"auth.json\", \"nested/dir\"]\nconflicts  = [\"{home}/.legacy\", \"{xdg_config}/old\"]")
	a, err := Parse([]byte(body), "conflicts")
	require.NoError(t, err)
	require.Equal(t, []string{"{home}/.legacy", "{xdg_config}/old"}, a.Dir.Conflicts)

	h := t.TempDir()
	v := Vars{Home: h, XDGConfig: filepath.Join(h, ".config"), XDGData: filepath.Join(h, ".local", "share")}
	require.NoError(t, a.CheckConflicts(v))
	old := filepath.Join(h, ".config", "old")
	require.NoError(t, os.MkdirAll(old, 0o700))
	require.EqualError(t, a.CheckConflicts(v), "fake: "+old+" exists and fake uses it instead of the isolated directory; move it (see the adapter's hint) to use identities")

	for name, c := range map[string]struct{ body, errSubstr string }{
		"conflict absolute": {replace(body, `"{xdg_config}/old"`, `"/etc/old"`), "dir.conflicts"},
		"conflict relative": {replace(body, `"{xdg_config}/old"`, `"old"`), "dir.conflicts"},
		"conflict is root":  {replace(body, `"{xdg_config}/old"`, `"{home}"`), "dir.conflicts"},
		"conflict on token": {token + "[dir]\nconflicts = [\"{home}/.tok\"]\n", "[dir] is not valid"},
	} {
		_, err := Parse([]byte(c.body), name)
		require.ErrorContains(t, err, c.errSubstr, name)
	}

	r, err := LoadRegistry(filepath.Join(t.TempDir(), "none"))
	require.NoError(t, err)
	wrangler, _ := r.Get("wrangler")
	require.Equal(t, []string{"{home}/.wrangler"}, wrangler.Dir.Conflicts)
	require.Contains(t, wrangler.Login.Hint, "~/.config/.wrangler")
}

func TestEnvTokenLoginCmd(t *testing.T) {
	body := replace(token, `hint = "Create one at https://example.com/tokens"`, "cmd  = [\"tok\", \"setup-token\"]\nhint = \"Paste the token tok prints\"")
	a, err := Parse([]byte(body), "tok login")
	require.NoError(t, err, "env-token adapters may run their own bin to obtain the token")
	require.Equal(t, []string{"tok", "setup-token"}, a.Login.Cmd)
	require.Equal(t, "Paste the token tok prints", a.Login.Hint)

	_, err = Parse([]byte(token+"[mcp]\nserver = \"tok\"\n"), "mcp on env-token")
	require.ErrorContains(t, err, "[mcp] is only valid for mcp adapters")
}

const mcpAcme = `name = "mcp-acme"
kind = "mcp"

[mcp]
server  = "acme"
url     = "https://mcp.acme.example/mcp"
headers = { Authorization = "Bearer {ACME_KEY}", X-Acme-Org = "{ACME_ORG}" }

[[secret]]
env   = "ACME_KEY"
label = "Acme API key"

[[secret]]
env      = "ACME_ORG"
label    = "Acme org"
plain    = true
optional = true

[login]
hint = "Create one at https://acme.example/keys"
`

func TestMCPAdapter(t *testing.T) {
	a, err := Parse([]byte(mcpAcme), "mcp")
	require.NoError(t, err)
	require.Equal(t, KindMCP, a.Kind)
	require.Empty(t, a.Bin)
	require.Equal(t, MCPSpec{
		Server:  "acme",
		URL:     "https://mcp.acme.example/mcp",
		Headers: map[string]string{"Authorization": "Bearer {ACME_KEY}", "X-Acme-Org": "{ACME_ORG}"},
	}, a.MCP)

	h, err := a.RenderHeaders(map[string]string{"ACME_KEY": "k1", "ACME_ORG": "org9"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"Authorization": "Bearer k1", "X-Acme-Org": "org9"}, h)

	h, err = a.RenderHeaders(map[string]string{"ACME_KEY": "k1"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"Authorization": "Bearer k1"}, h, "a header using an empty optional secret is omitted")

	_, err = a.RenderHeaders(map[string]string{"ACME_ORG": "org9"})
	require.EqualError(t, err, "mcp-acme: identity has no ACME_KEY")

	_, err = a.RenderHeaders(map[string]string{"ACME_KEY": "k1\r\nX-Evil: 1"})
	require.ErrorContains(t, err, "contains CR or LF")

	for _, u := range []string{"http://127.0.0.1:8080/mcp", "http://localhost/mcp", "http://[::1]:9/mcp"} {
		_, err := Parse([]byte(replace(mcpAcme, "https://mcp.acme.example/mcp", u)), u)
		require.NoError(t, err, "http is allowed for loopback %s", u)
	}
	_, err = Parse([]byte(replace(mcpAcme, `server  = "acme"`, "server  = \"acme\"\nauth    = \"header\"")), "auth header")
	require.NoError(t, err)
}

func TestMCPRejects(t *testing.T) {
	cases := map[string]struct{ body, errSubstr string }{
		"bin":              {replace(mcpAcme, `kind = "mcp"`, "kind = \"mcp\"\nbin = \"acme\""), "bin is not valid for mcp adapters"},
		"dir":              {mcpAcme + "[dir]\nglobal = \"{home}/.acme\"\n", "[dir] is not valid for mcp adapters"},
		"unset_env":        {replace(mcpAcme, `kind = "mcp"`, "kind = \"mcp\"\nunset_env = [\"ACME_TOKEN\"]"), "unset_env is not valid for mcp adapters"},
		"whoami":           {mcpAcme + "[whoami]\njson = \"user\"\n", "[whoami] is not valid for mcp adapters"},
		"login cmd":        {replace(mcpAcme, `hint = "Create one at https://acme.example/keys"`, `cmd = ["acme", "login"]`), "login.cmd is not valid for mcp adapters"},
		"server missing":   {replace(mcpAcme, `server  = "acme"`, ``), "mcp.server"},
		"server bad":       {replace(mcpAcme, `server  = "acme"`, `server  = "Acme Server"`), "mcp.server"},
		"server reserved":  {replace(mcpAcme, `server  = "acme"`, `server  = "run"`), "mcp.server"},
		"url http remote":  {replace(mcpAcme, "https://mcp.acme.example/mcp", "http://mcp.acme.example/mcp"), "only allowed for a loopback host"},
		"url ftp":          {replace(mcpAcme, "https://mcp.acme.example/mcp", "ftp://mcp.acme.example/mcp"), "must use https"},
		"url relative":     {replace(mcpAcme, "https://mcp.acme.example/mcp", "/mcp"), "absolute http(s) URL"},
		"url credentials":  {replace(mcpAcme, "https://mcp.acme.example/mcp", "https://u:p@mcp.acme.example/mcp"), "without credentials"},
		"no headers":       {replace(mcpAcme, `headers = { Authorization = "Bearer {ACME_KEY}", X-Acme-Org = "{ACME_ORG}" }`, ``), "mcp.headers needs at least one header"},
		"bad header name":  {replace(mcpAcme, `X-Acme-Org = "{ACME_ORG}"`, `"X Acme" = "{ACME_ORG}"`), "not a valid HTTP header name"},
		"header CRLF":      {replace(mcpAcme, `"Bearer {ACME_KEY}"`, `"Bearer {ACME_KEY}\r\nX-Evil: 1"`), "contains CR or LF"},
		"undeclared var":   {replace(mcpAcme, `"Bearer {ACME_KEY}"`, `"Bearer {ACME_KEY}{ACME_SECRET}"`), "{ACME_SECRET}, which is not a declared [[secret]]"},
		"typo var":         {replace(mcpAcme, `"Bearer {ACME_KEY}"`, `"Bearer {ACME-KEY}"`), "{ACME-KEY}, which is not a declared [[secret]]"},
		"unused secret":    {replace(mcpAcme, `X-Acme-Org = "{ACME_ORG}"`, `X-Acme-Org = "fixed"`), "secret ACME_ORG is not used"},
		"no secrets":       {"name = \"mcp-acme\"\nkind = \"mcp\"\n[mcp]\nserver = \"acme\"\nurl = \"https://a.example/mcp\"\nheaders = { X-A = \"1\" }\n", "mcp adapters need at least one [[secret]]"},
		"only optional":    {replace(mcpAcme, "label = \"Acme API key\"\n", "label = \"Acme API key\"\noptional = true\n"), "at least one non-optional [[secret]]"},
		"duplicate secret": {replace(mcpAcme, `env      = "ACME_ORG"`, `env      = "ACME_KEY"`), "declared twice"},
		"bad secret env":   {replace(mcpAcme, `env   = "ACME_KEY"`, `env   = "acme-key"`), "secret env name"},
		"auth oauth":       {replace(mcpAcme, `server  = "acme"`, "server  = \"acme\"\nauth    = \"oauth\""), `"oauth" is not supported yet`},
		"auth other":       {replace(mcpAcme, `server  = "acme"`, "server  = \"acme\"\nauth    = \"basic\""), `mcp.auth "basic" not supported`},
		"unknown mcp key":  {replace(mcpAcme, `server  = "acme"`, "server  = \"acme\"\ntransport = \"sse\""), "unknown keys"},
	}
	for name, c := range cases {
		_, err := Parse([]byte(c.body), name)
		require.ErrorContains(t, err, c.errSubstr, name)
	}
}

// mcpAdapter is a minimal user mcp adapter named name for server.
func mcpAdapter(name, server string) string {
	return `name = "` + name + `"
kind = "mcp"
[mcp]
server  = "` + server + `"
url     = "http://127.0.0.1:1/mcp"
headers = { Authorization = "Bearer {K}" }
[[secret]]
env = "K"
`
}

func TestMCPRegistry(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.toml"), []byte(mcpAdapter("mcp-a", "alpha")), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.toml"), []byte(mcpAdapter("mcp-b", "beta")), 0o600))
	r, err := LoadRegistry(dir)
	require.NoError(t, err, "mcp adapters have no bin, so they never collide on it")
	a, ok := r.ByServer("beta")
	require.True(t, ok)
	require.Equal(t, "mcp-b", a.Name)
	_, ok = r.ByServer("gamma")
	require.False(t, ok)
	_, ok = r.ByServer("")
	require.False(t, ok)
	_, ok = r.ByBin("")
	require.False(t, ok, "an empty bin never matches an mcp adapter")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "c.toml"), []byte(mcpAdapter("mcp-c", "alpha")), 0o600))
	_, err = LoadRegistry(dir)
	require.ErrorContains(t, err, `adapters "mcp-a" and "mcp-c" both use mcp.server "alpha"`)
}
