//go:build unix

package keystore

import (
	"errors"
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
