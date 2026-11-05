//go:build unix

package fsutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriteFileAtomic(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "f.json")
	require.NoError(t, WriteFileAtomic(p, []byte("one"), 0o600))
	require.NoError(t, WriteFileAtomic(p, []byte("two"), 0o600))

	data, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, "two", string(data))

	info, err := os.Stat(p)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	entries, err := os.ReadDir(filepath.Dir(p))
	require.NoError(t, err)
	require.Len(t, entries, 1, "temp files must not be left behind")
}
