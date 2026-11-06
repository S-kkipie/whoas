package keystore

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/S-kkipie/whoas/internal/fsutil"
)

// scryptWorkFactor is age's default; tests lower it.
var scryptWorkFactor = 18

// passphraseStore keeps the key in <data>/master.age, encrypted with a passphrase.
type passphraseStore struct {
	path   string
	getenv func(string) string
	prompt func(label string) (string, error)
}

func newPassphraseStore(dataDir string, getenv func(string) string) *passphraseStore {
	return &passphraseStore{path: filepath.Join(dataDir, "master.age"), getenv: getenv, prompt: ttyPrompt}
}

func (s *passphraseStore) Name() string       { return "passphrase (" + s.path + ")" }
func (s *passphraseStore) Slow() bool         { return true }
func (s *passphraseStore) TTL() time.Duration { return 8 * time.Hour }

func (s *passphraseStore) passphrase(label string) (string, error) {
	if v := s.getenv("WHOAS_PASSPHRASE"); v != "" {
		return v, nil
	}
	return s.prompt(label)
}

func (s *passphraseStore) Get() (string, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	pass, err := s.passphrase("whoas passphrase: ")
	if err != nil {
		return "", err
	}
	id, err := age.NewScryptIdentity(pass)
	if err != nil {
		return "", err
	}
	r, err := age.Decrypt(bytes.NewReader(data), id)
	if err != nil {
		return "", errors.New("wrong passphrase or corrupt key file")
	}
	secret, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(secret)), nil
}

func (s *passphraseStore) Set(secret string) error {
	pass := s.getenv("WHOAS_PASSPHRASE")
	if pass == "" {
		p1, err := s.prompt("new whoas passphrase: ")
		if err != nil {
			return err
		}
		p2, err := s.prompt("repeat passphrase: ")
		if err != nil {
			return err
		}
		if p1 != p2 {
			return errors.New("passphrases do not match")
		}
		pass = p1
	}
	if pass == "" {
		return errors.New("empty passphrase")
	}
	rcpt, err := age.NewScryptRecipient(pass)
	if err != nil {
		return err
	}
	rcpt.SetWorkFactor(scryptWorkFactor)
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, rcpt)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, secret); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("encrypt master key: %w", err)
	}
	return fsutil.WriteFileAtomic(s.path, buf.Bytes(), 0o600)
}
