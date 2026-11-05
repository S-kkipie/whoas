//go:build unix

package trust

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTrustLifecycle(t *testing.T) {
	db := filepath.Join(t.TempDir(), "trust.json")
	s, err := Load(db)
	require.NoError(t, err)

	file := "/p/.whoas.yaml"
	body := []byte("profile: work\n")
	require.False(t, s.IsTrusted(file, body))
	s.Trust(file, body)
	require.NoError(t, s.Save())

	s2, err := Load(db)
	require.NoError(t, err)
	require.True(t, s2.IsTrusted(file, body))
	require.False(t, s2.IsTrusted(file, []byte("profile: personal\n")), "edits revoke trust")
	require.True(t, s2.Untrust(file))
	require.False(t, s2.Untrust(file))
	require.False(t, s2.IsTrusted(file, body))
}

func TestLoadNullFile(t *testing.T) {
	db := filepath.Join(t.TempDir(), "trust.json")
	require.NoError(t, os.WriteFile(db, []byte("null"), 0o600))

	s, err := Load(db)
	require.NoError(t, err)

	file := "/p/.whoas.yaml"
	body := []byte("profile: work\n")
	s.Trust(file, body)
	require.True(t, s.IsTrusted(file, body))
}

func TestLoadCorruptFile(t *testing.T) {
	db := filepath.Join(t.TempDir(), "trust.json")
	require.NoError(t, os.WriteFile(db, []byte("{"), 0o600))

	s, err := Load(db)
	require.Error(t, err)
	require.Nil(t, s)
}
