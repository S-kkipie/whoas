package binpath

import (
	"fmt"
	"os"
	"path/filepath"
)

// Index resolves many names against one PATH, reading each directory once.
// Find stats every directory per name, which is slow when PATH has many
// entries on a slow filesystem (WSL's /mnt/c); listing them once is not.
type Index struct {
	names map[string][]string // name -> candidate paths, in PATH order
	me    string
}

// NewIndex lists every directory of pathList, skipping exclude (whoas
// shims); candidates resolving to self (the whoas binary) are skipped later.
func NewIndex(pathList, exclude, self string) *Index {
	ix := &Index{names: map[string][]string{}, me: canon(self)}
	ex := canon(exclude)
	seen := map[string]bool{}
	for _, dir := range filepath.SplitList(pathList) {
		if dir == "" {
			dir = "."
		}
		if seen[dir] || (ex != "" && canon(dir) == ex) {
			continue
		}
		seen[dir] = true
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			ix.names[e.Name()] = append(ix.names[e.Name()], filepath.Join(dir, e.Name()))
		}
	}
	return ix
}

// Find returns the first executable regular file named name, like Find.
func (ix *Index) Find(name string) (string, error) {
	for _, p := range ix.names[name] {
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		if ix.me != "" && canon(p) == ix.me {
			continue
		}
		return p, nil
	}
	return "", fmt.Errorf("%s: not found in PATH", name)
}
