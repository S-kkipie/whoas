//go:build unix

package app

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/S-kkipie/whoas/internal/envelope"
	"github.com/S-kkipie/whoas/internal/ref"
	"github.com/S-kkipie/whoas/internal/trust"
)

// proxyEnv points whoas at fresh directories (master key in the env store)
// and returns the root.
func proxyEnv(t *testing.T) string {
	root := t.TempDir()
	rt, err := os.MkdirTemp("", "wr") // short: unix socket paths are limited
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(rt) })
	home := filepath.Join(root, "home")
	for k, v := range map[string]string{
		"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, ".local", "share"),
		"WHOAS_CONFIG_DIR": filepath.Join(root, "cfg"), "WHOAS_DATA_DIR": filepath.Join(root, "data"), "WHOAS_RUNTIME_DIR": rt,
		"WHOAS_PROFILE": "", "WHOAS_CONTEXT_DIR": "", "WHOAS_KEYSTORE": "", "WHOAS_PASSPHRASE": "",
	} {
		t.Setenv(k, v)
	}
	id, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	t.Setenv("WHOAS_MASTER_KEY", id.String())
	require.NoError(t, os.MkdirAll(filepath.Join(root, "cfg", "adapters"), 0o700))
	t.Chdir(root)
	return root
}

func seal(t *testing.T, r ref.Ref, values map[string]string) {
	e, err := loadEnv()
	require.NoError(t, err)
	v, err := e.initVault()
	require.NoError(t, err)
	require.NoError(t, v.Seal(r, envelope.FromSecrets(r.Provider, values)))
}

func proxy(arg string) (int, string) {
	var stderr bytes.Buffer
	code := mcpProxy(context.Background(), arg, strings.NewReader(""), io.Discard, &stderr)
	return code, stderr.String()
}

func TestMCPAdapterArg(t *testing.T) {
	proxyEnv(t)
	e, err := loadEnv()
	require.NoError(t, err)
	for arg, want := range map[string][2]string{
		"posthog":          {"mcp-posthog", ""},
		"mcp-posthog":      {"mcp-posthog", ""},
		"mcp-linear@team":  {"mcp-linear", "team"},
		"posthog@acme":     {"mcp-posthog", "acme"},
		"linear@personal":  {"mcp-linear", "personal"},
		"mcp-posthog@acme": {"mcp-posthog", "acme"},
	} {
		a, alias, err := e.mcpAdapter(arg)
		require.NoError(t, err, arg)
		require.Equal(t, want, [2]string{a.Name, alias}, arg)
	}
	for _, arg := range []string{"gh", "claude@max", "nope", "Bad Name"} {
		_, _, err := e.mcpAdapter(arg)
		require.Error(t, err, arg)
	}
}

func TestMCPProxyNoIdentity(t *testing.T) {
	root := proxyEnv(t)
	code, stderr := proxy("posthog")
	require.Equal(t, 2, code)
	require.Equal(t, "whoas: mcp proxy posthog: no identity for mcp-posthog here (no profile); add one with whoas add mcp-posthog <alias>\n", stderr)

	require.NoError(t, os.WriteFile(filepath.Join(root, "cfg", "config.yaml"), []byte("default_profile: personal\nprofiles:\n  personal:\n    gh: me\n  work:\n    gh: corp\n"), 0o600))
	code, stderr = proxy("posthog")
	require.Equal(t, 2, code)
	require.Contains(t, stderr, "no identity for mcp-posthog here (profile personal);")
	t.Setenv("WHOAS_PROFILE", "work")
	code, stderr = proxy("mcp-posthog")
	require.Equal(t, 2, code)
	require.Equal(t, "whoas: mcp proxy mcp-posthog: no identity for mcp-posthog here (profile work); add one with whoas add mcp-posthog <alias>\n", stderr)

	code, stderr = proxy("posthog@acme")
	require.Equal(t, 2, code)
	require.Equal(t, "whoas: mcp proxy posthog@acme: identity mcp-posthog@acme (explicit) not found; add it with whoas add mcp-posthog acme\n", stderr)

	code, stderr = proxy("gh")
	require.Equal(t, 2, code, "a word that names no mcp adapter is a usage error")
	require.Contains(t, stderr, `"gh" is not an MCP server or mcp adapter`)
}

func TestMCPProxyLockedVault(t *testing.T) {
	root := proxyEnv(t)
	seal(t, ref.Ref{Provider: "mcp-posthog", Alias: "acme"}, map[string]string{"POSTHOG_API_KEY": "phx-secret"})
	// the master key now sits in a passphrase store and nobody gave the passphrase
	t.Setenv("WHOAS_MASTER_KEY", "")
	t.Setenv("WHOAS_KEYSTORE", "passphrase")
	require.NoError(t, os.WriteFile(filepath.Join(root, "data", "master.age"), []byte("sealed"), 0o600))
	code, stderr := proxy("mcp-posthog@acme")
	require.Equal(t, 3, code)
	require.Equal(t, "whoas: mcp proxy mcp-posthog@acme: the vault is locked and its key needs a terminal; run whoas unlock\n", stderr)
}

// fakeMCP is a Streamable HTTP MCP server that accepts only "Bearer <key>"
// and returns the key it got from its whoami tool.
func fakeMCP(t *testing.T, key string) string {
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "whoami"}, func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		got := strings.TrimPrefix(req.Extra.Header.Get("Authorization"), "Bearer ")
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "key=" + got}}}, nil, nil
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, "invalid API key", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp"
}

// callWhoami runs the proxy for arg on pipes, calls whoami through it as an
// agent would, and returns the tool's text, the exit code and stderr.
func callWhoami(t *testing.T, arg string) (string, int, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	toProxyR, toProxyW := io.Pipe()
	fromProxyR, fromProxyW := io.Pipe()
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- mcpProxy(ctx, arg, toProxyR, fromProxyW, &stderr)
		fromProxyW.Close()
	}()
	text := ""
	c := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "1"}, nil)
	cs, err := c.Connect(ctx, &mcp.IOTransport{Reader: fromProxyR, Writer: toProxyW}, nil)
	if err == nil {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "whoami"})
		require.NoError(t, err)
		text = res.Content[0].(*mcp.TextContent).Text
		require.NoError(t, cs.Close())
	} else {
		toProxyW.Close()
	}
	select {
	case code := <-done:
		return text, code, stderr.String()
	case <-ctx.Done():
		t.Fatal("proxy did not stop")
		return "", 0, ""
	}
}

func TestMCPProxyRuns(t *testing.T) {
	root := proxyEnv(t)
	url := fakeMCP(t, "acme-secret-key")
	require.NoError(t, os.WriteFile(filepath.Join(root, "cfg", "adapters", "fake.toml"), []byte(`name = "mcp-fake"
kind = "mcp"
[mcp]
server  = "fake"
url     = "`+url+`"
headers = { Authorization = "Bearer {FAKE_KEY}" }
[[secret]]
env = "FAKE_KEY"
`), 0o600))
	seal(t, ref.Ref{Provider: "mcp-fake", Alias: "acme"}, map[string]string{"FAKE_KEY": "acme-secret-key"})
	seal(t, ref.Ref{Provider: "mcp-fake", Alias: "other"}, map[string]string{"FAKE_KEY": "other-secret-key"})

	text, code, stderr := callWhoami(t, "fake@acme")
	require.Equal(t, 0, code, stderr)
	require.Equal(t, "key=acme-secret-key", text)
	require.Empty(t, stderr)

	// the project the agent was started in (WHOAS_CONTEXT_DIR) decides
	repo := filepath.Join(root, "repo")
	require.NoError(t, os.MkdirAll(repo, 0o700))
	pf := filepath.Join(repo, ".whoas.yaml")
	body := []byte("mcp-fake: acme\n")
	require.NoError(t, os.WriteFile(pf, body, 0o600))
	ts, err := trust.Load(filepath.Join(root, "data", "trust.json"))
	require.NoError(t, err)
	ts.Trust(pf, body)
	require.NoError(t, ts.Save())
	t.Setenv("WHOAS_CONTEXT_DIR", repo)
	text, code, _ = callWhoami(t, "fake")
	require.Equal(t, 0, code)
	require.Equal(t, "key=acme-secret-key", text)

	// the remote rejects the key: exit 1, the reason on stderr, no secret
	_, code, stderr = callWhoami(t, "fake@other")
	require.Equal(t, 1, code)
	require.Contains(t, stderr, "whoas: mcp proxy fake@other: "+url+": ")
	require.Contains(t, stderr, `"initialize"`)
	require.Contains(t, stderr, "Unauthorized")
	require.NotContains(t, stderr, "other-secret-key")
}

// TestMCPProxyUntrustedProject verifies that untrusted .whoas.yaml files are
// not used by the proxy (M1 rule: an untrusted project file counts as absent).
// The proxy must use the default profile's identity or exit 2 if none exists.
func TestMCPProxyUntrustedProject(t *testing.T) {
	root := proxyEnv(t)
	url := fakeMCP(t, "default-secret-key")
	require.NoError(t, os.WriteFile(filepath.Join(root, "cfg", "adapters", "fake.toml"), []byte(`name = "mcp-fake"
kind = "mcp"
[mcp]
server  = "fake"
url     = "`+url+`"
headers = { Authorization = "Bearer {FAKE_KEY}" }
[[secret]]
env = "FAKE_KEY"
`), 0o600))
	seal(t, ref.Ref{Provider: "mcp-fake", Alias: "default"}, map[string]string{"FAKE_KEY": "default-secret-key"})
	seal(t, ref.Ref{Provider: "mcp-fake", Alias: "untrusted"}, map[string]string{"FAKE_KEY": "untrusted-secret-key"})

	// Create a repo with an untrusted .whoas.yaml that maps to the untrusted alias
	repo := filepath.Join(root, "repo")
	require.NoError(t, os.MkdirAll(repo, 0o700))
	pf := filepath.Join(repo, ".whoas.yaml")
	untrustedBody := []byte("mcp-fake: untrusted\n")
	require.NoError(t, os.WriteFile(pf, untrustedBody, 0o600))
	// Note: we do NOT trust this file

	// Set up default profile in config
	require.NoError(t, os.WriteFile(filepath.Join(root, "cfg", "config.yaml"),
		[]byte("default_profile: default\nprofiles:\n  default:\n    mcp-fake: default\n"), 0o600))

	// (a) With default profile, proxy should use the default profile's key,
	// not the untrusted project file's key. Set WHOAS_CONTEXT_DIR to repo
	// and verify the default alias is used.
	t.Setenv("WHOAS_CONTEXT_DIR", repo)
	text, code, stderr := callWhoami(t, "fake")
	require.Equal(t, 0, code, stderr)
	require.Equal(t, "key=default-secret-key", text, "must use default profile, not untrusted project file")

	// (b) With no default profile (unset it), proxy should exit 2 when context dir
	// has an untrusted project file with no profile to fall back to
	require.NoError(t, os.WriteFile(filepath.Join(root, "cfg", "config.yaml"),
		[]byte(""), 0o600))
	code, stderr = proxy("fake")
	require.Equal(t, 2, code)
	require.Contains(t, stderr, "no identity for mcp-fake here")

	// Verify nothing was written to stdout in the exit-2 case
	// (stdout is the MCP channel and must remain clean)
	var stdout bytes.Buffer
	code = mcpProxy(context.Background(), "fake", strings.NewReader(""), &stdout, io.Discard)
	require.Equal(t, 2, code)
	require.Empty(t, stdout.String(), "no data should be written to stdout on exit 2")
}
