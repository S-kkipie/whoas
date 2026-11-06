//go:build unix

package paths

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveOverrides(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WHOAS_CONFIG_DIR", filepath.Join(root, "cfg"))
	t.Setenv("WHOAS_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("WHOAS_RUNTIME_DIR", filepath.Join(root, "run"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xc"))

	d := Resolve()
	require.Equal(t, filepath.Join(root, "cfg"), d.Config)
	require.Equal(t, filepath.Join(root, "data"), d.Data)
	require.Equal(t, filepath.Join(root, "run"), d.Runtime)
	require.Equal(t, filepath.Join(root, "xc"), d.XDGConfig)
	require.Equal(t, filepath.Join(root, "cfg", "config.yaml"), d.ConfigFile())
	require.Equal(t, filepath.Join(root, "data", "vault"), d.VaultDir())
	require.Equal(t, filepath.Join(root, "run", "id", "mcp", "posthog", "acme"), d.RunBase("mcp/posthog", "acme"))
}

func TestEnsureRuntime(t *testing.T) {
	d := Dirs{Runtime: filepath.Join(t.TempDir(), "run")}
	require.NoError(t, os.MkdirAll(d.Runtime, 0o755))
	require.NoError(t, d.EnsureRuntime())
	info, err := os.Stat(d.Runtime)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}
