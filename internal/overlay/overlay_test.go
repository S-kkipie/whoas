//go:build unix

package overlay

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

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

	replaced, err := Promote(dir, g, auth)
	require.NoError(t, err)
	require.Empty(t, replaced)
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
	replaced, err := Promote(dir, g, auth)
	require.NoError(t, err)
	require.Equal(t, []string{"a.txt"}, replaced)
	require.Equal(t, "shared", read(t, filepath.Join(g, "a.txt")))
}

func buildFixture(t *testing.T) (g, dir string) {
	g = globalFixture(t)
	dir = filepath.Join(t.TempDir(), "ov")
	require.NoError(t, Build(dir, g, auth, map[string]envelope.File{
		"auth.json":              {Data: []byte("id")},
		".w/config/default.toml": {Data: []byte("id-toml")},
	}))
	return g, dir
}

// renameOver saves rel the way many CLIs do: write a temp file, rename it over
// the mirrored symlink.
func renameOver(t *testing.T, dir, rel, body string, mode os.FileMode) {
	t.Helper()
	tmp := filepath.Join(dir, rel+".lock")
	write(t, tmp, body)
	require.NoError(t, os.Chmod(tmp, mode))
	require.NoError(t, os.Rename(tmp, filepath.Join(dir, rel)))
}

// A tool that saves a dotfile by writing a new file and renaming it over the
// mirrored symlink replaces the link inside the overlay: when the global file
// did not change meanwhile, Promote copies the new file back.
func TestPromoteCopiesBackReplacedMirroredFiles(t *testing.T) {
	g, dir := buildFixture(t)
	renameOver(t, dir, "a.txt", "edited", 0o640)                            // mirrored top-level symlink
	renameOver(t, dir, filepath.Join(".w", "other.txt"), "edited-w", 0o600) // mirrored symlink inside an auth parent
	renameOver(t, dir, "auth.json", "refreshed", 0o600)                     // auth file: sealed, never copied
	renameOver(t, dir, filepath.Join(".w", "config", "default.toml"), "refreshed", 0o600)
	write(t, filepath.Join(dir, "fresh.txt"), "new") // new entry: promoted

	replaced, err := Promote(dir, g, auth)
	require.NoError(t, err)
	require.Empty(t, replaced)
	require.Equal(t, "edited", read(t, filepath.Join(g, "a.txt")))
	info, err := os.Stat(filepath.Join(g, "a.txt"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o640), info.Mode().Perm(), "mode preserved")
	require.Equal(t, "edited-w", read(t, filepath.Join(g, ".w", "other.txt")))
	require.False(t, isLink(t, filepath.Join(g, "a.txt")))
	_, err = os.Lstat(filepath.Join(dir, "a.txt"))
	require.True(t, os.IsNotExist(err), "overlay entry moved out")
	require.True(t, isLink(t, filepath.Join(dir, "sub")), "untouched symlink")
	require.Equal(t, "global-auth", read(t, filepath.Join(g, "auth.json")))
	require.Equal(t, "global-toml", read(t, filepath.Join(g, ".w", "config", "default.toml")))
	require.Equal(t, "new", read(t, filepath.Join(g, "fresh.txt")))
	entries, err := os.ReadDir(g)
	require.NoError(t, err)
	for _, e := range entries {
		require.NotContains(t, e.Name(), ".whoas-tmp-")
	}
}

func TestPromoteKeepsGlobalThatChanged(t *testing.T) {
	g, dir := buildFixture(t)
	renameOver(t, dir, "a.txt", "overlay-edit", 0o600)
	gp := filepath.Join(g, "a.txt")
	write(t, gp, "global-edit-longer")
	require.NoError(t, os.Chtimes(gp, time.Now().Add(time.Hour), time.Now().Add(time.Hour)))

	replaced, err := Promote(dir, g, auth)
	require.NoError(t, err)
	require.Equal(t, []string{"a.txt"}, replaced)
	require.Equal(t, "global-edit-longer", read(t, gp))
}

func TestPromoteKeepsGlobalWithSameSizeNewMtime(t *testing.T) {
	g, dir := buildFixture(t)
	renameOver(t, dir, "a.txt", "overlay", 0o600)
	gp := filepath.Join(g, "a.txt")
	write(t, gp, "SHARED") // same size as "shared"
	require.NoError(t, os.Chtimes(gp, time.Now().Add(time.Hour), time.Now().Add(time.Hour)))
	replaced, err := Promote(dir, g, auth)
	require.NoError(t, err)
	require.Equal(t, []string{"a.txt"}, replaced)
	require.Equal(t, "SHARED", read(t, gp))
}

func TestPromoteUntouchedSymlinksNothing(t *testing.T) {
	g, dir := buildFixture(t)
	replaced, err := Promote(dir, g, auth)
	require.NoError(t, err)
	require.Empty(t, replaced)
	require.Equal(t, "shared", read(t, filepath.Join(g, "a.txt")))
	require.True(t, isLink(t, filepath.Join(dir, "a.txt")))
}

func TestPromoteReportsReplacedDirectory(t *testing.T) {
	g, dir := buildFixture(t)
	require.NoError(t, os.Remove(filepath.Join(dir, "sub")))
	write(t, filepath.Join(dir, "sub", "b.txt"), "overlay-b")
	replaced, err := Promote(dir, g, auth)
	require.NoError(t, err)
	require.Equal(t, []string{"sub"}, replaced)
	require.Equal(t, "shared-b", read(t, filepath.Join(g, "sub", "b.txt")))
}

func TestPromoteWithoutManifestKeepsGlobal(t *testing.T) {
	g, dir := buildFixture(t)
	renameOver(t, dir, "a.txt", "edited", 0o600)
	require.NoError(t, os.Remove(dir+".mirror.json"))
	replaced, err := Promote(dir, g, auth)
	require.NoError(t, err)
	require.Equal(t, []string{"a.txt"}, replaced)
	require.Equal(t, "shared", read(t, filepath.Join(g, "a.txt")))
}

func TestPromoteCopyBackCrossDevice(t *testing.T) {
	g, dir := buildFixture(t)
	renameOver(t, dir, "a.txt", "edited", 0o640)
	rename = func(a, b string) error { return &os.LinkError{Op: "rename", Old: a, New: b, Err: syscall.EXDEV} }
	t.Cleanup(func() { rename = os.Rename })
	replaced, err := Promote(dir, g, auth)
	require.NoError(t, err)
	require.Empty(t, replaced)
	require.Equal(t, "edited", read(t, filepath.Join(g, "a.txt")))
	info, err := os.Stat(filepath.Join(g, "a.txt"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o640), info.Mode().Perm())
	_, err = os.Lstat(filepath.Join(dir, "a.txt"))
	require.True(t, os.IsNotExist(err), "overlay src removed")
	entries, err := os.ReadDir(g)
	require.NoError(t, err)
	for _, e := range entries {
		require.NotContains(t, e.Name(), ".whoas-tmp-")
	}
}

func TestPromoteCorruptManifestKeepsGlobal(t *testing.T) {
	g, dir := buildFixture(t)
	renameOver(t, dir, "a.txt", "edited", 0o600)
	write(t, dir+".mirror.json", "{not json")
	replaced, err := Promote(dir, g, auth)
	require.NoError(t, err)
	require.Equal(t, []string{"a.txt"}, replaced)
	require.Equal(t, "shared", read(t, filepath.Join(g, "a.txt")))
}

func TestPromoteGlobalSymlinkNotReplaced(t *testing.T) {
	g := globalFixture(t)
	target := filepath.Join(t.TempDir(), "stow.txt")
	write(t, target, "stowed")
	require.NoError(t, os.Symlink(target, filepath.Join(g, "link.txt")))
	dir := filepath.Join(t.TempDir(), "ov")
	require.NoError(t, Build(dir, g, auth, nil))
	renameOver(t, dir, "link.txt", "edited", 0o600)
	replaced, err := Promote(dir, g, auth)
	require.NoError(t, err)
	require.Equal(t, []string{"link.txt"}, replaced)
	require.True(t, isLink(t, filepath.Join(g, "link.txt")))
	require.Equal(t, "stowed", read(t, target))
}

func TestPromoteGlobalPathMismatchKeepsGlobal(t *testing.T) {
	g, dir := buildFixture(t)
	renameOver(t, dir, "a.txt", "edited", 0o600)
	g2 := t.TempDir()
	write(t, filepath.Join(g2, "a.txt"), "shared")
	replaced, err := Promote(dir, g2, auth)
	require.NoError(t, err)
	require.Equal(t, []string{"a.txt"}, replaced)
	require.Equal(t, "shared", read(t, filepath.Join(g2, "a.txt")))
	require.Equal(t, "shared", read(t, filepath.Join(g, "a.txt")))
}

// A copy-back failure is reported and does not stop later entries.
func TestPromoteCopyBackFailureContinues(t *testing.T) {
	g, dir := buildFixture(t)
	renameOver(t, dir, "a.txt", "edited", 0o600)
	write(t, filepath.Join(dir, "zz-new.txt"), "new")
	rename = func(a, b string) error {
		if filepath.Base(a) == "a.txt" {
			return &os.LinkError{Op: "rename", Old: a, New: b, Err: syscall.EPERM}
		}
		return os.Rename(a, b)
	}
	t.Cleanup(func() { rename = os.Rename })
	replaced, err := Promote(dir, g, auth)
	require.NoError(t, err)
	require.Equal(t, []string{"a.txt"}, replaced)
	require.Equal(t, "shared", read(t, filepath.Join(g, "a.txt")))
	require.Equal(t, "new", read(t, filepath.Join(g, "zz-new.txt")))
}

// Cross-device: src swapped for a symlink (to an auth file) is never copied.
func TestPromoteCopyBackRefusesSymlinkSrc(t *testing.T) {
	g, dir := buildFixture(t)
	require.NoError(t, os.Remove(filepath.Join(dir, "a.txt")))
	require.NoError(t, os.Symlink(filepath.Join(dir, "auth.json"), filepath.Join(dir, "a.txt")))
	// A symlink in dir is skipped by Promote; call copyBack directly for the race window.
	rename = func(a, b string) error { return &os.LinkError{Op: "rename", Old: a, New: b, Err: syscall.EXDEV} }
	t.Cleanup(func() { rename = os.Rename })
	require.Error(t, copyBack(filepath.Join(dir, "a.txt"), filepath.Join(g, "a.txt")))
	require.Equal(t, "shared", read(t, filepath.Join(g, "a.txt")))
}

func TestManifestNextToDirAndRemoved(t *testing.T) {
	g, dir := buildFixture(t)
	_, err := os.Stat(dir + ".mirror.json")
	require.NoError(t, err)
	_, err = os.Lstat(filepath.Join(dir, ".mirror.json"))
	require.True(t, os.IsNotExist(err))
	require.NoError(t, Remove(dir))
	_, err = os.Stat(dir + ".mirror.json")
	require.True(t, os.IsNotExist(err), "manifest removed with the overlay")

	write(t, dir+".mirror.json.tmp", "{}")
	require.NoError(t, Remove(dir))
	_, err = os.Stat(dir + ".mirror.json.tmp")
	require.True(t, os.IsNotExist(err), "leftover temp manifest removed")
	require.Equal(t, "shared", read(t, filepath.Join(g, "a.txt")))
}

func TestPromoteCrossDevice(t *testing.T) {
	rename = func(a, b string) error { return &os.LinkError{Op: "rename", Old: a, New: b, Err: syscall.EXDEV} }
	t.Cleanup(func() { rename = os.Rename })
	g := t.TempDir()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "settings", "k.txt"), "v")
	_, err := Promote(dir, g, auth)
	require.NoError(t, err)
	require.Equal(t, "v", read(t, filepath.Join(g, "settings", "k.txt")))
	_, err = os.Stat(filepath.Join(dir, "settings"))
	require.True(t, os.IsNotExist(err))
	// Ensure no temporary files remain.
	entries, err := os.ReadDir(g)
	require.NoError(t, err)
	for _, e := range entries {
		require.NotContains(t, e.Name(), ".whoas-tmp-", "temporary files should be cleaned up")
	}
}

func TestRemoveDoesNotFollowLinks(t *testing.T) {
	g := globalFixture(t)
	dir := filepath.Join(t.TempDir(), "ov")
	require.NoError(t, Build(dir, g, auth, nil))
	require.NoError(t, Remove(dir))
	// Overlay is deleted; global is untouched.
	_, err := os.Stat(dir)
	require.True(t, os.IsNotExist(err), "overlay dir should be deleted")
	require.Equal(t, "shared", read(t, filepath.Join(g, "a.txt")))
	require.Equal(t, "shared-b", read(t, filepath.Join(g, "sub", "b.txt")))
}

func TestBuildRejectsKeysOutsideAuth(t *testing.T) {
	g := globalFixture(t)
	dir := filepath.Join(t.TempDir(), "ov")
	files := map[string]envelope.File{
		"a.txt": {Mode: 0o600, Data: []byte("bad")},
	}
	err := Build(dir, g, auth, files)
	require.Error(t, err)
	require.Contains(t, err.Error(), "outside the adapter's auth_files")
	// Global a.txt is untouched.
	require.Equal(t, "shared", read(t, filepath.Join(g, "a.txt")))
}

func TestBuildRejectsNonLocalKey(t *testing.T) {
	g := globalFixture(t)
	dir := filepath.Join(t.TempDir(), "ov")
	files := map[string]envelope.File{
		"../escape": {Mode: 0o600, Data: []byte("bad")},
	}
	err := Build(dir, g, auth, files)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a local path")
}

func TestBuildRejectsDotDotInsideAuthKey(t *testing.T) {
	g := globalFixture(t)
	dir := filepath.Join(t.TempDir(), "ov")
	files := map[string]envelope.File{
		"auth.json/../a.txt": {Mode: 0o600, Data: []byte("bad")},
	}
	err := Build(dir, g, auth, files)
	require.Error(t, err)
	require.Contains(t, err.Error(), "outside the adapter's auth_files")
	// Global a.txt is untouched.
	require.Equal(t, "shared", read(t, filepath.Join(g, "a.txt")))
}

func TestPromoteNonEXDEVErrorIsReturned(t *testing.T) {
	rename = func(a, b string) error {
		return &os.LinkError{Op: "rename", Old: a, New: b, Err: syscall.EPERM}
	}
	t.Cleanup(func() { rename = os.Rename })
	g := t.TempDir()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "settings", "k.txt"), "v")
	_, err := Promote(dir, g, auth)
	require.Error(t, err)
	// Source file must still exist after the error.
	require.Equal(t, "v", read(t, filepath.Join(dir, "settings", "k.txt")))
}
