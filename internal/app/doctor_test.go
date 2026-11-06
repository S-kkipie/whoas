//go:build unix

package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/stretchr/testify/require"

	"github.com/S-kkipie/whoas/internal/adapter"
	"github.com/S-kkipie/whoas/internal/paths"
	"github.com/S-kkipie/whoas/internal/runlock"
	"github.com/S-kkipie/whoas/internal/vault"
)

// A run dir left under an env-token adapter (e.g. by an older dir-overlay
// adapter of the same name) has no overlay to recover: doctor must not
// promote its entries anywhere.
func TestRecoverAllSkipsEnvToken(t *testing.T) {
	root := t.TempDir()
	userDir := filepath.Join(root, "adapters")
	require.NoError(t, os.MkdirAll(userDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(userDir, "tok.toml"), []byte(`name = "tok"
bin  = "tok"
kind = "env-token"
[[secret]]
env = "TOK_KEY"
`), 0o600))
	reg, err := adapter.LoadRegistry(userDir)
	require.NoError(t, err)
	home := filepath.Join(root, "home")
	e := &env{
		dirs:     paths.Dirs{Runtime: filepath.Join(root, "run"), Home: home, XDGConfig: filepath.Join(home, ".config"), XDGData: filepath.Join(home, ".local", "share")},
		registry: reg,
	}

	id, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	recipient := filepath.Join(root, "recipient")
	require.NoError(t, os.WriteFile(recipient, []byte(id.Recipient().String()), 0o644))
	v, err := vault.Open(recipient, filepath.Join(root, "vault"))
	require.NoError(t, err)

	base := e.dirs.RunBase("tok", "work")
	require.NoError(t, os.MkdirAll(base, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(base, "leftover.txt"), []byte("x"), 0o600))
	l, err := runlock.Acquire(context.Background(), base, time.Second)
	require.NoError(t, err)
	require.NoError(t, l.Write(runlock.State{Hash: "stale"}))
	require.NoError(t, l.Release())

	cwd := t.TempDir()
	t.Chdir(cwd)
	n, err := recoverAll(e, v)
	require.NoError(t, err)
	require.Zero(t, n)
	require.NoFileExists(t, filepath.Join(cwd, "leftover.txt"), "nothing promoted into the working directory")
	require.FileExists(t, filepath.Join(base, "leftover.txt"))
}
