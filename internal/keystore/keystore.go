// Package keystore stores the vault master key (an age X25519 identity).
package keystore

import (
	"errors"
	"fmt"
	"time"
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

// Select picks the backend. Order: WHOAS_MASTER_KEY, WHOAS_KEYSTORE override,
// WSL bridge to Windows Credential Manager, OS keyring, passphrase file.
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
