//go:build unix

package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
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

func startServer(t *testing.T) (*Server, *Client, string) {
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
	return srv, c, sock
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
	_, c, _ := startServer(t)
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
	srv, c, _ := startServer(t)
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

func TestSocketMode(t *testing.T) {
	_, _, sock := startServer(t)
	fi, err := os.Stat(sock)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
}

func TestSecondAgentRefused(t *testing.T) {
	_, _, sock := startServer(t)
	// Try to start a second agent on the same socket.
	srv2 := NewServer()
	err := srv2.Serve(context.Background(), sock)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already running")
	// First agent's client should still work.
	c := NewClient(sock)
	locked, err := c.Status()
	require.NoError(t, err)
	require.True(t, locked)
}

func TestConcurrentAgentsSingleton(t *testing.T) {
	sock := shortSocket(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const n = 8
	done := make(chan error, n)
	start := make(chan struct{})
	for range n {
		go func() {
			<-start
			done <- NewServer().Serve(ctx, sock)
		}()
	}
	close(start)
	for range n - 1 {
		select {
		case err := <-done:
			require.ErrorContains(t, err, "already running")
		case <-time.After(3 * time.Second):
			t.Fatal("losing agents did not return quickly")
		}
	}
	select {
	case err := <-done:
		t.Fatalf("a second agent returned too (%v): none is serving", err)
	case <-time.After(100 * time.Millisecond):
	}
	_, err := NewClient(sock).Status()
	require.NoError(t, err, "the winner serves")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not stop")
	}
	_, err = os.Stat(sock)
	require.ErrorIs(t, err, os.ErrNotExist, "socket removed on shutdown")
}

func TestDecryptRefreshesIdleTimer(t *testing.T) {
	srv, c, _ := startServer(t)
	now := time.Unix(1000, 0)
	advance := func(d time.Duration) {
		srv.mu.Lock()
		now = now.Add(d)
		srv.mu.Unlock()
	}
	srv.mu.Lock()
	srv.now = func() time.Time { return now }
	srv.mu.Unlock()

	id, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	ct := encrypt(t, id, "hello")
	require.NoError(t, c.Load(id.String(), time.Minute))

	advance(50 * time.Second)
	_, err = c.Decrypt(ct)
	require.NoError(t, err)

	advance(50 * time.Second)
	locked, err := c.Status()
	require.NoError(t, err)
	require.False(t, locked, "decrypt refreshed idle timer")

	advance(2 * time.Minute)
	locked, err = c.Status()
	require.NoError(t, err)
	require.True(t, locked, "key expires after another 2 minutes")
}

func TestReaperDropsExpiredKey(t *testing.T) {
	sock := shortSocket(t)
	srv := NewServer()
	srv.reapEvery = 10 * time.Millisecond
	now := time.Unix(1000, 0)
	srv.mu.Lock()
	srv.now = func() time.Time { return now }
	srv.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, sock) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	c := NewClient(sock)
	require.Eventually(t, func() bool { _, err := c.Status(); return err == nil }, 2*time.Second, 10*time.Millisecond)

	id, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	require.NoError(t, c.Load(id.String(), time.Minute))

	srv.mu.Lock()
	now = now.Add(2 * time.Minute)
	srv.mu.Unlock()

	// Reaper should drop the key.
	require.Eventually(t, func() bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return srv.key == nil
	}, 1*time.Second, 10*time.Millisecond)
}

func TestSocketPathTooLong(t *testing.T) {
	dir := filepath.Join(t.TempDir(), strings.Repeat("d", 120))
	err := NewServer().Serve(context.Background(), filepath.Join(dir, "agent.sock"))
	require.ErrorContains(t, err, "over the")
	require.ErrorContains(t, err, "WHOAS_RUNTIME_DIR")
}
