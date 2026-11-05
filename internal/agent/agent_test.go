//go:build unix

package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/stretchr/testify/require"
)

// shortSocket keeps unix socket paths under the ~104 byte limit.
func shortSocket(t *testing.T) string {
	dir, err := os.MkdirTemp("", "wa")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "a.sock")
}

func startServer(t *testing.T) (*Server, *Client) {
	t.Helper()
	sock := shortSocket(t)
	srv := NewServer()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, sock) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})
	c := NewClient(sock)
	require.Eventually(t, func() bool { _, err := c.Status(); return err == nil }, 2*time.Second, 10*time.Millisecond)
	return srv, c
}

func encrypt(t *testing.T, id *age.X25519Identity, msg string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, id.Recipient())
	require.NoError(t, err)
	_, err = w.Write([]byte(msg))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func TestAgentLifecycle(t *testing.T) {
	_, c := startServer(t)
	id, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	ct := encrypt(t, id, "hello")

	locked, err := c.Status()
	require.NoError(t, err)
	require.True(t, locked)
	_, err = c.Decrypt(ct)
	require.ErrorIs(t, err, ErrLocked)

	require.Error(t, c.Load("not-a-key", 0))
	require.NoError(t, c.Load(id.String(), 0))
	plain, err := c.Decrypt(ct)
	require.NoError(t, err)
	require.Equal(t, "hello", string(plain))

	require.NoError(t, c.Lock())
	_, err = c.Decrypt(ct)
	require.ErrorIs(t, err, ErrLocked)
}

func TestAgentIdleTTL(t *testing.T) {
	srv, c := startServer(t)
	now := time.Unix(1000, 0)
	advance := func(d time.Duration) { // guarded: the server reads now under mu
		srv.mu.Lock()
		now = now.Add(d)
		srv.mu.Unlock()
	}
	srv.mu.Lock()
	srv.now = func() time.Time { return now }
	srv.mu.Unlock()

	id, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	require.NoError(t, c.Load(id.String(), time.Minute))

	advance(30 * time.Second)
	locked, err := c.Status()
	require.NoError(t, err)
	require.False(t, locked)

	advance(2 * time.Minute)
	locked, err = c.Status()
	require.NoError(t, err)
	require.True(t, locked, "key expires after idle TTL")
}

func TestClientWithoutAgent(t *testing.T) {
	_, err := NewClient(shortSocket(t)).Status()
	require.Error(t, err)
}
