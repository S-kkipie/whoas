// Package agent caches the vault master key in a background process
// (ssh-agent model): clients send ciphertext, the agent returns plaintext,
// and the key never leaves the agent.
package agent

import "errors"

// ErrLocked means the agent holds no key.
var ErrLocked = errors.New("agent is locked")

// request is one JSON line from client to agent.
type request struct {
	Op   string `json:"op"`             // "status" | "load" | "lock" | "decrypt"
	Data []byte `json:"data,omitempty"` // decrypt: ciphertext
	Key  string `json:"key,omitempty"`  // load: AGE-SECRET-KEY-1...
	TTL  int64  `json:"ttl,omitempty"`  // load: idle TTL in seconds, 0 = none
}

// response is the agent's single JSON reply.
type response struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Data   []byte `json:"data,omitempty"`
	Locked bool   `json:"locked,omitempty"`
}
