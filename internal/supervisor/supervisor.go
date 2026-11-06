// Package supervisor runs one CLI invocation as an identity: it joins (or
// materializes) the identity's overlay under a lock, spawns the real binary
// pointed at the overlay, seals refreshed auth files back into the vault as
// they change, and tears the overlay down when the last user exits.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/S-kkipie/whoas/internal/adapter"
	"github.com/S-kkipie/whoas/internal/envelope"
	"github.com/S-kkipie/whoas/internal/overlay"
	"github.com/S-kkipie/whoas/internal/ref"
	"github.com/S-kkipie/whoas/internal/runlock"
	"github.com/S-kkipie/whoas/internal/vault"
)

// ErrStart wraps failures to start the real CLI.
var ErrStart = errors.New("start failed")

// Plan is everything one supervised run needs.
type Plan struct {
	Ref         ref.Ref
	Adapter     *adapter.Adapter
	Bin         string   // absolute path of the real CLI
	Args        []string // user args, passed verbatim after injected flags
	Global      string   // the CLI's real config dir
	RunBase     string   // overlay dir; .lock/.state.json sit next to it
	Vault       *vault.Vault
	Decrypter   vault.Decrypter
	Env         []string // base environment for the child
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
	Debounce    time.Duration // seal-on-write debounce; 0 = 200ms
	LockTimeout time.Duration // 0 = 10s
}

// Result is how the child ended.
type Result struct {
	Code   int
	Signal os.Signal // set when the child died from a signal
}

func (p *Plan) defaults() {
	if p.Debounce == 0 {
		p.Debounce = 200 * time.Millisecond
	}
	if p.LockTimeout == 0 {
		p.LockTimeout = 10 * time.Second
	}
	if p.Stderr == nil {
		p.Stderr = io.Discard
	}
}

// Run executes the plan and returns the child's result.
func Run(ctx context.Context, p Plan) (Result, error) {
	p.defaults()
	if err := p.join(ctx); err != nil {
		return Result{}, err
	}
	stop, err := p.watch(ctx)
	if err != nil {
		_ = p.leave(ctx)
		return Result{}, err
	}
	res, runErr := p.spawn()
	stop()
	if err := p.leave(ctx); err != nil && runErr == nil {
		runErr = err
	}
	return res, runErr
}

// join registers this process as a holder, materializing the overlay if it
// is the first one (recovering a crashed overlay first).
func (p *Plan) join(ctx context.Context) error {
	l, err := runlock.Acquire(context.WithoutCancel(ctx), p.RunBase, p.LockTimeout)
	if err != nil {
		return err
	}
	defer l.Release()
	st, err := l.Read()
	if err != nil {
		return err
	}
	st.Prune()
	if len(st.Holders) == 0 {
		if _, err := recoverStale(p.RunBase, p.Global, p.Adapter, p.Ref, p.Vault, st.Hash, p.Stderr); err != nil {
			return err
		}
		env, err := p.Vault.Load(p.Ref, p.Decrypter)
		if err != nil {
			return err
		}
		if err := overlay.Build(p.RunBase, p.Global, p.Adapter.Dir.AuthFiles, env.Files); err != nil {
			_ = overlay.Remove(p.RunBase)
			return err
		}
		st.Hash = envelope.Hash(env.Files)
	}
	st.Add(os.Getpid())
	return l.Write(st)
}

// leave seals final changes and removes the overlay if no holder remains.
func (p *Plan) leave(ctx context.Context) error {
	l, err := runlock.Acquire(context.WithoutCancel(ctx), p.RunBase, p.LockTimeout)
	if err != nil {
		return err
	}
	defer l.Release()
	st, err := l.Read()
	if err != nil {
		return err
	}
	if st.Hash, err = sealIfChanged(p.RunBase, p.Adapter, p.Ref, p.Vault, st.Hash); err != nil {
		return err
	}
	st.Remove(os.Getpid())
	st.Prune()
	if len(st.Holders) == 0 {
		promote(p.RunBase, p.Global, p.Adapter, p.Ref, p.Stderr)
		if err := overlay.Remove(p.RunBase); err != nil {
			return err
		}
		st.Hash = ""
	}
	return l.Write(st)
}

// sealIfChanged seals the overlay's auth files when their hash differs from
// prev and returns the current hash. An empty overlay (logged out) is not sealed.
func sealIfChanged(dir string, a *adapter.Adapter, r ref.Ref, v *vault.Vault, prev string) (string, error) {
	files, err := overlay.Collect(dir, a.Dir.AuthFiles)
	if err != nil || len(files) == 0 {
		return prev, err
	}
	h := envelope.Hash(files)
	if h == prev {
		return prev, nil
	}
	if err := v.Seal(r, envelope.New(r.Provider, files)); err != nil {
		return prev, fmt.Errorf("seal %s: %w", r, err)
	}
	return h, nil
}

// recoverStale seals and removes an overlay left by crashed holders. It is
// removed without sealing when no hash was recorded (join never completed; a
// completed join always records one) or when r is no longer in the vault
// (removed identities must not come back).
func recoverStale(dir, global string, a *adapter.Adapter, r ref.Ref, v *vault.Vault, hash string, warn io.Writer) (bool, error) {
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if hash != "" && v.Exists(r) {
		if _, err := sealIfChanged(dir, a, r, v, hash); err != nil {
			return false, err
		}
	}
	promote(dir, global, a, r, warn)
	return true, overlay.Remove(dir)
}

// promote shares the overlay's new non-auth files and warns about mirrored
// entries that were replaced during the run (their change is lost).
func promote(dir, global string, a *adapter.Adapter, r ref.Ref, warn io.Writer) {
	replaced, err := overlay.Promote(dir, global, a.Dir.AuthFiles)
	for _, name := range replaced {
		fmt.Fprintf(warn, "whoas: warning: %s: %s was replaced during the run; the change was not copied to %s\n", r, name, filepath.Join(global, filepath.FromSlash(name)))
	}
	if err != nil {
		fmt.Fprintf(warn, "whoas: warning: could not share new non-auth files: %v\n", err)
	}
}

// Recover cleans up r's overlay if every holder is dead. It reports whether
// a stale overlay was found.
func Recover(ctx context.Context, base, global string, a *adapter.Adapter, r ref.Ref, v *vault.Vault) (bool, error) {
	l, err := runlock.Acquire(ctx, base, 10*time.Second)
	if err != nil {
		return false, err
	}
	defer l.Release()
	st, err := l.Read()
	if err != nil {
		return false, err
	}
	st.Prune()
	if len(st.Holders) > 0 {
		return false, nil
	}
	found, err := recoverStale(base, global, a, r, v, st.Hash, os.Stderr)
	if err != nil {
		return found, err
	}
	return found, l.Write(runlock.State{})
}
