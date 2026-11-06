//go:build unix

package keystore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func getenvFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestSelect(t *testing.T) {
	dir := t.TempDir()

	s, err := Select(dir, getenvFrom(map[string]string{"WHOAS_MASTER_KEY": "AGE-SECRET-KEY-1X"}))
	require.NoError(t, err)
	require.Equal(t, "env (WHOAS_MASTER_KEY)", s.Name())

	s, err = Select(dir, getenvFrom(map[string]string{"WHOAS_KEYSTORE": "passphrase"}))
	require.NoError(t, err)
	require.Contains(t, s.Name(), "passphrase")

	s, err = Select(dir, getenvFrom(map[string]string{"WHOAS_KEYSTORE": "os"}))
	require.NoError(t, err)
	require.Equal(t, "os keyring", s.Name())

	_, err = Select(dir, getenvFrom(map[string]string{"WHOAS_KEYSTORE": "nope"}))
	require.ErrorContains(t, err, "unknown WHOAS_KEYSTORE")
}

func TestRememberWritesKind(t *testing.T) {
	for want, s := range map[string]Store{
		"os":         osStore{},
		"wsl":        newWSLStore("/x/whoas.exe"),
		"passphrase": newPassphraseStore(t.TempDir(), getenvFrom(nil)),
	} {
		dir := t.TempDir()
		require.NoError(t, Remember(dir, s))
		data, err := os.ReadFile(filepath.Join(dir, "keystore"))
		require.NoError(t, err)
		require.Equal(t, want+"\n", string(data))
		fi, err := os.Stat(filepath.Join(dir, "keystore"))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	}
}

func TestRememberSkipsEnvStore(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, Remember(dir, envStore{key: "k"}))
	_, err := os.Stat(filepath.Join(dir, "keystore"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestSelectHonoursRemembered(t *testing.T) {
	remember := func(t *testing.T, kind string) string {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "keystore"), []byte(kind+"\n"), 0o600))
		return dir
	}
	// Auto-detect would pick the WSL bridge here; the remembered backend wins.
	wslEnv := map[string]string{"WSL_DISTRO_NAME": "Ubuntu", "WHOAS_WIN_EXE": "/x/whoas.exe"}

	s, err := Select(remember(t, "os"), getenvFrom(wslEnv))
	require.NoError(t, err)
	require.Equal(t, "os keyring", s.Name())

	s, err = Select(remember(t, "passphrase"), getenvFrom(wslEnv))
	require.NoError(t, err)
	require.Contains(t, s.Name(), "passphrase")

	s, err = Select(remember(t, "wsl"), getenvFrom(map[string]string{"WHOAS_WIN_EXE": "/x/whoas.exe"}))
	require.NoError(t, err)
	require.Contains(t, s.Name(), "wsl")

	_, err = Select(remember(t, "wsl"), getenvFrom(nil))
	require.EqualError(t, err, `keystore is "wsl" but whoas.exe was not found (set WHOAS_WIN_EXE)`)

	// WHOAS_KEYSTORE and WHOAS_MASTER_KEY still override it.
	s, err = Select(remember(t, "passphrase"), getenvFrom(map[string]string{"WHOAS_KEYSTORE": "os"}))
	require.NoError(t, err)
	require.Equal(t, "os keyring", s.Name())
	s, err = Select(remember(t, "passphrase"), getenvFrom(map[string]string{"WHOAS_MASTER_KEY": "AGE-SECRET-KEY-1X"}))
	require.NoError(t, err)
	require.Equal(t, "env (WHOAS_MASTER_KEY)", s.Name())

	for _, bad := range []string{"keychain", ""} {
		_, err = Select(remember(t, bad), getenvFrom(wslEnv))
		require.ErrorContains(t, err, "unknown keystore", "content %q", bad)
	}
}

func TestEnvStore(t *testing.T) {
	s := envStore{key: "k"}
	got, err := s.Get()
	require.NoError(t, err)
	require.Equal(t, "k", got)
	require.Error(t, s.Set("x"))
}

func TestOSStoreWithMock(t *testing.T) {
	keyring.MockInit()
	s := OS()
	_, err := s.Get()
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, s.Set("secret"))
	got, err := s.Get()
	require.NoError(t, err)
	require.Equal(t, "secret", got)
}

func TestPassphraseStore(t *testing.T) {
	scryptWorkFactor = 10
	env := map[string]string{"WHOAS_PASSPHRASE": "correct horse"}
	s := newPassphraseStore(t.TempDir(), getenvFrom(env))
	s.prompt = func(string) (string, error) { return "", errors.New("no tty in tests") }

	_, err := s.Get()
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, s.Set("AGE-SECRET-KEY-1ABC"))
	got, err := s.Get()
	require.NoError(t, err)
	require.Equal(t, "AGE-SECRET-KEY-1ABC", got)

	env["WHOAS_PASSPHRASE"] = "wrong"
	_, err = s.Get()
	require.ErrorContains(t, err, "wrong passphrase")
}

func TestWSLStore(t *testing.T) {
	stored := ""
	s := &wslStore{exe: filepath.Join("C:", "whoas.exe"), run: func(args []string, stdin string) (string, int, error) {
		switch args[1] {
		case "get":
			if stored == "" {
				return "", 2, nil
			}
			return stored + "\r\n", 0, nil
		case "set":
			stored = stdin
			return "", 0, nil
		}
		return "", 1, nil
	}}
	_, err := s.Get()
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, s.Set("AGE-SECRET-KEY-1W"))
	got, err := s.Get()
	require.NoError(t, err)
	require.Equal(t, "AGE-SECRET-KEY-1W", got)
}

func TestNoPrompt(t *testing.T) {
	scryptWorkFactor = 10
	env := map[string]string{"WHOAS_PASSPHRASE": "correct horse"}
	s := newPassphraseStore(t.TempDir(), getenvFrom(env))
	require.NoError(t, s.Set("AGE-SECRET-KEY-1ABC"))
	s.prompt = func(string) (string, error) {
		t.Fatal("NoPrompt must never ask")
		return "", nil
	}
	quiet := NoPrompt(s)
	got, err := quiet.Get()
	require.NoError(t, err, "WHOAS_PASSPHRASE needs no terminal")
	require.Equal(t, "AGE-SECRET-KEY-1ABC", got)

	delete(env, "WHOAS_PASSPHRASE")
	_, err = quiet.Get()
	require.ErrorIs(t, err, ErrNeedsTerminal)

	e := envStore{key: "k"}
	require.Equal(t, Store(e), NoPrompt(e), "stores that never prompt are unchanged")
}
