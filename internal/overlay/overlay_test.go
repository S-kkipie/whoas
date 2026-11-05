//go:build unix

package overlay

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/S-kkipie/whoas/internal/envelope"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func isLink(t *testing.T, path string) bool {
	t.Helper()
	info, err := os.Lstat(path)
	require.NoError(t, err)
	return info.Mode()&os.ModeSymlink != 0
}

var auth = []string{"auth.json", ".w/config/default.toml"}

func globalFixture(t *testing.T) string {
	g := t.TempDir()
	write(t, filepath.Join(g, "a.txt"), "shared")
	write(t, filepath.Join(g, "sub", "b.txt"), "shared-b")
	write(t, filepath.Join(g, "auth.json"), "global-auth")
	write(t, filepath.Join(g, ".w", "other.txt"), "w-other")
	write(t, filepath.Join(g, ".w", "config", "default.toml"), "global-toml")
	return g
}

func TestBuildMirrorsAndMaterializes(t *testing.T) {
	g := globalFixture(t)
	dir := filepath.Join(t.TempDir(), "ov")
	files := map[string]envelope.File{
		"auth.json":              {Mode: 0o600, Data: []byte("id-auth")},
		".w/config/default.toml": {Mode: 0o600, Data: []byte("id-toml")},
	}
	require.NoError(t, Build(dir, g, auth, files))

	require.True(t, isLink(t, filepath.Join(dir, "a.txt")))
	require.True(t, isLink(t, filepath.Join(dir, "sub")))
	require.True(t, isLink(t, filepath.Join(dir, ".w", "other.txt")))
	require.False(t, isLink(t, filepath.Join(dir, ".w")))
	require.False(t, isLink(t, filepath.Join(dir, ".w", "config")))
	require.Equal(t, "id-auth", read(t, filepath.Join(dir, "auth.json")))
	require.Equal(t, "id-toml", read(t, filepath.Join(dir, ".w", "config", "default.toml")))
	require.Equal(t, "shared", read(t, filepath.Join(dir, "a.txt")))
	require.Equal(t, "global-auth", read(t, filepath.Join(g, "auth.json")), "global auth untouched")
}

func TestBuildWithoutGlobal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ov")
	require.NoError(t, Build(dir, filepath.Join(t.TempDir(), "missing"), auth, map[string]envelope.File{"auth.json": {Data: []byte("x")}}))
	require.Equal(t, "x", read(t, filepath.Join(dir, "auth.json")))
	info, err := os.Stat(filepath.Join(dir, "auth.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "zero mode defaults to 0600")
}

func TestCollect(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "auth.json"), "a")
	write(t, filepath.Join(root, "creds", "x", "adc.json"), "b")
	write(t, filepath.Join(root, "not-auth.txt"), "c")
	got, err := Collect(root, []string{"auth.json", "creds", "missing.json"})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "a", string(got["auth.json"].Data))
	require.Equal(t, "b", string(got["creds/x/adc.json"].Data))
	require.Equal(t, uint32(0o600), got["auth.json"].Mode)
}

func TestPromote(t *testing.T) {
	g := globalFixture(t)
	dir := filepath.Join(t.TempDir(), "ov")
	require.NoError(t, Build(dir, g, auth, map[string]envelope.File{"auth.json": {Data: []byte("id")}}))
	write(t, filepath.Join(dir, "new.txt"), "created-by-cli")
	write(t, filepath.Join(dir, "settings", "theme.txt"), "dark")
	write(t, filepath.Join(dir, ".w", "fresh.txt"), "in-ancestor")

	require.NoError(t, Promote(dir, g, auth))
	require.Equal(t, "created-by-cli", read(t, filepath.Join(g, "new.txt")))
	require.Equal(t, "dark", read(t, filepath.Join(g, "settings", "theme.txt")))
	require.Equal(t, "in-ancestor", read(t, filepath.Join(g, ".w", "fresh.txt")))
	require.Equal(t, "global-auth", read(t, filepath.Join(g, "auth.json")), "auth never promoted")
}

func TestPromoteKeepsExistingGlobal(t *testing.T) {
	g := globalFixture(t)
	dir := filepath.Join(t.TempDir(), "ov")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	write(t, filepath.Join(dir, "a.txt"), "overlay-version")
	require.NoError(t, Promote(dir, g, auth))
	require.Equal(t, "shared", read(t, filepath.Join(g, "a.txt")))
}

func TestPromoteCrossDevice(t *testing.T) {
	rename = func(a, b string) error { return &os.LinkError{Op: "rename", Old: a, New: b, Err: syscall.EXDEV} }
	t.Cleanup(func() { rename = os.Rename })
	g := t.TempDir()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "settings", "k.txt"), "v")
	require.NoError(t, Promote(dir, g, auth))
	require.Equal(t, "v", read(t, filepath.Join(g, "settings", "k.txt")))
	_, err := os.Stat(filepath.Join(dir, "settings"))
	require.True(t, os.IsNotExist(err))
}

func TestRemoveDoesNotFollowLinks(t *testing.T) {
	g := globalFixture(t)
	dir := filepath.Join(t.TempDir(), "ov")
	require.NoError(t, Build(dir, g, auth, nil))
	require.NoError(t, Remove(dir))
	require.Equal(t, "shared", read(t, filepath.Join(g, "a.txt")))
}
