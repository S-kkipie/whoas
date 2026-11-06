// Package envelope is the plaintext form of one sealed identity: the auth
// files a CLI wrote, keyed by slash-separated path relative to its config dir.
package envelope

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
	"strings"
)

// Version of the envelope format.
const Version = 1

// File is one auth file. Data is base64 in JSON.
type File struct {
	Mode uint32 `json:"mode"`
	Data []byte `json:"data"`
}

// Envelope is what gets age-encrypted into the vault.
type Envelope struct {
	V        int             `json:"v"`
	Provider string          `json:"provider"`
	Files    map[string]File `json:"files"`
}

// New builds a current-version envelope.
func New(provider string, files map[string]File) *Envelope {
	return &Envelope{V: Version, Provider: provider, Files: files}
}

// secretPrefix marks env-token secrets inside an envelope's files.
const secretPrefix = "env/"

// FromSecrets builds an env-token envelope: each non-empty value becomes the
// file "env/<NAME>".
func FromSecrets(provider string, values map[string]string) *Envelope {
	files := map[string]File{}
	for k, v := range values {
		if v != "" {
			files[secretPrefix+k] = File{Mode: 0o600, Data: []byte(v)}
		}
	}
	return New(provider, files)
}

// Secrets returns the env-token secrets stored in e (NAME -> value).
func (e *Envelope) Secrets() map[string]string {
	out := map[string]string{}
	for p, f := range e.Files {
		if name, ok := strings.CutPrefix(p, secretPrefix); ok {
			out[name] = string(f.Data)
		}
	}
	return out
}

// Hash is a stable sha256 over sorted paths, modes and contents.
func Hash(files map[string]File) string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	var n [8]byte
	for _, p := range paths {
		f := files[p]
		binary.BigEndian.PutUint64(n[:], uint64(len(p)))
		h.Write(n[:])
		h.Write([]byte(p))
		binary.BigEndian.PutUint64(n[:], uint64(f.Mode))
		h.Write(n[:])
		binary.BigEndian.PutUint64(n[:], uint64(len(f.Data)))
		h.Write(n[:])
		h.Write(f.Data)
	}
	return hex.EncodeToString(h.Sum(nil))
}
