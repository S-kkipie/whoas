package agent

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// agentSocketEnv tells the test binary, re-run as "<self> agent" by Spawn,
// where to serve. Its WHOAS_ prefix lets it through the agent's env filter.
const agentSocketEnv = "WHOAS_TEST_AGENT_SOCKET"

// TestMain doubles as the agent Spawn starts: Spawn runs "<self> agent",
// and here self is this test binary.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "agent" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer stop()
		if err := NewServer().Serve(ctx, os.Getenv(agentSocketEnv)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// agentPID asks the kernel which process listens on sock (SO_PEERCRED).
func agentPID(t *testing.T, sock string) int {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	require.NoError(t, err)
	defer conn.Close()
	raw, err := conn.(*net.UnixConn).SyscallConn()
	require.NoError(t, err)
	var cred *unix.Ucred
	var cerr error
	require.NoError(t, raw.Control(func(fd uintptr) {
		cred, cerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}))
	require.NoError(t, cerr)
	return int(cred.Pid)
}

func TestSpawnDropsCallerSecrets(t *testing.T) {
	sock := shortSocket(t)
	t.Setenv(agentSocketEnv, sock)
	// What a proxy or shim inside a "whoas claude@x" session would carry.
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "whoas-test-marker-not-a-secret")
	t.Setenv("HOME", t.TempDir())

	require.NoError(t, Spawn(os.Args[0], sock))
	pid := agentPID(t, sock)
	require.NotEqual(t, os.Getpid(), pid, "the spawned agent answers, not this process")
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		require.Eventually(t, func() bool {
			_, err := os.Stat(sock)
			return os.IsNotExist(err)
		}, 3*time.Second, 10*time.Millisecond, "agent did not stop")
	})

	raw, err := os.ReadFile(filepath.Join("/proc", fmt.Sprint(pid), "environ"))
	require.NoError(t, err)
	environ := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	for _, kv := range environ {
		require.False(t, strings.HasPrefix(kv, "CLAUDE_CODE_OAUTH_TOKEN="), "the agent inherited the caller's token")
	}
	require.Contains(t, environ, "HOME="+os.Getenv("HOME"), "allowlisted vars pass")
	require.Contains(t, environ, agentSocketEnv+"="+sock, "WHOAS_* vars pass")
}
