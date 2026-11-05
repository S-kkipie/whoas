// Package trust records which project files the user approved, by absolute
// path and content hash (direnv model). Any edit revokes trust.
package trust

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/S-kkipie/whoas/internal/fsutil"
)

// Store is the trust database.
type Store struct {
	path    string
	entries map[string]string // absolute file path → sha256 hex
}

// Load reads path; a missing file is an empty store.
func Load(path string) (*Store, error) {
	s := &Store{path: path, entries: map[string]string{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.entries); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if s.entries == nil {
		s.entries = map[string]string{}
	}
	return s, nil
}

// Hash is the sha256 of data in hex.
func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// IsTrusted reports whether file was trusted with exactly this content.
func (s *Store) IsTrusted(file string, data []byte) bool { return s.entries[file] == Hash(data) }

// Trust approves file with its current content.
func (s *Store) Trust(file string, data []byte) { s.entries[file] = Hash(data) }

// Untrust forgets file; it reports whether it was trusted.
func (s *Store) Untrust(file string) bool {
	_, ok := s.entries[file]
	delete(s.entries, file)
	return ok
}

// Save writes the store (0600).
func (s *Store) Save() error {
	data, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(s.path, data, 0o600)
}
