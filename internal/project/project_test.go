//go:build unix

package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFindWalksUp(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")
	require.NoError(t, os.MkdirAll(deep, 0o700))
	want := filepath.Join(root, "a", FileName)
	require.NoError(t, os.WriteFile(want, []byte("profile: work\n"), 0o600))

	got, err := Find(deep)
	require.NoError(t, err)
	require.Equal(t, want, got)

	got, err = Find(root)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestParse(t *testing.T) {
	f, err := Parse("p", []byte("profile: work\nvercel: client-x\nmcp/posthog: acme\n"))
	require.NoError(t, err)
	require.Equal(t, "work", f.Profile)
	require.Equal(t, map[string]string{"vercel": "client-x", "mcp/posthog": "acme"}, f.Overrides)

	empty, err := Parse("p", []byte(""))
	require.NoError(t, err)
	require.Empty(t, empty.Profile)
}

func TestParseRejects(t *testing.T) {
	for name, body := range map[string]string{
		"list value":  "profile: [work]\n",
		"map value":   "vercel: {cmd: rm}\n",
		"bad key":     "Vercel: x\n",
		"bad alias":   "vercel: ../x\n",
		"reserved":    "run: x\n",
		"broken yaml": "profile: [\n",
	} {
		_, err := Parse("p", []byte(body))
		require.Error(t, err, name)
	}
}
