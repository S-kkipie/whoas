// Package project finds and parses .whoas.yaml. The schema is a whitelist:
// "profile" plus provider: alias pairs, all strings. It can never declare
// adapters, commands, paths or env, so a project file cannot run code.
package project

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/lexer"
	"github.com/goccy/go-yaml/token"

	"github.com/S-kkipie/whoas/internal/ref"
)

// FileName is the project file name.
const FileName = ".whoas.yaml"

// maxFileSize is the maximum allowed project file size (64 KiB).
const maxFileSize = 64 << 10

// File is a parsed project file.
type File struct {
	Path      string
	Profile   string
	Overrides map[string]string // provider → alias
}

// Find walks up from start and returns the nearest project file that is a
// regular file, or "". Anything else named .whoas.yaml is skipped.
func Find(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		p := filepath.Join(dir, FileName)
		if fi, err := os.Stat(p); err == nil {
			if fi.Mode().IsRegular() {
				return p, nil
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// Read returns the contents of the project file at path. Only a regular file
// is read, and never more than maxFileSize bytes, so a directory, FIFO or
// device planted as .whoas.yaml cannot hang or exhaust whoas.
func Read(path string) ([]byte, error) {
	if fi, err := os.Stat(path); err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, errNotRegular(path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, errNotRegular(path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileSize {
		return nil, errTooLarge(path)
	}
	return data, nil
}

func errNotRegular(path string) error { return fmt.Errorf("%s: not a regular file", path) }

func errTooLarge(path string) error { return fmt.Errorf("%s: file too large (max 64 KiB)", path) }

// Parse validates data against the whitelist schema.
func Parse(path string, data []byte) (*File, error) {
	if len(data) > maxFileSize {
		return nil, errTooLarge(path)
	}
	for _, tk := range lexer.Tokenize(string(data)) {
		if tk.Type == token.AnchorType || tk.Type == token.AliasType {
			return nil, fmt.Errorf("%s: YAML anchors and aliases are not allowed", path)
		}
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: invalid YAML: %w", path, err)
	}
	f := &File{Path: path, Overrides: map[string]string{}}
	for k, v := range raw {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s: %q must be a string", path, k)
		}
		if err := ref.ValidAlias(s); err != nil {
			return nil, fmt.Errorf("%s: %s: %w", path, k, err)
		}
		if k == "profile" {
			f.Profile = s
			continue
		}
		if err := ref.ValidProvider(k); err != nil {
			return nil, fmt.Errorf("%s: invalid key: %w", path, err)
		}
		f.Overrides[k] = s
	}
	return f, nil
}
