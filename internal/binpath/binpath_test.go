//go:build unix

package binpath

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func exe(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755))
}

func TestFindSkipsShimsAndSelf(t *testing.T) {
	root := t.TempDir()
	self := filepath.Join(root, "bin", "whoas")
	exe(t, self)
	shims := filepath.Join(root, "shims")
	require.NoError(t, os.MkdirAll(shims, 0o700))
	require.NoError(t, os.Symlink(self, filepath.Join(shims, "gh")))
	other := filepath.Join(root, "other")
	require.NoError(t, os.MkdirAll(other, 0o700))
	require.NoError(t, os.Symlink(self, filepath.Join(other, "gh"))) // stray link to whoas
	real := filepath.Join(root, "usr", "gh")
	exe(t, real)
	require.NoError(t, os.WriteFile(filepath.Join(root, "noexec-gh"), []byte("x"), 0o644))

	path := strings.Join([]string{shims, other, filepath.Join(root, "usr")}, string(os.PathListSeparator))
	got, err := Find("gh", path, shims, self)
	require.NoError(t, err)
	require.Equal(t, real, got)

	got, err = Find("gh", path, "", "")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(shims, "gh"), got, "no exclusions finds the shim")

	_, err = Find("nope", path, shims, self)
	require.ErrorContains(t, err, "not found in PATH")
}
