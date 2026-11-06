// Package keystore stores the vault master key (an age X25519 identity).
package keystore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/S-kkipie/whoas/internal/fsutil"
)

// service is a var so integration tests can use a throwaway keyring entry.
var service = "whoas"

const account = "master"

// ErrNotFound means the backend holds no master key yet.
var ErrNotFound = errors.New("master key not found")

// ErrNeedsTerminal means reading the master key would ask on a terminal,
// and the store was made not to (NoPrompt).
var ErrNeedsTerminal = errors.New("reading the master key needs a terminal")

// Store is one place the master key can live.
type Store interface {
	Name() string
	Get() (string, error) // AGE-SECRET-KEY-1...; ErrNotFound when absent
	Set(secret string) error
	Slow() bool         // true when Get is slow or interactive; an agent should cache the key
	TTL() time.Duration // agent idle TTL for this store; 0 = no expiry
}

// NoPrompt returns s made to fail with ErrNeedsTerminal instead of asking
// for a passphrase: for processes whose terminal belongs to someone else,
// like an MCP proxy started by an agent. It only affects the passphrase store;
// other stores (env, OS keyring, WSL bridge) never prompt and are returned unchanged.
func NoPrompt(s Store) Store {
	if p, ok := s.(*passphraseStore); ok {
		c := *p
		c.prompt = func(string) (string, error) { return "", ErrNeedsTerminal }
		return &c
	}
	return s
}

// rememberFile names the file in the data dir that records the backend.
const rememberFile = "keystore"

// Select picks the backend. Order: WHOAS_MASTER_KEY, WHOAS_KEYSTORE override,
// the backend remembered in <dataDir>/keystore, then auto-detection: WSL
// bridge to Windows Credential Manager, OS keyring, passphrase file.
func Select(dataDir string, getenv func(string) string) (Store, error) {
	if k := getenv("WHOAS_MASTER_KEY"); k != "" {
		return envStore{key: k}, nil
	}
	switch v := getenv("WHOAS_KEYSTORE"); v {
	case "":
	case "os":
		return osStore{}, nil
	case "passphrase":
		return newPassphraseStore(dataDir, getenv), nil
	case "wsl":
		exe, err := findWinExe(getenv)
		if err != nil {
			return nil, err
		}
		return newWSLStore(exe), nil
	default:
		return nil, fmt.Errorf("unknown WHOAS_KEYSTORE %q (os|wsl|passphrase)", v)
	}
	if s, err := remembered(dataDir, getenv); s != nil || err != nil {
		return s, err
	}
	if isWSL(getenv) {
		if exe, err := findWinExe(getenv); err == nil {
			return newWSLStore(exe), nil
		}
	}
	if osAvailable() {
		return osStore{}, nil
	}
	return newPassphraseStore(dataDir, getenv), nil
}

// remembered returns the backend recorded by Remember, or nil when none is.
func remembered(dataDir string, getenv func(string) string) (Store, error) {
	path := filepath.Join(dataDir, rememberFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	switch kind := strings.TrimSpace(string(data)); kind {
	case "os":
		return osStore{}, nil
	case "passphrase":
		return newPassphraseStore(dataDir, getenv), nil
	case "wsl":
		exe, err := findWinExe(getenv)
		if err != nil {
			return nil, errors.New(`keystore is "wsl" but whoas.exe was not found (set WHOAS_WIN_EXE)`)
		}
		return newWSLStore(exe), nil
	default:
		return nil, fmt.Errorf("%s: unknown keystore %q (os|wsl|passphrase)", path, kind)
	}
}

// Remember records s as the backend in <dataDir>/keystore so later runs use
// it without probing (a probe of a locked keyring can block or prompt). The
// env store (WHOAS_MASTER_KEY) is never remembered.
func Remember(dataDir string, s Store) error {
	var kind string
	switch s.(type) {
	case osStore:
		kind = "os"
	case *wslStore:
		kind = "wsl"
	case *passphraseStore:
		kind = "passphrase"
	default:
		return nil
	}
	return fsutil.WriteFileAtomic(filepath.Join(dataDir, rememberFile), []byte(kind+"\n"), 0o600)
}
