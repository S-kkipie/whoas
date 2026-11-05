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

// Store is one place the master key can live.
type Store interface {
	Name() string
	Get() (string, error) // AGE-SECRET-KEY-1...; ErrNotFound when absent
	Set(secret string) error
	Slow() bool         // true when Get is slow or interactive; an agent should cache the key
	TTL() time.Duration // agent idle TTL for this store; 0 = no expiry
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
	if v := getenv("WHOAS_KEYSTORE"); v != "" {
		switch v {
		case "os", "passphrase", "wsl":
			return named(v, dataDir, getenv)
		}
		return nil, fmt.Errorf("unknown WHOAS_KEYSTORE %q (os|wsl|passphrase)", v)
	}
	path := filepath.Join(dataDir, rememberFile)
	data, err := os.ReadFile(path)
	if err == nil {
		switch kind := strings.TrimSpace(string(data)); kind {
		case "os", "passphrase":
			return named(kind, dataDir, getenv)
		case "wsl":
			exe, err := findWinExe(getenv)
			if err != nil {
				return nil, errors.New(`keystore is "wsl" but whoas.exe was not found (set WHOAS_WIN_EXE)`)
			}
			return newWSLStore(exe), nil
		default:
			return nil, fmt.Errorf("%s: unknown keystore %q (os|wsl|passphrase)", path, kind)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
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

// named returns the backend called kind (os, wsl or passphrase).
func named(kind, dataDir string, getenv func(string) string) (Store, error) {
	switch kind {
	case "os":
		return osStore{}, nil
	case "wsl":
		exe, err := findWinExe(getenv)
		if err != nil {
			return nil, err
		}
		return newWSLStore(exe), nil
	}
	return newPassphraseStore(dataDir, getenv), nil
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
