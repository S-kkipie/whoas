package keystore

import (
	"errors"
	"time"

	"github.com/zalando/go-keyring"
)

// osStore uses macOS Keychain, Windows Credential Manager or Linux Secret Service.
type osStore struct{}

// OS returns the OS keyring backend (used by the Windows keystore-helper).
func OS() Store { return osStore{} }

func (osStore) Name() string { return "os keyring" }

func (osStore) Get() (string, error) {
	s, err := keyring.Get(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return s, err
}

func (osStore) Set(secret string) error { return keyring.Set(service, account, secret) }
func (osStore) Slow() bool              { return false }
func (osStore) TTL() time.Duration      { return 0 }

// osAvailable reports whether the OS keyring answers at all.
func osAvailable() bool {
	_, err := keyring.Get(service, account)
	return err == nil || errors.Is(err, keyring.ErrNotFound)
}
