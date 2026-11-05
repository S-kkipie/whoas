//go:build unix

package vault

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/S-kkipie/whoas/internal/envelope"
	"github.com/S-kkipie/whoas/internal/keystore"
	"github.com/S-kkipie/whoas/internal/ref"
)

// memStore is an in-memory keystore.Store.
type memStore struct{ secret string }

func (m *memStore) Name() string { return "mem" }
func (m *memStore) Get() (string, error) {
	if m.secret == "" {
		return "", keystore.ErrNotFound
	}
	return m.secret, nil
}
func (m *memStore) Set(s string) error { m.secret = s; return nil }
func (m *memStore) Slow() bool         { return false }
func (m *memStore) TTL() time.Duration { return 0 }

func newVault(t *testing.T) (*Vault, *memStore, string) {
	t.Helper()
	root := t.TempDir()
	st := &memStore{}
	v, err := Init(filepath.Join(root, "recipient"), filepath.Join(root, "vault"), st)
	require.NoError(t, err)
	return v, st, root
}

func TestInitGeneratesKeyOnce(t *testing.T) {
	v, st, root := newVault(t)
	require.NotNil(t, v)
	require.Contains(t, st.secret, "AGE-SECRET-KEY-1")
	first := st.secret

	_, err := Init(filepath.Join(root, "recipient"), filepath.Join(root, "vault"), st)
	require.NoError(t, err)
	require.Equal(t, first, st.secret, "second Init must not rotate the key")
}

func TestOpenNotInitialized(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "recipient"), t.TempDir())
	require.ErrorIs(t, err, ErrNotInitialized)
}

func TestSealLoadRoundTrip(t *testing.T) {
	v, st, _ := newVault(t)
	r := ref.Ref{Provider: "gh", Alias: "work"}
	env := envelope.New("gh", map[string]envelope.File{"hosts.yml": {Mode: 0o600, Data: []byte("token: x")}})
	require.NoError(t, v.Seal(r, env))
	require.True(t, v.Exists(r))

	info, err := os.Stat(v.base(r) + ".age")
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	d, err := NewIdentityDecrypter(st.secret)
	require.NoError(t, err)
	got, err := v.Load(r, d)
	require.NoError(t, err)
	require.Equal(t, env, got)

	_, err = v.Load(ref.Ref{Provider: "gh", Alias: "nope"}, d)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestSealDoesNotNeedPrivateKey(t *testing.T) {
	v, st, root := newVault(t)
	st.secret = "" // private key unavailable
	reopened, err := Open(filepath.Join(root, "recipient"), filepath.Join(root, "vault"))
	require.NoError(t, err)
	require.NoError(t, reopened.Seal(ref.Ref{Provider: "gh", Alias: "x"}, envelope.New("gh", nil)))
	require.NotNil(t, v)
}

func TestListMetaRemove(t *testing.T) {
	v, _, _ := newVault(t)
	refs := []ref.Ref{{Provider: "vercel", Alias: "work"}, {Provider: "mcp/posthog", Alias: "acme"}, {Provider: "gh", Alias: "me"}}
	for _, r := range refs {
		require.NoError(t, v.Seal(r, envelope.New(r.Provider, nil)))
	}
	require.NoError(t, v.WriteMeta(refs[0], Meta{Account: "adrian@acme.com", Created: time.Unix(1, 0).UTC()}))

	list, err := v.List()
	require.NoError(t, err)
	require.Len(t, list, 3)
	require.Equal(t, "gh@me", list[0].Ref.String())
	require.Equal(t, "mcp/posthog@acme", list[1].Ref.String())
	require.Equal(t, "vercel@work", list[2].Ref.String())
	require.Equal(t, "adrian@acme.com", list[2].Meta.Account)

	require.NoError(t, v.Remove(refs[0]))
	require.False(t, v.Exists(refs[0]))
	require.ErrorIs(t, v.Remove(refs[0]), ErrNotFound)
}

func TestListEmptyVault(t *testing.T) {
	v, _, _ := newVault(t)
	list, err := v.List()
	require.NoError(t, err)
	require.Empty(t, list)
}
