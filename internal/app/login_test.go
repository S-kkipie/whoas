//go:build unix

package app

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// An env-token adapter's login.cmd runs before the prompts, without the
// adapter's secret or unset_env vars; --stdin skips it; a failure stops add.
func TestObtainSecretsRunsLoginCmd(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	bin := filepath.Join(dir, "tok")
	script := "#!/bin/sh\n" +
		"echo \"$@\" > '" + marker + "'\n" +
		"echo \"key=${TOK_KEY:-unset} legacy=${TOK_LEGACY:-unset}\" >> '" + marker + "'\n" +
		"echo helper-out\n" +
		"exit ${TOK_EXIT:-0}\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	a := tokAdapter()
	a.UnsetEnv = []string{"TOK_LEGACY"}
	a.Login.Cmd = []string{"tok", "setup-token"}
	t.Setenv("TOK_KEY", "inherited")
	t.Setenv("TOK_LEGACY", "old")

	ask := func(label string, hidden bool) (string, error) {
		require.FileExists(t, marker, "login.cmd runs before the prompts")
		if strings.Contains(label, "TOK_KEY") {
			return "k1", nil
		}
		return "", nil
	}
	var out bytes.Buffer
	got, err := obtainSecrets(a, tokRef, bin, false, strings.NewReader(""), &out, io.Discard, ask)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"TOK_KEY": "k1", "TOK_HOST": ""}, got)
	require.Equal(t, "helper-out\n", out.String(), "login.cmd writes to the caller's stdout")
	data, err := os.ReadFile(marker)
	require.NoError(t, err)
	require.Equal(t, "setup-token\nkey=unset legacy=unset\n", string(data))

	require.NoError(t, os.Remove(marker))
	got, err = obtainSecrets(a, tokRef, bin, true, strings.NewReader("k2\n"), io.Discard, io.Discard, nil)
	require.NoError(t, err)
	require.Equal(t, "k2", got["TOK_KEY"])
	require.NoFileExists(t, marker, "--stdin never runs login.cmd")

	t.Setenv("TOK_EXIT", "3")
	_, err = obtainSecrets(a, tokRef, bin, false, strings.NewReader(""), io.Discard, io.Discard, func(string, bool) (string, error) {
		t.Fatal("asked after a failed login.cmd")
		return "", nil
	})
	require.EqualError(t, err, "tok setup-token failed: exit status 3; nothing saved")
}
