// Package runlock coordinates processes sharing one identity overlay: an
// exclusive flock on <base>.lock guards <base>.state.json, which records the
// PIDs using the overlay and the hash of the auth files last sealed.
package runlock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/gofrs/flock"

	"github.com/S-kkipie/whoas/internal/fsutil"
)

// ErrBusy means the lock was not obtained in time.
var ErrBusy = errors.New("identity busy")

// State is the shared bookkeeping of one identity overlay.
type State struct {
	Holders []int  `json:"holders"`
	Hash    string `json:"hash,omitempty"`
}

// Lock is a held exclusive lock.
type Lock struct {
	fl    *flock.Flock
	state string
}

// Acquire takes the lock for base, waiting up to timeout.
func Acquire(ctx context.Context, base string, timeout time.Duration) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(base), 0o700); err != nil {
		return nil, err
	}
	fl := flock.New(base + ".lock")
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ok, err := fl.TryLockContext(ctx, 20*time.Millisecond)
	if !ok {
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: lock held for more than %s (%s.lock)", ErrBusy, timeout, base)
	}
	return &Lock{fl: fl, state: base + ".state.json"}, nil
}

// Read returns the state; missing state is the zero State.
func (l *Lock) Read() (State, error) { return readState(l.state) }

// Write stores the state; an empty state deletes the file.
func (l *Lock) Write(s State) error {
	if len(s.Holders) == 0 && s.Hash == "" {
		err := os.Remove(l.state)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(l.state, data, 0o600)
}

// Release drops the lock.
func (l *Lock) Release() error { return l.fl.Unlock() }

// Peek reads base's state without locking (for listings).
func Peek(base string) (State, error) { return readState(base + ".state.json") }

func readState(path string) (State, error) {
	var s State
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(data, &s)
}

// Add records pid as a holder.
func (s *State) Add(pid int) {
	if !slices.Contains(s.Holders, pid) {
		s.Holders = append(s.Holders, pid)
	}
}

// Remove drops pid from the holders.
func (s *State) Remove(pid int) {
	s.Holders = slices.DeleteFunc(s.Holders, func(p int) bool { return p == pid })
}

// Prune drops holders whose process no longer exists.
func (s *State) Prune() {
	s.Holders = slices.DeleteFunc(s.Holders, func(p int) bool { return !Alive(p) })
}

// Live returns the holders that are still running.
func (s State) Live() []int {
	return slices.DeleteFunc(slices.Clone(s.Holders), func(p int) bool { return !Alive(p) })
}
