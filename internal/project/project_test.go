//go:build unix

package project

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

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

func TestFindSkipsNonRegular(t *testing.T) {
	root := t.TempDir()
	want := filepath.Join(root, FileName)
	require.NoError(t, os.WriteFile(want, []byte("profile: work\n"), 0o600))

	dirCase := filepath.Join(root, "dir")
	require.NoError(t, os.MkdirAll(filepath.Join(dirCase, FileName), 0o700))
	fifoCase := filepath.Join(root, "fifo")
	require.NoError(t, os.MkdirAll(fifoCase, 0o700))
	require.NoError(t, syscall.Mkfifo(filepath.Join(fifoCase, FileName), 0o600))
	devCase := filepath.Join(root, "dev")
	require.NoError(t, os.MkdirAll(devCase, 0o700))
	require.NoError(t, os.Symlink("/dev/zero", filepath.Join(devCase, FileName)))

	for _, start := range []string{dirCase, fifoCase, devCase} {
		got, err := Find(start)
		require.NoError(t, err, start)
		require.Equal(t, want, got, "non-regular %s skipped, walk continues upward", start)
	}
}

func TestRead(t *testing.T) {
	dir := t.TempDir()
	ok := filepath.Join(dir, "ok.yaml")
	require.NoError(t, os.WriteFile(ok, []byte("profile: work\n"), 0o600))
	data, err := Read(ok)
	require.NoError(t, err)
	require.Equal(t, "profile: work\n", string(data))

	limit := filepath.Join(dir, "limit.yaml")
	require.NoError(t, os.WriteFile(limit, make([]byte, maxFileSize), 0o600))
	_, err = Read(limit)
	require.NoError(t, err, "exactly maxFileSize is accepted")

	big := filepath.Join(dir, "big.yaml")
	require.NoError(t, os.WriteFile(big, make([]byte, maxFileSize+1), 0o600))
	_, err = Read(big)
	require.ErrorContains(t, err, "too large")
}

func TestReadRejectsNonRegular(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o700))
	fifo := filepath.Join(dir, "fifo")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	zero := filepath.Join(dir, "zero")
	require.NoError(t, os.Symlink("/dev/zero", zero))

	for _, p := range []string{sub, fifo, zero} {
		done := make(chan error, 1)
		go func() { _, err := Read(p); done <- err }()
		select {
		case err := <-done:
			require.ErrorContains(t, err, "not a regular file", p)
		case <-time.After(5 * time.Second):
			t.Fatalf("Read(%s) hung", p)
		}
	}
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
