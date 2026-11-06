//go:build unix

package runlock

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	require.NoError(t, cmd.Run())
	return cmd.Process.Pid
}

func TestAcquireExclusive(t *testing.T) {
	base := filepath.Join(t.TempDir(), "gh", "work")
	l, err := Acquire(context.Background(), base, time.Second)
	require.NoError(t, err)

	_, err = Acquire(context.Background(), base, 100*time.Millisecond)
	require.ErrorIs(t, err, ErrBusy)

	require.NoError(t, l.Release())
	l2, err := Acquire(context.Background(), base, time.Second)
	require.NoError(t, err)
	require.NoError(t, l2.Release())
}

func TestStateRoundTripAndPrune(t *testing.T) {
	base := filepath.Join(t.TempDir(), "work")
	l, err := Acquire(context.Background(), base, time.Second)
	require.NoError(t, err)
	defer l.Release()

	s, err := l.Read()
	require.NoError(t, err)
	require.Empty(t, s.Holders)

	dead := deadPID(t)
	s.Add(os.Getpid())
	s.Add(dead)
	s.Add(os.Getpid())
	s.Hash = "h1"
	require.NoError(t, l.Write(s))

	got, err := Peek(base)
	require.NoError(t, err)
	require.Equal(t, []int{os.Getpid(), dead}, got.Holders)
	require.Equal(t, []int{os.Getpid()}, got.Live())

	got.Prune()
	require.Equal(t, []int{os.Getpid()}, got.Holders)
	got.Remove(os.Getpid())
	got.Hash = ""
	require.NoError(t, l.Write(got))
	_, err = os.Stat(base + ".state.json")
	require.True(t, os.IsNotExist(err), "empty state deletes the file")
}
