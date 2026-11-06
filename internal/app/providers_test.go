//go:build unix

package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/S-kkipie/whoas/internal/adapter"
)

func TestProviderRows(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tok.toml"), []byte(`name = "tok"
bin  = "tok"
kind = "env-token"
[[secret]]
env = "TOK_KEY"
`), 0o600))
	reg, err := adapter.LoadRegistry(dir)
	require.NoError(t, err)
	find := func(bin string) (string, error) {
		switch bin {
		case "gh", "tok", "railway":
			return "/usr/bin/" + bin, nil
		}
		return "", errors.New("not found")
	}

	rows, hidden := providerRows(reg, find, false)
	require.Equal(t, []providerRow{
		{Name: "gh", Bin: "gh", Kind: "dir-overlay", Source: "builtin", Installed: "/usr/bin/gh"},
		{Name: "mcp-linear", Bin: "-", Kind: "mcp", Source: "builtin", Installed: "n/a"},
		{Name: "mcp-posthog", Bin: "-", Kind: "mcp", Source: "builtin", Installed: "n/a"},
		{Name: "railway", Bin: "railway", Kind: "dir-overlay (experimental)", Source: "builtin", Installed: "/usr/bin/railway"},
		{Name: "tok", Bin: "tok", Kind: "env-token", Source: "user", Installed: "/usr/bin/tok"},
	}, rows, "mcp adapters need no binary, so they are always listed")
	require.Equal(t, len(reg.Names())-5, hidden)

	all, hidden := providerRows(reg, find, true)
	require.Len(t, all, len(reg.Names()))
	require.Zero(t, hidden)
}
