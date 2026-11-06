//go:build unix

package keysource

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/stretchr/testify/require"

	"github.com/S-kkipie/whoas/internal/agent"
	"github.com/S-kkipie/whoas/internal/vault"
)

type fakeStore struct {
	secret string
	slow   bool
	gets   int
}

func (f *fakeStore) Name() string         { return "fake" }
func (f *fakeStore) Get() (string, error) { f.gets++; return f.secret, nil }
func (f *fakeStore) Set(string) error     { return nil }
func (f *fakeStore) Slow() bool           { return f.slow }
func (f *fakeStore) TTL() time.Duration   { return 0 }

func socket(t *testing.T) string {
	dir, err := os.MkdirTemp("", "wk")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "a.sock")
}

func runAgent(t *testing.T, sock string) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = agent.NewServer().Serve(ctx, sock) }()
	t.Cleanup(cancel)
	require.Eventually(t, func() bool { _, err := agent.NewClient(sock).Status(); return err == nil }, 2*time.Second, 10*time.Millisecond)
}

func TestFastStoreNoAgent(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	st := &fakeStore{secret: id.String()}
	d, err := Decrypter(socket(t), st, "")
	require.NoError(t, err)
	require.IsType(t, &vault.IdentityDecrypter{}, d)
}

func TestSlowStoreLoadsRunningAgent(t *testing.T) {
	sock := socket(t)
	runAgent(t, sock)
	id, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	st := &fakeStore{secret: id.String(), slow: true}

	d, err := Decrypter(sock, st, "")
	require.NoError(t, err)
	require.IsType(t, &vault.IdentityDecrypter{}, d)
	require.Equal(t, 1, st.gets)

	d, err = Decrypter(sock, st, "")
	require.NoError(t, err)
	require.IsType(t, &agent.Client{}, d, "second call goes through the unlocked agent")
	require.Equal(t, 1, st.gets, "store not read again")
}
