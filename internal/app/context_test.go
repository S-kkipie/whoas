//go:build unix

package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/S-kkipie/whoas/internal/config"
	"github.com/S-kkipie/whoas/internal/paths"
	"github.com/S-kkipie/whoas/internal/trust"
)

func TestContextEnv(t *testing.T) {
	t.Chdir(t.TempDir())
	wd, err := os.Getwd()
	require.NoError(t, err)
	require.Equal(t, []string{"WHOAS_CONTEXT_DIR=" + wd}, contextEnv())
	require.Equal(t, []string{"PATH=/bin", "WHOAS_CONTEXT_DIR=" + wd},
		withContext([]string{"WHOAS_CONTEXT_DIR=/parent", "PATH=/bin"}), "a nested whoas overwrites its parent's context")
}

func TestMCPContextDir(t *testing.T) {
	t.Chdir(t.TempDir())
	wd, err := os.Getwd()
	require.NoError(t, err)
	t.Setenv("WHOAS_CONTEXT_DIR", "")
	got, err := mcpContextDir()
	require.NoError(t, err)
	require.Equal(t, wd, got, "without WHOAS_CONTEXT_DIR the proxy resolves from its working directory")
	t.Setenv("WHOAS_CONTEXT_DIR", "/code/repo")
	got, err = mcpContextDir()
	require.NoError(t, err)
	require.Equal(t, "/code/repo", got)
}

// Resolution reads .whoas.yaml from the context dir when one is given, else
// from the working directory.
func TestResolveFromContextDir(t *testing.T) {
	root := t.TempDir()
	e := &env{dirs: paths.Dirs{Data: filepath.Join(root, "data")}, cfg: &config.Config{}}
	require.NoError(t, os.MkdirAll(e.dirs.Data, 0o700))
	ts, err := trust.Load(e.dirs.TrustFile())
	require.NoError(t, err)
	repos := map[string]string{"a": "tok: work\n", "b": "tok: personal\n"}
	for name, body := range repos {
		dir := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o700))
		p := filepath.Join(dir, ".whoas.yaml")
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
		ts.Trust(p, []byte(body))
	}
	require.NoError(t, ts.Save())
	t.Setenv("WHOAS_PROFILE", "")
	t.Chdir(filepath.Join(root, "a"))

	res, err := e.resolveIdentity(runRequest{provider: "tok"})
	require.NoError(t, err)
	require.Equal(t, "tok@work", res.Ref.String())

	res, err = e.resolveIdentity(runRequest{provider: "tok", contextDir: filepath.Join(root, "b", "sub")})
	require.NoError(t, err)
	require.Equal(t, "tok@personal", res.Ref.String())
	require.Equal(t, "project "+filepath.Join(root, "b", ".whoas.yaml"), res.Source)
}
