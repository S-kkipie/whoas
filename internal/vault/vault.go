// Package vault stores sealed identities: one age file per provider@alias.
// Sealing needs only the public key (the recipient file); opening needs a
// Decrypter that holds or reaches the private key.
package vault

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/S-kkipie/whoas/internal/envelope"
	"github.com/S-kkipie/whoas/internal/fsutil"
	"github.com/S-kkipie/whoas/internal/keystore"
	"github.com/S-kkipie/whoas/internal/ref"
)

var (
	ErrNotInitialized = errors.New("vault not initialized (run: whoas add <provider> <alias>)")
	ErrNotFound       = errors.New("identity not found")
)

// Decrypter turns an age ciphertext into plaintext.
type Decrypter interface {
	Decrypt(ciphertext []byte) ([]byte, error)
}

// Meta is non-secret metadata stored next to each blob.
type Meta struct {
	Account string    `json:"account,omitempty"`
	Created time.Time `json:"created"`
}

// Entry is one identity in a listing.
type Entry struct {
	Ref  ref.Ref
	Meta Meta
}

// Vault is the sealed-identity store.
type Vault struct {
	dir       string
	recipient age.Recipient
}

// Init makes sure a master key exists in store (generating one if needed)
// and that the recipient file matches it, then opens the vault.
func Init(recipientFile, vaultDir string, store keystore.Store) (*Vault, error) {
	v, err := Open(recipientFile, vaultDir)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, ErrNotInitialized) {
		return nil, err
	}
	secret, err := store.Get()
	if errors.Is(err, keystore.ErrNotFound) {
		id, gerr := age.GenerateX25519Identity()
		if gerr != nil {
			return nil, gerr
		}
		secret = id.String()
		if err := store.Set(secret); err != nil {
			return nil, fmt.Errorf("store master key in %s: %w", store.Name(), err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("read master key from %s: %w", store.Name(), err)
	}
	id, err := age.ParseX25519Identity(strings.TrimSpace(secret))
	if err != nil {
		return nil, fmt.Errorf("master key in %s is invalid: %w", store.Name(), err)
	}
	if err := fsutil.WriteFileAtomic(recipientFile, []byte(id.Recipient().String()+"\n"), 0o644); err != nil {
		return nil, err
	}
	return Open(recipientFile, vaultDir)
}

// Open loads the recipient (public key); it never touches the master key.
func Open(recipientFile, vaultDir string) (*Vault, error) {
	data, err := os.ReadFile(recipientFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotInitialized
	}
	if err != nil {
		return nil, err
	}
	r, err := age.ParseX25519Recipient(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", recipientFile, err)
	}
	return &Vault{dir: vaultDir, recipient: r}, nil
}

func (v *Vault) base(r ref.Ref) string {
	return filepath.Join(v.dir, filepath.FromSlash(r.Provider), r.Alias)
}

// Exists reports whether r has a sealed blob.
func (v *Vault) Exists(r ref.Ref) bool {
	_, err := os.Stat(v.base(r) + ".age")
	return err == nil
}

// Seal encrypts env to the public key and writes it atomically (0600).
func (v *Vault) Seal(r ref.Ref, env *envelope.Envelope) error {
	plain, err := json.Marshal(env)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, v.recipient)
	if err != nil {
		return err
	}
	if _, err := w.Write(plain); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(v.base(r)+".age", buf.Bytes(), 0o600)
}

// Load decrypts r's envelope.
func (v *Vault) Load(r ref.Ref, d Decrypter) (*envelope.Envelope, error) {
	ct, err := os.ReadFile(v.base(r) + ".age")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", r, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	plain, err := d.Decrypt(ct)
	if err != nil {
		return nil, fmt.Errorf("decrypt %s: %w", r, err)
	}
	var env envelope.Envelope
	if err := json.Unmarshal(plain, &env); err != nil {
		return nil, fmt.Errorf("decode %s: %w", r, err)
	}
	return &env, nil
}

// WriteMeta stores r's metadata (0600).
func (v *Vault) WriteMeta(r ref.Ref, m Meta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(v.base(r)+".meta.json", data, 0o600)
}

// ReadMeta returns r's metadata; missing metadata is the zero Meta.
func (v *Vault) ReadMeta(r ref.Ref) (Meta, error) {
	var m Meta
	data, err := os.ReadFile(v.base(r) + ".meta.json")
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(data, &m)
}

// Remove deletes r's blob and metadata.
func (v *Vault) Remove(r ref.Ref) error {
	if !v.Exists(r) {
		return fmt.Errorf("%s: %w", r, ErrNotFound)
	}
	if err := os.Remove(v.base(r) + ".age"); err != nil {
		return err
	}
	if err := os.Remove(v.base(r) + ".meta.json"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// List returns every identity sorted by provider then alias.
func (v *Vault) List() ([]Entry, error) {
	var out []Entry
	err := filepath.WalkDir(v.dir, func(path string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return fs.SkipAll
		}
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".age") {
			return nil
		}
		rel, err := filepath.Rel(v.dir, strings.TrimSuffix(path, ".age"))
		if err != nil {
			return err
		}
		r := ref.Ref{Provider: filepath.ToSlash(filepath.Dir(rel)), Alias: filepath.Base(rel)}
		m, err := v.ReadMeta(r)
		if err != nil {
			return err
		}
		out = append(out, Entry{Ref: r, Meta: m})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.String() < out[j].Ref.String() })
	return out, err
}

// IdentityDecrypter decrypts with an in-memory age identity.
type IdentityDecrypter struct{ id *age.X25519Identity }

// NewIdentityDecrypter parses an AGE-SECRET-KEY-1... string.
func NewIdentityDecrypter(secret string) (*IdentityDecrypter, error) {
	id, err := age.ParseX25519Identity(strings.TrimSpace(secret))
	if err != nil {
		return nil, fmt.Errorf("invalid master key: %w", err)
	}
	return &IdentityDecrypter{id: id}, nil
}

func (d *IdentityDecrypter) Decrypt(ct []byte) ([]byte, error) {
	r, err := age.Decrypt(bytes.NewReader(ct), d.id)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}
