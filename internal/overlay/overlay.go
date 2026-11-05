// Package overlay builds per-identity copies of a CLI config dir: every entry
// is a symlink to the real (global) dir except the auth paths, which hold the
// identity's own files. Shared state stays shared; only auth differs.
package overlay

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/S-kkipie/whoas/internal/envelope"
)

// split returns the cleaned auth paths and every ancestor dir of them.
func split(authPaths []string) (auth, ancestors map[string]bool) {
	auth, ancestors = map[string]bool{}, map[string]bool{}
	for _, p := range authPaths {
		p = filepath.Clean(filepath.FromSlash(p))
		auth[p] = true
		for d := filepath.Dir(p); d != "."; d = filepath.Dir(d) {
			ancestors[d] = true
		}
	}
	return auth, ancestors
}

// Build creates dir mirroring global, then writes files (slash paths).
func Build(dir, global string, authPaths []string, files map[string]envelope.File) error {
	global, err := filepath.Abs(global)
	if err != nil {
		return err
	}
	auth, anc := split(authPaths)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := mirror(dir, global, ".", auth, anc); err != nil {
		return err
	}
	for rel, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		mode := os.FileMode(f.Mode) & 0o777
		if mode == 0 {
			mode = 0o600
		}
		if err := os.WriteFile(p, f.Data, mode); err != nil {
			return err
		}
	}
	return nil
}

func mirror(dir, global, rel string, auth, anc map[string]bool) error {
	entries, err := os.ReadDir(filepath.Join(global, rel))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		child := filepath.Join(rel, e.Name())
		switch {
		case auth[child]:
			// materialized from the vault, never linked
		case anc[child]:
			if err := os.MkdirAll(filepath.Join(dir, child), 0o700); err != nil {
				return err
			}
			if err := mirror(dir, global, child, auth, anc); err != nil {
				return err
			}
		default:
			if err := os.Symlink(filepath.Join(global, child), filepath.Join(dir, child)); err != nil {
				return err
			}
		}
	}
	return nil
}

// Collect reads the auth paths under root (files or whole dirs).
// Missing paths are skipped. Keys are slash paths relative to root.
func Collect(root string, authPaths []string) (map[string]envelope.File, error) {
	out := map[string]envelope.File{}
	for _, p := range authPaths {
		full := filepath.Join(root, filepath.FromSlash(p))
		info, err := os.Stat(full)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.Mode().IsRegular() {
			if err := add(out, root, full, info); err != nil {
				return nil, err
			}
			continue
		}
		if !info.IsDir() {
			continue
		}
		err = filepath.WalkDir(full, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.Type().IsRegular() {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				return err
			}
			return add(out, root, path, fi)
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func add(out map[string]envelope.File, root, path string, info fs.FileInfo) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	out[filepath.ToSlash(rel)] = envelope.File{Mode: uint32(info.Mode().Perm()), Data: data}
	return nil
}

// rename is swapped in tests to simulate a cross-device move.
var rename = os.Rename

// Promote moves entries the CLI created inside dir (real files or dirs, not
// auth, not symlinks) into global when global lacks them, so non-auth state
// stays shared. Existing global entries always win.
func Promote(dir, global string, authPaths []string) error {
	auth, anc := split(authPaths)
	return promote(dir, global, ".", auth, anc)
}

func promote(dir, global, rel string, auth, anc map[string]bool) error {
	entries, err := os.ReadDir(filepath.Join(dir, rel))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		child := filepath.Join(rel, e.Name())
		if auth[child] || e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		if anc[child] {
			if err := promote(dir, global, child, auth, anc); err != nil {
				return err
			}
			continue
		}
		dst := filepath.Join(global, child)
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := move(filepath.Join(dir, child), dst); err != nil {
			return err
		}
	}
	return nil
}

// move renames, falling back to copy+delete across filesystems (tmpfs → home).
func move(src, dst string) error {
	if err := rename(src, dst); err == nil {
		return nil
	}
	if err := copyTree(src, dst); err != nil {
		os.RemoveAll(dst)
		return err
	}
	return os.RemoveAll(src)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.Type().IsRegular():
			return copyFile(path, target, info.Mode().Perm())
		}
		return nil
	})
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Remove deletes dir; symlinks are removed, never followed.
func Remove(dir string) error { return os.RemoveAll(dir) }
