package supervisor

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/S-kkipie/whoas/internal/runlock"
)

// watch seals auth files shortly after the CLI rewrites them (seal-on-write),
// so refreshed tokens survive crashes. The returned stop is idempotent.
func (p *Plan) watch(ctx context.Context) (func(), error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		fmt.Fprintf(p.Stderr, "whoas: warning: seal-on-write disabled: %v\n", err)
		return func() {}, nil
	}
	for _, d := range p.watchDirs() {
		if err := w.Add(d); err != nil {
			w.Close()
			fmt.Fprintf(p.Stderr, "whoas: warning: seal-on-write disabled: %v\n", fmt.Errorf("watch %s: %w", d, err))
			return func() {}, nil
		}
	}
	var (
		mu    sync.Mutex
		timer *time.Timer
	)
	seal := func() {
		l, err := runlock.Acquire(context.WithoutCancel(ctx), p.RunBase, p.LockTimeout)
		if err != nil {
			fmt.Fprintln(p.Stderr, "whoas: seal skipped:", err)
			return
		}
		defer l.Release()
		st, err := l.Read()
		if err == nil {
			st.Hash, err = sealIfChanged(p.RunBase, p.Adapter, p.Ref, p.Vault, st.Hash)
		}
		if err == nil && len(st.Holders) > 0 {
			err = l.Write(st)
		}
		if err != nil {
			fmt.Fprintln(p.Stderr, "whoas: seal failed:", err)
		}
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if p.isAuthPath(ev.Name) {
					mu.Lock()
					if timer != nil {
						timer.Stop()
					}
					timer = time.AfterFunc(p.Debounce, seal)
					mu.Unlock()
				}
			case _, ok := <-w.Errors:
				if !ok {
					return
				}
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			<-finished
			w.Close()
			mu.Lock()
			if timer != nil {
				timer.Stop() // leave() does the final seal
			}
			mu.Unlock()
		})
	}, nil
}

// watchDirs are the overlay root, every ancestor of an auth path, and every
// existing directory inside directory-type auth paths.
func (p *Plan) watchDirs() []string {
	set := map[string]bool{}
	if info, err := os.Stat(p.RunBase); err == nil && info.IsDir() {
		set[p.RunBase] = true
	}
	for _, a := range p.Adapter.Dir.AuthFiles {
		full := filepath.Join(p.RunBase, filepath.FromSlash(a))
		for d := filepath.Dir(full); strings.HasPrefix(d, p.RunBase); d = filepath.Dir(d) {
			if info, err := os.Stat(d); err == nil && info.IsDir() {
				set[d] = true
			}
		}
		_ = filepath.WalkDir(full, func(path string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				set[path] = true
			}
			return nil
		})
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	return out
}

// isAuthPath reports whether an event path is an auth file or inside an auth dir.
func (p *Plan) isAuthPath(name string) bool {
	rel, err := filepath.Rel(p.RunBase, name)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	for _, a := range p.Adapter.Dir.AuthFiles {
		if rel == a || strings.HasPrefix(rel, a+"/") {
			return true
		}
	}
	return false
}
