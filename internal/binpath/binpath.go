// Package binpath finds the real CLI binary on PATH, skipping whoas shims.
package binpath

import (
	"fmt"
	"os"
	"path/filepath"
)

// Find looks name up in pathList. Directories equal to exclude are skipped,
// as are candidates that resolve to self (the whoas binary). Empty exclude or
// self disables that check.
func Find(name, pathList, exclude, self string) (string, error) {
	ex := canon(exclude)
	me := canon(self)
	for _, dir := range filepath.SplitList(pathList) {
		if dir == "" {
			dir = "."
		}
		if ex != "" && canon(dir) == ex {
			continue
		}
		p := filepath.Join(dir, name)
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		if me != "" && canon(p) == me {
			continue
		}
		return p, nil
	}
	return "", fmt.Errorf("%s: not found in PATH", name)
}

func canon(p string) string {
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	return filepath.Clean(p)
}
