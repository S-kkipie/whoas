//go:build unix

package binpath

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIndexMatchesFind(t *testing.T) {
	root := t.TempDir()
	self := filepath.Join(root, "bin", "whoas")
	exe(t, self)
	shims := filepath.Join(root, "shims")
	require.NoError(t, os.MkdirAll(shims, 0o700))
	require.NoError(t, os.Symlink(self, filepath.Join(shims, "gh")))
	first := filepath.Join(root, "a")
	exe(t, filepath.Join(first, "tool"))
	require.NoError(t, os.WriteFile(filepath.Join(first, "gh"), []byte("x"), 0o644)) // not executable
	second := filepath.Join(root, "b")
	exe(t, filepath.Join(second, "gh"))
	exe(t, filepath.Join(second, "tool"))

	path := strings.Join([]string{shims, first, second, first, filepath.Join(root, "missing")}, string(os.PathListSeparator))
	ix := NewIndex(path, shims, self)
	for _, name := range []string{"gh", "tool", "nope"} {
		want, wantErr := Find(name, path, shims, self)
		got, err := ix.Find(name)
		require.Equal(t, want, got, name)
		require.Equal(t, wantErr == nil, err == nil, name)
	}
	got, _ := ix.Find("gh")
	require.Equal(t, filepath.Join(second, "gh"), got)
}
