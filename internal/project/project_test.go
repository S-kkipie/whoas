//go:build unix

package project

import (
	"os"
	"path/filepath"
	"strings"
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
	require.True(t, got == "" || !strings.HasPrefix(got, root), "got %q should be empty or outside root %q", got, root)
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
	cases := map[string]struct {
		body      string
		substring string
	}{
		"list value":   {"profile: [work]\n", "must be a string"},
		"map value":    {"vercel: {cmd: rm}\n", "must be a string"},
		"bad key":      {"Vercel: x\n", "invalid key"},
		"bad alias":    {"vercel: ../x\n", "vercel"},
		"reserved":     {"run: x\n", "invalid key"},
		"broken yaml":  {"profile: [\n", "invalid YAML"},
		"anchor/alias": {"a: &x [1,2]\nb: *x\n", "anchors and aliases"},
		"oversized":    {"profile: work\n" + strings.Repeat("# pad\n", 20000), "too large"},
		"non-string":   {"vercel: 123\n", "must be a string"},
	}
	for name, c := range cases {
		_, err := Parse("p", []byte(c.body))
		require.ErrorContains(t, err, c.substring, name)
	}
}
