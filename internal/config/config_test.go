//go:build unix

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/S-kkipie/whoas/internal/ref"
)

func TestLoadMissingIsEmpty(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "config.yaml"))
	require.NoError(t, err)
	require.Empty(t, c.Profiles)
	require.Empty(t, c.DefaultProfile)
}

func TestSaveLoadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	c, err := Load(p)
	require.NoError(t, err)
	c.Bind("work", ref.Ref{Provider: "vercel", Alias: "work"})
	c.Bind("work", ref.Ref{Provider: "mcp/posthog", Alias: "acme"})
	c.EnsureProfile("empty")
	c.DefaultProfile = "work"
	require.NoError(t, c.Save(p))

	info, err := os.Stat(p)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	got, err := Load(p)
	require.NoError(t, err)
	require.Equal(t, "work", got.DefaultProfile)
	prof, ok := got.Profile("work")
	require.True(t, ok)
	require.Equal(t, map[string]string{"vercel": "work", "mcp/posthog": "acme"}, prof)
	require.Equal(t, []string{"empty", "work"}, got.ProfileNames())
}

func TestLoadRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"unknown key":     "colour: blue\n",
		"bad provider":    "profiles:\n  work:\n    Vercel: x\n",
		"bad alias":       "profiles:\n  work:\n    vercel: ../x\n",
		"missing default": "default_profile: nope\n",
	}
	for name, body := range cases {
		p := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
		_, err := Load(p)
		require.Error(t, err, name)
	}
}
