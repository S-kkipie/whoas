// Package overlay builds per-identity copies of a CLI config dir: every entry
// is a symlink to the real (global) dir except the auth paths, which hold the
// identity's own files. Shared state stays shared; only auth differs.
package overlay

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

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
// dir must be fresh (missing or empty); on error the caller removes it.
func Build(dir, global string, authPaths []string, files map[string]envelope.File) error {
	global, err := filepath.Abs(global)
	if err != nil {
		return err
	}
	auth, anc := split(authPaths)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	records := map[string]fingerprint{}
	if err := mirror(dir, global, ".", auth, anc, records); err != nil {
		return err
	}
	if err := writeManifest(dir, records); err != nil {
		return err
	}
	for rel, f := range files {
		// Validate that rel is a local path inside an auth path.
		p := filepath.Clean(filepath.FromSlash(rel))
		if !filepath.IsLocal(p) {
			return fmt.Errorf("envelope file %q is not a local path", rel)
		}
		// Check that p matches or is under an auth path.
		found := false
		for a := range auth {
			if p == a || strings.HasPrefix(p, a+string(filepath.Separator)) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("envelope file %q is outside the adapter's auth_files", rel)
		}
		// Construct the full path and check for symlinks.
		target := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		// Refuse to write through a symlink.
		if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to write %s through a symlink", target)
		}
		mode := os.FileMode(f.Mode) & 0o777
		if mode == 0 {
			mode = 0o600
		}
		if err := os.WriteFile(target, f.Data, mode); err != nil {
			return err
		}
	}
	return nil
}

func mirror(dir, global, rel string, auth, anc map[string]bool, records map[string]fingerprint) error {
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
			if err := mirror(dir, global, child, auth, anc, records); err != nil {
				return err
			}
		default:
			if err := os.Symlink(filepath.Join(global, child), filepath.Join(dir, child)); err != nil {
				return err
			}
			if info, err := os.Lstat(filepath.Join(global, child)); err == nil {
				records[filepath.ToSlash(child)] = fingerprintOf(info)
			}
		}
	}
	return nil
}

// fingerprint identifies a global entry as it was when Build mirrored it.
type fingerprint struct {
	File  bool   `json:"file"` // regular file
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // ns
	Mode  uint32 `json:"mode"`
}

func fingerprintOf(info fs.FileInfo) fingerprint {
	return fingerprint{File: info.Mode().IsRegular(), Size: info.Size(), MTime: info.ModTime().UnixNano(), Mode: uint32(info.Mode())}
}

// manifestPath lives next to dir, never inside it: the CLI would see a file in
// the overlay (for a root overlay, in $HOME).
func manifestPath(dir string) string { return filepath.Clean(dir) + ".mirror.json" }

func writeManifest(dir string, records map[string]fingerprint) error {
	data, err := json.Marshal(records)
	if err != nil {
		return err
	}
	path := manifestPath(dir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// readManifest returns nil when the manifest is missing or unreadable (an
// overlay from an older whoas or a crashed build): nothing is copied back.
func readManifest(dir string) map[string]fingerprint {
	data, err := os.ReadFile(manifestPath(dir))
	if err != nil {
		return nil
	}
	var records map[string]fingerprint
	if json.Unmarshal(data, &records) != nil {
		return nil
	}
	return records
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
// stays shared. It never writes auth files to global.
//
// Entries global already has that are no longer symlinks in dir were mirrored
// and then replaced during the run (e.g. a dotfile saved by writing a new file
// and renaming it). Such a regular file is copied back over the global file
// when the manifest Build wrote shows the global file unchanged since (same
// size, mtime, mode). Otherwise (no manifest, a directory, global not a plain
// file or changed too) the global entry wins and Promote returns the name, as
// a slash path relative to dir.
//
// Two identities of the same CLI that both replace the same file: the first to
// promote copies back, the second sees the global changed and is reported. The
// check-then-rename window is a small TOCTOU, accepted: no auth file is ever
// touched and a global entry that changed is never deleted.
func Promote(dir, global string, authPaths []string) ([]string, error) {
	auth, anc := split(authPaths)
	var replaced []string
	err := promote(dir, global, ".", auth, anc, readManifest(dir), &replaced)
	sort.Strings(replaced)
	return replaced, err
}

func promote(dir, global, rel string, auth, anc map[string]bool, records map[string]fingerprint, replaced *[]string) error {
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
			if err := promote(dir, global, child, auth, anc, records, replaced); err != nil {
				return err
			}
			continue
		}
		dst := filepath.Join(global, child)
		if gi, err := os.Lstat(dst); err == nil {
			name := filepath.ToSlash(child)
			rec, ok := records[name]
			if ok && rec.File && e.Type().IsRegular() && gi.Mode().IsRegular() && fingerprintOf(gi) == rec {
				if err := copyBack(filepath.Join(dir, child), dst); err != nil {
					return err
				}
				continue
			}
			*replaced = append(*replaced, name)
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

// copyBack moves the regular file src over the existing dst, keeping src's
// mode. Across filesystems it copies to a temp name beside dst first.
func copyBack(src, dst string) error {
	err := rename(src, dst)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.whoas-tmp-%d", dst, os.Getpid())
	_ = os.Remove(tmp)
	if err := copyFile(src, tmp, info.Mode().Perm()); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, info.Mode().Perm()); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Remove(src)
}

// move renames src to dst, falling back to copy+delete across filesystems (tmpfs → home).
// two overlays promoting the same new path at the same instant: last writer wins;
// pre-existing global data is never deleted.
func move(src, dst string) error {
	err := rename(src, dst)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	// Rename failed with cross-device link; copy to a temp name, then move into place.
	tmp := fmt.Sprintf("%s.whoas-tmp-%d", dst, os.Getpid())
	if err := copyTree(src, tmp); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.RemoveAll(tmp)
		// Pre-existing global entry won; continue cleanup.
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

// Remove deletes dir (symlinks are removed, never followed) and its manifest.
func Remove(dir string) error {
	err := os.RemoveAll(dir)
	if merr := os.Remove(manifestPath(dir)); merr != nil && !errors.Is(merr, fs.ErrNotExist) && err == nil {
		err = merr
	}
	return err
}
